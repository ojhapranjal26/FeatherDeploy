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
	RqliteAddr string     `json:"rqlite_addr"`
	LastSeen   *time.Time `json:"last_seen"`
	CreatedAt  time.Time  `json:"created_at"`
}

// AddNodeRequest is the payload for POST /api/nodes.
type AddNodeRequest struct {
	Name string `json:"name" validate:"required,min=2,max=64"`
	IP   string `json:"ip"   validate:"required"`
	Port int    `json:"port"`
}

// NodePingRequest is sent by a node to update its status and last-seen time.
type NodePingRequest struct {
	Status     NodeStatus `json:"status"`
	RqliteAddr string     `json:"rqlite_addr"`
}
