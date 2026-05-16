// Package nginx manages the Nginx reverse-proxy config for deployed services.
package nginx

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ojhapranjal26/featherdeploy/backend/internal/coordination"
)

var (
	EtcdClient *coordination.Client
	reloadMu   sync.Mutex
)

func SetEtcdClient(c *coordination.Client) { EtcdClient = c }

const (
	sitesAvailable = "/etc/nginx/sites-available"
	sitesEnabled   = "/etc/nginx/sites-enabled"
)

// WriteServiceConfig writes a dedicated Nginx config for a service.
// It returns true if the config was actually written/updated.
func WriteServiceConfig(domain, content string) (bool, error) {
	availablePath := filepath.Join(sitesAvailable, domain)
	enabledPath := filepath.Join(sitesEnabled, domain)

	// Check if content changed
	if existing, err := os.ReadFile(availablePath); err == nil {
		if string(existing) == content {
			// No change, check symlink
			if _, err := os.Stat(enabledPath); err == nil {
				return false, nil
			}
		}
	}

	// Write to sites-available
	if err := os.WriteFile(availablePath, []byte(content), 0644); err != nil {
		// Fallback to sudo tee
		cmd := exec.Command("sudo", "-n", "tee", availablePath)
		cmd.Stdin = strings.NewReader(content)
		if err := cmd.Run(); err != nil {
			return false, fmt.Errorf("failed to write nginx config: %w", err)
		}
	}

	// Create symlink to sites-enabled if it doesn't exist
	if _, err := os.Stat(enabledPath); os.IsNotExist(err) {
		cmd := exec.Command("sudo", "-n", "ln", "-s", availablePath, enabledPath)
		if err := cmd.Run(); err != nil {
			slog.Warn("nginx: could not create symlink", "err", err)
		}
	}

	return true, nil
}

// DeleteServiceConfig removes the Nginx config for a service.
func DeleteServiceConfig(domain string) error {
	availablePath := filepath.Join(sitesAvailable, domain)
	enabledPath := filepath.Join(sitesEnabled, domain)

	_ = exec.Command("sudo", "-n", "rm", enabledPath).Run()
	_ = exec.Command("sudo", "-n", "rm", availablePath).Run()

	return ReloadNginx()
}

// ReloadNginx signals Nginx to reload its config.
func ReloadNginx() error {
	reloadMu.Lock()
	defer reloadMu.Unlock()

	// Validate config first
	if out, err := exec.Command("sudo", "-n", "nginx", "-t").CombinedOutput(); err != nil {
		return fmt.Errorf("nginx config validation failed: %s", string(out))
	}

	if out, err := exec.Command("sudo", "-n", "systemctl", "reload", "nginx").CombinedOutput(); err != nil {
		return fmt.Errorf("nginx reload failed: %s", string(out))
	}

	slog.Info("nginx: reloaded successfully")
	return nil
}

// ProvisionSSL uses Certbot to provision a certificate for a domain.
func ProvisionSSL(domain, email string) error {
	slog.Info("nginx: provisioning SSL for domain", "domain", domain)
	
	// certbot --nginx -d domain --non-interactive --agree-tos -m email
	cmd := exec.Command("sudo", "-n", "certbot", "--nginx", "-d", domain, "--non-interactive", "--agree-tos", "-m", email)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("certbot failed: %s", string(out))
	}

	slog.Info("nginx: SSL provisioned successfully", "domain", domain)
	return nil
}

// PublishRoutes is kept for compatibility with existing architecture if needed, 
// but we prefer direct Nginx file management.
// PublishRoutes syncs domain configurations from the database to Etcd.
func PublishRoutes(db *sql.DB) {
	if EtcdClient == nil {
		return
	}

	rows, err := db.Query(`
		SELECT d.domain, s.id, s.name, s.node_id, s.host_port, s.app_port
		FROM domains d
		JOIN services s ON d.service_id = s.id
		WHERE d.status = 'active'
	`)
	if err != nil {
		slog.Error("nginx: query domains failed", "err", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var domain, svcName, nodeID string
		var svcID int64
		var hostPort, appPort int
		if err := rows.Scan(&domain, &svcID, &svcName, &nodeID, &hostPort, &appPort); err != nil {
			continue
		}

		route := map[string]interface{}{
			"domain":      domain,
			"service":     svcName,
			"target_node": nodeID,
			"target_port": appPort,
			"host_port":   hostPort,
			"mode":        "proxy",
			"version":     time.Now().Unix(),
		}
		data, _ := json.Marshal(route)
		
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := EtcdClient.EtcdClient().Put(ctx, "/routing/"+domain, string(data))
		cancel()
		
		if err != nil {
			slog.Warn("nginx: failed to publish route to etcd", "domain", domain, "err", err)
		}
	}
	slog.Info("nginx: finished publishing routes to etcd")
}
