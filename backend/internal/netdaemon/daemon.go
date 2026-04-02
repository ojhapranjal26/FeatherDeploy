// Package netdaemon implements FeatherDeploy's lightweight internal network
// proxy that replaces Podman named-bridge networks (netavark / aardvark-dns).
//
// Problem:  Podman's named-bridge networking relies on netavark + aardvark-dns.
// These helpers are not reliably installed on every Linux distribution, causing
// "network not found" failures at deployment time.
//
// Solution: Instead of named Podman networks each container:
//   - Is started with --network=slirp4netns (always available) or --network=host.
//   - Gets a unique published host port (-p hostPort:appPort).
//
// The Daemon runs inside the main FeatherDeploy process.  It keeps a registry of
// {project / service} → {hostPort, clusterPort} and starts one lightweight TCP
// proxy goroutine per active service.  The proxy listens on 0.0.0.0:clusterPort
// and forwards connections to 127.0.0.1:hostPort.
//
// Service discovery inside containers is handled by env-var injection:
//   <SVCNAME>_HOST=10.0.2.2       ← slirp4netns gateway → host loopback
//   <SVCNAME>_PORT=<clusterPort>  ← stable proxy port for that service
//   <SVCNAME>_URL=<proto>://10.0.2.2:<clusterPort>
//
// Multi-node extensibility: ServiceEntry.NodeAddr holds "127.0.0.1" for local
// services and a remote node IP for cross-node services.  The proxy just dials
// NodeAddr:HostPort in both cases, so no code change is needed to support
// remote nodes — only the registry content changes.
package netdaemon

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	// clusterPortMin / clusterPortMax define the range of ports the daemon
	// allocates for its per-service TCP proxies.  The range is intentionally
	// outside well-known port allocations (0-1023) and the host-port range
	// used for published container ports (10000-29999).
	clusterPortMin = 30000
	clusterPortMax = 59999
)

// ServiceEntry is the unit of registration.  It is also the JSON schema for
// the persistent state file so the proxy can be rebuilt after a restart.
type ServiceEntry struct {
	ProjectID     int64  `json:"project_id"`
	ServiceName   string `json:"service_name"`   // e.g. "webapp", "db", "redis"
	ContainerID   string `json:"container_id"`   // podman container ID (informational)
	ContainerPort int    `json:"container_port"` // app's listening port inside the container
	HostPort      int    `json:"host_port"`      // the -p published host port
	ClusterPort   int    `json:"cluster_port"`   // proxy listen port stable address
	// NodeAddr is "127.0.0.1" for local containers.  Set this to a remote
	// node's IP when extending to multi-node deployments.
	NodeAddr string `json:"node_addr"`
	// Status: "active" while the container is running; "stopped" otherwise.
	Status string `json:"status"`
}

// Daemon is the central in-process network proxy manager.  Create one with
// New() and call Start() once at server startup.
type Daemon struct {
	mu        sync.RWMutex
	services  map[string]*ServiceEntry // key: "projectID/serviceName"
	proxies   map[string]*tcpProxy     // key: same as services
	ports     map[int]bool             // allocated cluster ports
	statePath string
	done      chan struct{} // closed by Stop() to signal background goroutines
}

// New creates a Daemon using statePath for persistent state.
// It loads any previously saved state (port allocations, registrations) but
// does NOT start proxy goroutines — call ReconcileRegistered() after the
// container runtime is known to be running to restart proxies for active services.
func New(statePath string) *Daemon {
	d := &Daemon{
		services:  make(map[string]*ServiceEntry),
		proxies:   make(map[string]*tcpProxy),
		ports:     make(map[int]bool),
		statePath: statePath,
		done:      make(chan struct{}),
	}
	d.loadState()
	return d
}

// serviceKey returns the canonical map key for a project/service pair.
func serviceKey(projectID int64, svcName string) string {
	return fmt.Sprintf("%d/%s", projectID, svcName)
}

// Register adds or replaces a service in the registry and starts a TCP proxy
// for it.  Returns the cluster port that other containers in the project should
// use to reach this service.
//
// If the service was already registered (e.g. redeployment), the old proxy is
// stopped and a new one is started on a fresh port.
func (d *Daemon) Register(projectID int64, svcName, nodeAddr, containerID string, hostPort, containerPort int) (int, error) {
	if hostPort <= 0 {
		return 0, fmt.Errorf("fdnet: Register: hostPort must be > 0 (got %d)", hostPort)
	}
	if svcName == "" {
		return 0, fmt.Errorf("fdnet: Register: svcName must not be empty")
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	k := serviceKey(projectID, svcName)

	// Stop and remove any previous registration so we always start fresh.
	if old, ok := d.services[k]; ok {
		if p, ok := d.proxies[k]; ok {
			p.stop()
			delete(d.proxies, k)
		}
		delete(d.ports, old.ClusterPort)
	}

	cp, err := d.allocatePort()
	if err != nil {
		return 0, err
	}

	addr := nodeAddr
	if addr == "" {
		addr = "127.0.0.1"
	}

	entry := &ServiceEntry{
		ProjectID:     projectID,
		ServiceName:   svcName,
		ContainerID:   containerID,
		ContainerPort: containerPort,
		HostPort:      hostPort,
		ClusterPort:   cp,
		NodeAddr:      addr,
		Status:        "active",
	}
	d.services[k] = entry
	d.ports[cp] = true

	proxy := newTCPProxy(cp, addr, hostPort)
	if err := proxy.start(); err != nil {
		delete(d.services, k)
		delete(d.ports, cp)
		return 0, fmt.Errorf("fdnet: start proxy for %s: %w", k, err)
	}
	d.proxies[k] = proxy
	d.saveState()

	slog.Info("fdnet: service registered",
		"project", projectID, "name", svcName,
		"clusterPort", cp, "hostPort", hostPort, "node", addr)
	return cp, nil
}

// Deregister stops the proxy for a service and removes it from the registry.
// Safe to call even if the service was never registered.
func (d *Daemon) Deregister(projectID int64, svcName string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	k := serviceKey(projectID, svcName)
	if p, ok := d.proxies[k]; ok {
		p.stop()
		delete(d.proxies, k)
	}
	if entry, ok := d.services[k]; ok {
		delete(d.ports, entry.ClusterPort)
		delete(d.services, k)
	}
	d.saveState()
	slog.Info("fdnet: service deregistered", "project", projectID, "name", svcName)
}

// MarkStopped transitions a service entry to "stopped" without removing it.
// The cluster port is retained so that in-flight connections drain gracefully.
func (d *Daemon) MarkStopped(projectID int64, svcName string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	k := serviceKey(projectID, svcName)
	if entry, ok := d.services[k]; ok {
		entry.Status = "stopped"
	}
	if p, ok := d.proxies[k]; ok {
		p.stop()
		delete(d.proxies, k)
	}
	d.saveState()
}

// Resolve returns the cluster port for a service so callers can build env vars.
// Returns (0, false) when the service is not registered or is stopped.
func (d *Daemon) Resolve(projectID int64, svcName string) (clusterPort int, ok bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	k := serviceKey(projectID, svcName)
	if e, found := d.services[k]; found && e.Status == "active" {
		return e.ClusterPort, true
	}
	return 0, false
}

// ListProject returns all active ServiceEntry records for the given project.
// The slice is a copy — safe to read without holding the lock.
func (d *Daemon) ListProject(projectID int64) []ServiceEntry {
	d.mu.RLock()
	defer d.mu.RUnlock()

	prefix := fmt.Sprintf("%d/", projectID)
	var out []ServiceEntry
	for k, e := range d.services {
		if strings.HasPrefix(k, prefix) && e.Status == "active" {
			out = append(out, *e)
		}
	}
	return out
}

// ReconcileRegistered restarts TCP proxies for all entries that were loaded
// from the state file and are still marked "active".  Call once at startup
// after the Daemon is created.
func (d *Daemon) ReconcileRegistered() {
	d.mu.Lock()
	defer d.mu.Unlock()

	for k, entry := range d.services {
		if entry.Status != "active" {
			continue
		}
		if _, running := d.proxies[k]; running {
			continue // already started
		}
		proxy := newTCPProxy(entry.ClusterPort, entry.NodeAddr, entry.HostPort)
		if err := proxy.start(); err != nil {
			slog.Warn("fdnet: could not restart proxy for persisted service",
				"key", k, "err", err)
			continue
		}
		d.proxies[k] = proxy
		slog.Info("fdnet: proxy restarted for persisted service", "key", k,
			"clusterPort", entry.ClusterPort)
	}

	// Start the watchdog after reconcile so it monitors all just-started proxies.
	d.startWatchdog()
}

// Stop shuts down all active proxies.  Call during server shutdown.
func (d *Daemon) Stop() {
	// Signal the watchdog goroutine to exit.
	select {
	case <-d.done:
		// already closed
	default:
		close(d.done)
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	for k, p := range d.proxies {
		p.stop()
		delete(d.proxies, k)
	}
	slog.Info("fdnet: all proxies stopped")
}

// Stats returns a snapshot of registered services for health / debug endpoints.
func (d *Daemon) Stats() []ServiceEntry {
	d.mu.RLock()
	defer d.mu.RUnlock()

	out := make([]ServiceEntry, 0, len(d.services))
	for _, e := range d.services {
		out = append(out, *e)
	}
	return out
}

// ─── Internal helpers ─────────────────────────────────────────────────────────

func (d *Daemon) allocatePort() (int, error) {
	// Sequential search is O(n) but predictable and avoids the 1000-iteration
	// retry limit of the previous random approach.  With a 30k-port range and
	// at most a few hundred services, this is always fast.
	for p := clusterPortMin; p <= clusterPortMax; p++ {
		if !d.ports[p] {
			return p, nil
		}
	}
	return 0, fmt.Errorf("fdnet: exhausted cluster port range [%d-%d]",
		clusterPortMin, clusterPortMax)
}

// startWatchdog launches a background goroutine that checks every 30 seconds
// whether the TCP proxy for each "active" service is still running.  If a
// proxy goroutine has died (e.g. after a transient bind failure at startup)
// it is automatically restarted so services self-heal without requiring a
// full featherdeploy restart.
func (d *Daemon) startWatchdog() {
	ticker := time.NewTicker(30 * time.Second)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				d.repairDeadProxies()
			case <-d.done:
				return
			}
		}
	}()
}

// repairDeadProxies restarts any proxy that should be running but isn't.
// It is called periodically by the watchdog goroutine.
func (d *Daemon) repairDeadProxies() {
	d.mu.Lock()
	defer d.mu.Unlock()

	for k, entry := range d.services {
		if entry.Status != "active" {
			continue
		}
		if _, running := d.proxies[k]; running {
			continue
		}
		// Proxy is missing for an active service — restart it.
		proxy := newTCPProxy(entry.ClusterPort, entry.NodeAddr, entry.HostPort)
		if err := proxy.start(); err != nil {
			slog.Warn("fdnet watchdog: could not restart dead proxy",
				"key", k, "clusterPort", entry.ClusterPort, "err", err)
			continue
		}
		d.proxies[k] = proxy
		slog.Info("fdnet watchdog: restarted dead proxy",
			"key", k, "clusterPort", entry.ClusterPort)
	}
}

func (d *Daemon) saveState() {
	entries := make([]*ServiceEntry, 0, len(d.services))
	for _, e := range d.services {
		entries = append(entries, e)
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return
	}
	// Write atomically via a temp file + rename.
	tmp := d.statePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err == nil {
		os.Rename(tmp, d.statePath) //nolint — best-effort
	}
}

func (d *Daemon) loadState() {
	data, err := os.ReadFile(d.statePath)
	if err != nil {
		return // first run or missing file — normal
	}
	var entries []*ServiceEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		slog.Warn("fdnet: ignoring unreadable state file", "path", d.statePath, "err", err)
		return
	}
	for _, e := range entries {
		if e == nil || e.ServiceName == "" {
			continue
		}
		d.services[serviceKey(e.ProjectID, e.ServiceName)] = e
		d.ports[e.ClusterPort] = true
	}
	slog.Info("fdnet: loaded persisted state", "services", len(d.services))
}
