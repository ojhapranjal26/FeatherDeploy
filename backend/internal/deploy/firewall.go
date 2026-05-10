package deploy

import (
	"database/sql"
	"log/slog"
	"os/exec"
)

// ReconcileClusterFirewall ensures that only registered worker nodes and localhost can
// communicate with the internal cluster ports (rqlite, etcd, mTLS).
func ReconcileClusterFirewall(db *sql.DB) {
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

	// 1. Allow localhost (always safe for internal services)
	clusterPorts := []string{"4001", "4002", "2379", "2380", "7443"}
	for _, port := range clusterPorts {
		ruleSpec := []string{"-p", "tcp", "--dport", port, "-s", "127.0.0.1", "-j", "ACCEPT"}
		if iptCmd(append([]string{"-C", "INPUT"}, ruleSpec...)...).Run() != nil {
			iptCmd(append([]string{"-I", "INPUT", "1"}, ruleSpec...)...).Run()
		}
	}

	// 1b. Allow our own public IP (detected)
	myIP := detectNodeIP(db)
	if myIP != "" && myIP != "127.0.0.1" {
		for _, port := range clusterPorts {
			ruleSpec := []string{"-p", "tcp", "--dport", port, "-s", myIP,
				"-m", "comment", "--comment", "featherdeploy brain public ip", "-j", "ACCEPT"}
			if iptCmd(append([]string{"-C", "INPUT"}, ruleSpec...)...).Run() != nil {
				iptCmd(append([]string{"-I", "INPUT", "1"}, ruleSpec...)...).Run()
			}
		}
	}

	// 2. Allow all registered nodes (connected or pending)
	rows, err := db.Query(`SELECT ip FROM nodes WHERE ip != ''`)
	if err != nil {
		slog.Warn("ReconcileClusterFirewall: query nodes", "err", err)
		return
	}
	defer rows.Close()

	activeIPs := make(map[string]bool)
	for rows.Next() {
		var ip string
		if rows.Scan(&ip) != nil || ip == "" {
			continue
		}
		activeIPs[ip] = true

		for _, port := range clusterPorts {
			comment := "featherdeploy cluster port " + port + " from node"
			ruleSpec := []string{"-p", "tcp", "--dport", port, "-s", ip,
				"-m", "comment", "--comment", comment, "-j", "ACCEPT"}
			
			if iptCmd(append([]string{"-C", "INPUT"}, ruleSpec...)...).Run() != nil {
				iptCmd(append([]string{"-I", "INPUT", "1"}, ruleSpec...)...).Run()
			}
		}
	}

	// 3. Drop all other external traffic to these ports
	// We use -A (append) so it's checked after the ACCEPT rules.
	for _, port := range clusterPorts {
		comment := "featherdeploy protect cluster port " + port
		ruleSpec := []string{"-p", "tcp", "--dport", port,
			"-m", "comment", "--comment", comment, "-j", "DROP"}
		
		// If the DROP rule doesn't exist, append it.
		// Note: We use -C to check if it exists anywhere.
		if iptCmd(append([]string{"-C", "INPUT"}, ruleSpec...)...).Run() != nil {
			iptCmd(append([]string{"-A", "INPUT"}, ruleSpec...)...).Run()
		}
	}

	slog.Info("ReconcileClusterFirewall: reconciled rules", "nodes", len(activeIPs), "ports", len(clusterPorts))
}

// Deprecated: use ReconcileClusterFirewall
func ReconcileNodeRqliteIPTables(db *sql.DB) {
	ReconcileClusterFirewall(db)
}
