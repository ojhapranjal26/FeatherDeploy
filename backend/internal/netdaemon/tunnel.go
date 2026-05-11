package netdaemon

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// TunnelManager handles reverse tunnels over WebSockets with inner mTLS.
type TunnelManager struct {
	mu        sync.RWMutex
	sessions  map[string]*yamux.Session    // key: nodeID
	listeners map[string]net.Listener      // key: nodeID:port
	portMap   map[string]map[int]int       // key: nodeID, inner key: remotePort, value: localPort
}

func NewTunnelManager() *TunnelManager {
	return &TunnelManager{
		sessions:  make(map[string]*yamux.Session),
		listeners: make(map[string]net.Listener),
		portMap:   make(map[string]map[int]int),
	}
}

// ─── Brain Side (Server) ─────────────────────────────────────────────────────

func (tm *TunnelManager) HTTPHandler(caPEM string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			slog.Error("tunnel server: upgrade failed", "err", err)
			return
		}

		wsConn := &wsNetConn{conn}

		// Load server cert for inner mTLS
		certFile := "/etc/featherdeploy/node.crt"
		keyFile := "/etc/featherdeploy/node.key"
		serverCert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			slog.Error("tunnel server: load cert failed", "err", err)
			wsConn.Close()
			return
		}

		caPool := x509.NewCertPool()
		caPool.AppendCertsFromPEM([]byte(caPEM))

		tlsCfg := &tls.Config{
			Certificates: []tls.Certificate{serverCert},
			ClientAuth:   tls.RequireAndVerifyClientCert,
			ClientCAs:    caPool,
		}

		tlsConn := tls.Server(wsConn, tlsCfg)
		if err := tlsConn.Handshake(); err != nil {
			slog.Error("tunnel server: inner mTLS handshake failed", "err", err)
			tlsConn.Close()
			return
		}

		nodeID := tlsConn.ConnectionState().PeerCertificates[0].Subject.CommonName
		slog.Info("tunnel server: verified node identity via inner mTLS", "node", nodeID)

		session, err := yamux.Server(tlsConn, nil)
		if err != nil {
			slog.Error("tunnel server: yamux session", "node", nodeID, "err", err)
			tlsConn.Close()
			return
		}

		tm.mu.Lock()
		if old, ok := tm.sessions[nodeID]; ok {
			old.Close()
		}
		tm.sessions[nodeID] = session
		tm.mu.Unlock()

		tm.setupNodeProxies(nodeID)

		<-session.CloseChan()
		
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

	if _, ok := tm.portMap[nodeID]; !ok {
		tm.portMap[nodeID] = make(map[int]int)
	}

	ports := []int{4001, 4002, 2379, 2380, 7443}
	idx := len(tm.sessions)
	base := 20000 + (idx * 10)

	for i, p := range ports {
		localPort := base + i
		tm.portMap[nodeID][p] = localPort
		go tm.startInternalProxy(nodeID, localPort, p)
	}
}

func (tm *TunnelManager) startInternalProxy(nodeID string, localPort, remotePort int) {
	addr := fmt.Sprintf("127.0.0.1:%d", localPort)
	l, err := net.Listen("tcp", addr)
	if err != nil {
		slog.Error("tunnel server: failed to listen for proxy", "addr", addr, "err", err)
		return
	}
	tm.mu.Lock()
	tm.listeners[nodeID+":"+fmt.Sprint(localPort)] = l
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

func (tm *TunnelManager) StartClient(ctx context.Context, brainURL string, nodeID string, tlsCfg *tls.Config) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
			if err := tm.dialAndServeWS(ctx, brainURL, nodeID, tlsCfg); err != nil {
				slog.Error("tunnel client: disconnected, retrying in 5s", "err", err)
				time.Sleep(5 * time.Second)
			}
		}
	}
}

func (tm *TunnelManager) dialAndServeWS(ctx context.Context, brainURL string, nodeID string, tlsCfg *tls.Config) error {
	dialer := websocket.Dialer{
		Proxy:            http.ProxyFromEnvironment,
		HandshakeTimeout: 10 * time.Second,
		TLSClientConfig:  &tls.Config{InsecureSkipVerify: true},
	}
	conn, _, err := dialer.DialContext(ctx, brainURL, nil)
	if err != nil {
		return err
	}
	wsConn := &wsNetConn{conn}
	tlsConn := tls.Client(wsConn, tlsCfg)
	if err := tlsConn.Handshake(); err != nil {
		tlsConn.Close()
		return err
	}
	session, err := yamux.Client(tlsConn, nil)
	if err != nil {
		tlsConn.Close()
		return err
	}
	defer session.Close()
	tm.mu.Lock()
	tm.sessions["brain"] = session
	tm.mu.Unlock()
	slog.Info("tunnel client: established secure mTLS tunnel to brain", "url", brainURL)
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
		return
	}
	stream, err := session.Open()
	if err != nil {
		return
	}
	defer stream.Close()
	portBuf := []byte{byte(remotePort >> 8), byte(remotePort & 0xff)}
	stream.Write(portBuf)
	cp := func(dst, src net.Conn) { io.Copy(dst, src) }
	go cp(client, stream)
	io.Copy(stream, client)
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
			go tm.handleNodeToBrainProxyConn(remotePort, client)
		}
	}()
	return nil
}

func (tm *TunnelManager) handleNodeToBrainProxyConn(remotePort int, client net.Conn) {
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
	portBuf := []byte{byte(remotePort >> 8), byte(remotePort & 0xff)}
	stream.Write(portBuf)
	cp := func(dst, src net.Conn) { io.Copy(dst, src) }
	go cp(client, stream)
	io.Copy(stream, client)
}

func (tm *TunnelManager) handleIncomingStream(stream net.Conn) {
	defer stream.Close()
	portBuf := make([]byte, 2)
	if _, err := io.ReadFull(stream, portBuf); err != nil {
		return
	}
	remotePort := int(portBuf[0])<<8 | int(portBuf[1])
	local, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", remotePort))
	if err != nil {
		return
	}
	defer local.Close()
	cp := func(dst, src net.Conn) { io.Copy(dst, src) }
	go cp(local, stream)
	io.Copy(stream, local)
}

// ─── WebSocket to net.Conn Adapter ───────────────────────────────────────────

type wsNetConn struct {
	*websocket.Conn
}

func (c *wsNetConn) Read(b []byte) (n int, err error) {
	_, r, err := c.Conn.NextReader()
	if err != nil {
		return 0, err
	}
	return r.Read(b)
}

func (c *wsNetConn) Write(b []byte) (n int, err error) {
	w, err := c.Conn.NextWriter(websocket.BinaryMessage)
	if err != nil {
		return 0, err
	}
	defer w.Close()
	return w.Write(b)
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
