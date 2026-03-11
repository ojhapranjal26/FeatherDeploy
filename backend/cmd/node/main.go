package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/deploy-paas/backend/internal/crypto"
	appDb "github.com/deploy-paas/backend/internal/db"
	"github.com/deploy-paas/backend/internal/heartbeat"
	"github.com/deploy-paas/backend/internal/pki"
)

const (
	dataDir    = "/var/lib/featherdeploy"
	configDir  = "/etc/featherdeploy"
	envFile    = configDir + "/featherdeploy.env"
	nodeCert   = configDir + "/node.crt"
	nodeKey    = configDir + "/node.key"
	caCertFile = configDir + "/ca.crt"
	nodeIDFile = configDir + "/node.id"
	rqliteUnit = "/etc/systemd/system/rqlite-node.service"
	rqliteData = dataDir + "/rqlite-data"
	rqliteHTTP = "127.0.0.1:4001"
	rqliteRaft = "0.0.0.0:4002"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: featherdeploy-node <join|serve>")
		os.Exit(1)
	}
	switch os.Args[1] {
	case "join":
		runJoin(os.Args[2:])
	case "serve":
		runServe()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}

// ─── join ─────────────────────────────────────────────────────────────────────

// runJoin: registers this node with the main server, receives certs + encrypted
// env, then starts rqlite (joining the main Raft cluster) and optionally installs
// a systemd service for featherdeploy-node serve.
func runJoin(args []string) {
	mainURL, token, nodeAddr := "", "", ""
	for _, a := range args {
		if strings.HasPrefix(a, "--main=") {
			mainURL = strings.TrimPrefix(a, "--main=")
		} else if strings.HasPrefix(a, "--token=") {
			token = strings.TrimPrefix(a, "--token=")
		} else if strings.HasPrefix(a, "--node-addr=") {
			nodeAddr = strings.TrimPrefix(a, "--node-addr=")
		}
	}
	if mainURL == "" || token == "" {
		fmt.Fprintln(os.Stderr, "Usage: featherdeploy-node join --main=URL --token=TOKEN [--node-addr=HOST:PORT]")
		os.Exit(1)
	}
	if nodeAddr == "" {
		nodeAddr = rqliteRaft
	}

	must(os.MkdirAll(configDir, 0700))
	must(os.MkdirAll(dataDir, 0755))
	must(os.MkdirAll(rqliteData, 0700))

	// POST /api/nodes/{token}/complete-join
	body, _ := json.Marshal(map[string]string{"rqlite_addr": nodeAddr})
	resp, err := http.Post(mainURL+"/api/nodes/"+token+"/complete-join",
		"application/json", bytes.NewReader(body))
	if err != nil {
		fatalf("connection to main server failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		fatalf("join rejected (%d): %s", resp.StatusCode, b)
	}

	var reply struct {
		CACertPEM    string `json:"ca_cert_pem"`
		NodeCertPEM  string `json:"node_cert_pem"`
		NodeKeyPEM   string `json:"node_key_pem"`
		EncryptedEnv string `json:"encrypted_env"`
		RqliteMain   string `json:"rqlite_main"` // main Raft addr to join
		SSHPublicKey string `json:"ssh_public_key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		fatalf("decode response: %v", err)
	}

	// Persist certs
	writeFile(caCertFile, reply.CACertPEM, 0644)
	writeFile(nodeCert, reply.NodeCertPEM, 0644)
	writeFile(nodeKey, reply.NodeKeyPEM, 0600)

	// Decrypt and save env file
	decryptedEnv, err := crypto.Decrypt(reply.EncryptedEnv, token)
	if err != nil {
		fatalf("decrypt env: %v", err)
	}
	writeFile(envFile, decryptedEnv, 0600)

	// Store the join token so the node binary can request server binary later
	writeFile(configDir+"/join_token", token, 0600)
	writeFile(configDir+"/main_url", mainURL, 0644)

	// Node ID = hostname
	nodeID := hostname()
	writeFile(nodeIDFile, nodeID, 0644)

	// Install SSH public key for passwordless access from main server
	if reply.SSHPublicKey != "" {
		installSSHKey(reply.SSHPublicKey)
	}

	// Write and start rqlite service (join main Raft cluster)
	// Determine main IP from mainURL for Raft join address
	mainIP := extractHost(mainURL)
	rqliteJoinAddr := mainIP + ":4002"
	if reply.RqliteMain != "" {
		rqliteJoinAddr = mainIP + ":" + strings.Split(reply.RqliteMain, ":")[1]
	}
	writeRqliteService(nodeID, rqliteJoinAddr)
	runCmd("systemctl", "daemon-reload")
	runCmd("systemctl", "enable", "--now", "rqlite-node")
	waitForRqlite(60)

	// Connect to local rqlite to update our node record with the node_id
	if db, err := appDb.OpenRqlite("http://" + rqliteHTTP); err == nil {
		db.Exec(`UPDATE nodes SET node_id=? WHERE join_token IS NULL AND ip=(
			SELECT ip FROM nodes LIMIT 1)`, nodeID)
		db.Close()
	}

	// Write and enable featherdeploy-node serve service
	writeNodeServeService()
	runCmd("systemctl", "daemon-reload")
	runCmd("systemctl", "enable", "--now", "featherdeploy-node")

	fmt.Println("==> Node joined successfully!")
	fmt.Printf("    Node ID: %s\n", nodeID)
	fmt.Println("    Panel:   will mirror the main server panel (if available)")
	fmt.Println("    rqlite:  http://127.0.0.1:4001 (cluster member)")
}

// ─── serve ────────────────────────────────────────────────────────────────────

// runServe: runs the node management HTTP server + heartbeat + brain election.
func runServe() {
	slog.Info("featherdeploy-node starting")

	// Connect to local rqlite (already running as a Raft member)
	rqliteURL := envOr("RQLITE_URL", "http://"+rqliteHTTP)
	db, err := appDb.OpenRqlite(rqliteURL)
	if err != nil {
		slog.Warn("rqlite not ready yet, heartbeat will not start", "err", err)
	}

	myID := hostname()
	if data, err := os.ReadFile(nodeIDFile); err == nil {
		myID = strings.TrimSpace(string(data))
	}

	// Start node heartbeat + brain-election goroutine
	if db != nil {
		go runNodeHeartbeat(db, myID)
	}

	r := chi.NewRouter()
	r.Use(chimw.Recoverer)
	r.Use(chimw.RealIP)
	r.Use(chimw.RequestID)

	r.Get("/api/node/health", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})

	r.Get("/api/node/stats", func(w http.ResponseWriter, req *http.Request) {
		s := collectStats()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(s)
	})

	// Serve frontend bundle if present (fallback when main server is down)
	if info, err := os.Stat("/var/lib/featherdeploy/frontend"); err == nil && info.IsDir() {
		r.Handle("/*", http.FileServer(http.Dir("/var/lib/featherdeploy/frontend")))
	}

	addr := envOr("ADDR", ":7443")

	if _, err := os.Stat(nodeCert); err == nil {
		caPEM, _ := os.ReadFile(caCertFile)
		certPEM, _ := os.ReadFile(nodeCert)
		keyPEM, _ := os.ReadFile(nodeKey)
		tlsCfg, err := pki.TLSConfig(string(certPEM), string(keyPEM), string(caPEM))
		if err == nil {
			srv := &http.Server{Addr: addr, Handler: r, TLSConfig: tlsCfg}
			slog.Info("node serving (mTLS)", "addr", addr)
			if err := srv.ListenAndServeTLS("", ""); err != nil {
				fatalf("serve mTLS: %v", err)
			}
			return
		}
		slog.Warn("mTLS setup failed, falling back to plain HTTP", "err", err)
	}

	slog.Info("node serving (plain HTTP)", "addr", addr)
	if err := http.ListenAndServe(addr, r); err != nil {
		fatalf("serve: %v", err)
	}
}

// ─── rqlite service ──────────────────────────────────────────────────────────

const rqliteServiceTmpl = `[Unit]
Description=rqlite node
After=network.target

[Service]
User=root
ExecStart=/usr/local/bin/rqlited \
  -node-id=%s \
  -http-addr=%s \
  -raft-addr=%s \
  -join=%s \
  %s
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
`

func writeRqliteService(nodeID, mainRaft string) {
	content := fmt.Sprintf(rqliteServiceTmpl,
		nodeID, rqliteHTTP, rqliteRaft, mainRaft, rqliteData)
	writeFile(rqliteUnit, content, 0644)
}

// ─── featherdeploy-node serve service ────────────────────────────────────────

const nodeServeServiceTmpl = `[Unit]
Description=FeatherDeploy Node
After=network.target rqlite-node.service

[Service]
User=root
EnvironmentFile=/etc/featherdeploy/featherdeploy.env
ExecStart=/usr/local/bin/featherdeploy-node serve
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
`

func writeNodeServeService() {
	writeFile("/etc/systemd/system/featherdeploy-node.service", nodeServeServiceTmpl, 0644)
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func waitForRqlite(maxSecs int) {
	url := "http://" + rqliteHTTP + "/readyz"
	for i := 0; i < maxSecs; i++ {
		if resp, err := http.Get(url); err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			slog.Info("rqlite ready")
			return
		}
		time.Sleep(time.Second)
	}
	slog.Warn("rqlite did not become ready in time")
}

func hostname() string {
	h, _ := os.Hostname()
	if h == "" {
		return "node-" + fmt.Sprintf("%d", time.Now().UnixMilli())
	}
	return h
}

func writeFile(path, content string, perm os.FileMode) {
	must(os.MkdirAll(filepath.Dir(path), 0755))
	must(os.WriteFile(path, []byte(content), perm))
}

func runCmd(name string, args ...string) {
	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		slog.Warn("command failed", "cmd", name, "args", args, "err", err, "out", string(out))
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func must(err error) {
	if err != nil {
		fatalf("fatal: %v", err)
	}
}

func fatalf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "ERROR: "+format+"\n", args...)
	os.Exit(1)
}

// ─── heartbeat + election ─────────────────────────────────────────────────────

func runNodeHeartbeat(db *sql.DB, myID string) {
	// Look up this node's DB row id (used for stats update)
	var nodeDBID int64
	// Retry a few times since rqlite may not have our row yet
	for i := 0; i < 5; i++ {
		if err := db.QueryRow(`SELECT id FROM nodes WHERE node_id=?`, myID).Scan(&nodeDBID); err == nil {
			break
		}
		time.Sleep(2 * time.Second)
	}

	mainURL := ""
	if data, err := os.ReadFile(configDir + "/main_url"); err == nil {
		mainURL = strings.TrimSpace(string(data))
	}

	ticker := time.NewTicker(heartbeat.HeartbeatInterval)
	defer ticker.Stop()

	for range ticker.C {
		st := collectStats()

		// Write stats into the nodes table
		if nodeDBID > 0 {
			db.Exec(`UPDATE nodes SET
				status='connected', last_seen=datetime('now'),
				cpu_usage=?, ram_used=?, ram_total=?,
				disk_used=?, disk_total=?, last_stats_at=datetime('now')
				WHERE id=?`,
				st.CPU, st.RAMUsed, st.RAMTotal, st.DiskUsed, st.DiskTotal, nodeDBID)
		}

		// Check brain health
		if !heartbeat.IsBrainAlive(db) {
			slog.Warn("brain appears dead — attempting election", "my_id", myID)

			myAddr := "http://" + localIP() + ":8080"
			if mainURL != "" {
				// Re-use the scheme + port of the original brain URL
				if idx := strings.LastIndex(mainURL, ":"); idx > 7 {
					myAddr = "http://" + localIP() + mainURL[idx:]
				}
			}

			if heartbeat.TryClaimBrain(db, myID, myAddr) {
				slog.Info("WON brain election — promoting to brain", "addr", myAddr)
				go promoteAsBrain(db, myID, myAddr)
			} else {
				slog.Info("lost brain election — another node won")
			}
		}
	}
}

// promoteAsBrain starts the featherdeploy server binary on this node via systemd.
func promoteAsBrain(_ *sql.DB, myID, _ string) {
	slog.Info("promoting node to brain", "id", myID)

	binaryPath := "/usr/local/bin/featherdeploy"
	if _, err := os.Stat(binaryPath); err != nil {
		slog.Error("featherdeploy binary not found — cannot promote", "path", binaryPath)
		return
	}

	rqliteURL := "http://127.0.0.1:4001"
	if v := readEnvFileVar(envFile, "RQLITE_URL"); v != "" {
		rqliteURL = v
	}

	svc := fmt.Sprintf(`[Unit]
Description=FeatherDeploy Brain (promoted from node %s)
After=network.target rqlite-node.service
Requires=rqlite-node.service

[Service]
User=root
EnvironmentFile=%s
Environment=RQLITE_URL=%s
ExecStart=%s serve
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
`, myID, envFile, rqliteURL, binaryPath)

	writeFile("/etc/systemd/system/featherdeploy-brain.service", svc, 0644)
	runCmd("systemctl", "daemon-reload")
	runCmd("systemctl", "enable", "--now", "featherdeploy-brain")
	slog.Info("featherdeploy-brain service started")
}

// ─── stats collection ─────────────────────────────────────────────────────────
// collectStats, readMemInfo, diskStatfs, readCPUPercent are in stats_linux.go
// (linux) or stats_stub.go (other platforms).

// ─── SSH helpers ──────────────────────────────────────────────────────────────

// installSSHKey appends the main server's public key to /root/.ssh/authorized_keys.
func installSSHKey(pubKey string) {
	sshDir := "/root/.ssh"
	must(os.MkdirAll(sshDir, 0700))
	authKeys := sshDir + "/authorized_keys"

	// Check if already present
	if data, err := os.ReadFile(authKeys); err == nil {
		if strings.Contains(string(data), strings.TrimSpace(pubKey)) {
			return
		}
	}

	f, err := os.OpenFile(authKeys, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		slog.Warn("could not write authorized_keys", "err", err)
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "\n%s\n", strings.TrimSpace(pubKey))
	slog.Info("SSH public key installed")
}

// ─── misc helpers ─────────────────────────────────────────────────────────────

// localIP returns the first non-loopback IPv4 address.
func localIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "127.0.0.1"
	}
	for _, a := range addrs {
		if ipNet, ok := a.(*net.IPNet); ok && !ipNet.IP.IsLoopback() && ipNet.IP.To4() != nil {
			return ipNet.IP.String()
		}
	}
	return "127.0.0.1"
}

// extractHost returns the hostname/IP portion of a URL like https://1.2.3.4:8080.
func extractHost(rawURL string) string {
	// strip scheme
	s := rawURL
	if idx := strings.Index(s, "://"); idx >= 0 {
		s = s[idx+3:]
	}
	// strip path
	if idx := strings.Index(s, "/"); idx >= 0 {
		s = s[:idx]
	}
	// strip port
	if h, _, err := net.SplitHostPort(s); err == nil {
		return h
	}
	return s
}

// readEnvFileVar reads a KEY=VALUE line from an env file.
func readEnvFileVar(path, key string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	prefix := key + "="
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}
	return ""
}
