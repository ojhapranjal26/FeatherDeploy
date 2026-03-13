// Package deploy implements the real deployment pipeline for FeatherDeploy services.
//
// Flow per deployment:
//  1. Fetch service config from DB
//  2. (Optional) set up SSH key for private git repos
//  3. git clone --depth 1
//  4. Run build_command (if any)
//  5. Build container image with podman build (generates Dockerfile if absent)
//  6. Stop/remove existing container
//  7. podman run the new image
//  8. Mark deployment success / failure in DB
package deploy

import (
	"bytes"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	appCrypto "github.com/ojhapranjal26/featherdeploy/backend/internal/crypto"
)

// ─── SSH URL detection ────────────────────────────────────────────────────────

var sshURLRe = regexp.MustCompile(`^[A-Za-z0-9_]+@[A-Za-z0-9.\-]+:[A-Za-z0-9/_\-.]+`)

// IsSSHURL returns true for SCP-style git URLs (git@github.com:user/repo.git).
func IsSSHURL(u string) bool { return sshURLRe.MatchString(u) }

// ─── Log buffer ───────────────────────────────────────────────────────────────

type logBuf struct {
	mu    sync.Mutex
	lines []string
}

func (l *logBuf) add(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	l.mu.Lock()
	l.lines = append(l.lines, line)
	l.mu.Unlock()
	slog.Info("[deploy] " + line)
}

func (l *logBuf) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

// ─── Public entry point ───────────────────────────────────────────────────────

// Run executes a real deployment asynchronously. Call from a goroutine.
func Run(db *sql.DB, jwtSecret string, depID, svcID, userID int64) {
	log := &logBuf{}

	// Flush log buffer to DB every 2 s so the SSE log stream shows real-time
	// progress even while the deployment is still running.
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				db.Exec(`UPDATE deployments SET deploy_log=? WHERE id=?`, log.text(), depID) //nolint
			case <-done:
				return
			}
		}
	}()

	// ── 1. Fetch service config ───────────────────────────────────────────────
	var repoURL, repoBranch, framework, buildCmd, startCmd string
	var appPort int
	var hostPortNull sql.NullInt64
	err := db.QueryRow(
		`SELECT repo_url, repo_branch, framework, build_command, start_command,
		        app_port, host_port
		 FROM services WHERE id=?`, svcID,
	).Scan(&repoURL, &repoBranch, &framework, &buildCmd, &startCmd,
		&appPort, &hostPortNull)
	if err != nil {
		log.add("ERROR: could not load service config: %v", err)
		markFailed(db, depID, svcID, log.text())
		return
	}

	hostPort := int(hostPortNull.Int64)
	if hostPort <= 0 {
		// Auto-assign host port above 10000 range to avoid conflicts
		hostPort = 10000 + int(svcID)
	}
	if repoBranch == "" {
		repoBranch = "main"
	}

	// ── 2. SSH key setup for private / SSH repos ──────────────────────────────
	var sshKeyFile string // path to temp private key file, empty if not needed
	if IsSSHURL(repoURL) {
		kf, cleanup, keyErr := FetchSSHKey(db, jwtSecret, userID)
		if keyErr != nil {
			log.add("WARNING: SSH repo detected but no server-managed private key found for your user: %v", keyErr)
			log.add("  → Generate an SSH key in the dashboard (Settings → SSH Keys), copy the public key to GitHub, then retry.")
		} else {
			sshKeyFile = kf
			defer cleanup()
			log.add("[ssh] using key %s", kf)
		}
	}

	// ── 3. Clone ──────────────────────────────────────────────────────────────
	workDir, err := os.MkdirTemp("", fmt.Sprintf("fd-dep-%d-*", depID))
	if err != nil {
		log.add("ERROR: create work dir: %v", err)
		markFailed(db, depID, svcID, log.text())
		return
	}
	defer os.RemoveAll(workDir)

	log.add("[clone] git clone --depth 1 --branch %s %s", repoBranch, maskURL(repoURL))
	cloneErr := gitClone(workDir, sshKeyFile, repoURL, repoBranch, log)
	if cloneErr != nil {
		// Retry without explicit branch (use repo default)
		log.add("[clone] branch %q not found — retrying with default branch", repoBranch)
		os.RemoveAll(workDir)
		workDir2, _ := os.MkdirTemp("", fmt.Sprintf("fd-dep-%d-*", depID))
		workDir = workDir2
		if err2 := gitCloneDefault(workDir, sshKeyFile, repoURL, log); err2 != nil {
			log.add("ERROR: git clone failed: %v", err2)
			markFailed(db, depID, svcID, log.text())
			return
		}
	}

	// ── 4. Build command ──────────────────────────────────────────────────────
	if strings.TrimSpace(buildCmd) != "" {
		log.add("[build] %s", buildCmd)
		if err := runShell(workDir, sshKeyFile, buildCmd, log); err != nil {
			log.add("ERROR: build command failed: %v", err)
			markFailed(db, depID, svcID, log.text())
			return
		}
	}

	// ── 5. Ensure Dockerfile ──────────────────────────────────────────────────
	dockerfilePath := filepath.Join(workDir, "Dockerfile")
	if _, statErr := os.Stat(dockerfilePath); os.IsNotExist(statErr) {
		df := generateDockerfile(framework, buildCmd, startCmd, appPort)
		if writeErr := os.WriteFile(dockerfilePath, []byte(df), 0644); writeErr != nil {
			log.add("ERROR: write generated Dockerfile: %v", writeErr)
			markFailed(db, depID, svcID, log.text())
			return
		}
		log.add("[dockerfile] generated Dockerfile for framework=%q", framework)
	} else {
		log.add("[dockerfile] using Dockerfile from repository")
	}

	// ── 6. Podman build ───────────────────────────────────────────────────────
	imageName := fmt.Sprintf("featherdeploy/svc-%d:dep-%d", svcID, depID)
	log.add("[podman] building image %s", imageName)
	if err := runCapture(workDir, log, "podman", "build", "-t", imageName, "."); err != nil {
		log.add("ERROR: podman build failed: %v", err)
		markFailed(db, depID, svcID, log.text())
		return
	}

	// ── 7. Stop / remove existing container ──────────────────────────────────
	cName := fmt.Sprintf("fd-svc-%d", svcID)
	if containerExists(cName) {
		log.add("[podman] stopping existing container %s", cName)
		exec.Command("podman", "stop", "--time", "10", cName).Run() //nolint
		exec.Command("podman", "rm", "-f", cName).Run()             //nolint
	}

	// ── 8. Collect env vars ───────────────────────────────────────────────────
	envArgs := collectEnvArgs(db, svcID)

	// ── 9. Run new container ──────────────────────────────────────────────────
	runArgs := []string{
		"run", "-d",
		"--name", cName,
		"--restart", "unless-stopped",
		"-p", fmt.Sprintf("%d:%d", hostPort, appPort),
	}
	runArgs = append(runArgs, envArgs...)
	runArgs = append(runArgs, imageName)

	log.add("[podman] podman run -d --name %s -p %d:%d %s", cName, hostPort, appPort, imageName)
	out, err := exec.Command("podman", runArgs...).CombinedOutput()
	if err != nil {
		log.add("ERROR: podman run failed: %v\n%s", err, strings.TrimSpace(string(out)))
		markFailed(db, depID, svcID, log.text())
		return
	}
	newContainerID := strings.TrimSpace(string(out))

	// ── 10. Mark success ──────────────────────────────────────────────────────
	shortID := newContainerID
	if len(shortID) > 12 {
		shortID = shortID[:12]
	}
	log.add("[deploy] deployment succeeded! container=%s host_port=%d", shortID, hostPort)

	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	db.Exec(
		`UPDATE deployments SET status='success', finished_at=?, deploy_log=?, error_message='' WHERE id=?`,
		now, log.text(), depID)
	db.Exec(
		`UPDATE services SET status='running', container_id=?, host_port=?, updated_at=datetime('now') WHERE id=?`,
		newContainerID, hostPort, svcID)

	slog.Info("deployment succeeded", "dep_id", depID, "svc_id", svcID, "container", shortID)
}

// ─── Internal helpers ─────────────────────────────────────────────────────────

func markFailed(db *sql.DB, depID, svcID int64, logText string) {
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	db.Exec(
		`UPDATE deployments SET status='failed', finished_at=?, deploy_log=?, error_message=? WHERE id=?`,
		now, logText, lastLine(logText), depID)
	db.Exec(
		`UPDATE services SET status='error', updated_at=datetime('now') WHERE id=?`, svcID)
	slog.Error("deployment failed", "dep_id", depID, "svc_id", svcID)
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			return t
		}
	}
	return s
}

// FetchSSHKey finds the first server-managed private key for the user,
// writes it to a temp file, and returns the path + a cleanup func.
func FetchSSHKey(db *sql.DB, jwtSecret string, userID int64) (string, func(), error) {
	var encPriv string
	err := db.QueryRow(
		`SELECT encrypted_priv_key FROM ssh_keys
		 WHERE user_id=? AND encrypted_priv_key != '' ORDER BY id LIMIT 1`, userID,
	).Scan(&encPriv)
	if err == sql.ErrNoRows {
		return "", nil, fmt.Errorf("no private SSH key found — generate one in Settings → SSH Keys")
	}
	if err != nil {
		return "", nil, fmt.Errorf("query ssh_keys: %w", err)
	}

	privPEM, err := appCrypto.Decrypt(encPriv, jwtSecret)
	if err != nil {
		return "", nil, fmt.Errorf("decrypt SSH key: %w", err)
	}

	f, err := os.CreateTemp("", "fd-sshkey-*")
	if err != nil {
		return "", nil, fmt.Errorf("create temp key file: %w", err)
	}
	if err := os.Chmod(f.Name(), 0600); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", nil, err
	}
	if _, err := f.WriteString(privPEM); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", nil, err
	}
	f.Close()

	return f.Name(), func() { os.Remove(f.Name()) }, nil
}

// SSHGitEnv returns the GIT_SSH_COMMAND env var to use the given key file.
// StrictHostKeyChecking=no is acceptable here because we trust the server-side
// deployment environment and the alternative (known_hosts management) adds
// significant operational complexity for a self-hosted PaaS.
func SSHGitEnv(keyFile string) string {
	if keyFile == "" {
		return ""
	}
	return fmt.Sprintf(
		"GIT_SSH_COMMAND=ssh -i %s -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o BatchMode=yes",
		keyFile,
	)
}

func gitClone(workDir, sshKeyFile, repoURL, branch string, log *logBuf) error {
	return runCaptureWithSSH(workDir, sshKeyFile, log, "git", "clone", "--depth", "1", "--branch", branch, "--", repoURL, workDir)
}

func gitCloneDefault(workDir, sshKeyFile, repoURL string, log *logBuf) error {
	return runCaptureWithSSH(workDir, sshKeyFile, log, "git", "clone", "--depth", "1", "--", repoURL, workDir)
}

// runCapture runs a command, appending its combined output to log.
func runCapture(dir string, log *logBuf, name string, args ...string) error {
	return runCaptureWithSSH(dir, "", log, name, args...)
}

// runCaptureWithSSH is like runCapture but also sets GIT_SSH_COMMAND.
func runCaptureWithSSH(dir, sshKeyFile string, log *logBuf, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	env := os.Environ()
	if sshKeyFile != "" {
		env = append(env, SSHGitEnv(sshKeyFile))
	}
	cmd.Env = env
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	out := strings.TrimSpace(buf.String())
	if out != "" {
		for _, line := range strings.Split(out, "\n") {
			log.add("  %s", line)
		}
	}
	return err
}

// runShell runs a shell command string (like build_command) in dir.
func runShell(dir, sshKeyFile, command string, log *logBuf) error {
	return runCaptureWithSSH(dir, sshKeyFile, log, "/bin/sh", "-c", command)
}

func containerExists(name string) bool {
	out, err := exec.Command("podman", "ps", "-a", "--filter", "name=^"+name+"$", "--format", "{{.Names}}").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), name)
}

func collectEnvArgs(db *sql.DB, svcID int64) []string {
	rows, err := db.Query(
		`SELECT key, value FROM env_variables WHERE service_id=?`, svcID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var args []string
	for rows.Next() {
		var k, v string
		if rows.Scan(&k, &v) == nil {
			args = append(args, "-e", k+"="+v)
		}
	}
	return args
}

func maskURL(u string) string {
	// Hide credentials in URLs for log output
	if idx := strings.Index(u, "@"); idx > 0 && strings.Contains(u[:idx], "://") {
		// https://user:pass@host → https://***@host
		schemeEnd := strings.Index(u, "://") + 3
		return u[:schemeEnd] + "***@" + u[idx+1:]
	}
	return u
}

// generateDockerfile creates a simple Dockerfile for use when the repo
// doesn't include one. It is intentionally conservative (shell entrypoint,
// no multi-stage build) so it works for most language frameworks.
func generateDockerfile(framework, buildCmd, startCmd string, appPort int) string {
	baseImage := pickBaseImage(framework)
	if startCmd == "" {
		startCmd = "./app"
	}
	if appPort <= 0 {
		appPort = 8080
	}

	var sb strings.Builder
	sb.WriteString("FROM " + baseImage + "\n")
	sb.WriteString("WORKDIR /app\n")
	sb.WriteString("COPY . .\n")
	if buildCmd != "" {
		sb.WriteString("RUN " + buildCmd + "\n")
	}
	sb.WriteString(fmt.Sprintf("EXPOSE %d\n", appPort))
	sb.WriteString(`CMD ["/bin/sh", "-c", "` + strings.ReplaceAll(startCmd, `"`, `\"`) + `"]` + "\n")
	return sb.String()
}

func pickBaseImage(framework string) string {
	fw := strings.ToLower(framework)
	switch {
	case fw == "nextjs" || fw == "nuxt" || fw == "remix" || fw == "nodejs" || fw == "express" || fw == "fastify":
		return "node:20-alpine"
	case fw == "react" || fw == "vue" || fw == "svelte" || fw == "vite" || fw == "static":
		return "node:20-alpine"
	case fw == "django" || fw == "flask" || fw == "fastapi" || fw == "python":
		return "python:3.12-slim"
	case fw == "laravel" || fw == "symfony" || fw == "php":
		return "php:8.2-fpm-alpine"
	case fw == "rails" || fw == "ruby":
		return "ruby:3.3-alpine"
	case fw == "spring" || fw == "java":
		return "eclipse-temurin:21-jre-alpine"
	case fw == "go" || fw == "golang":
		return "golang:1.22-alpine"
	default:
		return "alpine:3.19"
	}
}
