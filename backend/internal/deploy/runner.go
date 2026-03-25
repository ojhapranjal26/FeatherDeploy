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
	stableImage := fmt.Sprintf("featherdeploy/svc-%d:stable", svcID)
	log.add("[podman] building image %s", imageName)
	buildErr := podmanBuild(workDir, log, imageName)
	var usingFallback bool
	if buildErr != nil {
		log.add("ERROR: podman build failed: %v", buildErr)
		// ── Fallback: use last stable image if available ──────────────────────
		var lastImage string
		db.QueryRow(`SELECT last_image FROM services WHERE id=?`, svcID).Scan(&lastImage) //nolint
		if lastImage != "" && podmanImageExists(lastImage) {
			log.add("[podman] falling back to last stable image: %s", lastImage)
			imageName = lastImage
			usingFallback = true
		} else {
			markFailed(db, depID, svcID, log.text())
			return
		}
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

	// Tag the freshly built image as the stable snapshot so it can be used as
	// a fallback if a future deployment build fails.
	if !usingFallback {
		if tagErr := exec.Command("podman", "tag", imageName, stableImage).Run(); tagErr != nil {
			log.add("[podman] warning: could not tag stable image: %v", tagErr)
		} else {
			log.add("[podman] saved %s as stable snapshot", stableImage)
			db.Exec(`UPDATE services SET last_image=? WHERE id=?`, stableImage, svcID) //nolint
		}
	}

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

	// Copy the current environment, stripping HOME and npm_config_cache so our
	// overrides below take effect (getenv returns the first match, so we must
	// remove the originals before appending replacements).
	raw := os.Environ()
	env := make([]string, 0, len(raw)+4)
	for _, e := range raw {
		if !strings.HasPrefix(e, "HOME=") && !strings.HasPrefix(e, "npm_config_cache=") {
			env = append(env, e)
		}
	}
	// Override HOME to /tmp so package managers (npm, pip, cargo, …) can write
	// their cache when the service user's home directory may be inaccessible.
	env = append(env, "HOME=/tmp", "npm_config_cache=/tmp/.fd-npm-cache")
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

// podmanBuild builds a container image with podman.
// It sets BUILDAH_ISOLATION=chroot to avoid user-namespace (newuidmap) errors
// that occur in restricted environments. If a namespace or storage error is
// detected in the output it automatically retries with --storage-driver=vfs.
func podmanBuild(dir string, log *logBuf, imageName string) error {
	out, err := podmanBuildAttempt(dir, imageName)
	for _, line := range strings.Split(out, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			log.add("  %s", t)
		}
	}
	if err == nil {
		return nil
	}
	// Detect namespace / user-mapping errors and retry with the slower but
	// always-available VFS storage driver.
	lower := strings.ToLower(out)
	if strings.Contains(lower, "newuidmap") || strings.Contains(lower, "uid_map") ||
		strings.Contains(lower, "operation not permitted") || strings.Contains(lower, "namespace") {
		log.add("[podman] namespace error detected — retrying with --storage-driver=vfs")
		out2, err2 := podmanBuildAttempt(dir, imageName, "--storage-driver=vfs")
		for _, line := range strings.Split(out2, "\n") {
			if t := strings.TrimSpace(line); t != "" {
				log.add("  %s", t)
			}
		}
		return err2
	}
	return err
}

// podmanBuildAttempt runs `podman build` with BUILDAH_ISOLATION=chroot and
// any extra arguments before the context path. It returns (combined output, error).
func podmanBuildAttempt(dir, imageName string, extraArgs ...string) (string, error) {
	args := append([]string{"build", "-t", imageName}, extraArgs...)
	args = append(args, ".")
	cmd := exec.Command("podman", args...)
	cmd.Dir = dir
	// Inherit environment, replacing BUILDAH_ISOLATION to skip user-namespace setup.
	raw := os.Environ()
	env := make([]string, 0, len(raw)+1)
	for _, e := range raw {
		if !strings.HasPrefix(e, "BUILDAH_ISOLATION=") {
			env = append(env, e)
		}
	}
	cmd.Env = append(env, "BUILDAH_ISOLATION=chroot")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return strings.TrimSpace(buf.String()), err
}

// podmanImageExists returns true if the named image exists in podman's local store.
func podmanImageExists(image string) bool {
	return exec.Command("podman", "image", "exists", image).Run() == nil
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
