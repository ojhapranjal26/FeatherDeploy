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
	writeRqliteService(svcUser)
	mustRun("systemctl", "daemon-reload")
	mustRun("systemctl", "enable", "rqlite")
	mustRun("systemctl", "start", "rqlite")
	fmt.Println("  Waiting for rqlite to be ready...")
	if err := waitForRqlite(30 * time.Second); err != nil {
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
	ensureSubIDEntry(username, "/etc/subuid", "100000", "65536")
	ensureSubIDEntry(username, "/etc/subgid", "100000", "65536")
	// Tell Podman to migrate its storage to use the new mapping.
	cmd := exec.Command("su", "-s", "/bin/sh", username, "-c", "podman system migrate")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		slog.Warn("podman system migrate failed (non-fatal)", "err", err)
	}
	fmt.Printf("  ✓ rootless Podman configured for %s\n", username)
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
	// Create the user if it doesn't already exist
	if runSilent("id", "-u", username) != nil {
		mustRun("useradd",
			"--create-home",
			"--shell", "/bin/bash",
			"--comment", "FeatherDeploy service account",
			username,
		)
		fmt.Printf("  ✓ created OS user: %s\n", username)
	} else {
		fmt.Printf("  OS user '%s' already exists — skipping creation\n", username)
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
		update:  []string{"apt-get", "update", "-y"},
		install: []string{"apt-get", "install", "-y", "podman", "crun", "caddy"},
	},
	"dnf": {
		update:  []string{"dnf", "check-update"},
		install: []string{"dnf", "install", "-y", "podman", "crun", "caddy"},
	},
	"yum": {
		update:  nil,
		install: []string{"yum", "install", "-y", "podman", "crun"},
	},
	"pacman": {
		update:  []string{"pacman", "-Sy"},
		install: []string{"pacman", "-S", "--noconfirm", "podman", "crun", "caddy"},
	},
	"apk": {
		update:  []string{"apk", "update"},
		install: []string{"apk", "add", "--no-cache", "podman", "crun", "caddy"},
	},
}

const rqliteVer = "8.36.5"
const rqliteSystemdUnit = "/etc/systemd/system/rqlite.service"

// configureCrun sets up crun as the default Podman OCI runtime.
func configureCrun() {
	if _, err := exec.LookPath("crun"); err != nil {
		fmt.Println("  WARNING: crun not found — skipping Podman runtime config")
		return
	}
	confDir := "/etc/containers"
	confFile := filepath.Join(confDir, "containers.conf")
	mustMkdir(confDir)
	engineSection := "[engine]\nruntime = \"crun\"\n"
	if data, err := os.ReadFile(confFile); err == nil {
		s := string(data)
		if strings.Contains(s, "runtime") {
			fmt.Println("  crun: containers.conf already configures a runtime")
			return
		}
		writeFile(confFile, s+"\n"+engineSection, 0644)
	} else {
		writeFile(confFile, engineSection, 0644)
	}
	fmt.Println("  ✓ crun configured as Podman OCI runtime")
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
// and the node is ready to accept write requests.
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
ExecStart=/usr/local/bin/rqlited \\
  -node-id=main \\
  -http-addr=127.0.0.1:4001 \\
  -raft-addr=0.0.0.0:4002 \\
  -bootstrap-expect=1 \\
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
`

func writeCaddyfile(domain string) {
	tmpl := template.Must(template.New("caddy").Parse(caddyfileTmpl))
	var buf strings.Builder
	tmpl.Execute(&buf, struct{ Domain, Port string }{domain, backendPort})
	writeFile(caddyConf, buf.String(), 0644)
	// Ensure Caddy log dir exists
	os.MkdirAll("/var/log/caddy", 0755)
}

// ─── Systemd service ──────────────────────────────────────────────────────────

const systemdTmpl = `[Unit]
Description=FeatherDeploy Panel
Documentation=https://github.com/ojhapranjal26/FeatherDeploy
After=network.target rqlite.service
Requires=rqlite.service

[Service]
Type=simple
User={{.User}}
Group={{.User}}
EnvironmentFile={{.EnvFile}}
ExecStart={{.Bin}} serve
Restart=always
RestartSec=5s
StartLimitIntervalSec=120
StartLimitBurst=5
StandardOutput=journal
StandardError=journal

# Security hardening
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ReadWritePaths={{.DataDir}}

[Install]
WantedBy=multi-user.target
`

func writeSystemdService(svcUser string) {
	tmpl := template.Must(template.New("svc").Parse(systemdTmpl))
	var buf strings.Builder
	tmpl.Execute(&buf, struct{ User, EnvFile, Bin, DataDir string }{
		svcUser, envFile, binDest, dataDir,
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
