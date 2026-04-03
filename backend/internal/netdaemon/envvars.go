package netdaemon

import (
	"fmt"
	"strings"
)

// SlirpGateway is the address that a container can use to reach services on
// the host. With host networking, the container shares the host's network
// namespace, so host-local services are reachable on 127.0.0.1 directly.
const SlirpGateway = "127.0.0.1"

// EnvVarsForPeers returns the env-var arguments ([-e KEY=VALUE, ...]) that
// should be injected into a newly started container so it can reach all
// other active services in the same project.
//
// For each peer service whose name is "mydb", the following vars are injected:
//
//	MYDB_HOST=127.0.0.1
//	MYDB_PORT=<clusterPort>
//	MYDB_URL=tcp://127.0.0.1:<clusterPort>
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
		// For cross-node peers the NodeAddr is the remote IP; use that directly.
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

// NetworkArgs returns the podman run arguments that configure container
// networking.
//
// FeatherDeploy uses --network host because the app now binds the allocated
// host port directly via the PORT env var. That removes the slirp4netns /
// rootlessport forwarding layer that was producing "no route to host" while
// still keeping the container rootless.
//
// Each service still gets a unique high host port, so even with host networking
// there are no port conflicts between services.
func NetworkArgs() []string {
	return []string{"--network", "host"}
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
