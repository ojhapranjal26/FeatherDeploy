// Package heartbeat implements the brain/node liveness protocol.
//
// The brain (main server) writes its heartbeat + resource stats to the
// cluster_state table every HeartbeatInterval.  Every node reads that row on
// the same interval; if last_heartbeat is older than DeadThreshold, the node
// attempts an atomic UPDATE to claim the brain role (Raft serialises writes so
// only one node wins the race).  The winner then promotes itself by starting
// the featherdeploy server binary.
package heartbeat

import (
	"context"
	"database/sql"
	"log/slog"
	"time"
)

const (
	HeartbeatInterval = 10 * time.Second
	DeadThreshold     = 30 * time.Second // 3 missed beats → brain is dead
)

// BrainStats are resource metrics written alongside each brain heartbeat.
type BrainStats struct {
	CPU       float64
	RAMUsed   int64
	RAMTotal  int64
	DiskUsed  int64
	DiskTotal int64
}

// ClusterBrain is the current leader info read from the DB.
type ClusterBrain struct {
	BrainID       string
	BrainAddr     string
	LastHeartbeat time.Time
	Alive         bool
	CPU           float64
	RAMUsed       int64
	RAMTotal      int64
	DiskUsed      int64
	DiskTotal     int64
}

// StartBrain starts a goroutine that writes the brain heartbeat every
// HeartbeatInterval.  stats() is called each tick to collect current metrics.
func StartBrain(ctx context.Context, db *sql.DB, brainID, brainAddr string, stats func() BrainStats) {
	go func() {
		writeBrain(db, brainID, brainAddr, stats())
		ticker := time.NewTicker(HeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				writeBrain(db, brainID, brainAddr, stats())
			case <-ctx.Done():
				return
			}
		}
	}()
}

func writeBrain(db *sql.DB, brainID, brainAddr string, s BrainStats) {
	_, err := db.Exec(`
		INSERT INTO cluster_state
		    (id, brain_id, brain_addr, last_heartbeat,
		     brain_cpu, brain_ram_used, brain_ram_total,
		     brain_disk_used, brain_disk_total, updated_at)
		VALUES (1, ?, ?, datetime('now'), ?, ?, ?, ?, ?, datetime('now'))
		ON CONFLICT(id) DO UPDATE SET
		    brain_id        = excluded.brain_id,
		    brain_addr      = excluded.brain_addr,
		    last_heartbeat  = excluded.last_heartbeat,
		    brain_cpu       = excluded.brain_cpu,
		    brain_ram_used  = excluded.brain_ram_used,
		    brain_ram_total = excluded.brain_ram_total,
		    brain_disk_used = excluded.brain_disk_used,
		    brain_disk_total= excluded.brain_disk_total,
		    updated_at      = excluded.updated_at`,
		brainID, brainAddr,
		s.CPU, s.RAMUsed, s.RAMTotal, s.DiskUsed, s.DiskTotal,
	)
	if err != nil {
		slog.Warn("brain heartbeat write failed", "err", err)
	}
}

// ReadBrain returns the current cluster brain information.
func ReadBrain(db *sql.DB) (*ClusterBrain, error) {
	var b ClusterBrain
	var hbStr sql.NullString
	err := db.QueryRow(`
		SELECT brain_id, brain_addr, last_heartbeat,
		       brain_cpu, brain_ram_used, brain_ram_total,
		       brain_disk_used, brain_disk_total
		FROM cluster_state WHERE id=1`,
	).Scan(&b.BrainID, &b.BrainAddr, &hbStr,
		&b.CPU, &b.RAMUsed, &b.RAMTotal, &b.DiskUsed, &b.DiskTotal)
	if err != nil {
		return nil, err
	}
	if hbStr.Valid && hbStr.String != "" {
		if t, err := time.Parse(time.RFC3339, hbStr.String); err == nil {
			b.LastHeartbeat = t
			b.Alive = time.Since(t) < DeadThreshold
		} else if t, err := time.Parse("2006-01-02 15:04:05", hbStr.String); err == nil {
			b.LastHeartbeat = t
			b.Alive = time.Since(t) < DeadThreshold
		}
	}
	return &b, nil
}

// IsBrainAlive returns true when the brain's last heartbeat is within DeadThreshold.
func IsBrainAlive(db *sql.DB) bool {
	b, err := ReadBrain(db)
	if err != nil {
		return false
	}
	return b.Alive
}

// TryClaimBrain atomically claims the brain role when the current brain is dead.
// Returns true if this node won the election (rows_affected = 1).
func TryClaimBrain(db *sql.DB, nodeID, nodeAddr string) bool {
	// Only claim if last_heartbeat is NULL or older than DeadThreshold
	res, err := db.Exec(`
		UPDATE cluster_state
		SET brain_id       = ?,
		    brain_addr     = ?,
		    last_heartbeat = datetime('now'),
		    updated_at     = datetime('now')
		WHERE id = 1
		  AND (last_heartbeat IS NULL
		       OR last_heartbeat <= datetime('now', '-30 seconds'))`,
		nodeID, nodeAddr,
	)
	if err != nil {
		slog.Error("brain election claim", "err", err)
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

// SetSSHPublicKey stores the cluster SSH public key in cluster_state.
func SetSSHPublicKey(db *sql.DB, pubKey string) error {
	_, err := db.Exec(`UPDATE cluster_state SET ssh_public_key=? WHERE id=1`, pubKey)
	return err
}

// GetSSHPublicKey returns the stored public key (empty string if not set).
func GetSSHPublicKey(db *sql.DB) string {
	var key string
	db.QueryRow(`SELECT ssh_public_key FROM cluster_state WHERE id=1`).Scan(&key)
	return key
}
