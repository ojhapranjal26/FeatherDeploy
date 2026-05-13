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

	clusterPorts := []string{"4001", "4002", "2379", "2380", "7443"}
	slog.Info("ReconcileClusterFirewall: starting reconciliation", "ports", clusterPorts)

	// 1. Allow localhost (always safe for internal services)
	for _, port := range clusterPorts {
		ruleSpec := []string{"-p", "tcp", "--dport", port, "-s", "127.0.0.1", "-j", "ACCEPT"}
		if iptCmd(append([]string{"-C", "INPUT"}, ruleSpec...)...).Run() != nil {
			iptCmd(append([]string{"-I", "INPUT", "1"}, ruleSpec...)...).Run()
		}
	}

	// 1b. Allow our own public IP (detected)
	myIP := detectNodeIP(db)
	if myIP != "" && myIP != "127.0.0.1" {
		slog.Info("ReconcileClusterFirewall: whitelisting self", "ip", myIP)
		for _, port := range clusterPorts {
			ruleSpec := []string{"-p", "tcp", "--dport", port, "-s", myIP,
				"-m", "comment", "--comment", "featherdeploy brain public ip", "-j", "ACCEPT"}
			if iptCmd(append([]string{"-C", "INPUT"}, ruleSpec...)...).Run() != nil {
				iptCmd(append([]string{"-I", "INPUT", "1"}, ruleSpec...)...).Run()
			}
		}
	}

	// 2. Allow all registered nodes (connected or pending)
	rows, err := db.Query(`SELECT name, ip FROM nodes WHERE ip != ''`)
	if err != nil {
		slog.Warn("ReconcileClusterFirewall: query nodes", "err", err)
		return
	}
	defer rows.Close()

	activeIPs := make(map[string]string)
	for rows.Next() {
		var name, ip string
		if rows.Scan(&name, &ip) != nil || ip == "" {
			continue
		}
		activeIPs[name] = ip

		slog.Info("ReconcileClusterFirewall: whitelisting node", "name", name, "ip", ip)
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
		
		if iptCmd(append([]string{"-C", "INPUT"}, ruleSpec...)...).Run() != nil {
			iptCmd(append([]string{"-A", "INPUT"}, ruleSpec...)...).Run()
		}
	}

	// 4. Reconcile WireGuard Mesh peers on wg0 interface
	wgPath, wgErr := exec.LookPath("wg")
	if wgErr == nil {
		wgRows, err := db.Query(`SELECT wg_public_key, wg_mesh_ip FROM nodes WHERE wg_public_key != '' AND wg_mesh_ip != ''`)
		if err == nil {
			defer wgRows.Close()
			for wgRows.Next() {
				var pubKey, meshIP string
				if wgRows.Scan(&pubKey, &meshIP) == nil {
					wgCmd := func(args ...string) *exec.Cmd {
						if sudo != "" {
							return exec.Command(sudo, append([]string{wgPath}, args...)...)
						}
						return exec.Command(wgPath, args...)
					}
					// Add peer to wg0 interface. AllowedIPs is meshIP/32.
					err := wgCmd("set", "wg0", "peer", pubKey, "allowed-ips", meshIP+"/32").Run()
					if err != nil {
						slog.Warn("ReconcileClusterFirewall: failed to add wg peer", "pubkey", pubKey, "ip", meshIP, "err", err)
					} else {
						slog.Info("ReconcileClusterFirewall: configured wg peer", "ip", meshIP)
					}
				}
			}
		}
	}

	slog.Info("ReconcileClusterFirewall: reconciliation complete", "nodes_whitelisted", len(activeIPs))
}

// Deprecated: use ReconcileClusterFirewall
func ReconcileNodeRqliteIPTables(db *sql.DB) {
	ReconcileClusterFirewall(db)
}
