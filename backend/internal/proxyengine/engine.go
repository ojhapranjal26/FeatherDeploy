package proxyengine

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/ojhapranjal26/featherdeploy/backend/internal/nginx"
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
	timer := time.NewTimer(time.Hour) // long initial wait
	timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.updateCh:
			timer.Reset(500 * time.Millisecond)
		case <-timer.C:
			e.applyToNginx()
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

func (e *Engine) applyToNginx() {
	e.mu.RLock()
	routes := make([]Route, 0, len(e.routes))
	for _, r := range e.routes {
		routes = append(routes, r)
	}
	nodes := make(map[string]NodeState)
	for k, v := range e.nodes {
		nodes[k] = v
	}
	e.mu.RUnlock()

	anyChanged := false
	for _, r := range routes {
		config := buildNginxConfig(r, nodes, e.nodeID)
		if config == "" {
			continue
		}
		changed, err := nginx.WriteServiceConfig(r.Domain, config)
		if err != nil {
			slog.Warn("proxyengine: failed to write nginx config", "domain", r.Domain, "err", err)
		}
		if changed {
			anyChanged = true
		}
	}

	if anyChanged {
		if err := nginx.ReloadNginx(); err != nil {
			slog.Error("proxyengine: batch nginx reload failed", "err", err)
		}
	}
}

func buildNginxConfig(r Route, nodes map[string]NodeState, myNodeID string) string {
	var dialAddr string
	if r.TargetNode == myNodeID || (myNodeID == "main" && (r.TargetNode == "" || r.TargetNode == "main")) {
		port := r.TargetPort
		if r.HostPort > 0 {
			port = r.HostPort
		}
		dialAddr = fmt.Sprintf("127.0.0.1:%d", port)
	} else {
		ns, healthy := nodes[r.TargetNode]
		if !healthy {
			return ""
		}
		targetIP := ns.IP
		if ns.WgMeshIP != "" {
			targetIP = ns.WgMeshIP
		}
		dialAddr = fmt.Sprintf("%s:%d", targetIP, r.TargetPort)
	}

	var sb strings.Builder
	sb.Grow(256)
	sb.WriteString("server {\n    listen 80;\n    server_name ")
	sb.WriteString(r.Domain)
	sb.WriteString(";\n\n    location / {\n        proxy_pass http://")
	sb.WriteString(dialAddr)
	sb.WriteString(";\n        proxy_set_header Host $host;\n        proxy_set_header X-Real-IP $remote_addr;\n        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;\n        proxy_set_header X-Forwarded-Proto $scheme;\n    }\n}\n")
	return sb.String()
}

