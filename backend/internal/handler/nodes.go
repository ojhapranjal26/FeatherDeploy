package handler

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"bufio"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ojhapranjal26/featherdeploy/backend/internal/heartbeat"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/deploy"
	crypto "github.com/ojhapranjal26/featherdeploy/backend/internal/crypto"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/middleware"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/model"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/netdaemon"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/pki"
	v "github.com/ojhapranjal26/featherdeploy/backend/internal/validator"
)

// NodeHandler manages worker node registration, status, and the join flow.
type NodeHandler struct {
	db         *sql.DB
	jwtSecret  string // used to encrypt/decrypt the CA private key in DB
	envFile    string
	binaryPath string
	domain     string
}

func NewNodeHandler(db *sql.DB, jwtSecret, envFile, binaryPath, domain string) *NodeHandler {
	return &NodeHandler{
		db:         db,
		jwtSecret:  jwtSecret,
		envFile:    envFile,
		binaryPath: binaryPath,
		domain:     domain,
	}
}

// EnsureCA creates a CA in the database if one doesn't already exist.
func (h *NodeHandler) EnsureCA() error {
	var count int
	err := h.db.QueryRow(`SELECT COUNT(*) FROM pki_ca WHERE id=1`).Scan(&count)
	if err != nil || count > 0 {
		return err
	}
	ca, err := pki.GenerateCA("FeatherDeploy Root CA")
	if err != nil {
		return fmt.Errorf("nodes: generate CA: %w", err)
	}
	encKey, err := pki.EncryptKey(ca.KeyPEM, h.jwtSecret)
	if err != nil {
		return fmt.Errorf("nodes: encrypt CA key: %w", err)
	}
	_, err = h.db.Exec(
		`INSERT OR IGNORE INTO pki_ca (id, cert_pem, key_pem) VALUES (1, ?, ?)`,
		ca.CertPEM, encKey,
	)
	return err
}

func (h *NodeHandler) loadCA() (*pki.CA, error) {
	var certPEM, encKeyPEM string
	err := h.db.QueryRow(`SELECT cert_pem, key_pem FROM pki_ca WHERE id=1`).
		Scan(&certPEM, &encKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("nodes: load CA from DB: %w", err)
	}
	keyPEM, err := pki.DecryptKey(encKeyPEM, h.jwtSecret)
	if err != nil {
		return nil, fmt.Errorf("nodes: decrypt CA key: %w", err)
	}
	return &pki.CA{CertPEM: certPEM, KeyPEM: keyPEM}, nil
}

// EnsureLocalPKI ensures that /etc/featherdeploy contains ca.crt, node.crt, and node.key.
// These are required for the main server to communicate with worker nodes via mTLS.
func (h *NodeHandler) EnsureLocalPKI() error {
	ca, err := h.loadCA()
	if err != nil {
		return err
	}

	confDir := "/etc/featherdeploy"
	if err := os.MkdirAll(confDir, 0700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	caFile := confDir + "/ca.crt"
	nodeCertFile := confDir + "/node.crt"
	nodeKeyFile := confDir + "/node.key"

	// Write CA cert
	if err := os.WriteFile(caFile, []byte(ca.CertPEM), 0644); err != nil {
		return fmt.Errorf("write ca.crt: %w", err)
	}

	// Check if node cert/key are missing, empty, or contain invalid PEM data
	var needsGen bool
	if _, err := tls.LoadX509KeyPair(nodeCertFile, nodeKeyFile); err != nil {
		needsGen = true
		slog.Warn("nodes: local mTLS certs are missing or invalid, regenerating...", "err", err)
	}

	if needsGen {
		serverIP := os.Getenv("SERVER_IP")
		if serverIP == "" {
			serverIP = "127.0.0.1"
		}
		// Try to detect real IP if default is loopback
		if serverIP == "127.0.0.1" {
			if conn, err := net.DialTimeout("udp", "1.1.1.1:80", 2*time.Second); err == nil {
				serverIP = conn.LocalAddr().(*net.UDPAddr).IP.String()
				conn.Close()
			}
		}

		nodeCert, err := pki.SignNodeCert(ca, "main", serverIP)
		if err != nil {
			return fmt.Errorf("sign main cert: %w", err)
		}
		if err := os.WriteFile(nodeCertFile, []byte(nodeCert.CertPEM), 0644); err != nil {
			return fmt.Errorf("write node.crt: %w", err)
		}
		if err := os.WriteFile(nodeKeyFile, []byte(nodeCert.KeyPEM), 0600); err != nil {
			return fmt.Errorf("write node.key: %w", err)
		}
		slog.Info("nodes: generated local mTLS certs for main server", "ip", serverIP)
	}

	return nil
}

// GET /api/nodes/{nodeID}/ip-history
func (h *NodeHandler) IPHistory(w http.ResponseWriter, r *http.Request) {
	nodeID, err := strconv.ParseInt(chi.URLParam(r, "nodeID"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid node ID"))
		return
	}

	rows, err := h.db.QueryContext(r.Context(),
		`SELECT old_ip, new_ip, changed_at FROM node_ip_history WHERE node_id=? ORDER BY changed_at DESC`, nodeID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("database error"))
		return
	}
	defer rows.Close()

	type HistoryEntry struct {
		OldIP     string `json:"old_ip"`
		NewIP     string `json:"new_ip"`
		ChangedAt string `json:"changed_at"`
	}
	var history []HistoryEntry
	for rows.Next() {
		var e HistoryEntry
		if err := rows.Scan(&e.OldIP, &e.NewIP, &e.ChangedAt); err == nil {
			history = append(history, e)
		}
	}
	writeJSON(w, http.StatusOK, history)
}

// GET /api/nodes — list all nodes (superadmin/admin only)
func (h *NodeHandler) List(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.QueryContext(r.Context(),
		`SELECT id, name, ip, port, status, rqlite_addr, last_seen, created_at,
		        cpu_usage, ram_used, ram_total, disk_used, disk_total
		 FROM nodes ORDER BY created_at DESC`)
	if err != nil {
		slog.Error("list nodes", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	defer rows.Close()

	nodes := make([]model.NodeSummary, 0)
	for rows.Next() {
		var n model.NodeSummary
		var status, createdAt string
		var rqliteAddr, lastSeen sql.NullString
		if err := rows.Scan(&n.ID, &n.Name, &n.IP, &n.Port, &status,
			&rqliteAddr, &lastSeen, &createdAt,
			&n.CPUUsage, &n.RAMUsed, &n.RAMTotal, &n.DiskUsed, &n.DiskTotal); err != nil {
			continue
		}
		n.Status = model.NodeStatus(status)
		n.RqliteAddr = rqliteAddr.String
		n.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		if lastSeen.Valid {
			t, _ := time.Parse(time.RFC3339, lastSeen.String)
			n.LastSeen = &t
		}
		// Load hostname, OS info, node_id, assigned_domains; check if tunnel is active
		var assignedDomainsJSON string
		h.db.QueryRowContext(r.Context(), `SELECT hostname, os_info, node_id, node_type, assigned_domains FROM nodes WHERE id=?`, n.ID).Scan(&n.Hostname, &n.OSInfo, &n.NodeID, &n.NodeType, &assignedDomainsJSON)
		n.IsBrain = n.NodeType == model.NodeTypeBrain
		if n.NodeID != "" && netdaemon.GlobalTunnel != nil {
			n.TunnelConnected = netdaemon.GlobalTunnel.GetNodeProxyAddr(n.NodeID, 7443) != ""
		}
		if assignedDomainsJSON != "" && assignedDomainsJSON != "[]" {
			json.Unmarshal([]byte(assignedDomainsJSON), &n.AssignedDomains)
		} else {
			n.AssignedDomains = make([]string, 0)
		}
		nodes = append(nodes, n)
	}
	writeJSON(w, http.StatusOK, nodes)
}

// AddNodeRequest is the payload for POST /api/nodes.
type AddNodeRequest struct {
	Name     string   `json:"name"      validate:"required,min=2,max=64"`
	IP       string   `json:"ip"        validate:"required"`
	NodeType model.NodeType `json:"node_type"` // "brain" or "worker" (default: worker)
}

// POST /api/nodes — register a new node slot and generate a join token
func (h *NodeHandler) Add(w http.ResponseWriter, r *http.Request) {
	var req AddNodeRequest
	if !v.DecodeAndValidate(w, r, &req) {
		return
	}

	// The port used for the internal featherdeploy-node API.
	port := 7443

	// Validate node_type; default to worker if not specified
	nodeType := req.NodeType
	if nodeType == "" {
		nodeType = model.NodeTypeWorker
	}
	if nodeType != model.NodeTypeBrain && nodeType != model.NodeTypeWorker {
		writeJSON(w, http.StatusBadRequest, errMap("node_type must be 'brain' or 'worker'"))
		return
	}

	token := randomHex20()
	expires := time.Now().Add(24 * time.Hour)

	_, err := h.db.ExecContext(r.Context(),
		`INSERT INTO nodes (name, ip, port, status, node_type, join_token, token_expires_at)
		 VALUES (?, ?, ?, 'pending', ?, ?, ?)`,
		req.Name, req.IP, port, nodeType, token, expires.Format(time.RFC3339),
	)
	if err != nil {
		if isUnique(err) {
			writeJSON(w, http.StatusConflict, errMap("a node with that name already exists"))
			return
		}
		slog.Error("add node", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}

	var id int64
	h.db.QueryRowContext(r.Context(), `SELECT id FROM nodes WHERE join_token=?`, token).Scan(&id)

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"id":               id,
		"name":             req.Name,
		"node_type":        nodeType,
		"join_token":       token,
		"token_expires_at": expires,
		"join_command":     h.joinCommand(token),
	})
}



// POST /api/nodes/{nodeID}/token — regenerate join token for a pending node
func (h *NodeHandler) RegenerateToken(w http.ResponseWriter, r *http.Request) {
	nodeID, err := strconv.ParseInt(chi.URLParam(r, "nodeID"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid node ID"))
		return
	}

	token := randomHex20()
	expires := time.Now().Add(24 * time.Hour)

	res, err := h.db.ExecContext(r.Context(),
		`UPDATE nodes SET join_token=?, token_expires_at=?, status='pending' WHERE id=?`,
		token, expires.Format(time.RFC3339), nodeID,
	)
	if err != nil {
		slog.Error("regenerate token", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeJSON(w, http.StatusNotFound, errMap("node not found"))
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"join_token":       token,
		"token_expires_at": expires,
		"join_command":     h.joinCommand(token),
	})
}

// GET /api/nodes/brain/logs — SSE stream of brain (main server) logs
// GET /api/nodes/{nodeID}/logs — SSE stream of a worker node's logs via tunnel
func (h *NodeHandler) NodeLogs(w http.ResponseWriter, r *http.Request) {
	nodeParam := chi.URLParam(r, "nodeID")

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Flush immediately to establish the SSE connection and prevent frontend/proxy timeouts
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	if nodeParam == "brain" {
		// Stream local brain logs via journalctl
		cmd := exec.CommandContext(r.Context(),
			"journalctl", "-u", "featherdeploy", "-n", "100", "--follow", "--no-pager", "--output=short-iso")
		
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			fmt.Fprintf(w, "data: {\"error\":\"failed to start journalctl: %s\"}\n\n", err)
			flusher.Flush()
			return
		}
		if err := cmd.Start(); err != nil {
			fmt.Fprintf(w, "data: {\"error\":\"journalctl start: %s\"}\n\n", err)
			flusher.Flush()
			return
		}
		defer cmd.Wait()

		scanner := bufio.NewScanner(stdout)
		lines := make(chan string)
		go func() {
			for scanner.Scan() {
				lines <- scanner.Text()
			}
			close(lines)
		}()

		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-r.Context().Done():
				return
			case line, ok := <-lines:
				if !ok {
					return // EOF
				}
				line = strings.TrimSpace(line)
				if line != "" {
					data, _ := json.Marshal(map[string]string{"line": line})
					fmt.Fprintf(w, "data: %s\n\n", data)
					flusher.Flush()
				}
			case <-ticker.C:
				fmt.Fprintf(w, ": ping\n\n")
				flusher.Flush()
			}
		}
	} else {
		// Worker node — stream logs via the persistent tunnel stream
		nodeID, err := strconv.ParseInt(nodeParam, 10, 64)
		if err != nil {
			fmt.Fprintf(w, "data: {\"error\":\"invalid node id\"}\n\n")
			flusher.Flush()
			return
		}

		var dbNodeID string
		var port int
		err = h.db.QueryRowContext(r.Context(),
			`SELECT node_id, port FROM nodes WHERE id=?`, nodeID).Scan(&dbNodeID, &port)
		if err != nil {
			fmt.Fprintf(w, "data: {\"error\":\"node not found\"}\n\n")
			flusher.Flush()
			return
		}

		// All node API traffic routes through the persistent yamux tunnel.
		// The tunnel proxy gives us a loopback address that maps to the remote port.
		scheme := "https"
		ip := ""
		tunnelPort := port
		if netdaemon.GlobalTunnel != nil {
			proxyAddr := netdaemon.GlobalTunnel.GetNodeProxyAddr(dbNodeID, 7443)
			if proxyAddr == "" && port != 7443 {
				proxyAddr = netdaemon.GlobalTunnel.GetNodeProxyAddr(dbNodeID, port)
			}
			if proxyAddr != "" {
				ip = "127.0.0.1"
				parts := strings.Split(proxyAddr, ":")
				if len(parts) == 2 {
					if p, err2 := strconv.Atoi(parts[1]); err2 == nil {
						tunnelPort = p
					}
				}
			}
		}
		if ip == "" {
			fmt.Fprintf(w, "data: {\"error\":\"node tunnel not connected\"}\n\n")
			flusher.Flush()
			return
		}

		// The node exposes /api/node/logs as an SSE endpoint, we proxy it
		nodeURL := fmt.Sprintf("%s://%s:%d/api/node/logs", scheme, ip, tunnelPort)
		req2, err := http.NewRequestWithContext(r.Context(), "GET", nodeURL, nil)
		if err != nil {
			fmt.Fprintf(w, "data: {\"error\":\"build request: %s\"}\n\n", err)
			flusher.Flush()
			return
		}
		caPEM, _ := os.ReadFile("/etc/featherdeploy/ca.crt")
		certPEM, _ := os.ReadFile("/etc/featherdeploy/node.crt")
		keyPEM, _ := os.ReadFile("/etc/featherdeploy/node.key")
		tlsCfg, errTls := pki.TLSConfig(string(certPEM), string(keyPEM), string(caPEM))
		if errTls != nil {
			tlsCfg = &tls.Config{InsecureSkipVerify: true}
		} else {
			tlsCfg.InsecureSkipVerify = true
		}
		client := &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: tlsCfg,
			},
			Timeout: 0,
		}
		resp2, err := client.Do(req2)
		if err != nil {
			fmt.Fprintf(w, "data: {\"error\":\"connect to node: %s\"}\n\n", err)
			flusher.Flush()
			return
		}
		defer resp2.Body.Close()

		type readResult struct {
			data []byte
			err  error
		}
		ch := make(chan readResult)
		go func() {
			buf := make([]byte, 4096)
			for {
				n, err := resp2.Body.Read(buf)
				if n > 0 {
					data := make([]byte, n)
					copy(data, buf[:n])
					ch <- readResult{data: data, err: nil}
				}
				if err != nil {
					ch <- readResult{data: nil, err: err}
					close(ch)
					return
				}
			}
		}()

		for {
			select {
			case <-r.Context().Done():
				return
			case res, ok := <-ch:
				if !ok {
					return
				}
				if len(res.data) > 0 {
					w.Write(res.data)
					flusher.Flush()
				}
				if res.err != nil {
					return
				}
			}
		}
	}
}

// DELETE /api/nodes/{nodeID} — remove a node
func (h *NodeHandler) Delete(w http.ResponseWriter, r *http.Request) {
	nodeID, err := strconv.ParseInt(chi.URLParam(r, "nodeID"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid node ID"))
		return
	}
	res, err := h.db.ExecContext(r.Context(), `DELETE FROM nodes WHERE id=?`, nodeID)
	if err != nil {
		slog.Error("delete node", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeJSON(w, http.StatusNotFound, errMap("node not found"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /api/nodes/{token}/join-script — serve the bash join script (no auth required;
// the token itself is the credential)
func (h *NodeHandler) JoinScript(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	if !h.tokenValid(r, token) {
		writeJSON(w, http.StatusNotFound, errMap("invalid or expired token"))
		return
	}

	ca, err := h.loadCA()
	if err != nil {
		slog.Error("join-script: load CA", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}

	scheme := "https"
	if strings.Contains(h.domain, "localhost") || strings.HasPrefix(h.domain, "127.") {
		scheme = "http"
	}
	mainURL := fmt.Sprintf("%s://%s", scheme, h.domain)

	sshPubKey := heartbeat.GetSSHPublicKey(h.db)

	script, err := renderJoinScript(mainURL, token, ca.CertPEM, sshPubKey)
	if err != nil {
		slog.Error("render join script", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}

	w.Header().Set("Content-Type", "text/x-shellscript")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(script))
}

// GET /api/nodes/binary/hash — returns the SHA256 hash of the node binary for auto-updates
func (h *NodeHandler) BinaryHash(w http.ResponseWriter, r *http.Request) {
	path := h.binaryPath
	if path == "" {
		path = "/usr/local/bin/featherdeploy-node"
	}
	f, err := os.Open(path)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errMap("node binary not found"))
		return
	}
	defer f.Close()

	hsh := sha256.New()
	if _, err := io.Copy(hsh, f); err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("failed to hash binary"))
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"hash": hex.EncodeToString(hsh.Sum(nil)),
	})
}

// GET /api/nodes/binary — serve the featherdeploy-node binary download
func (h *NodeHandler) BinaryDownload(w http.ResponseWriter, r *http.Request) {
	// Only allow authenticated superadmins or valid join-token requests
	// (token passed as ?token=...)
	token := r.URL.Query().Get("token")
	if token != "" {
		if !h.tokenValid(r, token) {
			writeJSON(w, http.StatusUnauthorized, errMap("invalid token"))
			return
		}
	} else {
		claims := middleware.GetClaims(r.Context())
		if claims == nil || (claims.Role != model.RoleSuperAdmin && claims.Role != model.RoleAdmin) {
			writeJSON(w, http.StatusForbidden, errMap("forbidden"))
			return
		}
	}

	path := h.binaryPath
	if path == "" {
		path = "/usr/local/bin/featherdeploy-node"
	}
	f, err := os.Open(path)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errMap("node binary not found on this server"))
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="featherdeploy-node"`)
	io.Copy(w, f)
}

// POST /api/nodes/{token}/complete-join — called by featherdeploy-node during join.
// The node sends its name and rqlite_addr; the main server:
//   - signs a TLS cert for the node
//   - returns CA cert, signed cert, node key, and encrypted env vars
func (h *NodeHandler) CompleteJoin(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	if !h.tokenValid(r, token) {
		writeJSON(w, http.StatusUnauthorized, errMap("invalid or expired token"))
		return
	}

	var payload struct {
		RqliteAddr string `json:"rqlite_addr"` // host:port for rqlite Raft (informational)
		Hostname   string `json:"hostname"`
		OSInfo     string `json:"os_info"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid JSON"))
		return
	}

	// Detect actual source IP (for record keeping only; not used for routing)
	nodeIP := r.Header.Get("X-Forwarded-For")
	if nodeIP == "" {
		nodeIP = r.Header.Get("X-Real-IP")
	}
	if nodeIP == "" {
		nodeIP, _, _ = net.SplitHostPort(r.RemoteAddr)
	}
	if nodeIP == "" {
		nodeIP = r.RemoteAddr
	}
	if idx := strings.Index(nodeIP, ","); idx >= 0 {
		nodeIP = strings.TrimSpace(nodeIP[:idx])
	}

	// Load node info (including node_type so we can tell the node its role)
	var node model.Node
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id, name, ip, port, node_type FROM nodes WHERE join_token=?`, token).
		Scan(&node.ID, &node.Name, &node.IP, &node.Port, &node.NodeType)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errMap("node not found"))
		return
	}

	// Node ID is assigned deterministically as "node-<db_id>"
	// This is the stable identity used for tunnel sessions and service routing.
	nodeID := fmt.Sprintf("node-%d", node.ID)

	// Sign certificate
	ca, err := h.loadCA()
	if err != nil {
		slog.Error("complete-join: load CA", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("CA not available"))
		return
	}
	nodeCert, err := pki.SignNodeCert(ca, node.Name, nodeIP)
	if err != nil {
		slog.Error("complete-join: sign cert", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("cert signing failed"))
		return
	}

	// Encrypt env vars using the join token as passphrase
	envVars := h.readEnvFile()
	encryptedEnv, err := crypto.Encrypt(envVars, token)
	if err != nil {
		slog.Error("complete-join: encrypt env", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("env encryption failed"))
		return
	}

	// Generate a permanent tunnel token (separate from the one-time registration join_token)
	tunnelToken := randomHex20()

	// Update node record: mark connected, save cert, clear join token, save source IP
	// IP is stored for display/SSH purposes only — all cluster traffic goes through tunnel
	_, err = h.db.ExecContext(r.Context(),
		`UPDATE nodes SET status='connected', ip=?, hostname=?, os_info=?, node_id=?, node_cert_pem=?,
		 rqlite_addr=?, join_token=NULL, token_expires_at=NULL, last_seen=datetime('now'),
		 port=7443, tunnel_token=?, updated_at=datetime('now') WHERE id=?`,
		nodeIP, payload.Hostname, payload.OSInfo, nodeID, nodeCert.CertPEM, payload.RqliteAddr, tunnelToken, node.ID,
	)
	if err != nil {
		slog.Error("complete-join: update node", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("DB update failed"))
		return
	}

	// Trigger firewall reconciliation (for iptables protection of cluster ports)
	deploy.ReconcileNodeRqliteIPTables(h.db)

	// Fetch SSH public key so the node can add it to authorized_keys
	sshPubKey := heartbeat.GetSSHPublicKey(h.db)

	// Cluster transport addresses:
	// rqlite and etcd traffic travels through the persistent yamux tunnel.
	// The node sets up local proxy ports (127.0.0.1:4001→brain via tunnel) during
	// featherdeploy-node serve startup, so we advertise loopback addresses.
	rqliteMain := "127.0.0.1:4001" // proxy port the node establishes on its own loopback
	etcdMain := "main=http://127.0.0.1:2380"

	// Fetch current cluster leader info so the joining node can connect correctly
	var leaderNodeID, leaderPublicIP string
	h.db.QueryRowContext(r.Context(), `SELECT leader_node_id, leader_public_ip FROM cluster_state WHERE id=1`).Scan(&leaderNodeID, &leaderPublicIP)
	if leaderPublicIP == "" {
		leaderPublicIP = os.Getenv("SERVER_IP")
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"node_id":        nodeID,
		"node_type":      string(node.NodeType),
		"ca_cert_pem":    ca.CertPEM,
		"node_cert_pem":  nodeCert.CertPEM,
		"node_key_pem":   nodeCert.KeyPEM,
		"encrypted_env":  encryptedEnv,
		"rqlite_main":    rqliteMain,
		"etcd_main":      etcdMain,
		"ssh_public_key": sshPubKey,
		"node_ip":        nodeIP,
		"tunnel_token":   tunnelToken,
		"leader_node_id": leaderNodeID,
		"leader_ip":      leaderPublicIP,
	})
}


func isPrivateIP(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil {
		switch {
		case ip4[0] == 10:
			return true
		case ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31:
			return true
		case ip4[0] == 192 && ip4[1] == 168:
			return true
		}
	}
	return false
}

// POST /api/nodes/{nodeID}/ping — node heartbeat + stats
func (h *NodeHandler) Ping(w http.ResponseWriter, r *http.Request) {
	nodeID, err := strconv.ParseInt(chi.URLParam(r, "nodeID"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid node ID"))
		return
	}

	var req model.NodePingRequest
	json.NewDecoder(r.Body).Decode(&req)

	status := string(req.Status)
	if status == "" {
		status = "connected"
	}

	h.db.ExecContext(r.Context(),
		`UPDATE nodes SET status=?, rqlite_addr=?, last_seen=datetime('now'),
		 cpu_usage=?, ram_used=?, ram_total=?,
		 disk_used=?, disk_total=?, last_stats_at=datetime('now'),
		 updated_at=datetime('now') WHERE id=?`,
		status, req.RqliteAddr,
		req.CPUUsage, req.RAMUsed, req.RAMTotal,
		req.DiskUsed, req.DiskTotal,
		nodeID,
	)
	writeJSON(w, http.StatusOK, map[string]string{"ok": "1"})
}

// GET /api/cluster/brain — returns current brain info + stats
func (h *NodeHandler) ClusterBrain(w http.ResponseWriter, r *http.Request) {
	brain, err := heartbeat.ReadBrain(h.db)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errMap("cluster state not available"))
		return
	}
	writeJSON(w, http.StatusOK, brain)
}

// GET /api/nodes/{nodeID}/ssh-command — returns the SSH command to connect to this node
// without a password (key-based, using the cluster SSH key)
func (h *NodeHandler) SSHCommand(w http.ResponseWriter, r *http.Request) {
	nodeID, err := strconv.ParseInt(chi.URLParam(r, "nodeID"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid node ID"))
		return
	}

	var ip string
	err = h.db.QueryRowContext(r.Context(), `SELECT ip FROM nodes WHERE id=?`, nodeID).Scan(&ip)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errMap("node not found"))
		return
	}

	keyPath := "/etc/featherdeploy/ssh_id"
	cmd := fmt.Sprintf("ssh -i %s -o StrictHostKeyChecking=no root@%s", keyPath, ip)
	writeJSON(w, http.StatusOK, map[string]string{
		"command":  cmd,
		"key_path": keyPath,
		"note":     "Run this command from the main server terminal (or from any host that has the private key). Key is passwordless.",
	})
}

// POST /api/nodes/{nodeID}/rotate-wireguard — removed (WireGuard replaced by tunnel transport)
func (h *NodeHandler) RotateWireguard(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusGone, errMap("WireGuard mesh has been removed; cluster uses persistent tunnel transport instead"))
}

// GET /api/cluster/github-token — returns a short-lived GitHub App installation token.
// Called by worker nodes over the tunnel before a git clone so they can access private repos.
// Only reachable from loopback (127.0.0.1) — enforced at handler level.
// The node authenticates via its tunnel_token passed in the X-Node-Token header.
func (h *NodeHandler) ClusterGitHubToken(w http.ResponseWriter, r *http.Request) {
	// Only allow from localhost (tunnel proxy)
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if host != "127.0.0.1" && host != "::1" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// Validate node tunnel token
	nodeToken := r.Header.Get("X-Node-Token")
	if nodeToken == "" {
		http.Error(w, "missing X-Node-Token", http.StatusUnauthorized)
		return
	}
	var nodeID string
	h.db.QueryRowContext(r.Context(),
		`SELECT node_id FROM nodes WHERE tunnel_token=? OR tunnel_token_prev=?`, nodeToken, nodeToken).Scan(&nodeID)
	if nodeID == "" {
		http.Error(w, "invalid node token", http.StatusUnauthorized)
		return
	}

	// Fetch GitHub App installation token from app_settings
	var encInstToken string
	h.db.QueryRowContext(r.Context(), `SELECT enc_value FROM app_settings WHERE key='github_installation_token'`).Scan(&encInstToken)
	if encInstToken != "" {
		plainToken, err := crypto.Decrypt(encInstToken[len("fdenc:"):], h.jwtSecret)
		if err == nil {
			writeJSON(w, http.StatusOK, map[string]string{"token": plainToken, "type": "installation"})
			return
		}
	}

	// Fallback: return empty (node will try with no token or user's OAuth)
	writeJSON(w, http.StatusOK, map[string]string{"token": "", "type": "none"})
}

// GET /api/cluster/leader — returns the current cluster leader node_id and public IP.
// Nodes call this to know where to direct Raft/rqlite connections.
// Accessible both from the tunnel (127.0.0.1) and internally.
func (h *NodeHandler) ClusterLeader(w http.ResponseWriter, r *http.Request) {
	var leaderNodeID, leaderPublicIP string
	h.db.QueryRowContext(r.Context(),
		`SELECT leader_node_id, leader_public_ip FROM cluster_state WHERE id=1`).
		Scan(&leaderNodeID, &leaderPublicIP)

	if leaderNodeID == "" {
		leaderNodeID = "main"
	}
	if leaderPublicIP == "" {
		leaderPublicIP = os.Getenv("SERVER_IP")
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"leader_node_id": leaderNodeID,
		"leader_ip":      leaderPublicIP,
	})
}

// POST /api/cluster/leader — called by the elected brain to announce its leadership.
// Only brain nodes (node_type='brain') may call this. Authenticated via tunnel_token.
func (h *NodeHandler) AnnounceLeader(w http.ResponseWriter, r *http.Request) {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if host != "127.0.0.1" && host != "::1" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	nodeToken := r.Header.Get("X-Node-Token")
	var nodeID, nodeType, nodeIP string
	h.db.QueryRowContext(r.Context(),
		`SELECT node_id, node_type, ip FROM nodes WHERE tunnel_token=? OR tunnel_token_prev=?`, nodeToken, nodeToken).
		Scan(&nodeID, &nodeType, &nodeIP)
	if nodeID == "" {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}
	if nodeType != string(model.NodeTypeBrain) {
		http.Error(w, "only brain nodes can announce leadership", http.StatusForbidden)
		return
	}

	var payload struct {
		PublicIP string `json:"public_ip"`
	}
	json.NewDecoder(r.Body).Decode(&payload)
	if payload.PublicIP == "" {
		payload.PublicIP = nodeIP
	}

	h.db.ExecContext(r.Context(),
		`UPDATE cluster_state SET leader_node_id=?, leader_public_ip=?, leader_updated_at=datetime('now') WHERE id=1`,
		nodeID, payload.PublicIP)

	slog.Info("cluster: leader announced", "node_id", nodeID, "ip", payload.PublicIP)
	writeJSON(w, http.StatusOK, map[string]string{"ok": "1", "leader": nodeID})
}


// GET /api/nodes/server-binary?token=TOKEN — serve the main featherdeploy server binary
// Nodes download this during join so they can promote to brain.
func (h *NodeHandler) ServerBinaryDownload(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token != "" {
		if !h.tokenValid(r, token) {
			writeJSON(w, http.StatusUnauthorized, errMap("invalid token"))
			return
		}
	} else {
		claims := middleware.GetClaims(r.Context())
		if claims == nil || (claims.Role != model.RoleSuperAdmin && claims.Role != model.RoleAdmin) {
			writeJSON(w, http.StatusForbidden, errMap("forbidden"))
			return
		}
	}

	path := "/usr/local/bin/featherdeploy"
	f, err := os.Open(path)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errMap("server binary not found — ensure featherdeploy is installed"))
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="featherdeploy"`)
	io.Copy(w, f)
}

// GET /api/nodes/{nodeID}/ca-cert — returns the CA cert (public info, no auth needed for nodes)
func (h *NodeHandler) CACert(w http.ResponseWriter, r *http.Request) {
	var certPEM string
	err := h.db.QueryRow(`SELECT cert_pem FROM pki_ca WHERE id=1`).Scan(&certPEM)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errMap("CA not initialized"))
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(certPEM))
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func (h *NodeHandler) tokenValid(r *http.Request, token string) bool {
	var expiresAt string
	err := h.db.QueryRow(
		`SELECT token_expires_at FROM nodes WHERE join_token=? AND status='pending'`, token,
	).Scan(&expiresAt)
	if err != nil {
		return false
	}
	if expiresAt == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return false
	}
	return time.Now().Before(t)
}

func (h *NodeHandler) joinCommand(token string) string {
	scheme := "https"
	if strings.Contains(h.domain, "localhost") || strings.HasPrefix(h.domain, "127.") {
		scheme = "http"
	}
	return fmt.Sprintf("curl -fsSL %s://%s/api/nodes/%s/join-script | sudo bash",
		scheme, h.domain, token)
}

func (h *NodeHandler) readEnvFile() string {
	if h.envFile == "" {
		return ""
	}
	data, _ := os.ReadFile(h.envFile)
	return string(data)
}

func randomHex20() string {
	b := make([]byte, 20)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ─── join script template ─────────────────────────────────────────────────────

const joinScriptTmpl = `#!/usr/bin/env bash
# FeatherDeploy node join script — generated by FeatherDeploy
# Run with: curl -fsSL {{.MainURL}}/api/nodes/{{.Token}}/join-script | sudo bash
set -euo pipefail

MAIN_URL="{{.MainURL}}"
JOIN_TOKEN="{{.Token}}"
NODE_BINARY="/usr/local/bin/featherdeploy-node"
SERVER_BINARY="/usr/local/bin/featherdeploy"
RQLITE_VER="8.36.5"
ETCD_VER="3.5.13"
SVC_USER="featherdeploy"
CA_CERT='{{.CACert}}'
SSH_PUB_KEY='{{.SSHPubKey}}'

if [ "$(id -u)" -ne 0 ]; then
	echo "ERROR: run as root (sudo)." >&2; exit 1
fi

echo "==> FeatherDeploy Node Setup"
echo "    Main server: $MAIN_URL"
echo ""

run_as_user_session() {
	local user="$1"
	shift
	local shell_cmd="$*"
	if command -v systemd-run >/dev/null 2>&1; then
		systemd-run --machine="${user}@" --quiet --user --collect --pipe --wait \
			/bin/sh -lc "cd / && ${shell_cmd}" && return 0
	fi
	su -s /bin/sh "$user" -c "cd / && ${shell_cmd}"
}

reset_host() {
	echo "==> Resetting previous FeatherDeploy/node state..."
	for svc in featherdeploy featherdeploy-node featherdeploy-brain rqlite rqlite-node etcd etcd-node; do
		systemctl stop "$svc" 2>/dev/null || true
		systemctl disable "$svc" 2>/dev/null || true
	done
	if id -u "$SVC_USER" >/dev/null 2>&1; then
		local svc_uid svc_home
		svc_uid=$(id -u "$SVC_USER")
		svc_home=$(getent passwd "$SVC_USER" | cut -d: -f6 || echo "/var/lib/featherdeploy")
		install -d -m 700 -o "$SVC_USER" -g "$SVC_USER" "/run/user/${svc_uid}" "/run/user/${svc_uid}/containers"
		if command -v podman >/dev/null 2>&1; then
			run_as_user_session "$SVC_USER" \
				"HOME=${svc_home} XDG_RUNTIME_DIR=/run/user/${svc_uid} XDG_CONFIG_HOME=${svc_home}/.config XDG_DATA_HOME=${svc_home}/.local/share XDG_CACHE_HOME=${svc_home}/.cache podman system reset --force 2>&1" \
				|| true
		fi
		if command -v loginctl >/dev/null 2>&1; then
			loginctl disable-linger "$SVC_USER" 2>/dev/null || true
		fi
		pkill -9 -u "$SVC_USER" 2>/dev/null || true
	fi
	rm -f /etc/systemd/system/featherdeploy.service
	rm -f /etc/systemd/system/featherdeploy-node.service
	rm -f /etc/systemd/system/featherdeploy-brain.service
	rm -f /etc/systemd/system/rqlite.service
	rm -f /etc/systemd/system/rqlite-node.service
	rm -f /etc/systemd/system/etcd.service
	rm -f /etc/systemd/system/etcd-node.service
	systemctl daemon-reload
	systemctl reset-failed featherdeploy featherdeploy-node featherdeploy-brain rqlite rqlite-node etcd etcd-node 2>/dev/null || true
	rm -f /usr/local/bin/featherdeploy
	rm -f /usr/local/bin/featherdeploy-node
	rm -f /usr/local/bin/featherdeploy-update
	rm -f /usr/local/bin/rqlite
	rm -f /usr/local/bin/rqlited
	rm -f /usr/local/bin/etcd
	rm -f /usr/local/bin/etcdctl
	rm -rf /etc/featherdeploy /var/lib/featherdeploy /home/featherdeploy /run/featherdeploy-runtime
	rm -rf /etc/containers /var/lib/containers /var/cache/libpod
	sed -i "/^${SVC_USER}:/d" /etc/subuid 2>/dev/null || true
	sed -i "/^${SVC_USER}:/d" /etc/subgid 2>/dev/null || true
	rm -f "/var/lib/systemd/linger/${SVC_USER}"
	if id -u "$SVC_USER" >/dev/null 2>&1; then
		userdel -r "$SVC_USER" 2>/dev/null || userdel "$SVC_USER" 2>/dev/null || true
	fi
	echo "  Previous state cleared"
}

install_rqlite() {
	command -v rqlited >/dev/null 2>&1 && { echo "  rqlited already installed"; return; }
	echo "==> Installing rqlite ${RQLITE_VER}..."
	local TAR="rqlite-v${RQLITE_VER}-linux-amd64.tar.gz"
	local TMP_DIR="/tmp/rqlite-install"
	mkdir -p "$TMP_DIR"
	if ! curl -fsSL "https://github.com/rqlite/rqlite/releases/download/v${RQLITE_VER}/${TAR}" -o "$TMP_DIR/${TAR}"; then
		echo "ERROR: Failed to download rqlite." >&2; exit 1
	fi
	tar -xzf "$TMP_DIR/${TAR}" -C "$TMP_DIR"
	local BIN_DIR; BIN_DIR=$(find "$TMP_DIR" -maxdepth 1 -type d -name "rqlite-v*" | head -1)
	if [ -z "$BIN_DIR" ]; then
		BIN_DIR="$TMP_DIR/rqlite-v${RQLITE_VER}-linux-amd64"
	fi
	install -m 755 "$BIN_DIR/rqlited" /usr/local/bin/rqlited
	install -m 755 "$BIN_DIR/rqlite"  /usr/local/bin/rqlite
	rm -rf "$TMP_DIR"
	echo "  rqlited installed"
}

install_etcd() {
	command -v etcd >/dev/null 2>&1 && { echo "  etcd already installed"; return; }
	echo "==> Installing etcd ${ETCD_VER}..."
	local TAR="etcd-v${ETCD_VER}-linux-amd64.tar.gz"
	local TMP_DIR="/tmp/etcd-install"
	mkdir -p "$TMP_DIR"
	if ! curl -fsSL "https://github.com/etcd-io/etcd/releases/download/v${ETCD_VER}/${TAR}" -o "$TMP_DIR/${TAR}"; then
		echo "ERROR: Failed to download etcd." >&2; exit 1
	fi
	tar -xzf "$TMP_DIR/${TAR}" -C "$TMP_DIR"
	local BIN_DIR; BIN_DIR=$(find "$TMP_DIR" -maxdepth 1 -type d -name "etcd-v*" | head -1)
	install -m 755 "$BIN_DIR/etcd" /usr/local/bin/etcd
	install -m 755 "$BIN_DIR/etcdctl" /usr/local/bin/etcdctl
	rm -rf "$TMP_DIR"
	echo "  etcd installed"
}

configure_crun() {
	command -v crun >/dev/null 2>&1 || return
	mkdir -p /etc/containers
	cat > /etc/containers/containers.conf <<'CONF'
[engine]
runtime = "crun"
cgroup_manager = "cgroupfs"
helper_binaries_dir = ["/usr/libexec/podman", "/usr/lib/podman", "/usr/local/lib/podman", "/usr/bin", "/usr/local/bin"]

[network]
network_backend = "netavark"
default_rootless_network_cmd = "slirp4netns"
CONF
	echo "  crun configured as Podman runtime"
}

ensure_service_user() {
	if ! id -u "$SVC_USER" >/dev/null 2>&1; then
		useradd --system --home-dir /var/lib/featherdeploy --create-home --shell /usr/sbin/nologin "$SVC_USER"
		echo "  Created service user: $SVC_USER"
	fi
	mkdir -p /var/lib/featherdeploy
	chown -R "$SVC_USER:$SVC_USER" /var/lib/featherdeploy
}

configure_rootless_podman() {
	local svc_home svc_uid svc_netdir
	svc_home=$(getent passwd "$SVC_USER" | cut -d: -f6 || echo "/var/lib/featherdeploy")
	svc_uid=$(id -u "$SVC_USER")
	svc_netdir="${svc_home}/.local/share/containers/storage/networks"
	install -d -m 700 -o "$SVC_USER" -g "$SVC_USER" "/run/user/${svc_uid}" "/run/user/${svc_uid}/containers"
	for subfile in /etc/subuid /etc/subgid; do
		grep -q "^${SVC_USER}:" "$subfile" 2>/dev/null || echo "${SVC_USER}:100000:65536" >> "$subfile"
	done
	for newmap in /usr/bin/newuidmap /usr/bin/newgidmap /usr/sbin/newuidmap /usr/sbin/newgidmap; do
		[ -f "$newmap" ] && chmod u+s "$newmap" || true
	done
	if command -v loginctl >/dev/null 2>&1; then
		loginctl enable-linger "$SVC_USER" 2>/dev/null || true
	fi

	mkdir -p "${svc_home}/.config/containers" "${svc_netdir}" "${svc_home}/.cache"
	cat > "${svc_home}/.config/containers/containers.conf" <<USERCONF
[engine]
cgroup_manager = "cgroupfs"

[network]
network_backend = "netavark"
default_rootless_network_cmd = "slirp4netns"
network_config_dir = "${svc_netdir}"
USERCONF
	rm -rf "${svc_home}/.config/containers/networks"
	chown -R "$SVC_USER:$SVC_USER" "${svc_home}" "/run/user/${svc_uid}"

	if command -v podman >/dev/null 2>&1; then
		run_as_user_session "$SVC_USER" \
			"HOME=${svc_home} XDG_RUNTIME_DIR=/run/user/${svc_uid} XDG_CONFIG_HOME=${svc_home}/.config XDG_DATA_HOME=${svc_home}/.local/share XDG_CACHE_HOME=${svc_home}/.cache podman system migrate" \
			>/dev/null 2>&1 || true
	fi
	echo "  Rootless Podman prepared for $SVC_USER"
}

reset_host

if command -v apt-get >/dev/null 2>&1; then
	export DEBIAN_FRONTEND=noninteractive
	apt-get update -y -q
	apt-get install -y -q curl uidmap slirp4netns netavark aardvark-dns passt containernetworking-plugins 2>/dev/null || true
	apt-get install -y -q podman crun caddy openssh-server 2>/dev/null || apt-get install -y -q curl podman caddy openssh-server uidmap
elif command -v dnf >/dev/null 2>&1; then
	dnf install -y -q curl shadow-utils slirp4netns netavark aardvark-dns passt containernetworking-plugins 2>/dev/null || true
	dnf install -y -q podman crun caddy openssh-server 2>/dev/null || dnf install -y -q curl podman caddy openssh-server
elif command -v yum >/dev/null 2>&1; then
	yum install -y -q curl shadow-utils slirp4netns netavark aardvark-dns passt containernetworking-plugins 2>/dev/null || true
	yum install -y -q podman crun openssh-server 2>/dev/null || yum install -y -q curl podman openssh-server
elif command -v apk >/dev/null 2>&1; then
	apk add --no-cache curl podman caddy crun openssh 2>/dev/null || apk add --no-cache curl podman caddy openssh
	apk add --no-cache slirp4netns netavark aardvark-dns passt 2>/dev/null || true
fi

install_rqlite
install_etcd
configure_crun
ensure_service_user
configure_rootless_podman

# -- Passwordless SSH: install brain's public key -----------------------------
if [ -n "$SSH_PUB_KEY" ]; then
	mkdir -p /root/.ssh
	chmod 700 /root/.ssh
	grep -qxF "$SSH_PUB_KEY" /root/.ssh/authorized_keys 2>/dev/null || \
		echo "$SSH_PUB_KEY" >> /root/.ssh/authorized_keys
	chmod 600 /root/.ssh/authorized_keys
	systemctl enable --now ssh sshd 2>/dev/null || true
	echo "  SSH public key installed (passwordless access configured)"
fi

# -- Download featherdeploy-node binary ---------------------------------------
echo "==> Downloading featherdeploy-node..."
curl -fsSL "${MAIN_URL}/api/nodes/binary?token=${JOIN_TOKEN}" -o "$NODE_BINARY"
chmod +x "$NODE_BINARY"
echo "  Node binary installed: $NODE_BINARY"

# -- Download featherdeploy server binary (needed for brain failover) ---------
echo "==> Downloading featherdeploy server binary (for failover)..."
curl -fsSL "${MAIN_URL}/api/nodes/server-binary?token=${JOIN_TOKEN}" -o "$SERVER_BINARY" || \
	echo "  (server binary not available yet — will retry on next update)"
chmod +x "$SERVER_BINARY" 2>/dev/null || true
echo "  Server binary cached: $SERVER_BINARY"

# -- Save CA certificate ------------------------------------------------------
mkdir -p /etc/featherdeploy
printf '%s' "$CA_CERT" > /etc/featherdeploy/ca.crt
chmod 644 /etc/featherdeploy/ca.crt
echo "  CA certificate saved"

# -- Run node join wizard -----------------------------------------------------
echo ""
echo "==> Running node join wizard..."
exec "$NODE_BINARY" join --main="$MAIN_URL" --token="$JOIN_TOKEN"
`

// POST /api/node/db-backup (internal mTLS route)
func (h *NodeHandler) DatabaseBackupStream(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DBID      int64  `json:"db_id"`
		JWTSecret string `json:"jwt_secret"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid request"))
		return
	}

	w.Header().Set("Content-Type", "application/x-tar")
	// Migration backup ALWAYS stops the container to ensure 100% data consistency
	if err := deploy.StreamDatabaseBackup(h.db, req.JWTSecret, req.DBID, true, w); err != nil {
		slog.Error("node: db backup stream failed", "db_id", req.DBID, "err", err)
	}
}

func renderJoinScript(mainURL, token, caCert, sshPubKey string) (string, error) {
	tmpl, err := template.New("join").Parse(joinScriptTmpl)
	if err != nil {
		return "", err
	}
	var buf strings.Builder
	err = tmpl.Execute(&buf, struct {
		MainURL   string
		Token     string
		CACert    string
		SSHPubKey string
	}{mainURL, token, caCert, sshPubKey})
	return buf.String(), err
}

