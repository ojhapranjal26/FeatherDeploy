package handler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/ojhapranjal26/featherdeploy/backend/internal/heartbeat"
)

// StatsHandler streams live system stats via Server-Sent Events (SSE).
// GET /api/stats/stream — authenticated (JWT via ?token= or Authorization header)
//
// The event format is:
//
//	event: stats
//	data: <JSON payload>
//
// Example payload:
//
//	{
//	  "brain": { "BrainID":"main", "CPU":12.5, "RAMUsed":..., ... },
//	  "nodes": [ { "id":1, "name":"worker-1", "cpu_usage":8.4, ... }, ... ]
//	}
type StatsHandler struct {
	db *sql.DB
}

func NewStatsHandler(db *sql.DB) *StatsHandler {
	return &StatsHandler{db: db}
}

// NodeStatsSummary is the per-node stats payload sent via SSE.
type NodeStatsSummary struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	Status      string  `json:"status"`
	CPUUsage    float64 `json:"cpu_usage"`
	RAMUsed     int64   `json:"ram_used"`
	RAMTotal    int64   `json:"ram_total"`
	DiskUsed    int64   `json:"disk_used"`
	DiskTotal   int64   `json:"disk_total"`
	LastStatsAt *string `json:"last_stats_at"`
	NodeID      string  `json:"node_id"`
}

type statsPayload struct {
	Brain *heartbeat.ClusterBrain `json:"brain"`
	Nodes []NodeStatsSummary      `json:"nodes"`
}

// Stream handles GET /api/stats/stream — SSE endpoint.
// It sends a stats event immediately and then every 5 seconds until the client
// disconnects.  Uses only the Go standard library (no WebSocket).
func (h *StatsHandler) Stream(w http.ResponseWriter, r *http.Request) {
	// SSE requires the response writer to support flushing.
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, errMap("streaming not supported"))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Allow cross-origin SSE (CORS middleware handles origins; this avoids
	// issues with some browsers that strip CORS headers on EventSource).
	w.Header().Set("X-Accel-Buffering", "no")

	send := func() {
		payload := statsPayload{}

		// Brain stats
		if b, err := heartbeat.ReadBrain(h.db); err == nil {
			payload.Brain = b
		}

		// Node stats
		rows, err := h.db.QueryContext(r.Context(), `
			SELECT id, name, status,
			       COALESCE(cpu_usage,0), COALESCE(ram_used,0), COALESCE(ram_total,0),
			       COALESCE(disk_used,0), COALESCE(disk_total,0),
			       last_stats_at, COALESCE(node_id,'')
			FROM nodes ORDER BY id`)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var n NodeStatsSummary
				var lastStatsAt sql.NullString
				if err := rows.Scan(
					&n.ID, &n.Name, &n.Status,
					&n.CPUUsage, &n.RAMUsed, &n.RAMTotal,
					&n.DiskUsed, &n.DiskTotal,
					&lastStatsAt, &n.NodeID,
				); err == nil {
					if lastStatsAt.Valid {
						v := lastStatsAt.String
						n.LastStatsAt = &v
					}
					payload.Nodes = append(payload.Nodes, n)
				}
			}
		} else {
			slog.Warn("stats stream: query nodes", "err", err)
		}

		data, err := json.Marshal(payload)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "event: stats\ndata: %s\n\n", data)
		flusher.Flush()
	}

	// Send first event immediately so the client doesn't wait 5 seconds.
	send()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			// Client disconnected
			return
		case <-ticker.C:
			send()
		}
	}
}
