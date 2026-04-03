package netdaemon

import (
	"fmt"
	"os/exec"
	"strings"
)

// SlirpGateway is the address that a container started with
// --network=slirp4netns can use to reach services on the host.
// With slirp4netns:allow_host_loopback=true (or the default behaviour on
// most distros), dialling this address from inside the container reaches
// 127.0.0.1 on the host, where the fdnet proxy is listening.
//
// When pasta is used instead (pasta-only systems), Podman's slirp4netns-compat
// layer keeps the same 10.0.2.2 gateway address, so this constant is correct
// for both backends.
const SlirpGateway = "10.0.2.2"

// usePasta returns true when slirp4netns is not available as an executable on
// this host and we must fall back to pasta.  The check is cached after the
// first call per process.
var (
	pastaModeOnce bool
	pastaModeVal  bool
)

func usePasta() bool {
	if pastaModeOnce {
		return pastaModeVal
	}
	pastaModeOnce = true
	// Look for slirp4netns in common installation paths.
	// exec.LookPath uses $PATH; we also check fixed paths used by Podman
	// itself (/usr/libexec/podman) in case $PATH is restricted.
	if _, err := exec.LookPath("slirp4netns"); err == nil {
		pastaModeVal = false
		return false
	}
	for _, p := range []string{
		"/usr/libexec/podman/slirp4netns",
		"/usr/lib/podman/slirp4netns",
		"/usr/local/bin/slirp4netns",
	} {
		if _, err := exec.LookPath(p); err == nil {
			pastaModeVal = false
			return false
		}
	}
	// slirp4netns not found — fall back to pasta if it exists.
	_, pastaErr := exec.LookPath("pasta")
	pastaModeVal = (pastaErr == nil)
	return pastaModeVal
}

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

// NetworkArgs returns the podman run arguments that configure container
// networking so that:
//  1. The fdnet proxy (listening on 127.0.0.1 of the host) is reachable from
//     inside the container via 10.0.2.2 (SlirpGateway).
//  2. Port forwardings like -p 127.0.0.1:hostPort:appPort are honoured.
//
// Primary path (slirp4netns available — all FeatherDeploy-managed installs):
//
//	--network slirp4netns:allow_host_loopback=true
//	  • Container gets 10.0.2.15, gateway 10.0.2.2 → host's 127.0.0.1
//	  • -p 127.0.0.1:port:port binds only on host loopback (security)
//
// Fallback path (pasta-only systems, slirp4netns binary absent):
//
//	--network pasta
//	  • In Podman's pasta integration (slirp4netns-compat layer), the
//	    container also gets 10.0.2.15 and the gateway 10.0.2.2 maps to the
//	    host's loopback, so SlirpGateway still works.
//	  • Port binding -p 127.0.0.1:port:port works in Podman's pasta mode.
//
// FeatherDeploy's build.sh and installer.go both write
// default_rootless_network_cmd="slirp4netns" to the service user's
// containers.conf so the primary path is always taken on managed installs.
func NetworkArgs() []string {
	if usePasta() {
		// pasta-only system: rely on Podman's pasta integration which emulates
		// the slirp4netns NAT behaviour (10.0.2.2 gateway, loopback port fwd).
		return []string{"--network", "pasta"}
	}
	return []string{"--network", "slirp4netns:allow_host_loopback=true"}
}

// PastaMode reports whether this host will use pasta (not slirp4netns) for
// container networking.  Exposed for the deployment runner to adapt the port
// publish argument when necessary.
func PastaMode() bool { return usePasta() }

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
