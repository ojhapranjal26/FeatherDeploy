package handler

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/deploy-paas/backend/internal/crypto"
	"github.com/deploy-paas/backend/internal/middleware"
	"github.com/deploy-paas/backend/internal/model"
	"github.com/deploy-paas/backend/internal/pki"
	v "github.com/deploy-paas/backend/internal/validator"
)

// NodeHandler manages worker node registration, status, and the join flow.
type NodeHandler struct {
	db         *sql.DB
	jwtSecret  string // used to encrypt/decrypt the CA private key in DB
	envFile    string // path to the main server's env file (shared with nodes)
	binaryPath string // path to featherdeploy-node binary
	domain     string // public domain of this main server
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

// GET /api/nodes — list all nodes (superadmin/admin only)
func (h *NodeHandler) List(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.QueryContext(r.Context(),
		`SELECT id, name, ip, port, status, rqlite_addr, last_seen, created_at
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
			&rqliteAddr, &lastSeen, &createdAt); err != nil {
			continue
		}
		n.Status = model.NodeStatus(status)
		n.RqliteAddr = rqliteAddr.String
		n.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		if lastSeen.Valid {
			t, _ := time.Parse(time.RFC3339, lastSeen.String)
			n.LastSeen = &t
		}
		nodes = append(nodes, n)
	}
	writeJSON(w, http.StatusOK, nodes)
}

// POST /api/nodes — register a new node slot and generate a join token
func (h *NodeHandler) Add(w http.ResponseWriter, r *http.Request) {
	var req model.AddNodeRequest
	if !v.DecodeAndValidate(w, r, &req) {
		return
	}

	if req.Port == 0 {
		req.Port = 7443
	}

	token := randomHex20()
	expires := time.Now().Add(24 * time.Hour)

	_, err := h.db.ExecContext(r.Context(),
		`INSERT INTO nodes (name, ip, port, status, join_token, token_expires_at)
		 VALUES (?, ?, ?, 'pending', ?, ?)`,
		req.Name, req.IP, req.Port, token, expires.Format(time.RFC3339),
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
		"join_token":       token,
		"token_expires_at": expires,
		"join_command":     h.joinCommand(token),
	})
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

	script, err := renderJoinScript(mainURL, token, ca.CertPEM)
	if err != nil {
		slog.Error("render join script", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}

	w.Header().Set("Content-Type", "text/x-shellscript")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(script))
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
		if claims.Role != model.RoleSuperAdmin && claims.Role != model.RoleAdmin {
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
		RqliteAddr string `json:"rqlite_addr"` // host:port for rqlite Raft
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid JSON"))
		return
	}

	// Load node info
	var node model.Node
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id, name, ip, port FROM nodes WHERE join_token=?`, token).
		Scan(&node.ID, &node.Name, &node.IP, &node.Port)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errMap("node not found"))
		return
	}

	// Sign certificate
	ca, err := h.loadCA()
	if err != nil {
		slog.Error("complete-join: load CA", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("CA not available"))
		return
	}
	nodeCert, err := pki.SignNodeCert(ca, node.Name, node.IP)
	if err != nil {
		slog.Error("complete-join: sign cert", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("cert signing failed"))
		return
	}

	// Encrypt env vars using the join token as passphrase (AES-256-GCM,
	// key derived via SHA-256 from the token — same as existing crypto package)
	envVars := h.readEnvFile()
	encryptedEnv, err := crypto.Encrypt(envVars, token)
	if err != nil {
		slog.Error("complete-join: encrypt env", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("env encryption failed"))
		return
	}

	// Update node record: mark connected, save cert, clear join token
	_, err = h.db.ExecContext(r.Context(),
		`UPDATE nodes SET status='connected', node_cert_pem=?, rqlite_addr=?,
		 join_token=NULL, token_expires_at=NULL, last_seen=datetime('now'),
		 updated_at=datetime('now') WHERE id=?`,
		nodeCert.CertPEM, payload.RqliteAddr, node.ID,
	)
	if err != nil {
		slog.Error("complete-join: update node", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("DB update failed"))
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"ca_cert_pem":   ca.CertPEM,
		"node_cert_pem": nodeCert.CertPEM,
		"node_key_pem":  nodeCert.KeyPEM,  // node key sent only once
		"encrypted_env": encryptedEnv,     // decrypt with join token
		"rqlite_main":   "127.0.0.1:4002", // main Raft addr to join
	})
}

// POST /api/nodes/{nodeID}/ping — node heartbeat
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
		 updated_at=datetime('now') WHERE id=?`,
		status, req.RqliteAddr, nodeID,
	)
	writeJSON(w, http.StatusOK, map[string]string{"ok": "1"})
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
RQLITE_VER="8.36.5"
CA_CERT='{{.CACert}}'

if [ "$(id -u)" -ne 0 ]; then
  echo "ERROR: run as root (sudo)." >&2; exit 1
fi

echo "==> FeatherDeploy Node Setup"
echo "    Main server: $MAIN_URL"
echo ""

# -- Install dependencies (podman + crun + caddy + rqlite) --------------------
install_rqlite() {
  command -v rqlited >/dev/null 2>&1 && { echo "  rqlited already installed"; return; }
  echo "==> Installing rqlite ${RQLITE_VER}..."
  local TAR="rqlite-v${RQLITE_VER}-linux-amd64.tar.gz"
  curl -fsSL "https://github.com/rqlite/rqlite/releases/download/v${RQLITE_VER}/${TAR}" -o "/tmp/${TAR}"
  local DIR; DIR=$(tar -tzf "/tmp/${TAR}" | head -1 | cut -f1 -d"/")
  tar -xzf "/tmp/${TAR}" -C /tmp/
  install -m 755 "/tmp/${DIR}/rqlited" /usr/local/bin/rqlited
  install -m 755 "/tmp/${DIR}/rqlite"  /usr/local/bin/rqlite
  rm -rf "/tmp/${TAR}" "/tmp/${DIR}"
  echo "  rqlited installed"
}

configure_crun() {
  command -v crun >/dev/null 2>&1 || return
  mkdir -p /etc/containers
  local c=/etc/containers/containers.conf
  [ -f "$c" ] || printf '[engine]\nruntime = "crun"\n' > "$c"
  grep -q 'runtime' "$c" || printf '\n[engine]\nruntime = "crun"\n' >> "$c"
  echo "  crun configured as Podman runtime"
}

if command -v apt-get >/dev/null 2>&1; then
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -y -q
  apt-get install -y -q curl podman crun caddy 2>/dev/null || apt-get install -y -q curl podman caddy
elif command -v dnf >/dev/null 2>&1; then
  dnf install -y -q curl podman crun caddy 2>/dev/null || dnf install -y -q curl podman caddy
elif command -v yum >/dev/null 2>&1; then
  yum install -y -q curl podman crun 2>/dev/null || yum install -y -q curl podman
elif command -v apk >/dev/null 2>&1; then
  apk add --no-cache curl podman caddy crun 2>/dev/null || apk add --no-cache curl podman caddy
fi

install_rqlite
configure_crun

# -- Download featherdeploy-node binary ---------------------------------------
echo "==> Downloading featherdeploy-node..."
curl -fsSL "${MAIN_URL}/api/nodes/binary?token=${JOIN_TOKEN}" -o "$NODE_BINARY"
chmod +x "$NODE_BINARY"
echo "  Binary installed: $NODE_BINARY"

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

func renderJoinScript(mainURL, token, caCert string) (string, error) {
	tmpl, err := template.New("join").Parse(joinScriptTmpl)
	if err != nil {
		return "", err
	}
	var buf strings.Builder
	err = tmpl.Execute(&buf, struct {
		MainURL string
		Token   string
		CACert  string
	}{mainURL, token, caCert})
	return buf.String(), err
}
