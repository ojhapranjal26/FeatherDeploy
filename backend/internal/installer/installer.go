// Package installer provides the interactive setup wizard for DeployPaaS.
// Run via:  deploypaaas install
//
// The wizard:
//  1. Checks Linux + root
//  2. Detects the package manager and installs Podman + Caddy
//  3. Prompts for panel domain, superadmin credentials
//  4. Creates the system user, data dirs, and env file
//  5. Seeds the database with the superadmin
//  6. Writes the Caddyfile and systemd service
//  7. Enables and starts the services
package installer

import (
	"bufio"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"text/template"
	"time"

	"golang.org/x/term"

	"github.com/ojhapranjal26/featherdeploy/backend/internal/auth"
	appDb "github.com/ojhapranjal26/featherdeploy/backend/internal/db"
)

const (
	binDest        = "/usr/local/bin/featherdeploy"
	dataDir        = "/var/lib/featherdeploy"
	configDir      = "/etc/featherdeploy"
	envFile        = "/etc/featherdeploy/featherdeploy.env"
	caddyConf      = "/etc/caddy/Caddyfile"
	systemdUnit    = "/etc/systemd/system/featherdeploy.service"
	defaultSvcUser = "featherdeploy"
	backendPort    = "8080"
)

// IsInstalled returns true when a prior installation is detected on this machine.
func IsInstalled() bool {
	_, envErr := os.Stat(envFile)
	_, unitErr := os.Stat(systemdUnit)
	return envErr == nil || unitErr == nil
}

// Run starts the interactive first-time setup wizard. Exits on error.
func Run() {
	if runtime.GOOS != "linux" {
		die("installer only supported on Linux (got %s)", runtime.GOOS)
	}
	if os.Geteuid() != 0 {
		die("installer must be run as root (use sudo)")
	}

	printBanner()

	// Open /dev/tty directly so interactive prompts work even when stdin is a
	// pipe (e.g. curl -fsSL ... | sudo bash). Without this, ReadString returns
	// empty immediately because the pipe EOF is inherited as stdin.
	tty, ttyErr := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if ttyErr != nil {
		tty = os.Stdin // fallback for environments without /dev/tty
	} else {
		defer tty.Close()
	}
	reader := bufio.NewReader(tty)

	// ── Step 0: Service OS user ───────────────────────────────────────────────
	fmt.Fprintf(tty, "Service OS username [%s]: ", defaultSvcUser)
	svcUserInput := strings.TrimRight(func() string { l, _ := reader.ReadString('\n'); return l }(), "\r\n")
	svcUser := defaultSvcUser
	if strings.TrimSpace(svcUserInput) != "" {
		svcUser = strings.TrimSpace(svcUserInput)
	}

	svcPassword := promptPassword(tty, fmt.Sprintf("Password for OS user '%s' (min 8 chars): ", svcUser))
	if len(svcPassword) < 8 {
		die("OS user password must be at least 8 characters")
	}
	confirmSvcPassword := promptPassword(tty, "Confirm OS user password: ")
	if svcPassword != confirmSvcPassword {
		die("OS user passwords do not match")
	}

	// ── Step 1: Panel domain ──────────────────────────────────────────────────
	domain := prompt(reader, "Panel domain (e.g. panel.example.com): ")
	if domain == "" {
		die("domain cannot be empty")
	}

	// ── Step 2: Superadmin credentials ───────────────────────────────────────
	adminEmail := prompt(reader, "Superadmin email: ")
	if !strings.Contains(adminEmail, "@") {
		die("invalid email address")
	}
	adminName := prompt(reader, "Superadmin full name: ")
	if len(strings.TrimSpace(adminName)) < 2 {
		die("name must be at least 2 characters")
	}
	adminPassword := promptPassword(tty, "Superadmin password (min 8 chars): ")
	if len(adminPassword) < 8 {
		die("password must be at least 8 characters")
	}
	confirmPassword := promptPassword(tty, "Confirm superadmin password: ")
	if adminPassword != confirmPassword {
		die("passwords do not match")
	}

	// ── Step 3: Install system packages ──────────────────────────────────────
	fmt.Println("\n── Installing system packages ──────────────────────────────────")
	if err := installPackages(); err != nil {
		die("package installation failed: %v", err)
	}
	installRqlite()
	configureCrun()

	// ── Step 4: Create service OS user + directories ─────────────────────────
	fmt.Println("\n── Preparing service user and directories ──────────────────────")
	// On a reinstall the previous featherdeploy/rqlite services may still be
	// running.  usermod -d refuses to update /etc/passwd while the target user
	// has ANY active processes, so we must kill everything first.
	killUserProcesses(svcUser)
	createServiceUser(svcUser, svcPassword)
	setupPodmanRootless(svcUser)
	mustMkdir(dataDir)
	mustMkdir(filepath.Join(dataDir, "rqlite-data"))
	mustMkdir(configDir)
	mustRun("chown", "-R", svcUser+":"+svcUser, dataDir)

	// ── Step 5: Copy binary ───────────────────────────────────────────────────
	fmt.Println("\n── Installing binary ───────────────────────────────────────────")
	self, err := os.Executable()
	if err != nil {
		die("cannot determine binary path: %v", err)
	}
	// Resolve symlinks so we can compare real paths.
	realSelf, _ := filepath.EvalSymlinks(self)
	realDest, _ := filepath.EvalSymlinks(binDest)
	if realSelf == realDest {
		// Already running from binDest (build.sh put us here) — no copy needed.
		fmt.Printf("  ✓ binary already at %s\n", binDest)
	} else {
		// Remove before copy to avoid "text file busy" when overwriting a
		// running executable.
		os.Remove(binDest)
		copyFile(self, binDest)
		mustRun("chmod", "+x", binDest)
		mustRun("chown", "root:"+svcUser, binDest)
		fmt.Printf("  ✓ installed %s\n", binDest)
	}

	// ── Step 5a: Write + start rqlite service ───────────────────────────────
	fmt.Println("\n── Configuring rqlite ──────────────────────────────────────────")
	// On a fresh install, always wipe any leftover Raft state so rqlite
	// bootstraps cleanly. Old data (from a failed install, partial uninstall,
	// or version mismatch) causes rqlite to exit status=1 immediately.
	rqliteDataDir := filepath.Join(dataDir, "rqlite-data")
	if err := os.RemoveAll(rqliteDataDir); err != nil {
		slog.Warn("could not wipe stale rqlite-data (non-fatal)", "err", err)
	} else {
		fmt.Printf("  ✓ cleared rqlite-data at %s\n", rqliteDataDir)
	}
	if err := os.MkdirAll(rqliteDataDir, 0755); err == nil {
		exec.Command("chown", "-R", svcUser+":"+svcUser, rqliteDataDir).Run() //nolint
	}
	writeRqliteService(svcUser)
	mustRun("systemctl", "daemon-reload")
	mustRun("systemctl", "enable", "rqlite")
	mustRun("systemctl", "start", "rqlite")
	fmt.Println("  Waiting for rqlite to be ready...")
	if err := waitForRqlite(60 * time.Second); err != nil {
		die("rqlite did not start: %v", err)
	}
	fmt.Println("  ✓ rqlite is ready")

	// ── Step 6: Generate secrets and write env file ───────────────────────────
	fmt.Println("\n── Writing configuration ───────────────────────────────────────")
	jwtSecret := randomHex(32)
	frontendOrigin := "https://" + domain
	rqliteURL := "http://127.0.0.1:4001"

	envContent := fmt.Sprintf(`# FeatherDeploy — generated %s
RQLITE_URL=%s
JWT_SECRET=%s
ADDR=127.0.0.1:%s
ORIGIN=%s
`, time.Now().Format(time.RFC3339), rqliteURL, jwtSecret, backendPort, frontendOrigin)

	writeFile(envFile, envContent, 0600)
	mustRun("chown", "root:"+svcUser, envFile)
	mustRun("chmod", "640", envFile)
	fmt.Printf("  ✓ wrote %s\n", envFile)

	// ── Step 7: Seed the database via rqlite ──────────────────────────────────
	fmt.Println("\n── Seeding database ────────────────────────────────────────────")
	db, err := appDb.OpenRqlite(rqliteURL)
	if err != nil {
		die("cannot connect to rqlite: %v", err)
	}
	if err := createSuperAdmin(db, adminEmail, adminName, adminPassword); err != nil {
		die("failed to create superadmin: %v", err)
	}
	db.Close()
	fmt.Printf("  ✓ superadmin created (%s)\n", adminEmail)

	// ── Step 8: Write Caddyfile ───────────────────────────────────────────────
	fmt.Println("\n── Configuring Caddy ───────────────────────────────────────────")
	writeCaddyfile(domain)
	fmt.Printf("  ✓ wrote %s\n", caddyConf)

	// ── Step 9: Systemd service ───────────────────────────────────────────────
	fmt.Println("\n── Installing systemd service ──────────────────────────────────")
	writeSystemdService(svcUser)
	mustRun("systemctl", "daemon-reload")
	mustRun("systemctl", "enable", "featherdeploy")
	mustRun("systemctl", "start", "featherdeploy")
	fmt.Println("  ✓ featherdeploy service enabled and started")

	// ── Step 10: Reload or start Caddy ───────────────────────────────────────
	if _, err := exec.LookPath("systemctl"); err == nil {
		if runSilent("systemctl", "is-active", "--quiet", "caddy") == nil {
			runSilent("systemctl", "reload", "caddy")
		} else {
			mustRun("systemctl", "enable", "caddy")
			mustRun("systemctl", "start", "caddy")
		}
		fmt.Println("  ✓ Caddy reloaded")
	}

	printSuccess(domain, svcUser)
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func printBanner() {
	fmt.Print(`
  _____         _   _          ____             _
 |  ___|__  __ _| |_| |__   ___|  _ \  ___ _ __ | | ___  _   _
 | |_ / _ \/ _' | __| '_ \ / _ \ | | |/ _ \ '_ \| |/ _ \| | | |
 |  _|  __/ (_| | |_| | | |  __/ |_| |  __/ |_) | | (_) | |_| |
 |_|  \___|\__,_|\__|_| |_|\___|____/ \___| .__/|_|\___/ \__, |
                                           |_|             |___/

  FeatherDeploy Installer
  ─────────────────────────────────────────────────────────
  This wizard will install and configure FeatherDeploy
  on this Linux server.  Run as root / sudo.
  ────────────────────────────────────────────────────────
`)
}

func printSuccess(domain, svcUser string) {
	fmt.Printf(`
  ══════════════════════════════════════════════════════
  ✓  FeatherDeploy installed successfully!

     Panel URL  : https://%s
     OS user    : %s  (not root — runs with limited privileges)

     The panel may take 30-60s to start.
     Caddy will automatically obtain a TLS certificate
     from Let's Encrypt for your domain.

     Switch to the service user:
       sudo su - %s

     Check service status:
       sudo systemctl status featherdeploy
       sudo systemctl status caddy

     View logs:
       sudo journalctl -u featherdeploy -f
  ══════════════════════════════════════════════════════
`, domain, svcUser, svcUser)
}

func prompt(r *bufio.Reader, label string) string {
	fmt.Print(label)
	line, _ := r.ReadString('\n')
	return strings.TrimRight(line, "\r\n")
}

func promptPassword(tty *os.File, label string) string {
	_, _ = fmt.Fprint(tty, label)
	password, err := term.ReadPassword(int(tty.Fd()))
	_, _ = fmt.Fprintln(tty) // move to next line after hidden input
	if err != nil {
		// Fallback: visible input (e.g. tty not a real terminal)
		_, _ = fmt.Fprint(tty, "(echo fallback) ")
		r := bufio.NewReader(tty)
		line, _ := r.ReadString('\n')
		return strings.TrimRight(line, "\r\n")
	}
	return string(password)
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "\nERROR: "+format+"\n", args...)
	os.Exit(1)
}

// setupPodmanRootless ensures the service user has subuid/subgid ranges in
// /etc/subuid and /etc/subgid, which are required for Podman rootless to work.
// Without these entries Podman cannot set up user namespaces and every
// `podman build` / `podman run` fails with "no subuid ranges found".
func setupPodmanRootless(username string) {
	fmt.Printf("\n── Configuring rootless Podman for %s ──────────────────\n", username)
	// Services were already stopped by killUserProcesses() before this call.

	ensureSubIDEntry(username, "/etc/subuid", "100000", "65536")
	ensureSubIDEntry(username, "/etc/subgid", "100000", "65536")

	// Ensure newuidmap/newgidmap have setuid root so user namespace mapping works.
	for _, p := range []string{"/usr/bin/newuidmap", "/usr/bin/newgidmap", "/usr/sbin/newuidmap", "/usr/sbin/newgidmap"} {
		if fi, err := os.Stat(p); err == nil {
			// Set setuid bit (04000) plus existing permission bits.
			_ = os.Chmod(p, fi.Mode()|0o4000)
		}
	}

	// Enable linger so the service user gets a persistent systemd user session
	// (creates /run/user/<uid>/ with a dbus socket even with no login session).
	if out, err := exec.Command("loginctl", "enable-linger", username).CombinedOutput(); err != nil {
		slog.Warn("loginctl enable-linger failed (non-fatal)", "err", err, "out", string(out))
	} else {
		fmt.Printf("  ✓ loginctl enable-linger %s\n", username)
	}

	// Write a per-user containers.conf forcing cgroupfs cgroup manager.
	// Without this, crun calls sd-bus to ask systemd to create a cgroup scope,
	// which fails with "Interactive authentication required" when no user
	// session dbus socket is available.
	homedir := userHomeDir(username)
	confDir := filepath.Join(homedir, ".config", "containers")
	if err := os.MkdirAll(confDir, 0755); err == nil {
		confFile := filepath.Join(confDir, "containers.conf")
		const userConf = "[engine]\ncgroup_manager = \"cgroupfs\"\n"
		if err2 := os.WriteFile(confFile, []byte(userConf), 0644); err2 == nil {
			fmt.Printf("  ✓ per-user containers.conf (cgroupfs) written to %s\n", confFile)
		}

		// Write a registries.conf so that short image names like "php:8.2-fpm-alpine"
		// resolve to docker.io without requiring a fully-qualified reference.
		// Without this file Podman exits 125: "short-name did not resolve to an alias
		// and no containers-registries.conf(5) was found".
		regFile := filepath.Join(confDir, "registries.conf")
		const regConf = "unqualified-search-registries = [\"docker.io\"]\n"
		if err2 := os.WriteFile(regFile, []byte(regConf), 0644); err2 == nil {
			fmt.Printf("  ✓ per-user registries.conf (docker.io) written to %s\n", regFile)
		}

		// Ensure the service user owns the entire .config tree.
		mustRun("chown", "-R", username+":"+username, filepath.Join(homedir, ".config"))
	}

	// Compute service user's UID and runtime dir for directory creation below.
	uid2 := "1000"
	if uidOut2, err2 := exec.Command("id", "-u", username).Output(); err2 == nil {
		if us := strings.TrimSpace(string(uidOut2)); us != "" {
			uid2 = us
		}
	}
	mRtDir := "/run/user/" + uid2
	if err2 := os.MkdirAll(mRtDir, 0700); err2 == nil {
		exec.Command("chown", username+":"+username, mRtDir).Run() //nolint
	}
	// NOTE: we intentionally skip 'podman system migrate' here.
	// ensureNetworkingBackend (called below) performs a full DB wipe and smoke
	// test that achieves the same result without the migrate failure modes.

	// Verify that named (bridge) networking actually works.
	// `podman build` uses slirp4netns (works without netavark), but
	// `podman run --network <name>` calls the netavark binary which must be
	// installed separately on RHEL 9 / AlmaLinux / Rocky.  If the test fails,
	// print a clear remediation message so the operator knows what to install.
	ensureNetworkingBackend(username, homedir)
	fmt.Printf("  ✓ rootless Podman configured for %s\n", username)
}

// userHomeDir returns the home directory for username from /etc/passwd,
// falling back to /var/lib/featherdeploy.
func userHomeDir(username string) string {
	out, err := exec.Command("getent", "passwd", username).Output()
	if err == nil {
		fields := strings.Split(strings.TrimSpace(string(out)), ":")
		if len(fields) >= 6 && fields[5] != "" {
			return fields[5]
		}
	}
	return "/var/lib/featherdeploy"
}

// ensureNetworkingBackend verifies that podman named networks work for username.
// On RHEL 9/AlmaLinux/Rocky, `podman` is shipped without `netavark` and
// `aardvark-dns`. Without them `podman run --network <name>` exits 127
// ("netavark: command not found") even though `podman network create` succeeds.
// This function:
//  1. Probes for well-known netavark/CNI binary locations.
//  2. Falls back to installing netavark via dnf/apt if missing.
//  3. Runs a smoke-test: network create → run hello-world → network rm.
func ensureNetworkingBackend(username, homedir string) {
	fmt.Println("  Checking Podman named networking backend (netavark/CNI)...")

	// Well-known paths for the netavark binary.
	netavarkPaths := []string{
		"/usr/libexec/podman/netavark",
		"/usr/lib/podman/netavark",
		"/usr/local/lib/podman/netavark",
		"/usr/bin/netavark",
	}
	found := false
	for _, p := range netavarkPaths {
		if _, err := os.Stat(p); err == nil {
			fmt.Printf("  \u2713 netavark found at %s\n", p)
			found = true
			break
		}
	}

	// Check for pasta (rootless network helper from the passt package).
	// pasta is preferred over slirp4netns because it does NOT interact with
	// the systemd user session (no dbus user.slice move), which eliminates
	// the "connect: permission denied on /run/user/<uid>/bus" error.
	pastaPaths := []string{"/usr/bin/pasta", "/usr/local/bin/pasta", "/usr/libexec/pasta"}
	pastaFound := false
	pastaActualPath := "" // real path of the pasta binary, used for symlinking
	for _, p := range pastaPaths {
		if _, err := os.Stat(p); err == nil {
			fmt.Printf("  \u2713 pasta found at %s\n", p)
			pastaFound = true
			pastaActualPath = p
			break
		}
	}
	if !pastaFound {
		fmt.Println("  WARNING: pasta not found — attempting to install passt package...")
		for pm, args := range map[string][]string{
			"dnf":     {"dnf", "install", "-y", "passt"},
			"apt-get": {"apt-get", "install", "-y", "-q", "passt"},
			"yum":     {"yum", "install", "-y", "--skip-broken", "passt"},
			"pacman":  {"pacman", "-S", "--noconfirm", "passt"},
		} {
			if _, lookErr := exec.LookPath(pm); lookErr == nil {
				cmd2 := exec.Command(args[0], args[1:]...)
				cmd2.Stdout = os.Stdout
				cmd2.Stderr = os.Stderr
				if err2 := cmd2.Run(); err2 == nil {
					// Re-check after install
					for _, p := range pastaPaths {
						if _, statErr := os.Stat(p); statErr == nil {
							fmt.Printf("  \u2713 pasta installed at %s\n", p)
							pastaFound = true
							pastaActualPath = p
							break
						}
					}
				} else {
					fmt.Printf("  WARNING: could not install passt via %s: %v\n", pm, err2)
				}
				break
			}
		}
		if !pastaFound {
			fmt.Println("  NOTE: pasta is unavailable. slirp4netns will be used, which may")
			fmt.Println("        cause 'user.slice dbus permission denied' warnings during")
			fmt.Println("        deployments. These are usually harmless if networking works,")
			fmt.Println("        but installing passt eliminates them entirely.")
		}
	}

	// Podman searches for helper binaries (pasta, slirp4netns) in a compiled-in
	// list of directories: /usr/libexec/podman, /usr/lib/podman, etc.  It does
	// NOT search $PATH for them.  On Debian/Ubuntu, pasta is installed in
	// /usr/bin/pasta (via the passt package), which is NOT in Podman's helper
	// search path.  When containers.conf says default_rootless_network_cmd="pasta"
	// and Podman can't find pasta in its helper dirs, it exits 127 ("command not
	// found") and falls back to slirp4netns, causing the dbus user.slice errors.
	//
	// Fix: create symlinks in every Podman helper dir so the binary is found
	// regardless of which Podman version is installed.
	if pastaFound && pastaActualPath != "" {
		podmanHelperDirs := []string{
			"/usr/libexec/podman",
			"/usr/lib/podman",
			"/usr/local/lib/podman",
		}
		for _, helperDir := range podmanHelperDirs {
			linkPath := filepath.Join(helperDir, "pasta")
			if _, statErr := os.Stat(linkPath); statErr == nil {
				continue // already exists — real binary or existing symlink
			}
			if mkErr := os.MkdirAll(helperDir, 0755); mkErr != nil {
				continue
			}
			if lnErr := os.Symlink(pastaActualPath, linkPath); lnErr == nil {
				fmt.Printf("  \u2713 symlinked %s → %s\n", linkPath, pastaActualPath)
			}
		}
	}

	if !found {
		fmt.Println("  WARNING: netavark not found — attempting to install networking packages...")
		for pm, args := range map[string][]string{
			"dnf": {"dnf", "install", "-y", "netavark", "aardvark-dns"},
			"apt-get": {"apt-get", "install", "-y", "-q", "netavark"},
			"yum":     {"yum", "install", "-y", "--skip-broken", "netavark", "aardvark-dns"},
			"pacman":  {"pacman", "-S", "--noconfirm", "netavark"},
		} {
			if _, err := exec.LookPath(pm); err == nil {
				cmd2 := exec.Command(args[0], args[1:]...)
				cmd2.Stdout = os.Stdout
				cmd2.Stderr = os.Stderr
				if err2 := cmd2.Run(); err2 == nil {
					fmt.Println("  \u2713 networking packages installed")
				} else {
					fmt.Printf("  WARNING: could not install networking packages via %s: %v\n", pm, err2)
					fmt.Println("  !! If deployments fail with 'network not found', run manually:")
					fmt.Println("       sudo dnf install -y netavark aardvark-dns   # RHEL/AlmaLinux/Rocky")
					fmt.Println("       sudo apt-get install -y netavark             # Ubuntu/Debian")
				}
				break
			}
		}
	}

	// Compute the correct XDG_RUNTIME_DIR from the user's actual numeric UID.
	// /run/user/<uid> is created and managed by systemd-logind when linger is
	// enabled. Using the real UID (not a custom path like /run/featherdeploy-
	// runtime) matches what the running service process uses, so networks and
	// container state created here and by the service are in the same location.
	rtDir := "/run/featherdeploy-runtime" // fallback if id -u fails
	if uidOut, uidErr := exec.Command("id", "-u", username).Output(); uidErr == nil {
		if uid := strings.TrimSpace(string(uidOut)); uid != "" && uid != "0" {
			rtDir = "/run/user/" + uid
		}
	}
	if err := os.MkdirAll(rtDir, 0700); err == nil {
		exec.Command("chown", username+":"+username, rtDir).Run() //nolint
	}

	// ── Ensure the service user's home dir in /etc/passwd is correct ─────────
	// Podman calls getpwuid() internally and ignores the $HOME env var.
	// If /etc/passwd lists /home/<username> (useradd default) but the actual
	// data dir is /var/lib/featherdeploy, every `podman run` fails with
	// "permission denied" trying to create ~/.local/share/containers.
	actualHome, _ := exec.Command("getent", "passwd", username).Output()
	passwdHome := ""
	if fields := strings.Split(strings.TrimSpace(string(actualHome)), ":"); len(fields) >= 6 {
		passwdHome = fields[5]
	}
	if passwdHome != dataDir {
		fmt.Printf("  Fixing home dir in /etc/passwd: %q → %q\n", passwdHome, dataDir)
		// usermod -d updates /etc/passwd without moving files.
		// The featherdeploy service is stopped before this function is called
		// (RunUpdate explicitly stops it), so usermod succeeds here.
		if out, err := exec.Command("usermod", "-d", dataDir, username).CombinedOutput(); err != nil {
			fmt.Printf("  WARNING: usermod -d failed: %v — %s\n", err, strings.TrimSpace(string(out)))
		} else {
			fmt.Printf("  ✓ home dir updated to %s\n", dataDir)
			// Refresh homedir so the smoke test uses the correct path.
			homedir = dataDir

			// When the home dir changes, podman's container storage is now at a
			// different path. Purge the stale state from the OLD home to avoid
			// split-brain: podman finding partial state in two different locations
			// causes "network not found" at runtime even though network create
			// appeared to succeed.
			if passwdHome != "" && passwdHome != "/" && passwdHome != dataDir {
				staleContainers := filepath.Join(passwdHome, ".local", "share", "containers")
				if _, statErr := os.Stat(staleContainers); statErr == nil {
					fmt.Printf("  Removing stale container storage at old home: %s\n", staleContainers)
					os.RemoveAll(staleContainers) //nolint
				}
			}
			// Also clean up any partial state at the new home so podman starts fresh.
			newContainers := filepath.Join(dataDir, ".local", "share", "containers")
			if _, statErr := os.Stat(newContainers); statErr == nil {
				fmt.Printf("  Removing partial container storage at new home: %s\n", newContainers)
				os.RemoveAll(newContainers) //nolint
			}
			// Re-initialize podman storage at the correct home.
			migrateEnv := fmt.Sprintf("HOME=%s XDG_RUNTIME_DIR=%s", dataDir, rtDir)
			migrateCmd := exec.Command("su", "-s", "/bin/sh", username, "-c",
				"cd / && "+migrateEnv+" podman system migrate 2>&1")
			if migrateOut, migrateErr := migrateCmd.CombinedOutput(); migrateErr == nil {
				fmt.Println("  ✓ podman storage re-initialized at new home")
			} else {
				fmt.Printf("  NOTE: podman system migrate: %s\n", strings.TrimSpace(string(migrateOut)))
			}
		}
	} else {
		fmt.Printf("  ✓ home dir is already %s\n", dataDir)
	}

	// After computing rtDir and fixing home dir, write per-user containers.conf
	// and registries.conf, and remove any broken storage.conf.
	// These are also written in setupPodmanRootless; we repeat it here so
	// RunUpdate (which calls ensureNetworkingBackend but not setupPodmanRootless)
	// also applies the correct config.
	userConfDir := filepath.Join(homedir, ".config", "containers")
	// Hardcode the network_config_dir to an absolute path so podman never
	// computes it from XDG_CONFIG_HOME at runtime.  In Podman 4.x (netavark),
	// 'podman network create' and 'podman run --network' BOTH resolve the
	// network config directory through this setting.  Without it, any
	// difference in XDG_CONFIG_HOME between the two calls causes a split-brain
	// where the network is created in one path and looked up in another, which
	// manifests as exit 127 / "network not found" in podman run.
	// Podman documents that rootless netavark stores network JSON under
	// $graphroot/networks, with graphroot defaulting to
	// $XDG_DATA_HOME/containers/storage.
	netCfgDir := filepath.Join(homedir, ".local", "share", "containers", "storage", "networks")
	legacyNetCfgDir := filepath.Join(homedir, ".config", "containers", "networks")
	if mkErr := os.MkdirAll(userConfDir, 0755); mkErr == nil {
		os.MkdirAll(netCfgDir, 0755) //nolint
		// Note: graph_root is intentionally NOT hardcoded here.  Pinning it
		// causes a "database configuration mismatch" error when an existing
		// install used a different graph_root (e.g. after a home-dir change).
		// Podman resolves graph_root correctly from $XDG_DATA_HOME which we
		// always set to homedir/.local/share in podmanEnv().
		// network_config_dir IS hardcoded to Podman's documented rootless
		// netavark path so every podman subcommand uses the same network store.
		//
		// default_rootless_network_cmd: prefer "pasta" (from the passt package)
		// over "slirp4netns".  pasta is the modern Podman 5.x default and does
		// NOT try to move its process into the systemd user.slice via dbus,
		// which causes "connect: permission denied" on /run/user/<uid>/bus when
		// no user-session dbus socket is present.  slirp4netns does attempt
		// that move (even with --cgroup-manager cgroupfs), causing the
		// misleading "network not found" error that actually means the rootless
		// network namespace setup was aborted.  If pasta is absent, let Podman
		// auto-detect (it will use slirp4netns if that's all that's installed).
		rootlessNetCmd := ""
		for _, p := range []string{"/usr/bin/pasta", "/usr/local/bin/pasta", "/usr/libexec/pasta"} {
			if _, statErr := os.Stat(p); statErr == nil {
				rootlessNetCmd = "\ndefault_rootless_network_cmd = \"pasta\""
				fmt.Printf("  ✓ pasta found at %s — using it as rootless network command\n", p)
				break
			}
		}
		if rootlessNetCmd == "" {
			fmt.Println("  NOTE: pasta not found — Podman will auto-select slirp4netns.")
			fmt.Println("        Install 'passt' for better rootless networking (avoids dbus/user.slice errors).")
		}
		contConf := fmt.Sprintf(
			"[engine]\ncgroup_manager = \"cgroupfs\"\n\n"+
				"[network]\nnetwork_backend = \"netavark\"%s\nnetwork_config_dir = \"%s\"\n",
			rootlessNetCmd, netCfgDir)
		os.WriteFile(filepath.Join(userConfDir, "containers.conf"), []byte(contConf), 0644) //nolint
		// Remove any storage.conf that overrides runroot — the default
		// $XDG_RUNTIME_DIR/containers is correct and must not be changed.
		storagePath := filepath.Join(userConfDir, "storage.conf")
		if _, statErr := os.Stat(storagePath); statErr == nil {
			os.Remove(storagePath)
			fmt.Println("  ✓ removed bad storage.conf (was overriding runroot, causing network not found)")
		}
		if _, statErr := os.Stat(legacyNetCfgDir); statErr == nil && legacyNetCfgDir != netCfgDir {
			os.RemoveAll(legacyNetCfgDir) //nolint
			fmt.Printf("  ✓ removed legacy network_config_dir at %s\n", legacyNetCfgDir)
		}
		exec.Command("chown", "-R", username+":"+username, userConfDir).Run() //nolint
		fmt.Printf("  ✓ per-user containers.conf: network_config_dir=%s\n", netCfgDir)
	}

	// ── Wipe ALL stale Podman state databases ───────────────────────────────
	// There are TWO separate SQLite databases that store runRoot:
	//
	//  1. $graphRoot/db.sql  — containers/storage layer DB (tracks image
	//     layers, containers).  This is written on the FIRST ever podman call
	//     with whatever runRoot was active then.  Old installs wrote it with
	//     runRoot="" (env not set) or runRoot="/run/featherdeploy-runtime/..."
	//     (from storage.conf).  When this mismatches the current configured
	//     runRoot, EVERY podman call fails: "database run root X does not
	//     match our run root Y".
	//
	//  2. $graphRoot/libpod/  — libpod/podman state DB (containers, pods).
	//
	// We delete BOTH.  Image data lives in $graphRoot/overlay/ and is NOT
	// touched.  Both DBs are automatically recreated on the next podman call
	// (the smoke test below).  Because every podman call now passes --root and
	// --runroot explicitly (see podmanCmd in runner.go), the new DB is always
	// created with the correct values.
	graphRoot := filepath.Join(homedir, ".local", "share", "containers", "storage")
	runRoot := rtDir + "/containers"

	for _, staleDB := range []string{
		filepath.Join(graphRoot, "db.sql"),                 // containers/storage DB
		filepath.Join(graphRoot, "libpod"),                 // libpod state dir
		filepath.Join(runRoot, "libpod"),                   // ephemeral libpod state
		"/run/featherdeploy-runtime",                       // old custom XDG_RUNTIME_DIR
	} {
		if _, statErr := os.Stat(staleDB); statErr == nil {
			if err := os.RemoveAll(staleDB); err == nil {
				fmt.Printf("  ✓ deleted stale Podman DB: %s\n", staleDB)
			} else {
				fmt.Printf("  WARNING: could not delete %s: %v\n", staleDB, err)
			}
		}
	}

	// Ensure the runroot container dir exists and is owned by the service user.
	// (The systemd ExecStartPre creates it at service start, but the smoke test
	// below runs outside the service.)
	os.MkdirAll(runRoot, 0700)                                    //nolint
	exec.Command("chown", username+":"+username, runRoot).Run()   //nolint

	// NOTE: We deliberately do NOT run 'podman system migrate' here.
	// system migrate calls podman initialization which writes a DB entry.
	// If the DB is empty (just deleted) or partially written, migrate itself
	// fails with "database run root does not match" and may leave a corrupted
	// DB behind.  Since we just wiped all DBs, the smoke test below will
	// trigger a clean first-run initialization that writes the correct values.

	// Ensure the data dir exists and is owned by the service user.
	if err := os.MkdirAll(dataDir, 0755); err == nil {
		exec.Command("chown", "-R", username+":"+username, dataDir).Run() //nolint
	}

	// Run a quick smoke-test as the service user:
	// create a test network, run a tiny container on it, then remove it.
	// Inject the full XDG env (same as podmanEnv() in runner.go) so the
	// smoke test reads the same containers.conf the running service will use.
	testNetName := fmt.Sprintf("fd-nettest-%d", time.Now().UnixNano())
	testEnv := fmt.Sprintf(
		// DBUS_SESSION_BUS_ADDRESS is set to a deliberately invalid address so
		// sd-bus fails the connection attempt immediately instead of trying the
		// default /run/user/<uid>/bus socket.  An empty string has the same
		// effect as unset — sd-bus falls back to the default path — which causes
		// slirp4netns to attempt the user.slice cgroup move and fail with
		// "connect: permission denied" when no user dbus session is running.
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin HOME=%s XDG_RUNTIME_DIR=%s XDG_CONFIG_HOME=%s XDG_DATA_HOME=%s XDG_CACHE_HOME=%s DBUS_SESSION_BUS_ADDRESS=disabled:",
		homedir, rtDir,
		homedir+"/.config",
		homedir+"/.local/share",
		homedir+"/.cache",
	)
	testNetCfgDir := filepath.Join(homedir, ".local", "share", "containers", "storage", "networks")
	testGraphRoot := filepath.Join(homedir, ".local", "share", "containers", "storage")
	testRunRoot := rtDir + "/containers"
	// Ensure the runroot directory exists before the smoke test (the systemd
	// ExecStartPre normally creates it, but we're running outside the service).
	os.MkdirAll(testRunRoot, 0700)                                        //nolint
	exec.Command("chown", username+":"+username, testRunRoot).Run()        //nolint
	smoke := fmt.Sprintf(
		"%s podman --cgroup-manager cgroupfs --root %s --runroot %s --network-config-dir %s network rm -f %s >/dev/null 2>&1 || true; "+
			"%s podman --cgroup-manager cgroupfs --root %s --runroot %s --network-config-dir %s network create %s 2>&1 && "+
			"%s podman --cgroup-manager cgroupfs --root %s --runroot %s --network-config-dir %s run --rm --network %s docker.io/library/alpine true 2>&1 && "+
			"%s podman --cgroup-manager cgroupfs --root %s --runroot %s --network-config-dir %s network rm -f %s 2>/dev/null",
		testEnv, testGraphRoot, testRunRoot, testNetCfgDir, testNetName,
		testEnv, testGraphRoot, testRunRoot, testNetCfgDir, testNetName,
		testEnv, testGraphRoot, testRunRoot, testNetCfgDir, testNetName,
		testEnv, testGraphRoot, testRunRoot, testNetCfgDir, testNetName)
	// Prefix with 'cd /' so su doesn't inherit CWD=/root (permission denied).
	smokecmd := exec.Command("su", "-s", "/bin/sh", username, "-c", "cd / && "+smoke)
	if out, err := smokecmd.CombinedOutput(); err != nil {
		outStr := strings.TrimSpace(string(out))
		fmt.Printf("  WARNING: named network smoke-test failed: %v\n  output: %s\n", err, outStr)
		fmt.Println("  !! Service deployments will fail until this is resolved.")
		switch {
		// Check dbus/user.slice FIRST — the output contains "connect: permission
		// denied" (from the dbus socket) which would otherwise match the
		// "permission denied" storage case incorrectly.
		case strings.Contains(outStr, "user.slice") || strings.Contains(outStr, "session bus") || strings.Contains(outStr, "dbus"):
			// slirp4netns is trying to move its process into the systemd user.slice
			// even with --cgroup-manager cgroupfs.  This happens because slirp4netns
			// manages its own cgroup independently of Podman's cgroup manager setting.
			// Installing pasta (from the passt package) eliminates this entirely —
			// pasta is the modern Podman 5.x default and does not interact with dbus.
			fmt.Println("  Cause: slirp4netns is trying to reach the systemd user session bus")
			fmt.Println("         (even with cgroupfs cgroup manager) and fails because no")
			fmt.Println("         user-session dbus socket is available for the service account.")
			fmt.Println("  Fix (recommended): install pasta (passt package) and re-run the installer:")
			fmt.Println("    sudo dnf install -y passt   # RHEL/AlmaLinux/Rocky")
			fmt.Println("    sudo apt-get install -y passt  # Ubuntu/Debian")
			fmt.Println("    sudo featherdeploy update")
			fmt.Println("  Fix (alternative): enable the systemd user session for the service account:")
			fmt.Printf("    sudo loginctl enable-linger %s\n", username)
			fmt.Printf("    sudo systemctl --machine=%s@ --user start dbus.socket\n", username)
		case strings.Contains(outStr, "permission denied"):
			// Home dir was corrected above. If it still fails, the storage
			// directory doesn't exist or has wrong ownership.
			fmt.Printf("  Cause: Podman cannot create container storage.\n")
			fmt.Printf("  Fix:\n")
			fmt.Printf("    sudo mkdir -p %s\n", dataDir)
			fmt.Printf("    sudo chown -R %s:%s %s\n", username, username, dataDir)
			fmt.Printf("    sudo systemctl restart featherdeploy\n")
		case strings.Contains(outStr, "netavark") || strings.Contains(outStr, "command not found"):
			fmt.Println("  Fix: sudo dnf install -y netavark aardvark-dns passt  (RHEL/AlmaLinux/Rocky)")
			fmt.Println("       sudo apt-get install -y netavark passt            (Ubuntu/Debian)")
		case strings.Contains(outStr, "network not found"):
			// Network was created but run could not find it — classic split-brain.
			// This usually means the networking backend helper (netavark) exited
			// non-zero when setting up the container's network namespace.
			fmt.Println("  Cause: network was created but 'podman run' cannot attach to it.")
			fmt.Println("         This is usually caused by netavark failing silently, or by")
			fmt.Println("         slirp4netns aborting due to missing dbus (see user.slice error).")
			fmt.Println("  Fix: install pasta and re-run the installer:")
			fmt.Println("    sudo dnf install -y passt netavark aardvark-dns  # RHEL/AlmaLinux/Rocky")
			fmt.Println("    sudo apt-get install -y passt netavark            # Ubuntu/Debian")
			fmt.Println("    sudo featherdeploy update")
		default:
			fmt.Printf("  Diagnostics: sudo -u %s HOME=%s XDG_RUNTIME_DIR=%s podman network create test123 2>&1\n",
				username, homedir, rtDir)
		}
	} else {
		fmt.Println("  \u2713 named networking smoke-test passed")
	}
}

// ensureSubIDEntry appends a subuid/subgid range entry for username to file
// if no entry for that user already exists.
func ensureSubIDEntry(username, file, start, count string) {
	data, _ := os.ReadFile(file)
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, username+":") {
			fmt.Printf("  %s already has a %s entry — skipping\n", username, file)
			return
		}
	}
	f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		slog.Warn("cannot open subid file for writing", "file", file, "err", err)
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s:%s:%s\n", username, start, count)
	fmt.Printf("  ✓ %s → %s:%s:%s\n", file, username, start, count)
}

// createServiceUser creates a real login user that owns and runs the service.
// The user can SSH in or `su` to it, but does not have sudo/root access.
func createServiceUser(username, password string) {
	// Create the user if it doesn't already exist.
	// We explicitly set --home-dir to dataDir so that /etc/passwd records the
	// correct home.  Podman calls getpwuid() internally and ignores the $HOME
	// environment variable, so if the passwd entry points to /home/featherdeploy
	// (the default) but that directory is absent, every `podman run` fails with
	// "permission denied" trying to create ~/.local/share/containers.
	if runSilent("id", "-u", username) != nil {
		mustRun("useradd",
			"--create-home",
			"--home-dir", dataDir,
			"--shell", "/bin/bash",
			"--comment", "FeatherDeploy service account",
			username,
		)
		fmt.Printf("  ✓ created OS user: %s (home: %s)\n", username, dataDir)
	} else {
		// For pre-existing accounts, ensure /etc/passwd has the right home dir.
		// usermod -d does not move files; the installer will (re-)create them.
		mustRun("usermod", "-d", dataDir, username)
		fmt.Printf("  OS user '%s' already exists — home dir set to %s\n", username, dataDir)
	}
	// Set the password via chpasswd
	cmd := exec.Command("chpasswd")
	cmd.Stdin = strings.NewReader(username + ":" + password)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		slog.Warn("chpasswd failed", "err", err)
	} else {
		fmt.Printf("  ✓ password set for OS user: %s\n", username)
	}

	// Explicitly unlock the account. On Ubuntu/Debian, useradd leaves the
	// account in a locked state (password field starts with '!') until a
	// password is set. chpasswd sets the hash but some PAM configurations
	// still treat the account as locked. `passwd -u` removes that lock so
	// `su - <username>` works correctly even from root.
	mustRun("passwd", "-u", username)

	// Ensure the account never expires (chage -E -1 clears the expiry date).
	// Without this, some distros default to an expiry that causes PAM to deny
	// interactive login with "Permission denied" immediately after creation.
	mustRun("chage", "-E", "-1", username)
	mustRun("chage", "-M", "-1", username)
}

func mustRun(name string, args ...string) {
	if _, err := exec.LookPath(name); err != nil {
		slog.Warn("command not found — skipping", "cmd", name)
		return
	}
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		slog.Warn("command failed", "cmd", name, "err", err)
	}
}

func runSilent(name string, args ...string) error {
	return exec.Command(name, args...).Run()
}

// killUserProcesses stops all featherdeploy/rqlite services and kills every
// remaining process owned by username.  It sends SIGTERM first, then polls
// with pgrep until the user has no processes (up to 5 s), and finally sends
// SIGKILL to any survivors.  This guarantees usermod -d can run immediately
// after this call without hitting "user is currently used by process N".
func killUserProcesses(username string) {
	exec.Command("systemctl", "stop", "featherdeploy").Run() //nolint
	exec.Command("systemctl", "stop", "rqlite").Run()        //nolint
	exec.Command("pkill", "-TERM", "-u", username).Run()     //nolint
	// Poll until all processes owned by the user have exited.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(300 * time.Millisecond)
		out, _ := exec.Command("pgrep", "-u", username).Output()
		if len(strings.TrimSpace(string(out))) == 0 {
			return
		}
	}
	// Still alive after 5 s — force kill.
	exec.Command("pkill", "-KILL", "-u", username).Run() //nolint
	time.Sleep(300 * time.Millisecond)
}

func mustMkdir(path string) {
	if err := os.MkdirAll(path, 0755); err != nil {
		die("cannot create directory %s: %v", path, err)
	}
}

func copyFile(src, dst string) {
	data, err := os.ReadFile(src)
	if err != nil {
		die("cannot read %s: %v", src, err)
	}
	if err := os.WriteFile(dst, data, 0755); err != nil {
		die("cannot write %s: %v", dst, err)
	}
}

func writeFile(path, content string, perm os.FileMode) {
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		die("cannot write %s: %v", path, err)
	}
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ─── Package installation ─────────────────────────────────────────────────────

type pkgCmd struct {
	update  []string
	install []string
}

var pkgManagers = map[string]pkgCmd{
	"apt-get": {
		update: []string{"apt-get", "update", "-y"},
		// passt provides the 'pasta' binary, which is the modern replacement for
		// slirp4netns as Podman's rootless network helper.  pasta does NOT try to
		// move its process into the systemd user.slice via dbus, eliminating the
		// "connect: permission denied on /run/user/<uid>/bus" error that slirp4netns
		// triggers when no user-session dbus socket is present.
		install: []string{"apt-get", "install", "-y", "podman", "crun", "caddy", "netavark", "passt"},
	},
	"dnf": {
		update: []string{"dnf", "check-update"},
		// netavark and aardvark-dns are NOT pulled in as podman dependencies on
		// RHEL 9 / AlmaLinux / Rocky Linux. Without them, `podman network create`
		// appears to succeed (writes a JSON file) but `podman run --network <name>`
		// exits with code 127 because the netavark binary is missing.
		// passt (pasta) is the modern rootless network helper — avoids dbus issues.
		install: []string{"dnf", "install", "-y", "podman", "crun", "caddy", "netavark", "aardvark-dns", "passt"},
	},
	"yum": {
		update: nil,
		// On RHEL 8, netavark and passt may not exist in repos; containernetworking-plugins
		// is the CNI fallback. All are listed — yum ignores unavailable packages
		// when using --skip-broken.
		install: []string{"yum", "install", "-y", "--skip-broken", "podman", "crun",
			"netavark", "aardvark-dns", "containernetworking-plugins", "passt"},
	},
	"pacman": {
		update:  []string{"pacman", "-Sy"},
		install: []string{"pacman", "-S", "--noconfirm", "podman", "crun", "caddy", "netavark", "passt"},
	},
	"apk": {
		update:  []string{"apk", "update"},
		install: []string{"apk", "add", "--no-cache", "podman", "crun", "caddy"},
	},
}

const rqliteVer = "8.36.5"
const rqliteSystemdUnit = "/etc/systemd/system/rqlite.service"

// configureCrun sets up crun as the default Podman OCI runtime and forces
// cgroupfs cgroup management.
//
// cgroup_manager=cgroupfs is critical: the service user runs under a systemd
// *system* unit with no interactive user session, so there is no dbus socket.
// If the cgroup manager is "systemd", crun calls sd-bus to create a cgroup
// scope and fails with "Interactive authentication required."  cgroupfs
// bypasses dbus entirely.
func configureCrun() {
	if _, err := exec.LookPath("crun"); err != nil {
		fmt.Println("  WARNING: crun not found — skipping Podman runtime config")
		return
	}
	confDir := "/etc/containers"
	confFile := filepath.Join(confDir, "containers.conf")
	mustMkdir(confDir)

	var s string
	if data, err := os.ReadFile(confFile); err == nil {
		s = string(data)
	}

	// Ensure [engine] section exists.
	if !strings.Contains(s, "[engine]") {
		s += "\n[engine]\n"
	}
	// Set or replace runtime = "crun".
	if strings.Contains(s, "runtime") {
		s = regexp.MustCompile(`(?m)^\s*runtime\s*=.*$`).ReplaceAllString(s, `runtime = "crun"`)
	} else {
		s = strings.Replace(s, "[engine]", "[engine]\nruntime = \"crun\"", 1)
	}
	// Set or replace cgroup_manager = "cgroupfs".
	if strings.Contains(s, "cgroup_manager") {
		s = regexp.MustCompile(`(?m)^\s*cgroup_manager\s*=.*$`).ReplaceAllString(s, `cgroup_manager = "cgroupfs"`)
	} else {
		s = strings.Replace(s, "[engine]", "[engine]\ncgroup_manager = \"cgroupfs\"", 1)
	}

	writeFile(confFile, s, 0644)
	fmt.Println("  ✓ crun + cgroupfs configured in", confFile)

	// Set or ensure network_backend = "netavark" in the [network] section.
	// Without this, RHEL 9/AlmaLinux may default to CNI for some podman
	// subcommands (notably podman run) while using netavark for others
	// (podman network create/inspect). The mismatch causes "network not found"
	// because run looks in CNI config dirs while create wrote a netavark JSON.
	var ns string
	if data2, err := os.ReadFile(confFile); err == nil {
		ns = string(data2)
	} else {
		ns = s
	}
	if !strings.Contains(ns, "[network]") {
		ns += "\n[network]\n"
	}
	if strings.Contains(ns, "network_backend") {
		ns = regexp.MustCompile(`(?m)^\s*network_backend\s*=.*$`).ReplaceAllString(ns, `network_backend = "netavark"`)
	} else {
		ns = strings.Replace(ns, "[network]", "[network]\nnetwork_backend = \"netavark\"", 1)
	}

	// Set default_rootless_network_cmd = "pasta" if pasta is available.
	// This is the SYSTEM-LEVEL override — Podman reads this before the
	// per-user containers.conf. Without it, some Podman builds ignore the
	// user-level setting and default to slirp4netns, causing the dbus error.
	pastaSystemPaths := []string{"/usr/bin/pasta", "/usr/local/bin/pasta", "/usr/libexec/pasta"}
	pastaSystemFound := false
	for _, pp := range pastaSystemPaths {
		if _, statErr := os.Stat(pp); statErr == nil {
			pastaSystemFound = true
			break
		}
	}
	if pastaSystemFound {
		if strings.Contains(ns, "default_rootless_network_cmd") {
			ns = regexp.MustCompile(`(?m)^\s*default_rootless_network_cmd\s*=.*$`).
				ReplaceAllString(ns, `default_rootless_network_cmd = "pasta"`)
		} else {
			ns = strings.Replace(ns, "[network]", "[network]\ndefault_rootless_network_cmd = \"pasta\"", 1)
		}
	}

	// Add helper_binaries_dir so Podman searches /usr/bin and /usr/local/bin for
	// helper binaries (pasta, slirp4netns, etc.).  On Debian/Ubuntu, pasta is
	// installed to /usr/bin/pasta by the passt package, but Podman's compiled-in
	// helper search path only includes /usr/libexec/podman and /usr/lib/podman.
	// Without this, Podman can't find pasta, exits 127, and falls back to
	// slirp4netns — which causes the user.slice/dbus permission error.
	const helperBinDirs = `helper_binaries_dir = ["/usr/libexec/podman", "/usr/lib/podman", "/usr/local/lib/podman", "/usr/bin", "/usr/local/bin"]`
	if !strings.Contains(ns, "helper_binaries_dir") {
		ns = strings.Replace(ns, "[engine]", "[engine]\n"+helperBinDirs, 1)
	}

	writeFile(confFile, ns, 0644)
	fmt.Println("  ✓ network_backend=netavark set in", confFile)
	// Without this file every `podman build` fails with:
	//   "open /etc/containers/policy.json: no such file or directory"
	// The 'insecureAcceptAnything' type skips signature verification, which is
	// the correct default for a self-hosted PaaS pulling arbitrary user images.
	policyFile := filepath.Join(confDir, "policy.json")
	if _, err := os.Stat(policyFile); os.IsNotExist(err) {
		const policyJSON = `{"default":[{"type":"insecureAcceptAnything"}]}`
		if err2 := os.WriteFile(policyFile, []byte(policyJSON+"\n"), 0644); err2 == nil {
			fmt.Println("  ✓ policy.json (insecureAcceptAnything) written to", policyFile)
		}
	}

	// Write a system-wide registries.conf so short image names (e.g.
	// "php:8.2-fpm-alpine") resolve to docker.io.  Without this Podman exits
	// 125 with "short-name did not resolve to an alias and no
	// containers-registries.conf(5) was found".
	regFile := filepath.Join(confDir, "registries.conf")
	if _, err := os.Stat(regFile); os.IsNotExist(err) {
		const regConf = "unqualified-search-registries = [\"docker.io\"]\n"
		if err2 := os.WriteFile(regFile, []byte(regConf), 0644); err2 == nil {
			fmt.Println("  ✓ registries.conf (docker.io) written to", regFile)
		}
	}
}

// installRqlite downloads and installs the rqlite binary if not already present.
// If an existing binary is found but is corrupt (fails --version), it is removed
// and re-downloaded.
func installRqlite() {
	if path, err := exec.LookPath("rqlited"); err == nil {
		// Verify the binary is not corrupt by running --version
		if out, verErr := exec.Command(path, "--version").CombinedOutput(); verErr == nil && len(out) > 0 {
			fmt.Println("  rqlited already installed — skipping")
			return
		}
		fmt.Println("  WARNING: existing rqlited binary appears corrupt — removing and reinstalling")
		os.Remove(path)
		os.Remove("/usr/local/bin/rqlite")
	}
	fmt.Printf("  Downloading rqlite %s...\n", rqliteVer)
	tarName := fmt.Sprintf("rqlite-v%s-linux-amd64.tar.gz", rqliteVer)
	dlURL := fmt.Sprintf("https://github.com/rqlite/rqlite/releases/download/v%s/%s", rqliteVer, tarName)
	tmpTar := filepath.Join("/tmp", tarName)
	if err := downloadHTTP(dlURL, tmpTar); err != nil {
		fmt.Printf("  WARNING: cannot download rqlite: %v — install manually\n", err)
		return
	}
	dirName := fmt.Sprintf("rqlite-v%s-linux-amd64", rqliteVer)
	mustRun("tar", "-xzf", tmpTar, "-C", "/tmp/")
	for _, bin := range []string{"rqlited", "rqlite"} {
		src := filepath.Join("/tmp", dirName, bin)
		if _, err := os.Stat(src); err == nil {
			copyFile(src, "/usr/local/bin/"+bin)
			mustRun("chmod", "+x", "/usr/local/bin/"+bin)
		}
	}
	os.RemoveAll(filepath.Join("/tmp", dirName))
	os.Remove(tmpTar)
	fmt.Println("  ✓ rqlited installed")
}

// downloadHTTP downloads url to destPath.
func downloadHTTP(url, destPath string) error {
	resp, err := http.Get(url) //nolint:gosec — URL is constructed from a constant version
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	return os.WriteFile(destPath, data, 0644)
}

// waitForRqlite polls the rqlite /readyz endpoint until it responds or times out.
// /readyz (unlike /status) only returns 200 once Raft leader election is complete
// and the node is ready to accept write requests.  On timeout, the last 40 lines
// of the rqlite journal are printed so the operator can see the actual error.
func waitForRqlite(maxWait time.Duration) error {
	deadline := time.Now().Add(maxWait)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://127.0.0.1:4001/readyz") //nolint:gosec
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				// Extra 500ms grace period to let rqlite fully commit its Raft state
				time.Sleep(500 * time.Millisecond)
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	// Dump journal so the operator can see why rqlite crashed.
	fmt.Println("\n  ── rqlite journal (last 40 lines) ──────────────────────────")
	jctl := exec.Command("journalctl", "-u", "rqlite", "-n", "40", "--no-pager")
	jctl.Stdout = os.Stdout
	jctl.Stderr = os.Stderr
	jctl.Run() //nolint
	fmt.Println("  ──────────────────────────────────────────────────────────")
	return fmt.Errorf("rqlite not ready after %s", maxWait)
}

const rqliteServiceTmpl = `[Unit]
Description=rqlite Distributed SQLite
After=network.target
Before=featherdeploy.service

[Service]
Type=simple
User={{.User}}
Group={{.User}}
# Ensure the data directory is owned by the service user on every start
# (handles reboots where ownership may be reset or the dir was created as root)
ExecStartPre=/bin/bash -c 'mkdir -p {{.DataDir}}/rqlite-data && chown -R {{.User}}:{{.User}} {{.DataDir}}/rqlite-data'
ExecStart=/usr/local/bin/rqlited \
  -node-id=main \
  -http-addr=127.0.0.1:4001 \
  -raft-addr=127.0.0.1:4002 \
  -raft-adv-addr=127.0.0.1:4002 \
  -bootstrap-expect=1 \
  {{.DataDir}}/rqlite-data
Restart=always
RestartSec=5s
StartLimitIntervalSec=0
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
`

func writeRqliteService(svcUser string) {
	tmpl := template.Must(template.New("rqlite").Parse(rqliteServiceTmpl))
	var buf strings.Builder
	tmpl.Execute(&buf, struct{ User, DataDir string }{svcUser, dataDir})
	writeFile(rqliteSystemdUnit, buf.String(), 0644)
	fmt.Printf("  ✓ wrote %s\n", rqliteSystemdUnit)
}

func installPackages() error {
	for pm, cmds := range pkgManagers {
		if _, err := exec.LookPath(pm); err != nil {
			continue
		}
		fmt.Printf("  Detected package manager: %s\n", pm)
		if cmds.update != nil {
			cmd := exec.Command(cmds.update[0], cmds.update[1:]...)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			cmd.Run() //nolint — update failures are non-fatal
		}
		cmd := exec.Command(cmds.install[0], cmds.install[1:]...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s: %w", pm, err)
		}
		return nil
	}
	fmt.Println("  WARNING: no supported package manager found.")
	fmt.Println("  Please install podman and caddy manually, then re-run the installer.")
	return nil
}

// ─── Database seed ────────────────────────────────────────────────────────────

func createSuperAdmin(db *sql.DB, email, name, password string) error {
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	_, err = db.Exec(
		`INSERT OR IGNORE INTO users (email, name, password_hash, role) VALUES (?,?,?,?)`,
		email, name, hash, "superadmin",
	)
	return err
}

// ─── Caddyfile ────────────────────────────────────────────────────────────────

const caddyfileTmpl = `# FeatherDeploy — generated by installer
{
    # Global options
    email admin@{{.Domain}}
}

{{.Domain}} {
    # The deploypaaas binary serves both the API and the embedded frontend.
    reverse_proxy 127.0.0.1:{{.Port}}

    # Security headers
    header {
        Strict-Transport-Security "max-age=31536000; includeSubDomains"
        X-Frame-Options "SAMEORIGIN"
        X-Content-Type-Options "nosniff"
        Referrer-Policy "strict-origin-when-cross-origin"
        -Server
    }

    encode gzip
    log {
        output file /var/log/caddy/deploypaaas.log
    }
}

# Service domain routing — managed automatically by FeatherDeploy
import /etc/caddy/featherdeploy-services.caddy
`

func writeCaddyfile(domain string) {
	tmpl := template.Must(template.New("caddy").Parse(caddyfileTmpl))
	var buf strings.Builder
	tmpl.Execute(&buf, struct{ Domain, Port string }{domain, backendPort})
	writeFile(caddyConf, buf.String(), 0644)
	// Ensure Caddy log dir exists
	os.MkdirAll("/var/log/caddy", 0755)
	// Create the services include file so the import directive doesn't fail
	// on a fresh install before any service has been deployed.
	ensureCaddyServicesFile()
}

// ensureCaddyServicesFile creates /etc/caddy/featherdeploy-services.caddy with
// correct ownership if it does not already exist.
func ensureCaddyServicesFile() {
	const servicesFile = "/etc/caddy/featherdeploy-services.caddy"
	if _, err := os.Stat(servicesFile); err == nil {
		return // already exists
	}
	if err := os.WriteFile(servicesFile, []byte("# Auto-generated by FeatherDeploy\n"), 0644); err != nil {
		slog.Warn("installer: could not create caddy services file", "err", err)
	}
}

// ensureCaddyImport appends the services import directive to the main Caddyfile
// if it is missing.  Called during updates to migrate existing installations.
func ensureCaddyImport() {
	data, err := os.ReadFile(caddyConf)
	if err != nil {
		return
	}
	if strings.Contains(string(data), "featherdeploy-services.caddy") {
		return
	}
	f, err := os.OpenFile(caddyConf, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		slog.Warn("installer: could not update Caddyfile", "err", err)
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "\n# Service domain routing — managed automatically by FeatherDeploy\nimport /etc/caddy/featherdeploy-services.caddy\n")
	slog.Info("installer: added caddy services import to Caddyfile")
}

// ─── Systemd service ──────────────────────────────────────────────────────────

const systemdTmpl = `[Unit]
Description=FeatherDeploy Panel
Documentation=https://github.com/ojhapranjal26/FeatherDeploy
After=network.target rqlite.service
Requires=rqlite.service
StartLimitIntervalSec=120
StartLimitBurst=5

[Service]
Type=simple
User={{.User}}
Group={{.User}}
EnvironmentFile={{.EnvFile}}
# Rootless podman needs HOME to locate its image store (~/.local/share/containers)
# and XDG_RUNTIME_DIR for its socket / networking namespace.
# RuntimeDirectory creates /run/featherdeploy-runtime owned by the service user
# before the process starts, making it available as a stable XDG runtime dir.
Environment=HOME={{.DataDir}}
# XDG_RUNTIME_DIR is the standard rootless-podman runtime directory.
# We embed the service user's numeric UID directly (resolved at install/update
# time) rather than using the %U specifier, which systemd documents as
# defaulting to 0 when the user lookup fails — causing '/run/user/0 is not
# owned by the current user' errors on every podman call.
Environment=XDG_RUNTIME_DIR=/run/user/{{.UID}}
Environment=XDG_CONFIG_HOME={{.DataDir}}/.config
Environment=XDG_DATA_HOME={{.DataDir}}/.local/share
Environment=XDG_CACHE_HOME={{.DataDir}}/.cache
# Disable dbus: an empty value is treated identically to "unset" by sd-bus,
# which then falls back to the default /run/user/<uid>/bus socket and causes
# slirp4netns to attempt a user.slice cgroup move that fails with
# "connect: permission denied". "disabled:" is an invalid address that makes
# sd-bus abort immediately without touching any real socket.
Environment=DBUS_SESSION_BUS_ADDRESS=disabled:
# Guarantee /run/user/<uid> exists before ExecStart in case systemd-logind
# has not yet created it (e.g. first boot, or linger just enabled).
# The '+' prefix runs this pre-start command as root.
ExecStartPre=+/bin/bash -c 'mkdir -p /run/user/{{.UID}} /run/user/{{.UID}}/containers && chown {{.User}}:{{.User}} /run/user/{{.UID}} /run/user/{{.UID}}/containers && chmod 700 /run/user/{{.UID}} /run/user/{{.UID}}/containers'
ExecStart={{.Bin}} serve
Restart=always
RestartSec=5s
StandardOutput=journal
StandardError=journal
# PrivateTmp must NOT be set here. Rootless podman re-execs itself into a new
# user namespace (via newuidmap) to set up UID mapping. The child process does
# not inherit systemd's private /tmp bind-mount, so any build context written
# to /tmp by the parent becomes invisible to the podman build worker — causing
# "cannot chdir to /tmp/fd-dep-XXX: No such file or directory".
# Security isolation is provided by podman's own user namespaces instead.
#
# NoNewPrivileges must NOT be set: rootless podman forks newuidmap/newgidmap which
# are setuid-root binaries. NoNewPrivileges blocks their setuid bit, causing
# "write to uid_map failed: Operation not permitted" errors on every build/run.

[Install]
WantedBy=multi-user.target
`

func writeSystemdService(svcUser string) {
	// Resolve the service user's numeric UID at install/update time and embed
	// it directly in the unit. Using the %U systemd specifier is unreliable:
	// it defaults to 0 when the user lookup fails, making every podman call
	// fail with "XDG_RUNTIME_DIR /run/user/0 is not owned by current user".
	uid := "1000" // safe fallback
	if out, err := exec.Command("id", "-u", svcUser).Output(); err == nil {
		if s := strings.TrimSpace(string(out)); s != "" {
			uid = s
		}
	}
	tmpl := template.Must(template.New("svc").Parse(systemdTmpl))
	var buf strings.Builder
	tmpl.Execute(&buf, struct{ User, EnvFile, Bin, DataDir, UID string }{
		svcUser, envFile, binDest, dataDir, uid,
	})
	writeFile(systemdUnit, buf.String(), 0644)
}

// ─── Update (non-interactive) ─────────────────────────────────────────────────

// RunUpdate updates an existing FeatherDeploy installation in-place:
//   - The new binary is already in place (build.sh copied it before calling this)
//   - Opens the existing DB so appDb.Open applies any new schema migrations
//   - Restarts the systemd service
//   - Reloads Caddy
func RunUpdate() {
	if runtime.GOOS != "linux" {
		die("update only supported on Linux (got %s)", runtime.GOOS)
	}
	if os.Geteuid() != 0 {
		die("update must be run as root (use sudo)")
	}

	printBanner()
	fmt.Println("  Updating existing FeatherDeploy installation...")
	fmt.Println()

	// Determine rqlite URL from the existing env file (legacy DB_PATH no longer used)
	_ = readEnvVar(envFile, "DB_PATH") // kept for graceful forward-compat reads

	// ── Run database migrations ───────────────────────────────────────────────
	fmt.Println("── Applying database migrations ────────────────────────────────")
	fmt.Println("  (migrations applied via rqlite after service restart below)")

	// ── Rewrite systemd unit to pick up any template changes (e.g. security
	// hardening directives) without requiring a full reinstall.
	fmt.Println("\n── Updating systemd service unit ───────────────────────────────")
	// Detect the service user from the existing unit file so a customised
	// username is preserved; fall back to the default if not readable.
	svcUser := defaultSvcUser
	if data, err := os.ReadFile(systemdUnit); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "User=") {
				svcUser = strings.TrimPrefix(strings.TrimSpace(line), "User=")
				break
			}
		}
	}
	writeSystemdService(svcUser)
	fmt.Printf("  ✓ systemd unit updated (User=%s)\n", svcUser)

	// Re-enable linger for the service user so /run/user/<uid> is created
	// and maintained by systemd-logind (required for the new unit's
	// XDG_RUNTIME_DIR=/run/user/%U to exist at service start).
	if out, err := exec.Command("loginctl", "enable-linger", svcUser).CombinedOutput(); err != nil {
		slog.Warn("loginctl enable-linger failed (non-fatal)", "err", err, "out", string(out))
	} else {
		fmt.Printf("  ✓ loginctl enable-linger %s\n", svcUser)
	}
	// Pre-create /run/user/<uid> now so it exists before the service restarts.
	if uidOut, err := exec.Command("id", "-u", svcUser).Output(); err == nil {
		uidStr := strings.TrimSpace(string(uidOut))
		rtDir := "/run/user/" + uidStr
		if err2 := os.MkdirAll(rtDir, 0700); err2 == nil {
			exec.Command("chown", svcUser+":"+svcUser, rtDir).Run() //nolint
			fmt.Printf("  ✓ /run/user/%s ready\n", uidStr)
		}
	}

	// Stop services and kill all user processes before any user/storage changes.
	// killUserProcesses polls until pgrep confirms zero processes, then SIGKILLs
	// any survivors — guaranteeing usermod -d can run safely immediately after.
	fmt.Println("\n── Stopping services for maintenance ───────────────────────────")
	killUserProcesses(svcUser)
	fmt.Printf("  ✓ all %s processes stopped\n", svcUser)

	// ── Ensure Podman networking backend is installed ─────────────────────────
	// Existing installs may be missing netavark/aardvark-dns; this step installs
	// them if absent so `podman run --network <name>` stops failing with exit 127.
	fmt.Println("\n── Ensuring Podman named-network backend (netavark) ────────────")
	homedir := userHomeDir(svcUser)
	ensureNetworkingBackend(svcUser, homedir)

	// ── Restart rqlite + featherdeploy ───────────────────────────────────────
	fmt.Println("\n── Restarting services ─────────────────────────────────────────")
	mustRun("systemctl", "daemon-reload")
	mustRun("systemctl", "restart", "rqlite")
	if err := waitForRqlite(45 * time.Second); err != nil {
		slog.Warn("rqlite did not respond after restart", "err", err)
	} else {
		fmt.Println("  ✓ rqlite ready (leader elected)")
	}

	// Run schema migrations via rqlite
	rqliteURL := readEnvVar(envFile, "RQLITE_URL")
	if rqliteURL == "" {
		rqliteURL = "http://127.0.0.1:4001"
	}
	if mdb, err := appDb.OpenRqlite(rqliteURL); err == nil {
		mdb.Close()
		fmt.Printf("  ✓ DB migrations applied to %s\n", rqliteURL)
	} else {
		slog.Warn("DB migration check failed", "err", err)
	}

	mustRun("systemctl", "restart", "featherdeploy")
	fmt.Println("  ✓ featherdeploy service restarted")

	// ── Reload Caddy ──────────────────────────────────────────────────────────
	if runSilent("systemctl", "is-active", "--quiet", "caddy") == nil {
		ensureCaddyServicesFile()
		ensureCaddyImport()
		runSilent("systemctl", "reload", "caddy")
		fmt.Println("  ✓ Caddy reloaded")
	}

	fmt.Println(`
  ══════════════════════════════════════════════════════
  ✓  FeatherDeploy updated successfully!

     Check status:  sudo systemctl status featherdeploy
     View logs:     sudo journalctl -u featherdeploy -f

     Edit config:   sudo nano /etc/featherdeploy/featherdeploy.env
                    sudo systemctl restart featherdeploy
  ══════════════════════════════════════════════════════`)
}

// readEnvVar reads a KEY=VALUE pair from a shell env file.
func readEnvVar(file, key string) string {
	data, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[0]) == key {
			return strings.TrimSpace(parts[1])
		}
	}
	return ""
}
