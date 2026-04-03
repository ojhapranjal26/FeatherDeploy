package netdaemon

import (
	"fmt"
	"strings"
)

// SlirpGateway is the address that a container can use to reach services on
// the host.  With --network host (the current mode), the container IS the
// host, so host-local services are reachable on the standard loopback address.
//
// The constant is also used for cross-node peers where NodeAddr will be a
// remote IP instead; in that case the value here is never used.
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
// FeatherDeploy uses --network host (container joins the host network
// namespace).  This is the most reliable mode because:
//   - No rootlessport daemon is needed.  rootlessport (the slirp4netns port-
//     forwarder) can fail to bind the host port, silently leaving the service
//     unreachable ("no route to host" / "connection refused") even though the
//     container is running.
//   - No slirp4netns helper process that can crash or fail to set up.
//   - fdnet and Caddy dial 127.0.0.1:hostPort directly; one fewer network hop.
//   - The service user runs rootless Podman so the container cannot bind
//     privileged ports (<1024) even in host-network mode — the rootless
//     user-namespace restriction still applies.
//
// Trade-off: containers share the host network namespace (no kernel-level
// network isolation between services).  For a self-hosted single-tenant PaaS
// this is an acceptable trade-off for reliability.
//
// Port uniqueness: each service is assigned a unique hostPort (10000+svcID)
// and PORT=hostPort is injected at runtime. Modern frameworks (Node.js, Python,
// Ruby, Go, etc.) read the PORT env var and bind on whatever port they're told,
// so there are no port conflicts between services on the same host.
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
