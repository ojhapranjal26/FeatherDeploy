// Package installer provides the interactive setup wizard for DeployPaaS.
// Run via:  deploypaaas install
//
// The wizard:
//  1. Checks Linux + root
//  2. Detects the package manager and installs Podman + Caddy
//  3. Prompts for panel domain, superadmin credentials
//  4. Creates the system user, data dirs, and env file
//  5. Seeds the database with the superadmin
//  6. Writes the Caddyfile and systemd service
//  7. Enables and starts the services
package installer

import (
	"bufio"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"
	"time"

	"github.com/deploy-paas/backend/internal/auth"
	appDb "github.com/deploy-paas/backend/internal/db"
)

const (
	binDest        = "/usr/local/bin/featherdeploy"
	dataDir        = "/var/lib/featherdeploy"
	configDir      = "/etc/featherdeploy"
	envFile        = "/etc/featherdeploy/featherdeploy.env"
	caddyConf      = "/etc/caddy/Caddyfile"
	systemdUnit    = "/etc/systemd/system/featherdeploy.service"
	defaultSvcUser = "featherdeploy"
	backendPort    = "8080"
)

// IsInstalled returns true when a prior installation is detected on this machine.
func IsInstalled() bool {
	_, envErr := os.Stat(envFile)
	_, unitErr := os.Stat(systemdUnit)
	return envErr == nil || unitErr == nil
}

// Run starts the interactive first-time setup wizard. Exits on error.
func Run() {
	if runtime.GOOS != "linux" {
		die("installer only supported on Linux (got %s)", runtime.GOOS)
	}
	if os.Geteuid() != 0 {
		die("installer must be run as root (use sudo)")
	}

	printBanner()

	reader := bufio.NewReader(os.Stdin)

	// ── Step 0: Service OS user ───────────────────────────────────────────────
	fmt.Printf("Service OS username [%s]: ", defaultSvcUser)
	svcUserInput := strings.TrimRight(func() string { l, _ := reader.ReadString('\n'); return l }(), "\r\n")
	svcUser := defaultSvcUser
	if strings.TrimSpace(svcUserInput) != "" {
		svcUser = strings.TrimSpace(svcUserInput)
	}

	svcPassword := promptPassword(reader, fmt.Sprintf("Password for OS user '%s' (min 8 chars): ", svcUser))
	if len(svcPassword) < 8 {
		die("OS user password must be at least 8 characters")
	}
	confirmSvcPassword := promptPassword(reader, "Confirm OS user password: ")
	if svcPassword != confirmSvcPassword {
		die("OS user passwords do not match")
	}

	// ── Step 1: Panel domain ──────────────────────────────────────────────────
	domain := prompt(reader, "Panel domain (e.g. panel.example.com): ")
	if domain == "" {
		die("domain cannot be empty")
	}

	// ── Step 2: Superadmin credentials ───────────────────────────────────────
	adminEmail := prompt(reader, "Superadmin email: ")
	if !strings.Contains(adminEmail, "@") {
		die("invalid email address")
	}
	adminName := prompt(reader, "Superadmin full name: ")
	if len(strings.TrimSpace(adminName)) < 2 {
		die("name must be at least 2 characters")
	}
	adminPassword := promptPassword(reader, "Superadmin password (min 8 chars): ")
	if len(adminPassword) < 8 {
		die("password must be at least 8 characters")
	}
	confirmPassword := promptPassword(reader, "Confirm superadmin password: ")
	if adminPassword != confirmPassword {
		die("passwords do not match")
	}

	// ── Step 3: Install system packages ──────────────────────────────────────
	fmt.Println("\n── Installing system packages ──────────────────────────────────")
	if err := installPackages(); err != nil {
		die("package installation failed: %v", err)
	}

	// ── Step 4: Create service OS user + directories ─────────────────────────
	fmt.Println("\n── Preparing service user and directories ──────────────────────")
	createServiceUser(svcUser, svcPassword)
	mustMkdir(dataDir)
	mustMkdir(configDir)
	mustRun("chown", "-R", svcUser+":"+svcUser, dataDir)

	// ── Step 5: Copy binary ───────────────────────────────────────────────────
	fmt.Println("\n── Installing binary ───────────────────────────────────────────")
	self, err := os.Executable()
	if err != nil {
		die("cannot determine binary path: %v", err)
	}
	copyFile(self, binDest)
	mustRun("chmod", "+x", binDest)
	mustRun("chown", "root:"+svcUser, binDest)
	fmt.Printf("  ✓ installed %s\n", binDest)

	// ── Step 6: Generate secrets and write env file ───────────────────────────
	fmt.Println("\n── Writing configuration ───────────────────────────────────────")
	jwtSecret := randomHex(32)
	dbPath := filepath.Join(dataDir, "deploy.db")
	frontendOrigin := "https://" + domain

	envContent := fmt.Sprintf(`# FeatherDeploy — generated %s
DB_PATH=%s
JWT_SECRET=%s
ADDR=127.0.0.1:%s
ORIGIN=%s
`, time.Now().Format(time.RFC3339), dbPath, jwtSecret, backendPort, frontendOrigin)

	writeFile(envFile, envContent, 0600)
	mustRun("chown", "root:"+svcUser, envFile)
	mustRun("chmod", "640", envFile)
	fmt.Printf("  ✓ wrote %s\n", envFile)

	// ── Step 7: Seed the database ─────────────────────────────────────────────
	fmt.Println("\n── Seeding database ────────────────────────────────────────────")
	db, err := appDb.Open(dbPath)
	if err != nil {
		die("cannot open database: %v", err)
	}
	if err := createSuperAdmin(db, adminEmail, adminName, adminPassword); err != nil {
		die("failed to create superadmin: %v", err)
	}
	db.Close()
	mustRun("chown", svcUser+":"+svcUser, dbPath)
	fmt.Printf("  ✓ superadmin created (%s)\n", adminEmail)

	// ── Step 8: Write Caddyfile ───────────────────────────────────────────────
	fmt.Println("\n── Configuring Caddy ───────────────────────────────────────────")
	writeCaddyfile(domain)
	fmt.Printf("  ✓ wrote %s\n", caddyConf)

	// ── Step 9: Systemd service ───────────────────────────────────────────────
	fmt.Println("\n── Installing systemd service ──────────────────────────────────")
	writeSystemdService(svcUser)
	mustRun("systemctl", "daemon-reload")
	mustRun("systemctl", "enable", "featherdeploy")
	mustRun("systemctl", "start", "featherdeploy")
	fmt.Println("  ✓ featherdeploy service enabled and started")

	// ── Step 10: Reload or start Caddy ───────────────────────────────────────
	if _, err := exec.LookPath("systemctl"); err == nil {
		if runSilent("systemctl", "is-active", "--quiet", "caddy") == nil {
			runSilent("systemctl", "reload", "caddy")
		} else {
			mustRun("systemctl", "enable", "caddy")
			mustRun("systemctl", "start", "caddy")
		}
		fmt.Println("  ✓ Caddy reloaded")
	}

	printSuccess(domain, svcUser)
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func printBanner() {
	fmt.Println(`
  _____         _   _          ____             _
 |  ___|__  __ _| |_| |__   ___|  _ \  ___ _ __ | | ___  _   _
 | |_ / _ \/ _' | __| '_ \ / _ \ | | |/ _ \ '_ \| |/ _ \| | | |
 |  _|  __/ (_| | |_| | | |  __/ |_| |  __/ |_) | | (_) | |_| |
 |_|  \___|\__,_|\__|_| |_|\___|____/ \___| .__/|_|\___/ \__, |
                                           |_|             |___/

  FeatherDeploy Installer
  ─────────────────────────────────────────────────────────
  This wizard will install and configure FeatherDeploy
  on this Linux server.  Run as root / sudo.
  ────────────────────────────────────────────────────────
`)
}

func printSuccess(domain, svcUser string) {
	fmt.Printf(`
  ══════════════════════════════════════════════════════
  ✓  FeatherDeploy installed successfully!

     Panel URL  : https://%s
     OS user    : %s  (not root — runs with limited privileges)

     The panel may take 30-60s to start.
     Caddy will automatically obtain a TLS certificate
     from Let's Encrypt for your domain.

     Switch to the service user:
       sudo su - %s

     Check service status:
       sudo systemctl status featherdeploy
       sudo systemctl status caddy

     View logs:
       sudo journalctl -u featherdeploy -f
  ══════════════════════════════════════════════════════
`, domain, svcUser, svcUser)
}

func prompt(r *bufio.Reader, label string) string {
	fmt.Print(label)
	line, _ := r.ReadString('\n')
	return strings.TrimRight(line, "\r\n")
}

func promptPassword(r *bufio.Reader, label string) string {
	// Print a note since we cannot mask input without platform-specific syscalls
	fmt.Print(label + "(input visible) ")
	line, _ := r.ReadString('\n')
	return strings.TrimRight(line, "\r\n")
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "\nERROR: "+format+"\n", args...)
	os.Exit(1)
}

// createServiceUser creates a real login user that owns and runs the service.
// The user can SSH in or `su` to it, but does not have sudo/root access.
func createServiceUser(username, password string) {
	// Create the user if it doesn't already exist
	if runSilent("id", "-u", username) != nil {
		mustRun("useradd",
			"--create-home",
			"--shell", "/bin/bash",
			"--comment", "FeatherDeploy service account",
			username,
		)
		fmt.Printf("  ✓ created OS user: %s\n", username)
	} else {
		fmt.Printf("  OS user '%s' already exists — skipping creation\n", username)
	}
	// Set the password via chpasswd
	cmd := exec.Command("chpasswd")
	cmd.Stdin = strings.NewReader(username + ":" + password)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		slog.Warn("chpasswd failed", "err", err)
	} else {
		fmt.Printf("  ✓ password set for OS user: %s\n", username)
	}
}

func mustRun(name string, args ...string) {
	if _, err := exec.LookPath(name); err != nil {
		slog.Warn("command not found — skipping", "cmd", name)
		return
	}
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		slog.Warn("command failed", "cmd", name, "err", err)
	}
}

func runSilent(name string, args ...string) error {
	return exec.Command(name, args...).Run()
}

func mustMkdir(path string) {
	if err := os.MkdirAll(path, 0755); err != nil {
		die("cannot create directory %s: %v", path, err)
	}
}

func copyFile(src, dst string) {
	data, err := os.ReadFile(src)
	if err != nil {
		die("cannot read %s: %v", src, err)
	}
	if err := os.WriteFile(dst, data, 0755); err != nil {
		die("cannot write %s: %v", dst, err)
	}
}

func writeFile(path, content string, perm os.FileMode) {
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		die("cannot write %s: %v", path, err)
	}
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ─── Package installation ─────────────────────────────────────────────────────

type pkgCmd struct {
	update  []string
	install []string
}

var pkgManagers = map[string]pkgCmd{
	"apt-get": {
		update:  []string{"apt-get", "update", "-y"},
		install: []string{"apt-get", "install", "-y", "podman", "caddy"},
	},
	"dnf": {
		update:  []string{"dnf", "check-update"},
		install: []string{"dnf", "install", "-y", "podman", "caddy"},
	},
	"yum": {
		update:  nil,
		install: []string{"yum", "install", "-y", "podman"},
	},
	"pacman": {
		update:  []string{"pacman", "-Sy"},
		install: []string{"pacman", "-S", "--noconfirm", "podman", "caddy"},
	},
	"apk": {
		update:  []string{"apk", "update"},
		install: []string{"apk", "add", "--no-cache", "podman", "caddy"},
	},
}

func installPackages() error {
	for pm, cmds := range pkgManagers {
		if _, err := exec.LookPath(pm); err != nil {
			continue
		}
		fmt.Printf("  Detected package manager: %s\n", pm)
		if cmds.update != nil {
			cmd := exec.Command(cmds.update[0], cmds.update[1:]...)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			cmd.Run() //nolint — update failures are non-fatal
		}
		cmd := exec.Command(cmds.install[0], cmds.install[1:]...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s: %w", pm, err)
		}
		return nil
	}
	fmt.Println("  WARNING: no supported package manager found.")
	fmt.Println("  Please install podman and caddy manually, then re-run the installer.")
	return nil
}

// ─── Database seed ────────────────────────────────────────────────────────────

func createSuperAdmin(db *sql.DB, email, name, password string) error {
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	_, err = db.Exec(
		`INSERT OR IGNORE INTO users (email, name, password_hash, role) VALUES (?,?,?,?)`,
		email, name, hash, "superadmin",
	)
	return err
}

// ─── Caddyfile ────────────────────────────────────────────────────────────────

const caddyfileTmpl = `# FeatherDeploy — generated by installer
{
    # Global options
    email admin@{{.Domain}}
}

{{.Domain}} {
    # The deploypaaas binary serves both the API and the embedded frontend.
    reverse_proxy 127.0.0.1:{{.Port}}

    # Security headers
    header {
        Strict-Transport-Security "max-age=31536000; includeSubDomains"
        X-Frame-Options "SAMEORIGIN"
        X-Content-Type-Options "nosniff"
        Referrer-Policy "strict-origin-when-cross-origin"
        -Server
    }

    encode gzip
    log {
        output file /var/log/caddy/deploypaaas.log
    }
}
`

func writeCaddyfile(domain string) {
	tmpl := template.Must(template.New("caddy").Parse(caddyfileTmpl))
	var buf strings.Builder
	tmpl.Execute(&buf, struct{ Domain, Port string }{domain, backendPort})
	writeFile(caddyConf, buf.String(), 0644)
	// Ensure Caddy log dir exists
	os.MkdirAll("/var/log/caddy", 0755)
}

// ─── Systemd service ──────────────────────────────────────────────────────────

const systemdTmpl = `[Unit]
Description=FeatherDeploy Panel
Documentation=https://github.com/ojhapranjal26/FeatherDeploy
After=network.target

[Service]
Type=simple
User={{.User}}
Group={{.User}}
EnvironmentFile={{.EnvFile}}
ExecStart={{.Bin}} serve
Restart=always
RestartSec=5s
StandardOutput=journal
StandardError=journal

# Security hardening
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ReadWritePaths={{.DataDir}}

[Install]
WantedBy=multi-user.target
`

func writeSystemdService(svcUser string) {
	tmpl := template.Must(template.New("svc").Parse(systemdTmpl))
	var buf strings.Builder
	tmpl.Execute(&buf, struct{ User, EnvFile, Bin, DataDir string }{
		svcUser, envFile, binDest, dataDir,
	})
	writeFile(systemdUnit, buf.String(), 0644)
}

// ─── Update (non-interactive) ─────────────────────────────────────────────────

// RunUpdate updates an existing FeatherDeploy installation in-place:
//   - The new binary is already in place (build.sh copied it before calling this)
//   - Opens the existing DB so appDb.Open applies any new schema migrations
//   - Restarts the systemd service
//   - Reloads Caddy
func RunUpdate() {
	if runtime.GOOS != "linux" {
		die("update only supported on Linux (got %s)", runtime.GOOS)
	}
	if os.Geteuid() != 0 {
		die("update must be run as root (use sudo)")
	}

	printBanner()
	fmt.Println("  Updating existing FeatherDeploy installation...")
	fmt.Println()

	// Determine DB path from the existing env file
	dbPath := readEnvVar(envFile, "DB_PATH")
	if dbPath == "" {
		dbPath = filepath.Join(dataDir, "deploy.db")
	}

	// ── Run database migrations ───────────────────────────────────────────────
	fmt.Println("── Applying database migrations ────────────────────────────────")
	if _, err := os.Stat(dbPath); err == nil {
		db, err := appDb.Open(dbPath)
		if err != nil {
			die("cannot open database for migration: %v", err)
		}
		db.Close()
		fmt.Printf("  ✓ migrations applied to %s\n", dbPath)
	} else {
		fmt.Println("  (no existing database found — skipping migrations)")
	}

	// ── Restart service ───────────────────────────────────────────────────────
	fmt.Println("\n── Restarting service ──────────────────────────────────────────")
	mustRun("systemctl", "daemon-reload")
	mustRun("systemctl", "restart", "featherdeploy")
	fmt.Println("  ✓ featherdeploy service restarted")

	// ── Reload Caddy ──────────────────────────────────────────────────────────
	if runSilent("systemctl", "is-active", "--quiet", "caddy") == nil {
		runSilent("systemctl", "reload", "caddy")
		fmt.Println("  ✓ Caddy reloaded")
	}

	fmt.Println(`
  ══════════════════════════════════════════════════════
  ✓  FeatherDeploy updated successfully!

     Check status:  sudo systemctl status featherdeploy
     View logs:     sudo journalctl -u featherdeploy -f
  ══════════════════════════════════════════════════════`)
}

// readEnvVar reads a KEY=VALUE pair from a shell env file.
func readEnvVar(file, key string) string {
	data, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[0]) == key {
			return strings.TrimSpace(parts[1])
		}
	}
	return ""
}
