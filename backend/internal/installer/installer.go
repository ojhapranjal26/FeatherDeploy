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
	"net"
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
	fmt.Println("\n── Configuring rqlite and etcd ───────────────────────────")
	installRqlite()
	installEtcd()
	writeRqliteService(svcUser)
	writeEtcdService(svcUser)
	mustRun("systemctl", "daemon-reload")
	mustRun("systemctl", "enable", "rqlite")
	mustRun("systemctl", "enable", "etcd")
	mustRun("systemctl", "restart", "rqlite")
	mustRun("systemctl", "restart", "etcd")

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

	// Detect the server's public IP so DNS verification works correctly.
	serverIP := installerDetectPublicIP()

	envContent := fmt.Sprintf(`# FeatherDeploy — generated %s
RQLITE_URL=%s
JWT_SECRET=%s
ADDR=127.0.0.1:%s
ORIGIN=%s
SERVER_IP=%s
`, time.Now().Format(time.RFC3339), rqliteURL, jwtSecret, backendPort, frontendOrigin, serverIP)

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
	// Hand ownership of the Caddyfile to the service user so ensureImport()
	// can update the import directive directly without needing sudo.
	exec.Command("chown", svcUser+":"+svcUser, caddyConf).Run() //nolint

	// ── Step 8b: Write sudoers rules so featherdeploy can reload Caddy ───────
	writeSudoersFile(svcUser)
	fmt.Println("  ✓ sudoers rules written")

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

	// ── Step 11: Protect internal port ranges ────────────────────────────────
	setupIPTablesProtection()

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

	// Verify that containers can actually be started with slirp4netns.
	// FDNet (internal TCP proxy daemon) uses slirp4netns:allow_host_loopback=true
	// for ALL containers, eliminating the need for netavark/aardvark-dns.
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

// loginSessionCmd executes shellCmd in a real systemd user session for username.
// Podman's rootless+cgroupv2 mode expects a valid login session. Running podman
// via plain 'su -c' inside a system service does not provide one reliably, and
// rootless networking can fail even when XDG paths look correct. systemd-run's
// --machine=<user>@ --user form is the integration Podman's own docs recommend.
func loginSessionCmd(username, shellCmd string) *exec.Cmd {
	if _, err := exec.LookPath("systemd-run"); err == nil {
		return exec.Command(
			"systemd-run",
			"--machine="+username+"@",
			"--quiet",
			"--user",
			"--collect",
			"--pipe",
			"--wait",
			"/bin/sh",
			"-lc",
			"cd / && "+shellCmd,
		)
	}
	return exec.Command("su", "-s", "/bin/sh", username, "-c", "cd / && "+shellCmd)
}

// ensureNetworkingBackend verifies that podman can start containers with
// slirp4netns (the universal rootless networking backend used by FDNet).
// Named Podman networks (netavark/aardvark-dns) are NO LONGER required —
// all container networking is handled by the in-process TCP proxy daemon.
func ensureNetworkingBackend(username, homedir string) {
	fmt.Println("  Checking Podman slirp4netns backend (required for fdnet)...")

	// Ensure slirp4netns is installed.
	slirpPaths := []string{
		"/usr/bin/slirp4netns",
		"/usr/local/bin/slirp4netns",
		"/usr/libexec/podman/slirp4netns",
	}
	slirpFound := false
	for _, p := range slirpPaths {
		if _, err := os.Stat(p); err == nil {
			fmt.Printf("  ✓ slirp4netns found at %s\n", p)
			slirpFound = true
			break
		}
	}
	if !slirpFound {
		fmt.Println("  slirp4netns not found — attempting to install...")
		for pm, args := range map[string][]string{
			"dnf":     {"dnf", "install", "-y", "slirp4netns"},
			"apt-get": {"apt-get", "install", "-y", "-q", "slirp4netns"},
			"yum":     {"yum", "install", "-y", "--skip-broken", "slirp4netns"},
			"pacman":  {"pacman", "-S", "--noconfirm", "slirp4netns"},
		} {
			if _, lookErr := exec.LookPath(pm); lookErr == nil {
				cmd2 := exec.Command(args[0], args[1:]...)
				cmd2.Stdout = os.Stdout
				cmd2.Stderr = os.Stderr
				if err2 := cmd2.Run(); err2 == nil {
					for _, p := range slirpPaths {
						if _, statErr := os.Stat(p); statErr == nil {
							fmt.Printf("  ✓ slirp4netns installed at %s\n", p)
							slirpFound = true
							break
						}
					}
				}
				break
			}
		}
		if !slirpFound {
			fmt.Println("  WARNING: slirp4netns could not be installed.")
			fmt.Println("  Run: sudo apt-get install -y slirp4netns   # Debian/Ubuntu")
			fmt.Println("       sudo dnf install -y slirp4netns       # RHEL/Fedora")
		}
	}

	// NOTE: aardvark-dns and netavark are NOT required by FeatherDeploy.
	// The fdnet daemon handles all container-to-container networking via a
	// lightweight TCP proxy — no named Podman networks are created at runtime.

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
			migrateEnv := fmt.Sprintf("HOME=%s XDG_RUNTIME_DIR=%s XDG_CONFIG_HOME=%s XDG_DATA_HOME=%s XDG_CACHE_HOME=%s",
				dataDir, rtDir, dataDir+"/.config", dataDir+"/.local/share", dataDir+"/.cache")
			migrateCmd := loginSessionCmd(username, migrateEnv+" podman system migrate 2>&1")
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
		// default_rootless_network_cmd is explicitly set to "slirp4netns".
		// FeatherDeploy's fdnet proxy uses 10.0.2.2 (the slirp4netns gateway)
		// for service-to-service routing, and host ports are bound on 127.0.0.1
		// only (-p 127.0.0.1:port:port).  Pasta's native mode clones the host
		// network namespace, assigns the container the host's real IP (not
		// 10.0.2.15), and port forwarding to 127.0.0.1 is unreliable in that
		// mode — Caddy receives "connection refused" and returns 502.  Forcing
		// slirp4netns prevents Podman from silently switching to pasta even when
		// pasta is installed alongside slirp4netns.
		contConf := fmt.Sprintf(
			"[engine]\ncgroup_manager = \"cgroupfs\"\n\n"+
				"[network]\nnetwork_backend = \"netavark\"\ndefault_rootless_network_cmd = \"slirp4netns\"\nnetwork_config_dir = \"%s\"\n",
			netCfgDir)
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

	// ── Smoke-test: run a minimal container with slirp4netns ─────────────────
	// This is the exact network mode FDNet uses for every deployment container.
	// Verifies cgroup setup, container storage, and slirp4netns integration.
	// Named networks are NOT tested here — they are no longer used.
	testEnv := fmt.Sprintf(
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/usr/libexec/podman:/usr/lib/podman:/usr/local/lib/podman HOME=%s XDG_RUNTIME_DIR=%s XDG_CONFIG_HOME=%s XDG_DATA_HOME=%s XDG_CACHE_HOME=%s",
		homedir, rtDir,
		homedir+"/.config",
		homedir+"/.local/share",
		homedir+"/.cache",
	)
	testGraphRoot := filepath.Join(homedir, ".local", "share", "containers", "storage")
	testRunRoot := rtDir + "/containers"
	testNetCfgDir := filepath.Join(homedir, ".local", "share", "containers", "storage", "networks")
	os.MkdirAll(testRunRoot, 0700)                                        //nolint
	exec.Command("chown", username+":"+username, testRunRoot).Run()        //nolint
	smoke := fmt.Sprintf(
		"%s podman --cgroup-manager cgroupfs --root %s --runroot %s --network-config-dir %s"+
			" run --rm --network slirp4netns:allow_host_loopback=true docker.io/library/alpine true 2>&1",
		testEnv, testGraphRoot, testRunRoot, testNetCfgDir)
	smokecmd := loginSessionCmd(username, smoke)
	if out, err := smokecmd.CombinedOutput(); err != nil {
		outStr := strings.TrimSpace(string(out))
		fmt.Printf("  WARNING: slirp4netns smoke-test failed: %v\n  output: %s\n", err, outStr)
		fmt.Println("  !! Service deployments may fail until this is resolved.")
		switch {
		case strings.Contains(outStr, "user.slice") || strings.Contains(outStr, "session bus") || strings.Contains(outStr, "dbus"):
			fmt.Println("  Cause: rootless Podman was executed without a valid login session.")
			fmt.Println("  Fix:")
			fmt.Printf("    sudo loginctl enable-linger %s\n", username)
			fmt.Println("    sudo featherdeploy update")
		case strings.Contains(outStr, "permission denied"):
			fmt.Printf("  Cause: Podman cannot create container storage.\n")
			fmt.Printf("  Fix:\n")
			fmt.Printf("    sudo mkdir -p %s\n", dataDir)
			fmt.Printf("    sudo chown -R %s:%s %s\n", username, username, dataDir)
			fmt.Printf("    sudo systemctl restart featherdeploy\n")
		case strings.Contains(outStr, "slirp4netns") || strings.Contains(outStr, "command not found"):
			fmt.Println("  Fix: sudo apt-get install -y slirp4netns   # Debian/Ubuntu")
			fmt.Println("       sudo dnf install -y slirp4netns       # RHEL/AlmaLinux/Rocky")
		default:
			fmt.Printf("  Diagnostics: sudo -u %s HOME=%s XDG_RUNTIME_DIR=%s podman run --rm --network slirp4netns alpine true 2>&1\n",
				username, homedir, rtDir)
		}
	} else {
		fmt.Println("  ✓ slirp4netns smoke-test passed")
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

// writeSudoersFile writes the NOPASSWD sudoers rules that allow the service
// user to:
//   - Reload Caddy via systemctl (both /bin and /usr/bin paths)
//   - Write the Caddy services include file via tee
//   - Run the self-update helper script
//
// This is called by both Run() and RunUpdate() so the file is always current
// regardless of which install path was used.
func writeSudoersFile(svcUser string) {
	const sudoersFile = "/etc/sudoers.d/featherdeploy-podman"
	content := svcUser + " ALL=(root) NOPASSWD: /bin/systemctl reload caddy\n" +
		svcUser + " ALL=(root) NOPASSWD: /usr/bin/systemctl reload caddy\n" +
		svcUser + " ALL=(root) NOPASSWD: /usr/bin/tee /etc/caddy/featherdeploy-services.caddy\n" +
		// Allow tee-based append for the main Caddyfile (used by ensureImport fallback).
		svcUser + " ALL=(root) NOPASSWD: /usr/bin/tee /etc/caddy/Caddyfile\n" +
		svcUser + " ALL=(root) NOPASSWD: /usr/local/bin/featherdeploy-update\n" +
		// Allow iptables for public database access toggle.
		svcUser + " ALL=(root) NOPASSWD: /sbin/iptables\n" +
		svcUser + " ALL=(root) NOPASSWD: /usr/sbin/iptables\n" +
		svcUser + " ALL=(root) NOPASSWD: /sbin/iptables-save\n" +
		svcUser + " ALL=(root) NOPASSWD: /usr/sbin/iptables-save\n" +
		// Allow ufw for public database access toggle (persists across UFW reloads).
		svcUser + " ALL=(root) NOPASSWD: /usr/sbin/ufw\n" +
		svcUser + " ALL=(root) NOPASSWD: /usr/local/bin/etcdctl\n"
	if err := os.WriteFile(sudoersFile, []byte(content), 0440); err != nil {
		slog.Warn("installer: could not write sudoers file", "path", sudoersFile, "err", err)
		return
	}
	// Validate syntax using -c (portable short flag supported on all sudo versions
	// and distros).  Avoid --check which is sudo 1.9+ only and silently deleted
	// valid files on older systems where the flag was unrecognised.
	if _, err := exec.LookPath("visudo"); err == nil {
		if out, err := exec.Command("visudo", "-c", "-f", sudoersFile).CombinedOutput(); err != nil {
			slog.Warn("installer: sudoers file failed visudo check — removing",
				"err", err, "out", strings.TrimSpace(string(out)))
			os.Remove(sudoersFile)
			return
		}
	}
	fmt.Printf("  ✓ %s\n", sudoersFile)
}

func copyFile(src, dst string) {
	data, err := os.ReadFile(src)
	if err != nil {
		die("cannot read %s: %v", src, err)
	}
	if err := os.WriteFile(dst, data, 0755); err != nil {
		die("cannot write %s: %v", dst, err)
	}
	os.Chmod(dst, 0755) // Ensure executable even if file existed with different perms
}

func writeFile(path, content string, perm os.FileMode) {
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		die("cannot write %s: %v", path, err)
	}
}

// setupIPTablesProtection installs iptables INPUT DROP rules that block
// direct external access to FeatherDeploy's internal port ranges:
//
//	10000–14999  service container host ports (rootlessport bound)
//	15000–29999  database container host ports (rootlessport bound)
//	30000–59999  fdnet cluster ports (Go TCP proxy, reachable only via Caddy)
//
// Containers use "-p 0.0.0.0:hostPort:appPort" so rootlessport can bind
// reliably (loopback-only binding is unreliable in some nftables/netavark
// configurations). These iptables rules ensure those ports are never
// reachable from the internet while still being accessible via localhost
// (Caddy → fdnet → rootlessport → container).
//
// IMPORTANT: rules use -m conntrack --ctstate NEW so that only brand-new
// inbound connections are dropped.  Response packets for connections this
// server or its containers initiated (git clone, npm install, podman pull,
// etc.) carry ctstate ESTABLISHED or RELATED and are therefore never
// affected.  A belt-and-suspenders ESTABLISHED,RELATED ACCEPT rule is also
// inserted at position 1.
//
// Rules are written to /etc/iptables/rules.v4 (Debian/Ubuntu) and
// /etc/sysconfig/iptables (RHEL/Fedora) for persistence across reboots.
func setupIPTablesProtection() {
	fmt.Println("\n── Protecting internal port ranges with iptables ───────────────")

	// Shift the kernel ephemeral port range above our internal port ranges
	// (10000–59999). This is a belt-and-suspenders measure: even on kernels
	// without conntrack, outbound connections use source ports ≥ 61000 so
	// their response packets never hit the DROP rules below.
	setEphemeralPortRange()

	// Check if iptables is available.
	iptablesPath, err := exec.LookPath("iptables")
	if err != nil {
		fmt.Println("  WARNING: iptables not found — skipping port protection")
		fmt.Println("  Install manually: apt-get install -y iptables")
		return
	}

	type rule struct {
		comment            string
		startPort, endPort int
	}
	rules := []rule{
		{"featherdeploy service ports", 10000, 14999},
		{"featherdeploy database ports", 15000, 29999},
		{"featherdeploy cluster ports", 30000, 59999},
	}

	// ── Remove legacy bare DROP rules (without --ctstate NEW) ─────────────────
	// Earlier versions inserted DROP rules without state-matching, which caused
	// them to also drop ESTABLISHED/RELATED response packets and break all
	// outbound TCP connections (git clone, npm install, podman pull, etc.).
	for _, r := range rules {
		portRange := fmt.Sprintf("%d:%d", r.startPort, r.endPort)
		legacySpec := []string{"!", "-i", "lo", "-p", "tcp",
			"--dport", portRange, "-m", "comment", "--comment", r.comment,
			"-j", "DROP"}
		// -D removes the first matching rule; loop until none remain.
		for exec.Command(iptablesPath, append([]string{"-C", "INPUT"}, legacySpec...)...).Run() == nil {
			exec.Command(iptablesPath, append([]string{"-D", "INPUT"}, legacySpec...)...).Run() //nolint:errcheck
		}
	}

	// ── Ensure ESTABLISHED/RELATED ACCEPT is at the top of INPUT ──────────────
	// This guarantees response packets for connections this server initiated are
	// accepted regardless of what DROP rules follow.
	estCheck := []string{"-C", "INPUT", "-m", "conntrack",
		"--ctstate", "ESTABLISHED,RELATED", "-j", "ACCEPT"}
	if exec.Command(iptablesPath, estCheck...).Run() != nil {
		insEst := []string{"-I", "INPUT", "1", "-m", "conntrack",
			"--ctstate", "ESTABLISHED,RELATED", "-j", "ACCEPT"}
		if out, err2 := exec.Command(iptablesPath, insEst...).CombinedOutput(); err2 != nil {
			fmt.Printf("  WARNING: could not add ESTABLISHED/RELATED ACCEPT rule: %v — %s\n",
				err2, strings.TrimSpace(string(out)))
		} else {
			fmt.Println("  ✓ iptables INPUT ACCEPT ESTABLISHED,RELATED (position 1)")
		}
	} else {
		fmt.Println("  ✓ iptables ESTABLISHED/RELATED ACCEPT already present")
	}

	// ── Allow traffic from private subnets (RFC1918) ──────────────────────────
	// This allows cross-node service and database communication within the
	// cluster's private network while still blocking the internet.
	// 10.0.2.2 and 127.0.0.1 are explicitly included to allow slirp4netns
	// containers to reach the host-side fdnet proxy.
	privateSubnets := []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.1/32", "10.0.2.2/32"}
	for _, subnet := range privateSubnets {
		for _, r := range rules {
			portRange := fmt.Sprintf("%d:%d", r.startPort, r.endPort)
			if runSilent(iptablesPath, "-C", "INPUT", "-s", subnet, "-p", "tcp", "--dport", portRange, "-j", "ACCEPT") != nil {
				mustRun(iptablesPath, "-I", "INPUT", "1", "-s", subnet, "-p", "tcp", "--dport", portRange, "-j", "ACCEPT")
			}
		}
	}

	// ── Add NEW-only DROP rules for internal port ranges ──────────────────────
	added := 0
	for _, r := range rules {
		portRange := fmt.Sprintf("%d:%d", r.startPort, r.endPort)
		// Check if the correct rule (with --ctstate NEW) already exists.
		checkArgs := []string{"-C", "INPUT", "!", "-i", "lo", "-p", "tcp",
			"--dport", portRange, "-m", "conntrack", "--ctstate", "NEW",
			"-m", "comment", "--comment", r.comment, "-j", "DROP"}
		if exec.Command(iptablesPath, checkArgs...).Run() == nil {
			continue // already present
		}
		// Append after the ESTABLISHED/RELATED ACCEPT at position 1.
		// --ctstate NEW ensures only unsolicited inbound connections are dropped.
		addArgs := []string{"-A", "INPUT", "!", "-i", "lo", "-p", "tcp",
			"--dport", portRange, "-m", "conntrack", "--ctstate", "NEW",
			"-m", "comment", "--comment", r.comment, "-j", "DROP"}
		if out, err2 := exec.Command(iptablesPath, addArgs...).CombinedOutput(); err2 != nil {
			fmt.Printf("  WARNING: could not add iptables rule for %s: %v — %s\n",
				r.comment, err2, strings.TrimSpace(string(out)))
		} else {
			fmt.Printf("  ✓ iptables INPUT DROP NEW tcp %s (%s)\n", portRange, r.comment)
			added++
		}
	}
	if added == 0 {
		fmt.Println("  ✓ iptables DROP rules already present — no changes needed")
	}

	// Persist rules so they survive reboots.
	persistIPTables()

	// ── Ensure cluster ports are open ────────────────────────────────────────
	fmt.Println("\n── Opening cluster ports (4001, 4002, 2379, 2380, 7443) ────────")
	clusterPorts := []string{"4001", "4002", "2379", "2380", "7443"}
	for _, p := range clusterPorts {
		if runSilent(iptablesPath, "-C", "INPUT", "-p", "tcp", "--dport", p, "-j", "ACCEPT") != nil {
			mustRun(iptablesPath, "-I", "INPUT", "1", "-p", "tcp", "--dport", p, "-j", "ACCEPT")
		}
	}
}

// setEphemeralPortRange shifts the kernel's local (ephemeral) port range to
// 61000–65535, placing it above FeatherDeploy's internal port ranges
// (10000–59999).  This is a belt-and-suspenders complement to conntrack state
// matching: outbound connections from this server (and from containers via
// slirp4netns) will use source ports ≥ 61000, so their response packets can
// never be confused with unsolicited inbound traffic on our internal ports.
func setEphemeralPortRange() {
	const (
		procPath    = "/proc/sys/net/ipv4/ip_local_port_range"
		sysctlFile  = "/etc/sysctl.d/99-featherdeploy.conf"
		wantProc    = "61000\t65535"
		wantSetting = "net.ipv4.ip_local_port_range = 61000 65535"
	)

	// Apply immediately to the running kernel.
	current, _ := os.ReadFile(procPath)
	if strings.TrimSpace(string(current)) != wantProc {
		if err := os.WriteFile(procPath, []byte(wantProc+"\n"), 0644); err != nil {
			fmt.Printf("  WARNING: could not set ephemeral port range: %v\n", err)
		} else {
			fmt.Println("  ✓ ephemeral port range set to 61000-65535 (avoids internal port overlap)")
		}
	} else {
		fmt.Println("  ✓ ephemeral port range already 61000-65535")
	}

	// Persist across reboots via sysctl.d (append if not already present).
	existing, _ := os.ReadFile(sysctlFile)
	if !strings.Contains(string(existing), "ip_local_port_range") {
		f, err := os.OpenFile(sysctlFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err == nil {
			fmt.Fprintf(f, "%s\n", wantSetting)
			f.Close()
		}
	}
}

// persistIPTables saves the current iptables ruleset for persistence.
func persistIPTables() {
	// Debian / Ubuntu: iptables-persistent reads /etc/iptables/rules.v4.
	if err := os.MkdirAll("/etc/iptables", 0755); err == nil {
		if out, err := exec.Command("iptables-save").Output(); err == nil {
			if wErr := os.WriteFile("/etc/iptables/rules.v4", out, 0640); wErr == nil {
				fmt.Println("  ✓ iptables rules saved to /etc/iptables/rules.v4")
				return
			}
		}
	}
	// RHEL / Fedora: /etc/sysconfig/iptables.
	if err := os.MkdirAll("/etc/sysconfig", 0755); err == nil {
		if out, err := exec.Command("iptables-save").Output(); err == nil {
			if wErr := os.WriteFile("/etc/sysconfig/iptables", out, 0640); wErr == nil {
				fmt.Println("  ✓ iptables rules saved to /etc/sysconfig/iptables")
				return
			}
		}
	}
	fmt.Println("  NOTE: could not persist iptables rules — they will be lost on reboot")
	fmt.Println("  Run: apt-get install -y iptables-persistent   # Debian/Ubuntu")
	fmt.Println("       dnf install -y iptables-services          # RHEL/Fedora")
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// installerDetectPublicIP uses a UDP routing trick followed by a web-based
// fallback to find the server's public-facing IP.
func installerDetectPublicIP() string {
	// Try UDP routing trick first (fast, no external traffic)
	ip := ""
	if conn, err := net.DialTimeout("udp", "1.1.1.1:80", 2*time.Second); err == nil {
		ip = conn.LocalAddr().(*net.UDPAddr).IP.String()
		conn.Close()
	}

	// If we got a private IP or no IP, use external lookup services
	if ip == "" || isPrivateIP(ip) {
		urls := []string{"https://ident.me", "https://ifconfig.me/ip", "https://api.ipify.org"}
		for _, u := range urls {
			client := http.Client{Timeout: 3 * time.Second}
			if resp, err := client.Get(u); err == nil {
				if body, err := io.ReadAll(resp.Body); err == nil {
					extIP := strings.TrimSpace(string(body))
					if net.ParseIP(extIP) != nil {
						ip = extIP
						resp.Body.Close()
						break
					}
				}
				resp.Body.Close()
			}
		}
	}
	return ip
}

func isPrivateIP(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return true
	}
	// Check for RFC1918 and other private ranges
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil {
		switch {
		case ip4[0] == 10:
			return true
		case ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31:
			return true
		case ip4[0] == 192 && ip4[1] == 168:
			return true
		}
	}
	return false
}

// ─── Package installation ─────────────────────────────────────────────────────

type pkgCmd struct {
	update  []string
	install []string
}

var pkgManagers = map[string]pkgCmd{
	"apt-get": {
		update: []string{"apt-get", "update", "-y"},
		// aardvark-dns is REQUIRED by netavark bridge networking — without it,
		// netavark cannot start the per-network DNS forwarder and 'podman run
		// --network <name>' fails with the misleading error "network not found"
		// (netavark tries to exec aardvark-dns, gets ENOENT, exits 127, and
		// Podman maps the helper failure to "unable to find network").
		// passt is still useful to have available, but FeatherDeploy pins
		// slirp4netns for rootless user-defined networks because inter-container
		// connectivity is a first-class requirement.
		install: []string{"apt-get", "install", "-y", "podman", "crun", "caddy", "netavark", "aardvark-dns", "slirp4netns", "passt", "containernetworking-plugins"},
	},
	"dnf": {
		update: []string{"dnf", "check-update"},
		// netavark and aardvark-dns are NOT pulled in as podman dependencies on
		// RHEL 9 / AlmaLinux / Rocky Linux. Without them, `podman network create`
		// appears to succeed (writes a JSON file) but `podman run --network <name>`
		// exits with code 127 because the netavark binary is missing.
		// passt is installed as an available helper, but FeatherDeploy keeps
		// slirp4netns as the explicit rootless network command for named networks.
		install: []string{"dnf", "install", "-y", "podman", "crun", "caddy", "netavark", "aardvark-dns", "slirp4netns", "passt", "containernetworking-plugins"},
	},
	"yum": {
		update: nil,
		// On RHEL 8, netavark and passt may not exist in repos; containernetworking-plugins
		// is the CNI fallback. All are listed — yum ignores unavailable packages
		// when using --skip-broken.
		install: []string{"yum", "install", "-y", "--skip-broken", "podman", "crun",
			"netavark", "aardvark-dns", "slirp4netns", "containernetworking-plugins", "passt"},
	},
	"pacman": {
		update:  []string{"pacman", "-Sy"},
		install: []string{"pacman", "-S", "--noconfirm", "podman", "crun", "caddy", "netavark", "aardvark-dns", "slirp4netns", "passt"},
	},
	"apk": {
		update:  []string{"apk", "update"},
		install: []string{"apk", "add", "--no-cache", "podman", "crun", "caddy"},
	},
}

const rqliteVer = "8.36.5"
const etcdVer = "3.5.13"
const rqliteSystemdUnit = "/etc/systemd/system/rqlite.service"
const etcdSystemdUnit = "/etc/systemd/system/etcd.service"

// configureCrun sets up crun as the default Podman OCI runtime and forces
// cgroupfs cgroup management.
//
// cgroup_manager=cgroupfs is critical: even with a PAM/logind session, this
// service still runs under a system unit and should not depend on crun talking
// to systemd over sd-bus just to manage container cgroups. cgroupfs keeps the
// runtime behavior stable and avoids scope-creation failures.
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

	// FeatherDeploy relies on user-defined networks for internal service
	// communication. Podman 5+ defaults to pasta for rootless networking,
	// which avoids DBus user.slice errors. We dynamically configure the default
	// based on what's available.
	rootlessNetCmd := "slirp4netns"
	if _, err := exec.LookPath("pasta"); err == nil {
		rootlessNetCmd = "pasta"
	}

	if strings.Contains(ns, "default_rootless_network_cmd") {
		ns = regexp.MustCompile(`(?m)^\s*default_rootless_network_cmd\s*=.*$`).
			ReplaceAllString(ns, `default_rootless_network_cmd = "`+rootlessNetCmd+`"`)
	} else {
		ns = strings.Replace(ns, "[network]", "[network]\ndefault_rootless_network_cmd = \""+rootlessNetCmd+"\"", 1)
	}

	// Add helper_binaries_dir so Podman searches /usr/bin and /usr/local/bin for
	// helper binaries such as slirp4netns, netavark, aardvark-dns, and pasta.
	// Several distro builds place these outside Podman's compiled-in helper path.
	const helperBinDirs = `helper_binaries_dir = ["/usr/libexec/podman", "/usr/lib/podman", "/usr/local/lib/podman", "/usr/bin", "/usr/local/bin"]`
	if !strings.Contains(ns, "helper_binaries_dir") {
		ns = strings.Replace(ns, "[engine]", "[engine]\n"+helperBinDirs, 1)
	} else {
		// Ensure /usr/bin is included for distros that install helper binaries there.
		if !strings.Contains(ns, "/usr/bin") {
			ns = regexp.MustCompile(`(?m)^\s*helper_binaries_dir\s*=.*$`).
				ReplaceAllString(ns, helperBinDirs)
		}
	}

	// Rootless FeatherDeploy relies on user-defined networks for internal service
	// communication. We let the service run inside a real PAM/logind session
	// instead of trying to hack around a missing session via unsupported config keys.
	ns = regexp.MustCompile(`(?m)^\s*slirp4netns_args\s*=.*$\n?`).ReplaceAllString(ns, "")

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
	mustRun("tar", "-xzf", tmpTar, "-C", "/tmp/")
	matches, _ := filepath.Glob("/tmp/rqlite-v*-linux-amd64/rqlited")
	if len(matches) == 0 {
		fmt.Printf("  WARNING: could not find rqlited in extracted tarball — extraction may have failed\n")
		return
	}
	srcDir := filepath.Dir(matches[0])
	for _, bin := range []string{"rqlited", "rqlite"} {
		src := filepath.Join(srcDir, bin)
		if _, err := os.Stat(src); err == nil {
			copyFile(src, "/usr/local/bin/"+bin)
		}
	}
	os.RemoveAll(srcDir)
	os.Remove(tmpTar)

	// Verify
	if out, err := exec.Command("/usr/local/bin/rqlited", "-version").CombinedOutput(); err != nil {
		fmt.Printf("  WARNING: rqlited verification failed: %v\n", err)
		fmt.Printf("  Output: %s\n", string(out))
	} else {
		fmt.Println("  ✓ rqlited installed and verified")
	}
}

func installEtcd() {
	if path, err := exec.LookPath("etcd"); err == nil {
		if out, verErr := exec.Command(path, "--version").CombinedOutput(); verErr == nil && len(out) > 0 {
			fmt.Println("  etcd already installed — skipping")
			return
		}
		os.Remove(path)
		os.Remove("/usr/local/bin/etcdctl")
	}
	fmt.Printf("  Downloading etcd v%s...\n", etcdVer)
	tarName := fmt.Sprintf("etcd-v%s-linux-amd64.tar.gz", etcdVer)
	dlURL := fmt.Sprintf("https://github.com/etcd-io/etcd/releases/download/v%s/%s", etcdVer, tarName)
	tmpTar := filepath.Join("/tmp", tarName)
	if err := downloadHTTP(dlURL, tmpTar); err != nil {
		fmt.Printf("  WARNING: cannot download etcd: %v — install manually\n", err)
		return
	}
	mustRun("tar", "-xzf", tmpTar, "-C", "/tmp/")
	matches, _ := filepath.Glob("/tmp/etcd-v*-linux-amd64/etcd")
	if len(matches) == 0 {
		fmt.Printf("  WARNING: could not find etcd in extracted tarball — extraction may have failed\n")
		return
	}
	srcDir := filepath.Dir(matches[0])
	for _, bin := range []string{"etcd", "etcdctl"} {
		src := filepath.Join(srcDir, bin)
		if _, err := os.Stat(src); err == nil {
			copyFile(src, "/usr/local/bin/"+bin)
		}
	}
	os.RemoveAll(srcDir)
	os.Remove(tmpTar)

	// Verify
	if out, err := exec.Command("/usr/local/bin/etcd", "--version").CombinedOutput(); err != nil {
		fmt.Printf("  WARNING: etcd verification failed: %v\n", err)
		fmt.Printf("  Output: %s\n", string(out))
		// Also check file info
		info, _ := exec.Command("ls", "-l", "/usr/local/bin/etcd").CombinedOutput()
		fmt.Printf("  File info: %s\n", string(info))
	} else {
		fmt.Println("  ✓ etcd installed and verified")
	}
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
	fmt.Printf("  ✓ downloaded %d bytes\n", len(data))
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
StartLimitIntervalSec=0

[Service]
Type=simple
User={{.User}}
Group={{.User}}
# Ensure the data directory is owned by the service user on every start
# (handles reboots where ownership may be reset or the dir was created as root)
ExecStartPre=/bin/bash -c 'mkdir -p {{.DataDir}}/rqlite-data && chown -R {{.User}}:{{.User}} {{.DataDir}}/rqlite-data'
ExecStart=/usr/local/bin/rqlited \
  -node-id=main \
  -http-addr=0.0.0.0:4001 \
  -http-adv-addr={{.PublicIP}}:4001 \
  -raft-addr=0.0.0.0:4002 \
  -raft-adv-addr={{.PublicIP}}:4002 \
  -bootstrap-expect=1 \
  {{.DataDir}}/rqlite-data
Restart=always
RestartSec=5s
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
`

func writeRqliteService(svcUser string) {
	publicIP := installerDetectPublicIP()
	if publicIP == "" {
		publicIP = "127.0.0.1"
	}
	tmpl := template.Must(template.New("rqlite").Parse(rqliteServiceTmpl))
	var buf strings.Builder
	tmpl.Execute(&buf, struct{ User, DataDir, PublicIP string }{svcUser, dataDir, publicIP})
	writeFile(rqliteSystemdUnit, buf.String(), 0644)
	fmt.Printf("  ✓ wrote %s\n", rqliteSystemdUnit)
}

const etcdServiceTmpl = `[Unit]
Description=etcd Key-Value Store
After=network.target

[Service]
Type=simple
User={{.User}}
Group={{.User}}
ExecStartPre=/bin/bash -c 'mkdir -p {{.DataDir}}/etcd-data && chown -R {{.User}}:{{.User}} {{.DataDir}}/etcd-data'
ExecStart=/usr/local/bin/etcd \
  --name=main \
  --data-dir={{.DataDir}}/etcd-data \
  --listen-client-urls=http://0.0.0.0:2379 \
  --advertise-client-urls=http://{{.PublicIP}}:2379 \
  --listen-peer-urls=http://0.0.0.0:2380 \
  --initial-advertise-peer-urls=http://{{.PublicIP}}:2380 \
  --initial-cluster=main=http://{{.PublicIP}}:2380 \
  --initial-cluster-token=etcd-cluster-1 \
  --initial-cluster-state=new
Restart=always
RestartSec=5s

[Install]
WantedBy=multi-user.target
`

func writeEtcdService(svcUser string) {
	publicIP := installerDetectPublicIP()
	if publicIP == "" {
		publicIP = "127.0.0.1"
	}
	tmpl := template.Must(template.New("etcd").Parse(etcdServiceTmpl))
	var buf strings.Builder
	tmpl.Execute(&buf, struct{ User, DataDir, PublicIP string }{svcUser, dataDir, publicIP})
	writeFile(etcdSystemdUnit, buf.String(), 0644)
	fmt.Printf("  ✓ wrote %s\n", etcdSystemdUnit)
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
	ensureCaddyServicesFile(defaultSvcUser)
}

// ensureCaddyServicesFile creates /etc/caddy/featherdeploy-services.caddy with
// correct ownership if it does not already exist.
func ensureCaddyServicesFile(svcUser string) {
	const servicesFile = "/etc/caddy/featherdeploy-services.caddy"
	if _, err := os.Stat(servicesFile); err == nil {
		// File exists — ensure correct ownership so the service process can update it.
		exec.Command("chown", svcUser+":"+svcUser, servicesFile).Run() //nolint
	} else {
		if err := os.WriteFile(servicesFile, []byte("# Auto-generated by FeatherDeploy\n"), 0644); err != nil {
			slog.Warn("installer: could not create caddy services file", "err", err)
		} else {
			exec.Command("chown", svcUser+":"+svcUser, servicesFile).Run() //nolint
		}
	}
	// Make /etc/caddy/ writable by the service user's primary group so the
	// running daemon can atomically rename temp config files into the directory
	// (the fastest / safest write path in writeServicesFile).
	exec.Command("chgrp", svcUser, "/etc/caddy").Run()  //nolint
	exec.Command("chmod", "g+w", "/etc/caddy").Run()    //nolint
	// Transfer Caddyfile ownership so ensureImport() can append to it directly
	// without sudo.  (Belt-and-suspenders: the installer also does this after
	// writeCaddyfile, but update runs ensureCaddyServicesFile without calling
	// writeCaddyfile.)
	if _, err := os.Stat(caddyConf); err == nil {
		exec.Command("chown", svcUser, caddyConf).Run() //nolint
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
# Create a real PAM/logind session for the service user.
# Podman's rootless+cgroupv2 mode expects a valid login session; without one,
# slirp4netns may fail to integrate with the user session and custom networks
# break even when XDG paths look correct.
# 'systemd-user' is safer than 'login' as it avoids pam_lastlog.so errors on Ubuntu 24.04.
PAMName=systemd-user
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
# Guarantee /run/user/<uid> exists before ExecStart in case systemd-logind
# has not yet created it (e.g. first boot, or linger just enabled).
# The '+' prefix runs this pre-start command as root.
ExecStartPre=+/bin/bash -c 'install -d -m 700 -o {{.User}} -g {{.User}} /run/user/{{.UID}} /run/user/{{.UID}}/containers'
ExecStart={{.Bin}} serve
Restart=always
RestartSec=5s
StandardOutput=journal
StandardError=journal
SyslogIdentifier=featherdeploy
# Delegate=yes hands off cgroup subtree management to featherdeploy/podman.
# Without this systemd retains cgroup ownership and rootless podman cannot
# create child cgroups for resource limits (--cpus / --memory), causing every
# database container to exit 127 immediately.  With it, systemd enables the
# cpu/memory/io/pids controllers in this unit's delegated slice so podman can
# manage container cgroups normally.
Delegate=yes
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
	writeRqliteService(svcUser)
	writeEtcdService(svcUser)
	fmt.Printf("  ✓ systemd units updated (User=%s)\n", svcUser)

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
	// configureCrun rewrites the system-wide /etc/containers/containers.conf to
	// ensure: crun runtime, cgroupfs cgroup manager, netavark backend,
	// helper_binaries_dir (so Podman finds pasta/slirp4netns in /usr/bin), and
	// slirp4netns_args=[--disable-sandbox] (suppresses dbus user.slice errors).
	// This must be called on every update so new config keys are always present.
	configureCrun()
	ensureNetworkingBackend(svcUser, homedir)

	// ── Install/Update binaries ──────────────────────────────────────────────
	fmt.Println("\n── Updating rqlite and etcd binaries ───────────────────────────")
	installRqlite()
	installEtcd()

	// ── Restart rqlite + etcd + featherdeploy ────────────────────────────────
	fmt.Println("\n── Restarting services ─────────────────────────────────────────")
	mustRun("systemctl", "daemon-reload")
	mustRun("systemctl", "enable", "rqlite")
	mustRun("systemctl", "enable", "etcd")
	mustRun("systemctl", "restart", "rqlite")
	mustRun("systemctl", "restart", "etcd")
	fmt.Println("  Waiting for rqlite and etcd to be ready...")
	if err := waitForRqlite(45 * time.Second); err != nil {
		slog.Warn("rqlite did not respond after restart", "err", err)
	}
	if runSilent("systemctl", "is-active", "--quiet", "etcd") != nil {
		fmt.Println("  WARNING: etcd failed to start. Last 20 lines of logs:")
		out, _ := exec.Command("journalctl", "-u", "etcd", "-n", "20", "--no-pager").CombinedOutput()
		fmt.Println(string(out))
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

	// ── Ensure SERVER_IP is in the env file ───────────────────────────────────
	// Existing installations (before this installer version) won't have
	// SERVER_IP set, causing DNS verification to show "expected: (empty)".
	if readEnvVar(envFile, "SERVER_IP") == "" {
		if publicIP := installerDetectPublicIP(); publicIP != "" {
			if f, err := os.OpenFile(envFile, os.O_APPEND|os.O_WRONLY, 0640); err == nil {
				fmt.Fprintf(f, "SERVER_IP=%s\n", publicIP)
				f.Close()
				fmt.Printf("  ✓ SERVER_IP=%s written to env file\n", publicIP)
			}
		}
	}

	// ── Reload Caddy ──────────────────────────────────────────────────────────
	if runSilent("systemctl", "is-active", "--quiet", "caddy") == nil {
		ensureCaddyServicesFile(defaultSvcUser)
		ensureCaddyImport()
		writeSudoersFile(defaultSvcUser)
		runSilent("systemctl", "reload", "caddy")
		fmt.Println("  ✓ Caddy reloaded")
	}

	// ── Protect internal port ranges ──────────────────────────────────────────
	// Block external access to host/cluster port ranges so containers can use
	// 0.0.0.0 port binding (which is more reliable than 127.0.0.1 binding with
	// rootlessport/netavark) without exposing raw service ports to the internet.
	setupIPTablesProtection()

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
