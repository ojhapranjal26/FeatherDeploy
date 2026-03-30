package netdaemon

import (
	"fmt"
	"strings"
)

// SlirpGateway is the address that a container started with
// --network=slirp4netns can use to reach services on the host.
// With slirp4netns:allow_host_loopback=true (or the default behaviour on
// most distros), dialling this address from inside the container reaches
// 127.0.0.1 on the host, where the fdnet proxy is listening.
const SlirpGateway = "10.0.2.2"

// EnvVarsForPeers returns the env-var arguments ([-e KEY=VALUE, ...]) that
// should be injected into a newly started container so it can reach all
// other active services in the same project.
//
// For each peer service whose name is "mydb", the following vars are injected:
//
//	MYDB_HOST=10.0.2.2
//	MYDB_PORT=<clusterPort>
//	MYDB_URL=tcp://10.0.2.2:<clusterPort>
//
// Callers (e.g. StartDatabase, the deployment runner) should call this *after*
// the peer services are registered so the cluster ports are known.
//
// ownServiceName should be set to the name of the container being launched so
// we skip self-references.  Pass "" to include all project peers.
func (d *Daemon) EnvVarsForPeers(projectID int64, ownServiceName string) []string {
	peers := d.ListProject(projectID)

	var args []string
	for _, peer := range peers {
		if peer.ServiceName == ownServiceName {
			continue
		}
		prefix := strings.ToUpper(sanitizeEnvKey(peer.ServiceName))
		gatewayHost := SlirpGateway
		// For cross-node peers the NodeAddr is the remote IP; use that directly
		// instead of the slirp gateway.
		if peer.NodeAddr != "" && peer.NodeAddr != "127.0.0.1" {
			gatewayHost = peer.NodeAddr
		}
		args = append(args,
			"-e", fmt.Sprintf("%s_HOST=%s", prefix, gatewayHost),
			"-e", fmt.Sprintf("%s_PORT=%d", prefix, peer.ClusterPort),
			"-e", fmt.Sprintf("%s_URL=tcp://%s:%d", prefix, gatewayHost, peer.ClusterPort),
		)
	}
	return args
}

// NetworkArgs returns the podman run arguments that tell the container to use
// slirp4netns with host-loopback access enabled.  This replaces the
// --network=fd-proj-X named-bridge approach.
//
// allow_host_loopback=true is the key: it makes the slirp4netns gateway
// (10.0.2.2) route to the host's 127.0.0.1, which is where fdnet proxies
// listen.  This option is supported on all distributions that ship slirp4netns
// (no aardvark-dns or netavark package needed).
//
// If slirp4netns is unavailable (e.g. very minimal environments), the caller
// can fall back to --network=host; all cluster ports are still reachable on
// localhost in that case.
func NetworkArgs() []string {
	return []string{"--network", "slirp4netns:allow_host_loopback=true"}
}

// sanitizeEnvKey converts an arbitrary service/database name into a valid
// shell identifier by uppercasing and replacing non-alphanumeric chars with _.
func sanitizeEnvKey(name string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(name) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}
