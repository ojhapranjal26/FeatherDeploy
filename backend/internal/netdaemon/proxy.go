package netdaemon

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// copyBufPool recycles 32 KB byte slices for io.CopyBuffer.
// 32 KB matches the typical Linux TCP socket buffer and halves syscall overhead
// versus the previous 4 KB slice on high-throughput connections.
var copyBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 32*1024)
		return &b
	},
}

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
		// Fall back to loopback-only if the wildcard bind fails (unusual config).
		ln, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p.listenPort))
		if err != nil {
			return fmt.Errorf("fdnet proxy: listen on port %d: %w", p.listenPort, err)
		}
	}
	p.listener = ln

	p.wg.Add(1)
	go p.accept()
	slog.Info("fdnet proxy: listening", "port", p.listenPort, "target", p.targetAddr)
	return nil
}

// stop gracefully shuts down the proxy. Existing forwarding goroutines drain
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

// reachable performs a quick dial to check if the backend is currently up.
// Used by the daemon watchdog to detect rootlessport binding failures early.
func (p *tcpProxy) reachable() bool {
	conn, err := net.DialTimeout("tcp", p.targetAddr, 500*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
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

		slog.Info("fdnet proxy: incoming connection",
			"listenPort", p.listenPort,
			"client", conn.RemoteAddr().String(),
			"target", p.targetAddr)

		p.wg.Add(1)
		go p.forward(conn)
	}
}

// forward copies data bidirectionally between the accepted client connection
// (src) and a fresh connection to the backend (dst).
func (p *tcpProxy) forward(src net.Conn) {
	defer p.wg.Done()
	defer src.Close()

	dst, err := dialBackendWithRetry(p.targetAddr, 90*time.Second)
	if err != nil {
		slog.Error("fdnet proxy: dial backend failed — 502 Bad Gateway",
			"listenPort", p.listenPort, "target", p.targetAddr, "err", err)
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
		copyConn(dst, src)
		// Signal the other direction to stop.
		if tc, ok := dst.(*net.TCPConn); ok {
			tc.CloseWrite() //nolint:errcheck
		}
	}()
	go func() {
		defer wg.Done()
		copyConn(src, dst)
		if tc, ok := src.(*net.TCPConn); ok {
			tc.CloseWrite() //nolint:errcheck
		}
	}()
	wg.Wait()
}

// dialBackendWithRetry attempts to connect to target until either the
// connection succeeds or maxWait has elapsed.
//
// Root cause of 502: with slirp4netns + "-p port:port", rootlessport takes
// 1–5 seconds to bind the host port after the container starts. Caddy may
// send the first request before rootlessport is ready; retrying for 90 s
// ensures the connection eventually succeeds.
//
// Error classification:
//   - ECONNREFUSED: nothing is listening yet (rootlessport still starting).
//     Use a short per-attempt timeout so we retry frequently.
//   - Timeout / other: port is bound but not forwarding. Longer per-attempt
//     timeout to allow rootlessport to finish initialising.
func dialBackendWithRetry(target string, maxWait time.Duration) (net.Conn, error) {
	deadline := time.Now().Add(maxWait)
	backoff := 100 * time.Millisecond
	attempt := 0
	var lastErr error

	for {
		attempt++

		// Short timeout on loopback: a refused connection returns immediately
		// (RST), and a successful one completes in < 1 ms.
		perAttemptTimeout := 1 * time.Second
		if isTimeoutErr(lastErr) {
			// Previous attempt timed out — port is bound but not forwarding yet.
			perAttemptTimeout = 3 * time.Second
		}

		conn, err := net.DialTimeout("tcp", target, perAttemptTimeout)
		if err == nil {
			if attempt > 1 {
				elapsed := maxWait - time.Until(deadline)
				slog.Info("fdnet proxy: backend became reachable",
					"target", target, "attempts", attempt,
					"elapsed", elapsed.Truncate(time.Millisecond))
			}
			return conn, nil
		}
		lastErr = err

		if time.Now().After(deadline) {
			break
		}

		// Log progress on attempt 1, 5, 15 to avoid spamming logs.
		if attempt == 1 || attempt == 5 || attempt == 15 {
			if isConnRefused(err) {
				slog.Warn("fdnet proxy: backend not ready yet (rootlessport still starting)",
					"target", target, "attempt", attempt)
			} else {
				slog.Warn("fdnet proxy: backend dial stalled — if persists, check: ss -tlnp | grep PORT",
					"target", target, "attempt", attempt, "err", err)
			}
		}

		sleepTime := backoff
		if remaining := time.Until(deadline); sleepTime > remaining {
			sleepTime = remaining
		}
		time.Sleep(sleepTime)
		backoff *= 2
		if backoff > 2*time.Second {
			backoff = 2 * time.Second
		}
	}

	return nil, fmt.Errorf("backend %s unavailable after %s: %w", target, maxWait, lastErr)
}

// isConnRefused returns true when err is a "connection refused" error.
func isConnRefused(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "connection refused")
}

// isTimeoutErr returns true when err is a dial timeout (i/o timeout).
func isTimeoutErr(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// copyConn copies from src to dst using a pooled 4 KB buffer to minimise
// per-connection heap allocations and GC overhead.
func copyConn(dst io.Writer, src io.Reader) {
	bufp := copyBufPool.Get().(*[]byte)
	defer copyBufPool.Put(bufp)
	io.CopyBuffer(dst, src, *bufp) //nolint:errcheck — EOF is normal
}

// setKeepalive enables TCP keepalive on a connection so stalled idle
// connections are detected and cleaned up (avoids zombie goroutines).
func setKeepalive(conn net.Conn) {
	if tc, ok := conn.(*net.TCPConn); ok {
		tc.SetKeepAlive(true)                  //nolint:errcheck
		tc.SetKeepAlivePeriod(30 * time.Second) //nolint:errcheck
		tc.SetNoDelay(true)                    //nolint:errcheck
	}
}
