package netdaemon

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
)

var upgrader = websocket.Upgrader{
	CheckOrigin:     func(r *http.Request) bool { return true },
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
}

// GlobalTunnel is a singleton instance initialized by the main server
// so other packages (like deploy/scheduler) can query proxy addresses.
var GlobalTunnel *TunnelManager

// TunnelManager handles reverse tunnels over WebSockets.
type TunnelManager struct {
	mu        sync.RWMutex
	sessions  map[string]*yamux.Session
	portMap   map[string]map[int]int    // nodeID -> remotePort -> localPort
	listeners map[string][]net.Listener // nodeID -> active listeners

	// ValidateToken is called during handshake to verify the node token.
	// Return the nodeID if valid, empty string if invalid.
	ValidateToken func(token string) string

	// OnNodeConnect is called when a node successfully connects to the tunnel.
	OnNodeConnect func(nodeID, ip string)
}

func NewTunnelManager() *TunnelManager {
	return &TunnelManager{
		sessions:  make(map[string]*yamux.Session),
		portMap:   make(map[string]map[int]int),
		listeners: make(map[string][]net.Listener),
	}
}

// ─── Brain Side (Server) ─────────────────────────────────────────────────────

// HTTPHandler returns the http.HandlerFunc for /api/cluster/tunnel.
// Authentication: the node sends its join token via the X-Node-Token header.
func (tm *TunnelManager) HTTPHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 1. Authenticate the node via its token.
		token := r.Header.Get("X-Node-Token")
		nodeID := r.Header.Get("X-Node-ID")

		if token == "" || nodeID == "" {
			slog.Warn("tunnel server: missing auth headers", "remote", r.RemoteAddr)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		// If a validator is registered, use it; otherwise trust the header.
		if tm.ValidateToken != nil {
			verified := tm.ValidateToken(token)
			if verified == "" {
				slog.Warn("tunnel server: invalid token", "node", nodeID)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			nodeID = verified // use the name from the DB, not the header
		}

		// 2. Upgrade to WebSocket.
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			slog.Error("tunnel server: upgrade failed", "err", err)
			return
		}

		slog.Info("tunnel server: node connected", "node", nodeID, "remote", r.RemoteAddr)

		// 3. Wrap the WebSocket as a net.Conn and create a yamux session.
		wsConn := &wsNetConn{Conn: conn}
		session, err := yamux.Server(wsConn, yamuxConfig())
		if err != nil {
			slog.Error("tunnel server: yamux session failed", "node", nodeID, "err", err)
			conn.Close()
			return
		}

		// 4. Register the session
		tm.mu.Lock()
		if old, ok := tm.sessions[nodeID]; ok {
			slog.Info("tunnel server: replacing stale session", "node", nodeID)
			old.Close()
			tm.mu.Unlock()
			tm.Cleanup(nodeID)
			tm.mu.Lock()
		}
		tm.sessions[nodeID] = session
		tm.mu.Unlock()

		// 5. Set up local proxy ports for this node.
		tm.setupNodeProxies(nodeID)

		if tm.OnNodeConnect != nil {
			ip, _, _ := net.SplitHostPort(r.RemoteAddr)
			if ip == "" {
				ip = r.RemoteAddr
			}
			go tm.OnNodeConnect(nodeID, ip)
		}

		// 5.5 Accept incoming streams from the node (node-to-brain).
		go func() {
			for {
				stream, err := session.Accept()
				if err != nil {
					return
				}
				go tm.handleIncomingStream(stream)
			}
		}()

		// 6. Block until the session closes.
		<-session.CloseChan()
		slog.Info("tunnel server: node disconnected", "node", nodeID)
		tm.Cleanup(nodeID)

		tm.mu.Lock()
		if tm.sessions[nodeID] == session {
			delete(tm.sessions, nodeID)
		}
		tm.mu.Unlock()
	}
}

func (tm *TunnelManager) setupNodeProxies(nodeID string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	// Clear any existing port mappings and listeners for this node to ensure a clean start
	if _, ok := tm.portMap[nodeID]; !ok {
		tm.portMap[nodeID] = make(map[int]int)
	}

	// Use a deterministic base port for each node based on its DB ID
	// nodeID format is "node-<id>" (or "main")
	idVal := 0
	if nodeID == "main" {
		idVal = 0
	} else if strings.HasPrefix(nodeID, "node-") {
		fmt.Sscanf(nodeID, "node-%d", &idVal)
	} else {
		// Fallback for legacy IDs
		idVal = len(tm.sessions) 
	}
	base := 20000 + (idVal * 10)

	ports := []int{443, 4003, 4004, 2381, 2382, 7443, 8080}
	for i, remotePort := range ports {
		localPort := base + i
		tm.portMap[nodeID][remotePort] = localPort
		go tm.startInternalProxy(nodeID, localPort, remotePort)
		slog.Info("tunnel server: proxy mapped", "node", nodeID, "localPort", localPort, "remotePort", remotePort)
	}
}

// EnsureServiceProxy ensures that a dynamic local proxy port is mapped to the remote container port over the secure reverse tunnel.
// Returns the local port assigned on the brain server.
func (tm *TunnelManager) EnsureServiceProxy(nodeID string, remotePort int) int {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if tm.portMap == nil {
		tm.portMap = make(map[string]map[int]int)
	}
	if tm.portMap[nodeID] == nil {
		tm.portMap[nodeID] = make(map[int]int)
	}

	// If already mapped, return the existing local port
	if lp, ok := tm.portMap[nodeID][remotePort]; ok {
		return lp
	}

	idVal := 0
	if strings.HasPrefix(nodeID, "node-") {
		fmt.Sscanf(nodeID, "node-%d", &idVal)
	} else if nodeID != "main" {
		idVal = len(tm.sessions)
	}
	// Give each node a deterministic 500-port block starting at 30000 to safely map service container ports
	localPort := 30000 + (idVal * 500) + (remotePort % 500)

	tm.portMap[nodeID][remotePort] = localPort
	go tm.startInternalProxy(nodeID, localPort, remotePort)
	slog.Info("tunnel server: service container proxy dynamically mapped", "node", nodeID, "localPort", localPort, "remotePort", remotePort)
	return localPort
}

func (tm *TunnelManager) Cleanup(nodeID string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if session, ok := tm.sessions[nodeID]; ok {
		session.Close()
		delete(tm.sessions, nodeID)
	}

	if listeners, ok := tm.listeners[nodeID]; ok {
		for _, l := range listeners {
			l.Close()
		}
		delete(tm.listeners, nodeID)
	}
	delete(tm.portMap, nodeID)
}

func (tm *TunnelManager) startInternalProxy(nodeID string, localPort, remotePort int) {
	addr := fmt.Sprintf("127.0.0.1:%d", localPort)
	l, err := net.Listen("tcp", addr)
	if err != nil {
		slog.Error("tunnel server: failed to start proxy listener", "node", nodeID, "port", localPort, "err", err)
		return
	}

	tm.mu.Lock()
	tm.listeners[nodeID] = append(tm.listeners[nodeID], l)
	tm.mu.Unlock()

	defer l.Close()
	for {
		client, err := l.Accept()
		if err != nil {
			return
		}
		go tm.handleProxyConn(nodeID, remotePort, client)
	}
}

// GetNodeProxyAddr returns the local loopback address that tunnels to the node's port.
func (tm *TunnelManager) GetNodeProxyAddr(nodeID string, remotePort int) string {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	if m, ok := tm.portMap[nodeID]; ok {
		if lp, ok := m[remotePort]; ok {
			return fmt.Sprintf("127.0.0.1:%d", lp)
		}
	}
	return ""
}

// ─── Worker Side (Client) ────────────────────────────────────────────────────

func (tm *TunnelManager) StartClient(ctx context.Context, brainURL string, nodeID string, token string, tlsCfg *tls.Config) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
			if err := tm.dialAndServeWS(ctx, brainURL, nodeID, token, tlsCfg); err != nil {
				slog.Error("tunnel client: disconnected, retrying in 5s", "err", err)
				time.Sleep(5 * time.Second)
			}
		}
	}
}

func (tm *TunnelManager) dialAndServeWS(ctx context.Context, brainURL string, nodeID string, token string, tlsCfg *tls.Config) error {
	dialer := websocket.Dialer{
		Proxy:            http.ProxyFromEnvironment,
		HandshakeTimeout: 15 * time.Second,
		TLSClientConfig:  tlsCfg,
	}

	header := http.Header{}
	header.Set("X-Node-ID", nodeID)
	header.Set("X-Node-Token", token)

	conn, resp, err := dialer.DialContext(ctx, brainURL, header)
	if err != nil {
		// Log the actual HTTP response to diagnose the rejection
		if resp != nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return fmt.Errorf("dial: HTTP %d %s — body: %s", resp.StatusCode, resp.Status, strings.TrimSpace(string(body)))
		}
		return fmt.Errorf("dial: %w", err)
	}

	wsConn := &wsNetConn{Conn: conn}
	session, err := yamux.Client(wsConn, yamuxConfig())
	if err != nil {
		conn.Close()
		return fmt.Errorf("yamux: %w", err)
	}

	// Graceful cancellation: if ctx is canceled, close session and conn
	go func() {
		<-ctx.Done()
		session.Close()
		conn.Close()
	}()

	defer session.Close()

	tm.mu.Lock()
	tm.sessions["brain"] = session
	tm.mu.Unlock()

	slog.Info("tunnel client: connected to brain", "url", brainURL, "node", nodeID)

	for {
		stream, err := session.Accept()
		if err != nil {
			return err
		}
		go tm.handleIncomingStream(stream)
	}
}

// ─── Proxy Helpers ───────────────────────────────────────────────────────────

func (tm *TunnelManager) handleProxyConn(nodeID string, remotePort int, client net.Conn) {
	defer client.Close()

	tm.mu.RLock()
	session, ok := tm.sessions[nodeID]
	tm.mu.RUnlock()
	if !ok {
		slog.Warn("tunnel server: no session for node", "node", nodeID)
		return
	}

	stream, err := session.Open()
	if err != nil {
		return
	}
	defer stream.Close()

	// Send the target port as the first 2 bytes.
	stream.Write([]byte{byte(remotePort >> 8), byte(remotePort & 0xff)})
	pipe(client, stream)
}

func (tm *TunnelManager) ProxyNodeToBrain(localPort, remotePort int) error {
	addr := fmt.Sprintf("127.0.0.1:%d", localPort)
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	go func() {
		defer l.Close()
		for {
			client, err := l.Accept()
			if err != nil {
				return
			}
			go tm.forwardToBrain(remotePort, client)
		}
	}()
	return nil
}

func (tm *TunnelManager) forwardToBrain(remotePort int, client net.Conn) {
	defer client.Close()
	tm.mu.RLock()
	session := tm.sessions["brain"]
	tm.mu.RUnlock()
	if session == nil {
		return
	}
	stream, err := session.Open()
	if err != nil {
		return
	}
	defer stream.Close()
	stream.Write([]byte{byte(remotePort >> 8), byte(remotePort & 0xff)})
	pipe(client, stream)
}

func (tm *TunnelManager) handleIncomingStream(stream net.Conn) {
	defer stream.Close()
	portBuf := make([]byte, 2)
	if _, err := io.ReadFull(stream, portBuf); err != nil {
		return
	}
	port := int(portBuf[0])<<8 | int(portBuf[1])
	local, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		slog.Warn("tunnel client: cannot connect to local port", "port", port, "err", err)
		return
	}
	defer local.Close()
	pipe(local, stream)
}

func pipe(a, b net.Conn) {
	done := make(chan struct{}, 2)
	go func() { io.Copy(a, b); done <- struct{}{} }()
	go func() { io.Copy(b, a); done <- struct{}{} }()
	<-done
}

// ─── yamux config ─────────────────────────────────────────────────────────────

func yamuxConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.KeepAliveInterval = 10 * time.Second
	cfg.ConnectionWriteTimeout = 10 * time.Second
	return cfg
}

// ─── WebSocket → net.Conn adapter ────────────────────────────────────────────

type wsNetConn struct {
	*websocket.Conn
	reader io.Reader
	mu     sync.Mutex // protect concurrent writes
}

func (c *wsNetConn) Read(b []byte) (n int, err error) {
	for {
		if c.reader == nil {
			_, r, err := c.Conn.NextReader()
			if err != nil {
				return 0, err
			}
			c.reader = r
		}
		n, err = c.reader.Read(b)
		if err == io.EOF {
			c.reader = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (c *wsNetConn) Write(b []byte) (n int, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	w, err := c.Conn.NextWriter(websocket.BinaryMessage)
	if err != nil {
		return 0, err
	}
	n, err = w.Write(b)
	w.Close()
	return n, err
}

func (c *wsNetConn) LocalAddr() net.Addr                { return c.Conn.LocalAddr() }
func (c *wsNetConn) RemoteAddr() net.Addr               { return c.Conn.RemoteAddr() }
func (c *wsNetConn) SetReadDeadline(t time.Time) error  { return c.Conn.SetReadDeadline(t) }
func (c *wsNetConn) SetWriteDeadline(t time.Time) error { return c.Conn.SetWriteDeadline(t) }

func (c *wsNetConn) SetDeadline(t time.Time) error {
	if err := c.Conn.SetReadDeadline(t); err != nil {
		return err
	}
	return c.Conn.SetWriteDeadline(t)
}

func (c *wsNetConn) Close() error {
	return c.Conn.Close()
}
