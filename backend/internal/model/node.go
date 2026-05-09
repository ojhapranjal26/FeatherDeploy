package model

import "time"

// NodeStatus represents the current connection state of a worker node.
type NodeStatus string

const (
	NodeStatusPending   NodeStatus = "pending"
	NodeStatusConnected NodeStatus = "connected"
	NodeStatusOffline   NodeStatus = "offline"
	NodeStatusError     NodeStatus = "error"
)

// Node is a remote worker server connected to the main FeatherDeploy instance
// via mTLS.  The main server can push deployments to it and distribute the
// application binary so nodes can serve the panel when the main is unreachable.
type Node struct {
	ID             int64      `json:"id"`
	Name           string     `json:"name"`
	IP             string     `json:"ip"`
	Port           int        `json:"port"` // mTLS API port (default 7443)
	Status         NodeStatus `json:"status"`
	Hostname       string     `json:"hostname"`
	OSInfo         string     `json:"os_info"`
	JoinToken      string     `json:"join_token,omitempty"`
	TokenExpiresAt *time.Time `json:"token_expires_at,omitempty"`
	NodeCertPEM    string     `json:"-"`           // not exposed to clients
	RqliteAddr     string     `json:"rqlite_addr"` // host:port for Raft cluster
	LastSeen       *time.Time `json:"last_seen"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// NodeSummary is the list-view representation shown to the frontend.
type NodeSummary struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	IP         string     `json:"ip"`
	Port       int        `json:"port"`
	Status     NodeStatus `json:"status"`
	Hostname   string     `json:"hostname"`
	OSInfo     string     `json:"os_info"`
	RqliteAddr string     `json:"rqlite_addr"`
	LastSeen   *time.Time `json:"last_seen"`
	CreatedAt  time.Time  `json:"created_at"`
	// Resource stats (updated every 10s via node heartbeat)
	CPUUsage  float64 `json:"cpu_usage"`
	RAMUsed   int64   `json:"ram_used"`
	RAMTotal  int64   `json:"ram_total"`
	DiskUsed  int64   `json:"disk_used"`
	DiskTotal int64   `json:"disk_total"`
}

// AddNodeRequest is the payload for POST /api/nodes.
type AddNodeRequest struct {
	Name string `json:"name" validate:"required,min=2,max=64"`
	IP   string `json:"ip"   validate:"required"`
	Port int    `json:"port"`
}

// NodePingRequest is sent by a node to update its status, stats, and last-seen time.
type NodePingRequest struct {
	Status     NodeStatus `json:"status"`
	RqliteAddr string     `json:"rqlite_addr"`
	// Resource stats (collected on the node every heartbeat tick)
	CPUUsage  float64 `json:"cpu_usage"`  // 0-100 percent
	RAMUsed   int64   `json:"ram_used"`   // bytes
	RAMTotal  int64   `json:"ram_total"`  // bytes
	DiskUsed  int64   `json:"disk_used"`  // bytes
	DiskTotal int64   `json:"disk_total"` // bytes
}
