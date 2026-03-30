package netdaemon

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
)

// tcpProxy listens on a cluster port and transparently forwards each TCP
// connection to a single backend address (nodeAddr:hostPort).
//
// Design notes:
//   - Each proxy is one goroutine for the accept loop plus two goroutines per
//     active connection (bidirectional copy).  Memory and CPU overhead per idle
//     proxy is effectively zero.
//   - Connections are forwarded at the TCP stream level; the proxy is protocol-
//     agnostic so it works with HTTP, PostgreSQL, Redis, MySQL, gRPC, etc.
//   - For multi-node use, targetAddr is simply set to remoteNode:hostPort.
type tcpProxy struct {
	listenPort int
	targetAddr string // "host:port" to dial for every incoming connection

	listener net.Listener
	stopped  atomic.Bool
	wg       sync.WaitGroup
}

func newTCPProxy(listenPort int, targetHost string, targetPort int) *tcpProxy {
	return &tcpProxy{
		listenPort: listenPort,
		targetAddr: fmt.Sprintf("%s:%d", targetHost, targetPort),
	}
}

// start opens the listen socket and begins accepting connections.
func (p *tcpProxy) start() error {
	ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", p.listenPort))
	if err != nil {
		// Some kernels/configs keep listening on 0.0.0.0 but not on a specific
		// address. Fall back to loopback-only if the wildcard bind fails.
		ln, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p.listenPort))
		if err != nil {
			return fmt.Errorf("fdnet proxy: listen on port %d: %w", p.listenPort, err)
		}
	}
	p.listener = ln

	p.wg.Add(1)
	go p.accept()
	slog.Debug("fdnet proxy: listening", "port", p.listenPort, "target", p.targetAddr)
	return nil
}

// stop gracefully shuts down the proxy.  Existing forwarding goroutines drain
// naturally when the underlying connections are closed by the peers.
func (p *tcpProxy) stop() {
	if p.stopped.Swap(true) {
		return // already stopping
	}
	if p.listener != nil {
		p.listener.Close()
	}
	p.wg.Wait()
	slog.Debug("fdnet proxy: stopped", "port", p.listenPort)
}

func (p *tcpProxy) accept() {
	defer p.wg.Done()
	for {
		conn, err := p.listener.Accept()
		if err != nil {
			if !p.stopped.Load() {
				slog.Warn("fdnet proxy: accept error",
					"port", p.listenPort, "err", err)
			}
			return
		}
		p.wg.Add(1)
		go p.forward(conn)
	}
}

// forward copies data bidirectionally between the accepted client connection
// (src) and a fresh connection to the backend (dst).
func (p *tcpProxy) forward(src net.Conn) {
	defer p.wg.Done()
	defer src.Close()

	dst, err := net.Dial("tcp", p.targetAddr)
	if err != nil {
		// Backend is unreachable — close the client cleanly.
		slog.Debug("fdnet proxy: dial backend failed",
			"target", p.targetAddr, "err", err)
		return
	}
	defer dst.Close()

	// Enable TCP keepalive on both sides so stalled connections are detected
	// and cleaned up instead of accumulating as zombie goroutines.
	setKeepalive(src)
	setKeepalive(dst)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		io.Copy(dst, src) //nolint:errcheck — EOF on copy is normal
		// Signal the other direction to stop.
		if tc, ok := dst.(*net.TCPConn); ok {
			tc.CloseWrite() //nolint:errcheck
		}
	}()
	go func() {
		defer wg.Done()
		io.Copy(src, dst) //nolint:errcheck
		if tc, ok := src.(*net.TCPConn); ok {
			tc.CloseWrite() //nolint:errcheck
		}
	}()
	wg.Wait()
}

// setKeepalive enables TCP keepalive on a connection if it supports it.
func setKeepalive(conn net.Conn) {
	if tc, ok := conn.(*net.TCPConn); ok {
		tc.SetKeepAlive(true) //nolint:errcheck
	}
}
