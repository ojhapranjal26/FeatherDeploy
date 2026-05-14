package proxyengine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/ojhapranjal26/featherdeploy/backend/internal/netdaemon"
)

// Route defines the desired state of a routing rule in Etcd.
type Route struct {
	Domain     string `json:"domain"`
	Service    string `json:"service"`
	TargetNode string `json:"target_node"`
	TargetIP   string `json:"target_ip"` // fallback or initial IP
	TargetPort int    `json:"target_port"`
	HostPort   int    `json:"host_port"`   // Direct Podman host port
	Mode       string `json:"mode"`
	Version    int64  `json:"version"`
}

type NodeState struct {
	IP       string `json:"ip"`
	Port     int    `json:"port"`
	WgMeshIP string `json:"wg_mesh_ip"`
}

type Engine struct {
	etcd            *clientv3.Client
	nodeID          string
	assignedDomains map[string]bool
	isBrain         bool

	mu     sync.RWMutex
	routes map[string]Route     // domain -> Route
	nodes  map[string]NodeState // node_id -> live NodeState

	updateCh chan struct{}
}

func NewEngine(etcdCli *clientv3.Client, nodeID string, isBrain bool, assignedDomains []string) *Engine {
	domainMap := make(map[string]bool)
	for _, d := range assignedDomains {
		domainMap[d] = true
	}
	return &Engine{
		etcd:            etcdCli,
		nodeID:          nodeID,
		assignedDomains: domainMap,
		isBrain:         isBrain,
		routes:          make(map[string]Route),
		nodes:           make(map[string]NodeState),
		updateCh:        make(chan struct{}, 1),
	}
}

func (e *Engine) Start(ctx context.Context) {
	// 1. Initial full sync
	e.fullSync(ctx)

	// 2. Start watchers
	go e.watchRoutes(ctx)
	go e.watchNodes(ctx)

	// 3. Debounced Caddy update loop
	go e.updateLoop(ctx)

	// 4. Reconciliation loop
	go e.reconcileLoop(ctx)
}

func (e *Engine) triggerUpdate() {
	select {
	case e.updateCh <- struct{}{}:
	default:
	}
}

func (e *Engine) updateLoop(ctx context.Context) {
	var timer *time.Timer
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.updateCh:
			if timer != nil {
				timer.Stop()
			}
			timer = time.AfterFunc(500*time.Millisecond, func() {
				e.applyToCaddy()
			})
		}
	}
}

func (e *Engine) reconcileLoop(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.fullSync(ctx)
		}
	}
}

func (e *Engine) isRouteRelevant(r Route) bool {
	// Route is relevant if this node handles ingress for it,
	// or this node is the host running the container.
	if e.isBrain {
		return true // Brain routes everything
	}
	if e.assignedDomains[r.Domain] {
		return true // We are edge ingress for this domain
	}
	if r.TargetNode == e.nodeID {
		return true // We host this container
	}
	return false
}

func (e *Engine) fullSync(ctx context.Context) {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Fetch Nodes
	nodeResp, err := e.etcd.Get(ctx, "/nodes/heartbeat/", clientv3.WithPrefix())
	if err == nil {
		newNodes := make(map[string]NodeState)
		for _, kv := range nodeResp.Kvs {
			id := strings.TrimPrefix(string(kv.Key), "/nodes/heartbeat/")
			var ns NodeState
			if err := json.Unmarshal(kv.Value, &ns); err == nil {
				newNodes[id] = ns
			}
		}
		e.nodes = newNodes
	}

	// Fetch Routes
	routeResp, err := e.etcd.Get(ctx, "/routing/", clientv3.WithPrefix())
	if err == nil {
		newRoutes := make(map[string]Route)
		for _, kv := range routeResp.Kvs {
			var r Route
			if err := json.Unmarshal(kv.Value, &r); err == nil {
				if e.isRouteRelevant(r) {
					newRoutes[r.Domain] = r
				}
			}
		}
		e.routes = newRoutes
	}
	e.triggerUpdate()
}

func (e *Engine) watchRoutes(ctx context.Context) {
	watchChan := e.etcd.Watch(ctx, "/routing/", clientv3.WithPrefix())
	for watchResp := range watchChan {
		e.mu.Lock()
		changed := false
		for _, ev := range watchResp.Events {
			domain := strings.TrimPrefix(string(ev.Kv.Key), "/routing/")
			if ev.Type == mvccpb.DELETE {
				if _, ok := e.routes[domain]; ok {
					delete(e.routes, domain)
					changed = true
				}
			} else {
				var r Route
				if err := json.Unmarshal(ev.Kv.Value, &r); err == nil {
					if e.isRouteRelevant(r) {
						existing, ok := e.routes[domain]
						if !ok || r.Version > existing.Version {
							e.routes[domain] = r
							changed = true
						}
					} else {
						if _, ok := e.routes[domain]; ok {
							delete(e.routes, domain)
							changed = true
						}
					}
				}
			}
		}
		e.mu.Unlock()
		if changed {
			e.triggerUpdate()
		}
	}
}

func (e *Engine) watchNodes(ctx context.Context) {
	watchChan := e.etcd.Watch(ctx, "/nodes/heartbeat/", clientv3.WithPrefix())
	for watchResp := range watchChan {
		e.mu.Lock()
		changed := false
		for _, ev := range watchResp.Events {
			id := strings.TrimPrefix(string(ev.Kv.Key), "/nodes/heartbeat/")
			if ev.Type == mvccpb.DELETE {
				if _, ok := e.nodes[id]; ok {
					delete(e.nodes, id)
					changed = true
				}
			} else {
				var ns NodeState
				if err := json.Unmarshal(ev.Kv.Value, &ns); err == nil {
					e.nodes[id] = ns
					changed = true
				}
			}
		}
		e.mu.Unlock()
		if changed {
			e.triggerUpdate()
		}
	}
}

func (e *Engine) applyToCaddy() {
	e.mu.RLock()
	routes := make([]Route, 0, len(e.routes))
	for _, r := range e.routes {
		routes = append(routes, r)
	}
	// Take snapshot of nodes to resolve IPs
	nodes := make(map[string]NodeState)
	for k, v := range e.nodes {
		nodes[k] = v
	}
	e.mu.RUnlock()

	caddyRoutes := buildCaddyRoutes(routes, nodes, e.nodeID)
	// 1. Fetch existing routes from Caddy to preserve the Panel route and other Caddyfile routes.
	var existingRoutes []map[string]any
	respGet, err := http.Get("http://localhost:2019/config/apps/http/servers/srv0/routes")
	if err == nil && respGet.StatusCode == 200 {
		b, _ := io.ReadAll(respGet.Body)
		json.Unmarshal(b, &existingRoutes)
		respGet.Body.Close()
	} else if respGet != nil {
		respGet.Body.Close()
	}

	// 2. Filter out our old dynamic routes (identified by @id starting with fd_dynamic_)
	var mergedRoutes []map[string]any
	for _, r := range existingRoutes {
		idStr, _ := r["@id"].(string)
		if !strings.HasPrefix(idStr, "fd_dynamic_") {
			mergedRoutes = append(mergedRoutes, r)
		}
	}

	// 3. Append our new dynamic routes
	mergedRoutes = append(mergedRoutes, caddyRoutes...)

	payload, _ := json.Marshal(mergedRoutes)

	req, err := http.NewRequest("PATCH", "http://localhost:2019/config/apps/http/servers/srv0/routes", bytes.NewReader(payload))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Warn("proxyengine: failed to patch caddy", "err", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		slog.Warn("proxyengine: caddy rejected config", "status", resp.StatusCode, "body", string(b))
	} else {
		slog.Info("proxyengine: successfully pushed batched routes to Caddy", "dynamic_count", len(caddyRoutes), "total_count", len(mergedRoutes))
	}
}

func buildCaddyRoutes(routes []Route, nodes map[string]NodeState, myNodeID string) []map[string]any {
	var out []map[string]any

	for _, r := range routes {
		var dialAddr string
		var useTLS bool
		if r.TargetNode == myNodeID || (myNodeID == "main" && (r.TargetNode == "" || r.TargetNode == "main")) {
			// Local routing: talk directly to Podman host port if available, otherwise cluster port
			port := r.TargetPort
			if r.HostPort > 0 {
				port = r.HostPort
			}
			dialAddr = fmt.Sprintf("127.0.0.1:%d", port)
			useTLS = false
		} else {
			// Cross-node routing
			var targetIP string
			ns, healthy := nodes[r.TargetNode]
			if !healthy {
				slog.Debug("proxyengine: dropping route, target node offline", "domain", r.Domain, "target", r.TargetNode)
				continue
			}

			if ns.WgMeshIP != "" {
				targetIP = ns.WgMeshIP
			} else {
				targetIP = ns.IP
			}

			// If we are on the brain, prioritize routing through the tunnel directly to the worker's application port.
			// This is much more reliable as it bypasses the worker's Caddy and its potential certificate issues.
			if myNodeID == "main" && netdaemon.GlobalTunnel != nil {
				if localPort := netdaemon.GlobalTunnel.EnsureServiceProxy(r.TargetNode, r.TargetPort); localPort > 0 {
					dialAddr = fmt.Sprintf("127.0.0.1:%d", localPort)
					useTLS = false // Tunnel is already secure, backend (fdnet/container) speaks HTTP
				} else if proxyAddr := netdaemon.GlobalTunnel.GetNodeProxyAddr(r.TargetNode, 443); proxyAddr != "" {
					dialAddr = proxyAddr
					useTLS = true
				} else {
					dialAddr = fmt.Sprintf("%s:443", targetIP)
					useTLS = true
				}
			} else {
				// Fallback to mesh/public IP + TLS
				dialAddr = fmt.Sprintf("%s:443", targetIP)
				useTLS = true
			}
		}

		route := map[string]any{
			"@id": "fd_dynamic_" + r.Domain,
			"match": []map[string]any{
				{
					"host": []string{r.Domain},
					"not": []map[string]any{
						{
							"path": []string{"/.well-known/acme-challenge/*"},
						},
					},
				},
			},
			"handle": []map[string]any{
				{
					"handler": "reverse_proxy",
					"upstreams": []map[string]any{
						{
							"dial": dialAddr,
						},
					},
				},
			},
			"terminal": true,
		}

		// If TLS is required for this hop, configure the transport
		if useTLS {
			route["handle"].([]map[string]any)[0]["transport"] = map[string]any{
				"protocol": "http",
				"tls": map[string]any{
					"insecure_skip_verify": true, // Using self-signed / internal CA
				},
			}
		}

		out = append(out, route)
	}

	return out
}
