package model

import "time"

// NodeStatus represents the current connection state of a worker node.
type NodeStatus string

// NodeType distinguishes between brain replica nodes and plain worker nodes.
type NodeType string

const (
	NodeStatusPending   NodeStatus = "pending"
	NodeStatusConnected NodeStatus = "connected"
	NodeStatusOffline   NodeStatus = "offline"
	NodeStatusError     NodeStatus = "error"

	// NodeTypeBrain — eligible for leader election; runs rqlite replica; receives panel binary.
	// Brain nodes are trusted for consensus and can take over as the primary controller.
	NodeTypeBrain NodeType = "brain"

	// NodeTypeWorker — never becomes leader; no rqlite; etcd-only for service/routing metadata.
	// Worker nodes only run containers and sync etcd for discovery.
	NodeTypeWorker NodeType = "worker"
)

// Node is a remote server connected to the main FeatherDeploy instance via mTLS.
type Node struct {
	ID             int64      `json:"id"`
	Name           string     `json:"name"`
	IP             string     `json:"ip"`
	Port           int        `json:"port"` // mTLS API port (default 7443)
	Status         NodeStatus `json:"status"`
	NodeType       NodeType   `json:"node_type"`
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

// IsBrainNode returns true if this node participates in leader election and rqlite replication.
func (n *Node) IsBrainNode() bool { return n.NodeType == NodeTypeBrain }

// NodeSummary is the list-view representation shown to the frontend.
type NodeSummary struct {
	ID       int64      `json:"id"`
	NodeID   string     `json:"node_id"`
	Name     string     `json:"name"`
	IP       string     `json:"ip"`
	Port     int        `json:"port"`
	Status   NodeStatus `json:"status"`
	NodeType NodeType   `json:"node_type"`
	Hostname string     `json:"hostname"`
	OSInfo   string     `json:"os_info"`
	// IsBrain is true for nodes eligible for leader election and rqlite replication.
	IsBrain    bool   `json:"is_brain"`
	RqliteAddr string `json:"rqlite_addr"`
	LastSeen   *time.Time `json:"last_seen"`
	CreatedAt  time.Time  `json:"created_at"`
	// Resource stats (updated every 10s via node heartbeat)
	CPUUsage  float64 `json:"cpu_usage"`
	RAMUsed   int64   `json:"ram_used"`
	RAMTotal  int64   `json:"ram_total"`
	DiskUsed  int64   `json:"disk_used"`
	DiskTotal int64   `json:"disk_total"`
	// Tunnel connectivity info
	// Tunnel connectivity info
	TunnelConnected bool `json:"tunnel_connected"`
	// Domains assigned/pointed to this node via DNS
	AssignedDomains []string `json:"assigned_domains"`
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

