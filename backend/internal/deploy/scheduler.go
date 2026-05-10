package deploy

import (
	"database/sql"
	"fmt"
	"log/slog"
)

// SelectTargetNode finds the best node to deploy on based on targetNodeID.
// If targetNodeID is 'auto', it picks the least loaded node.
// Returns the node_id (e.g. 'main' or a worker hostname).
func SelectTargetNode(db *sql.DB, targetNodeID string) (string, error) {
	if targetNodeID != "" && targetNodeID != "auto" {
		// Verify node exists and is connected
		if targetNodeID == "main" {
			return "main", nil
		}
		var status string
		err := db.QueryRow(`SELECT status FROM nodes WHERE node_id=?`, targetNodeID).Scan(&status)
		if err != nil {
			return "", fmt.Errorf("node %q not found", targetNodeID)
		}
		if status != "connected" {
			return "", fmt.Errorf("node %q is not connected (status: %s)", targetNodeID, status)
		}
		return targetNodeID, nil
	}

	// Auto mode: find least loaded node (lowest CPU + RAM usage percent)
	// We check 'main' (from cluster_state) and workers (from nodes table)
	
	type nodeScore struct {
		id    string
		score float64 // lower is better
	}
	var nodes []nodeScore

	// 1. Check main (brain)
	var bCPU float64
	var bRAMU, bRAMT, bDiskU, bDiskT int64
	err := db.QueryRow(`SELECT brain_cpu, brain_ram_used, brain_ram_total, brain_disk_used, brain_disk_total FROM cluster_state LIMIT 1`).Scan(&bCPU, &bRAMU, &bRAMT, &bDiskU, &bDiskT)
	if err == nil {
		ramPct := 0.0
		if bRAMT > 0 {
			ramPct = (float64(bRAMU) / float64(bRAMT)) * 100
		}
		diskPct := 0.0
		if bDiskT > 0 {
			diskPct = (float64(bDiskU) / float64(bDiskT)) * 100
		}
		// Final score is average of CPU, RAM, and Disk percentages
		nodes = append(nodes, nodeScore{id: "main", score: (bCPU + ramPct + diskPct) / 3})
	}

	// 2. Check workers
	rows, err := db.Query(`SELECT node_id, cpu_usage, ram_used, ram_total, disk_used, disk_total FROM nodes WHERE status='connected'`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var nid string
			var cpu float64
			var ru, rt, du, dt int64
			if err := rows.Scan(&nid, &cpu, &ru, &rt, &du, &dt); err == nil {
				ramPct := 0.0
				if rt > 0 {
					ramPct = (float64(ru) / float64(rt)) * 100
				}
				diskPct := 0.0
				if dt > 0 {
					diskPct = (float64(du) / float64(dt)) * 100
				}
				nodes = append(nodes, nodeScore{id: nid, score: (cpu + ramPct + diskPct) / 3})
			}
		}
	}

	if len(nodes) == 0 {
		return "main", nil // fallback
	}

	// Find minimum score
	best := nodes[0]
	for _, n := range nodes {
		if n.score < best.score {
			best = n
		}
	}

	slog.Info("scheduler: selected node", "node_id", best.id, "score", best.score)
	return best.id, nil
}
