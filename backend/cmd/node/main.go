package main

import (
	"bytes"
	"context"
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
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/ojhapranjal26/featherdeploy/backend/internal/crypto"
	appDb "github.com/ojhapranjal26/featherdeploy/backend/internal/db"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/deploy"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/heartbeat"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/pki"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/coordination"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/netdaemon"
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
	rqliteHTTP = "127.0.0.1:4003" // Worker's local rqlite (for HA only)
	rqliteRaft = "127.0.0.1:4004"
	etcdClient = "127.0.0.1:2381"
	etcdPeer   = "127.0.0.1:2382"
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
	payload := map[string]string{
		"rqlite_addr": nodeAddr,
		"hostname":    hostname(),
		"os_info":     getOSInfo(),
	}
	body, _ := json.Marshal(payload)
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
		NodeID       string `json:"node_id"`
		CACertPEM    string `json:"ca_cert_pem"`
		NodeCertPEM  string `json:"node_cert_pem"`
		NodeKeyPEM   string `json:"node_key_pem"`
		EncryptedEnv string `json:"encrypted_env"`
		RqliteMain   string `json:"rqlite_main"`
		EtcdMain     string `json:"etcd_main"`
		SSHPublicKey string `json:"ssh_public_key"`
		NodeIP       string `json:"node_ip"`
		TunnelToken  string `json:"tunnel_token"` // permanent token for WebSocket tunnel auth
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

	// Persist tokens and connection info
	writeFile(configDir+"/join_token", token, 0600)               // kept for binary downloads
	writeFile(configDir+"/tunnel_token", reply.TunnelToken, 0600) // permanent tunnel auth
	writeFile(configDir+"/main_url", mainURL, 0644)

	// Node ID = assigned node_id or fallback to hostname
	nodeIP := reply.NodeIP
	if nodeIP == "" {
		nodeIP = localIP()
	}
	nodeID := reply.NodeID
	if nodeID == "" {
		nodeID = hostname()
	}
	writeFile(nodeIDFile, nodeID, 0644)

	// Install SSH public key for passwordless access from main server
	if reply.SSHPublicKey != "" {
		installSSHKey(reply.SSHPublicKey)
	}

	// Worker node relies entirely on the tunnel to reach brain services.

	// Connectivity check: ensure we can reach the main server's WebSocket tunnel
	wsURL := strings.Replace(mainURL, "http", "ws", 1) + "/api/cluster/tunnel"
	fmt.Printf("==> Establishing secure tunnel to brain at %s...\n", wsURL)

	tunnelMgr := netdaemon.NewTunnelManager()
	
	// Use a simple TLS config that trusts the OS cert pool (for the outer WSS)
	tunnelTLSCfg := &tls.Config{InsecureSkipVerify: true}
	
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // Stop temporary tunnel when script finishes
	go tunnelMgr.StartClient(ctx, wsURL, nodeID, reply.TunnelToken, tunnelTLSCfg)

	// Wait for connection to establish
	time.Sleep(4 * time.Second)

	// Setup local proxies so this node can reach the brain's services via the tunnel
	tunnelMgr.ProxyNodeToBrain(4001, 4001)
	tunnelMgr.ProxyNodeToBrain(4002, 4002)
	tunnelMgr.ProxyNodeToBrain(2379, 2379)
	tunnelMgr.ProxyNodeToBrain(2380, 2380)

	fmt.Println("==> Checking connectivity through tunnel...")
	httpClient := &http.Client{Timeout: 5 * time.Second}
	if resp, err := httpClient.Get("http://127.0.0.1:4001/readyz"); err != nil {
		fmt.Printf("  WARNING: cannot reach main rqlite via tunnel: %v\n", err)
	} else {
		resp.Body.Close()
		fmt.Println("  ✓ Rqlite connectivity confirmed via tunnel")
	}

	// Write all service unit files before reloading systemd to prevent missing unit errors
	writeNodeServeService()

	runCmd("systemctl", "daemon-reload")


	// Explicitly stop the temporary setup tunnel so the background service
	// can bind to the proxy ports (4001, 2379, etc) without "address already in use" errors.
	cancel()
	time.Sleep(1 * time.Second)

	// Enable and start the background featherdeploy-node serve service
	runCmd("systemctl", "enable", "--now", "featherdeploy-node")

	fmt.Println("==> Node joined successfully!")
	fmt.Printf("    Node ID: %s\n", nodeID)
	fmt.Println("    Panel:   will mirror the main server panel (if available)")
	fmt.Println("    rqlite:  http://127.0.0.1:4001 (proxied to brain)")
}

// ─── serve ────────────────────────────────────────────────────────────────────

// runServe: runs the node management HTTP server + heartbeat + brain election.
func runServe() {
	slog.Info("featherdeploy-node starting")

	myID := hostname()
	if data, err := os.ReadFile(nodeIDFile); err == nil {
		myID = strings.TrimSpace(string(data))
	}

	// ─── FeatherTunnel: Reverse Tunneling ──────────────────────────────────────
	// Start the tunnel FIRST so that proxies are available for DB/etcd connections
	tunnelMgr := netdaemon.NewTunnelManager()
	mainURL := ""
	if data, err := os.ReadFile(configDir + "/main_url"); err == nil {
		mainURL = strings.TrimSpace(string(data))
	}
	nodeToken := ""
	if data, err := os.ReadFile(configDir + "/tunnel_token"); err == nil {
		nodeToken = strings.TrimSpace(string(data))
	}
	if mainURL != "" && nodeToken != "" {
		wsURL := strings.Replace(mainURL, "http", "ws", 1) + "/api/cluster/tunnel"
		tunnelTLSCfg := &tls.Config{InsecureSkipVerify: true}
		slog.Info("tunnel client: connecting to brain", "url", wsURL, "node", myID)
		go tunnelMgr.StartClient(context.Background(), wsURL, myID, nodeToken, tunnelTLSCfg)

		// Setup persistent proxies to the brain's services
		tunnelMgr.ProxyNodeToBrain(4001, 4001)
		tunnelMgr.ProxyNodeToBrain(4002, 4002)
		tunnelMgr.ProxyNodeToBrain(2379, 2379)
		tunnelMgr.ProxyNodeToBrain(2380, 2380)
	}

	// Wait a moment for tunnel to initialize
	time.Sleep(2 * time.Second)

	// Connect to the Brain's rqlite via the tunnel proxy (always 127.0.0.1:4001 on workers).
	// We retry until the tunnel is established.
	var db *sql.DB
	var err error
	for i := 0; i < 30; i++ {
		db, err = appDb.OpenRqlite("http://127.0.0.1:4001")
		if err == nil {
			break
		}
		slog.Info("waiting for tunnel and brain rqlite...", "attempt", i+1, "err", err)
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		slog.Error("could not connect to brain rqlite via tunnel", "err", err)
	}

	// Start node heartbeat + brain-election goroutine
	if db != nil {
		go runNodeHeartbeat(db, myID)
	}

	if mainURL != "" && nodeToken != "" {
		tunnelTLSCfg := &tls.Config{InsecureSkipVerify: true}
		go runAutoUpdater(mainURL, nodeToken, tunnelTLSCfg)
	}

	// ─── etcd Coordination Layer ──────────────────────────────────────────────
	// Initialize etcd client for real-time coordination (pointing to local proxy)
	etcdClient, err := coordination.NewClient([]string{"http://127.0.0.1:2379"})
	if err != nil {
		slog.Warn("could not connect to etcd proxy, real-time coordination disabled", "err", err)
	} else {
		defer etcdClient.Close()
		// Start real-time heartbeat in etcd
		go func() {
			slog.Info("etcd: registering node heartbeat", "id", myID)
			_, err := etcdClient.RegisterNode(context.Background(), myID, 15)
			if err != nil {
				slog.Error("etcd: node registration failed", "err", err)
			}
		}()
	}

	r := chi.NewRouter()
	r.Use(chimw.Recoverer)
	r.Use(chimw.RealIP)
	r.Use(chimw.RequestID)

	r.Get("/api/node/health", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})

	// Brain pushes a new tunnel token here during rotation
	r.Post("/api/node/rotate-tunnel-token", func(w http.ResponseWriter, req *http.Request) {
		// Only accept from localhost (via tunnel proxy — never from public internet)
		host, _, _ := net.SplitHostPort(req.RemoteAddr)
		if host != "127.0.0.1" && host != "::1" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		var payload struct {
			TunnelToken string `json:"tunnel_token"`
		}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil || payload.TunnelToken == "" {
			http.Error(w, "invalid payload", http.StatusBadRequest)
			return
		}
		writeFile(configDir+"/tunnel_token", payload.TunnelToken, 0600)
		slog.Info("node: tunnel token rotated by brain")
		w.WriteHeader(http.StatusNoContent)
	})

	// Brain proxies this endpoint to stream live node logs to the UI
	r.Get("/api/node/logs", func(w http.ResponseWriter, req *http.Request) {
		// Only allow from localhost (tunnel proxy), never public internet
		host, _, _ := net.SplitHostPort(req.RemoteAddr)
		if host != "127.0.0.1" && host != "::1" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		// Flush headers immediately to establish connection without proxy timeout
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		cmd := exec.CommandContext(req.Context(),
			"journalctl", "-u", "featherdeploy-node", "-n", "100", "--follow", "--no-pager", "--output=short-iso")
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			fmt.Fprintf(w, "data: {\"error\":\"journalctl: %s\"}\n\n", err)
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
			case <-req.Context().Done():
				return
			case line, ok := <-lines:
				if !ok {
					return
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
	})

	// Brain discovery: poll cluster_state (via local rqlite) for brain changes
	go func() {
		var knownBrain string
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			if db == nil {
				continue
			}
			var brainAddr string
			if err := db.QueryRow(`SELECT brain_addr FROM cluster_state WHERE id=1`).Scan(&brainAddr); err != nil || brainAddr == "" {
				continue
			}
			// Normalize: strip trailing slash
			brainAddr = strings.TrimRight(brainAddr, "/")
			if brainAddr == knownBrain {
				continue
			}
			// If the brain address is a loopback address, do not attempt to reconnect to it
			// as loopback is local to the brain server itself.
			if strings.Contains(brainAddr, "127.0.0.1") || strings.Contains(brainAddr, "localhost") {
				knownBrain = brainAddr
				continue
			}
			slog.Info("node: brain changed, reconnecting tunnel", "old", knownBrain, "new", brainAddr)
			knownBrain = brainAddr
			// Read current token
			var tok []byte
			tok, _ = os.ReadFile(configDir + "/tunnel_token")
			newToken := strings.TrimSpace(string(tok))
			if newToken == "" {
				continue
			}
			newWS := strings.Replace(brainAddr, "http", "ws", 1) + "/api/cluster/tunnel"
			newTLS := &tls.Config{InsecureSkipVerify: true}
			// The existing StartClient goroutine will keep retrying the old URL.
			// Start a new one for the new brain — the old one will die when its session closes.
			go tunnelMgr.StartClient(context.Background(), newWS, myID, newToken, newTLS)
		}
	}()

	r.Post("/api/node/deploy", func(w http.ResponseWriter, req *http.Request) {
		var payload struct {
			DepID  int64  `json:"dep_id"`
			SvcID  int64  `json:"svc_id"`
			UserID int64  `json:"user_id"`
			Secret string `json:"jwt_secret"`
		}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			http.Error(w, "invalid JSON", 400)
			return
		}
		// Run deployment in background
		go func() {
			if db == nil {
				slog.Error("node/deploy: DB not connected")
				return
			}
			deploy.Run(context.Background(), db, payload.Secret, payload.DepID, payload.SvcID, payload.UserID)
		}()
		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte(`{"status":"queued"}`))
	})

	r.Post("/api/node/stop", func(w http.ResponseWriter, req *http.Request) {
		var payload struct {
			ContainerID string `json:"container_id"`
		}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			http.Error(w, "invalid JSON", 400)
			return
		}
		slog.Info("node/stop: stopping container", "id", payload.ContainerID)
		// Use -f to stop and remove
		if out, err := exec.Command("podman", "rm", "-f", payload.ContainerID).CombinedOutput(); err != nil {
			slog.Warn("node/stop failed", "id", payload.ContainerID, "err", err, "out", string(out))
		}
		w.WriteHeader(http.StatusNoContent)
	})

	r.Post("/api/node/rotate-cert", func(w http.ResponseWriter, req *http.Request) {
		var payload struct {
			CertPEM string `json:"cert_pem"`
			KeyPEM  string `json:"key_pem"`
			CAPEM   string `json:"ca_pem"`
		}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			http.Error(w, "invalid JSON", 400)
			return
		}
		slog.Info("node/rotate-cert: received new certificates")
		
		// Save new certs
		if payload.CAPEM != "" {
			writeFile(caCertFile, payload.CAPEM, 0644)
		}
		writeFile(nodeCert, payload.CertPEM, 0644)
		writeFile(nodeKey, payload.KeyPEM, 0600)
		
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
		
		// Restart server in a bit to apply new certs
		go func() {
			time.Sleep(2 * time.Second)
			slog.Info("node/rotate-cert: restarting to apply new certs")
			os.Exit(0) // Systemd/Docker will restart us
		}()
	})

	// Serve frontend bundle if present
	if info, err := os.Stat("/var/lib/featherdeploy/frontend"); err == nil && info.IsDir() {
		r.Handle("/*", http.FileServer(http.Dir("/var/lib/featherdeploy/frontend")))
	}

	r.Post("/api/node/db-start", func(w http.ResponseWriter, req *http.Request) {
		var payload struct {
			DBID   int64  `json:"db_id"`
			Secret string `json:"jwt_secret"`
		}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			http.Error(w, "invalid JSON", 400)
			return
		}
		go deploy.StartDatabase(db, payload.Secret, payload.DBID)
		w.WriteHeader(http.StatusAccepted)
	})

	r.Post("/api/node/db-backup", func(w http.ResponseWriter, req *http.Request) {
		var payload struct {
			DBID   int64  `json:"db_id"`
			Secret string `json:"jwt_secret"`
			Stop   bool   `json:"stop"`
		}
		// Default to true for backward compatibility with migration triggers
		payload.Stop = true
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			// If decoding fails, we still have the default stop=true
		}
		path, _, err := deploy.CreateDatabaseBackup(db, payload.Secret, payload.DBID, payload.Stop)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer os.Remove(path)
		f, _ := os.Open(path)
		defer f.Close()
		io.Copy(w, f)
	})

	r.Post("/api/node/db-restore", func(w http.ResponseWriter, req *http.Request) {
		dbIDStr := req.URL.Query().Get("db_id")
		dbID, _ := strconv.ParseInt(dbIDStr, 10, 64)
		secret := req.Header.Get("X-JWT-Secret")
		
		tmp, err := os.CreateTemp("", "restore-*.tar")
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer os.Remove(tmp.Name())
		io.Copy(tmp, req.Body)
		tmp.Close()
		
		if err := deploy.RestoreDatabaseBackup(db, secret, dbID, tmp.Name()); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

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


// ─── featherdeploy-node serve service ────────────────────────────────────────

const nodeServeServiceTmpl = `[Unit]
Description=FeatherDeploy Node
After=network.target

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

func waitForRqlite(maxSecs int) error {
	url := "http://" + rqliteHTTP + "/readyz"
	for i := 0; i < maxSecs; i++ {
		if resp, err := http.Get(url); err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			slog.Info("rqlite ready")
			return nil
		}
		time.Sleep(time.Second)
	}
	fmt.Println("\nWARN: rqlite did not become ready in time")
	fmt.Println("This node may have failed to join the Raft cluster.")
	fmt.Println("Troubleshooting:")
	fmt.Println("  1. Check node logs:  sudo journalctl -u rqlite-node -n 100 --no-pager")
	fmt.Println("  2. Check server logs: sudo journalctl -u rqlite -n 100 --no-pager")
	fmt.Println("  3. Ensure ports 4001 and 4002 are open in your cloud console.")
	return fmt.Errorf("timeout")
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
	// nodeDBID is the internal INTEGER PRIMARY KEY from the nodes table.
	// We need this to efficiently update stats.
	var nodeDBID int64

	mainURL := ""
	if data, err := os.ReadFile(configDir + "/main_url"); err == nil {
		mainURL = strings.TrimSpace(string(data))
	}

	ticker := time.NewTicker(heartbeat.HeartbeatInterval)
	defer ticker.Stop()

	promoted := false

	// Initial collection
	st := collectStats()

	for range ticker.C {
		// Re-collect stats each tick
		st = collectStats()

		// If we don't have the DB ID yet, try to find it.
		// This can happen if the node joined but the DB row wasn't replicated
		// to the local rqlite instance yet.
		if nodeDBID == 0 {
			err := db.QueryRow(`SELECT id FROM nodes WHERE node_id=?`, myID).Scan(&nodeDBID)
			if err != nil {
				slog.Warn("heartbeat: could not find node row in DB, retrying next tick", "node_id", myID, "err", err)
			} else {
				slog.Info("heartbeat: found node row", "node_id", myID, "db_id", nodeDBID)
			}
		}

		// Write stats into the nodes table if we have the ID
		if nodeDBID > 0 {
			_, err := db.Exec(`UPDATE nodes SET
				status='connected', last_seen=datetime('now'),
				cpu_usage=?, ram_used=?, ram_total=?,
				disk_used=?, disk_total=?, last_stats_at=datetime('now')
				WHERE id=?`,
				st.CPU, st.RAMUsed, st.RAMTotal, st.DiskUsed, st.DiskTotal, nodeDBID)
			if err != nil {
				slog.Error("heartbeat: update stats failed", "err", err)
			}
		}

		// Skip election checks once we have already promoted to brain.
		if promoted {
			continue
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
				promoted = true
				go promoteAsBrain(myID)
			} else {
				slog.Info("lost brain election — another node won")
			}
		}
	}
}

// promoteAsBrain starts the featherdeploy server binary on this node via systemd.
func promoteAsBrain(myID string) {
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
	// Try to detect the IP used to reach the internet (UDP trick)
	conn, err := net.DialTimeout("udp", "1.1.1.1:80", 2*time.Second)
	if err == nil {
		defer conn.Close()
		if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok {
			return addr.IP.String()
		}
	}

	// Fallback to first non-loopback IPv4
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

func getOSInfo() string {
	out, err := exec.Command("uname", "-snrvm").Output()
	if err != nil {
		return "Linux"
	}
	return strings.TrimSpace(string(out))
}

func runAutoUpdater(mainURL, nodeToken string, tlsCfg *tls.Config) {
	// Every 5 minutes, check the brain for a binary update
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	// Use the tunnel's inner IP if we are connected, but actually we are the node,
	// so the brain proxy for the main HTTP API isn't strictly necessary if mainURL is reachable.
	// However, we want to talk to the brain API. If the main URL is unreachable,
	// we could talk to the brain through a proxy? The node DOES proxy port 7443 to the brain.
	// But `featherdeploy-update` doesn't run on port 7443. The brain API is port 8080 or mainURL.
	// It's safest to just use `mainURL`.

	apiURL := strings.TrimRight(mainURL, "/")

	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
		Timeout:   30 * time.Second,
	}

	for range ticker.C {
		// 1. Get remote hash
		req, err := http.NewRequest("GET", apiURL+"/api/nodes/binary/hash", nil)
		if err != nil {
			continue
		}
		// Since we're hitting the open endpoint, we can pass token just in case
		req.Header.Set("Authorization", "Bearer "+nodeToken)
		
		resp, err := client.Do(req)
		if err != nil {
			slog.Debug("auto-update: hash check failed", "err", err)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			continue
		}

		var payload map[string]string
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			resp.Body.Close()
			continue
		}
		resp.Body.Close()

		remoteHash := payload["hash"]
		if remoteHash == "" {
			continue
		}

		// 2. Get local hash
		f, err := os.Open("/usr/local/bin/featherdeploy-node")
		if err != nil {
			continue
		}
		hsh := sha256.New()
		io.Copy(hsh, f)
		f.Close()
		localHash := hex.EncodeToString(hsh.Sum(nil))

		// 3. Compare and update
		if localHash != remoteHash {
			slog.Info("auto-update: new binary version detected", "local", localHash[:8], "remote", remoteHash[:8])
			
			// Download to temp file
			tmpFile, err := os.CreateTemp("/tmp", "fd-node-new-*")
			if err != nil {
				slog.Error("auto-update: create temp file failed", "err", err)
				continue
			}
			
			dlReq, _ := http.NewRequest("GET", apiURL+"/api/nodes/binary", nil)
			dlReq.Header.Set("Authorization", "Bearer "+nodeToken)
			dlResp, err := client.Do(dlReq)
			if err != nil || dlResp.StatusCode != http.StatusOK {
				slog.Error("auto-update: download failed")
				tmpFile.Close()
				os.Remove(tmpFile.Name())
				if dlResp != nil {
					dlResp.Body.Close()
				}
				continue
			}
			
			_, err = io.Copy(tmpFile, dlResp.Body)
			dlResp.Body.Close()
			tmpFile.Close()
			if err != nil {
				os.Remove(tmpFile.Name())
				continue
			}

			// Chmod and replace
			os.Chmod(tmpFile.Name(), 0755)
			
			// Try to atomically replace
			err = os.Rename(tmpFile.Name(), "/usr/local/bin/featherdeploy-node")
			if err != nil {
				// Fallback if cross-device
				cmd := exec.Command("mv", "-f", tmpFile.Name(), "/usr/local/bin/featherdeploy-node")
				cmd.Run()
			}
			
			slog.Info("auto-update: binary replaced successfully. restarting service...")
			
			// Restart service (we must run this in background so we don't block and get killed abruptly if we can avoid it,
			// actually systemd will just kill us immediately when we call restart, which is fine)
			exec.Command("systemctl", "restart", "featherdeploy-node").Start()
			
			// Exit just in case systemctl takes too long, systemd will start us again
			os.Exit(0)
		}
	}
}


