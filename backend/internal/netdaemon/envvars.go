package netdaemon

import (
	"fmt"
	"strings"
)

// SlirpGateway is the address that a container can use to reach services on
// the host via the slirp4netns virtual network. 10.0.2.2 is the default
// gateway address assigned by slirp4netns that routes to the host's loopback.
const SlirpGateway = "10.0.2.2"

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
// FeatherDeploy uses slirp4netns because it is universally available on
// rootless Podman and does not require netavark/aardvark-dns.
//
// allow_host_loopback=true enables containers to reach 10.0.2.2 (the
// slirp4netns gateway) which routes traffic back to the host's loopback.
// This is used for service-to-service communication via the fdnet proxy.
//
// IMPORTANT: with rootless podman, --network host gives the container access
// to the *rootless user's* network namespace, NOT the system namespace where
// the FeatherDeploy process runs. This means 127.0.0.1:hostPort bound inside
// a host-network container is invisible to the fdnet daemon, causing i/o
// timeout errors. slirp4netns + rootlessport (-p flag) is the correct approach
// because rootlessport binds the host port in the system's network namespace.
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
