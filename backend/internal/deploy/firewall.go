package deploy

import (
	"database/sql"
	"log/slog"
	"os/exec"
	"strings"
)

// ReconcileNodeRqliteIPTables ensures that only registered worker nodes can
// communicate with the rqlite cluster ports (4001, 4002).
func ReconcileNodeRqliteIPTables(db *sql.DB) {
	ipt, err := exec.LookPath("iptables")
	if err != nil {
		return
	}
	sudo, _ := exec.LookPath("sudo")
	iptCmd := func(args ...string) *exec.Cmd {
		if sudo != "" {
			return exec.Command(sudo, append([]string{ipt}, args...)...)
		}
		return exec.Command(ipt, args...)
	}

	rows, err := db.Query(`SELECT ip FROM nodes WHERE status='connected'`)
	if err != nil {
		slog.Warn("ReconcileNodeRqliteIPTables: query nodes", "err", err)
		return
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var ip string
		if rows.Scan(&ip) != nil || ip == "" {
			continue
		}

		// Port 4002 (Raft)
		ruleSpec := []string{"-p", "tcp", "--dport", "4002", "-s", ip,
			"-m", "comment", "--comment", "featherdeploy rqlite raft from node",
			"-j", "ACCEPT"}
		checkArgs := append([]string{"-C", "INPUT"}, ruleSpec...)
		if iptCmd(checkArgs...).Run() != nil {
			insertArgs := append([]string{"-I", "INPUT", "1"}, ruleSpec...)
			if out, runErr := iptCmd(insertArgs...).CombinedOutput(); runErr != nil {
				slog.Warn("ReconcileNodeRqliteIPTables: could not add iptables rule for 4002",
					"ip", ip, "err", runErr, "out", strings.TrimSpace(string(out)))
			} else {
				count++
			}
		}

		// Port 4001 (HTTP API)
		ruleSpecHTTP := []string{"-p", "tcp", "--dport", "4001", "-s", ip,
			"-m", "comment", "--comment", "featherdeploy rqlite http from node",
			"-j", "ACCEPT"}
		checkArgsHTTP := append([]string{"-C", "INPUT"}, ruleSpecHTTP...)
		if iptCmd(checkArgsHTTP...).Run() != nil {
			insertArgs := append([]string{"-I", "INPUT", "1"}, ruleSpecHTTP...)
			iptCmd(insertArgs...).Run() //nolint
		}
	}
	if count > 0 {
		slog.Info("ReconcileNodeRqliteIPTables: added iptables rules for nodes", "count", count)
	}
}
