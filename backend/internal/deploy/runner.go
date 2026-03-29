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
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
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
	"sync"
	"time"

	appCrypto "github.com/ojhapranjal26/featherdeploy/backend/internal/crypto"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/caddy"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/detect"
	"github.com/golang-jwt/jwt/v5"
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

// ─── Deployment queue ─────────────────────────────────────────────────────────
// A fixed-size worker pool limits concurrent deployments based on available CPU
// cores, preventing the server from being overwhelmed when multiple users trigger
// deployments simultaneously.

type deployJob struct {
	db        *sql.DB
	jwtSecret string
	depID     int64
	svcID     int64
	userID    int64
}

var (
	queueOnce sync.Once
	jobCh     chan deployJob
)

// networkMu serializes all per-project network operations (create / rm / inspect)
// across concurrent deployments and the startup reconcile goroutine.
// Using a per-project striped lock avoids blocking unrelated projects.
var (
	networkMuMap sync.Map // key: int64 projectID → *sync.Mutex
)

func projectNetworkLock(projectID int64) *sync.Mutex {
	v, _ := networkMuMap.LoadOrStore(projectID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// buildTmpDir returns the base directory for deployment work dirs.
// We deliberately avoid /tmp because the systemd unit previously used
// PrivateTmp=yes, and rootless podman re-execs itself into a new user
// namespace where the private /tmp bind-mount is not inherited — making
// any /tmp path invisible to the podman build worker.  Using a subdir of
// HOME (the service data dir) is always accessible from within podman.
func buildTmpDir() string {
	home := os.Getenv("HOME")
	if home == "" {
		home = "/var/lib/featherdeploy"
	}
	dir := filepath.Join(home, "tmp")
	os.MkdirAll(dir, 0755) //nolint
	return dir
}

// InitQueue starts the deployment worker pool. concurrency defaults to
// runtime.NumCPU() when <= 0. Call once at server startup.
func InitQueue(concurrency int) {
	queueOnce.Do(func() {
		if concurrency <= 0 {
			concurrency = runtime.NumCPU()
		}
		if concurrency < 1 {
			concurrency = 1
		}
		// Buffer 512 pending jobs; beyond that Enqueue fails the deployment immediately
		jobCh = make(chan deployJob, 512)
		for i := 0; i < concurrency; i++ {
			go deployWorker()
		}
		slog.Info("deployment queue started", "workers", concurrency)
	})
}

func deployWorker() {
	for job := range jobCh {
		// Transition: pending → running
		job.db.Exec( //nolint
			`UPDATE deployments SET status='running', started_at=datetime('now') WHERE id=?`, job.depID)
		job.db.Exec( //nolint
			`UPDATE services SET status='deploying', updated_at=datetime('now') WHERE id=?`, job.svcID)
		Run(job.db, job.jwtSecret, job.depID, job.svcID, job.userID)
	}
}

// Enqueue queues a deployment for execution. The deployment must already exist in
// the DB with status='pending'. A worker transitions it to 'running' when it is
// picked up. If the queue channel is full, the deployment is failed immediately.
func Enqueue(db *sql.DB, jwtSecret string, depID, svcID, userID int64) {
	if jobCh == nil {
		// Fallback: queue not initialised — run directly in a goroutine
		go func() {
			db.Exec(`UPDATE deployments SET status='running', started_at=datetime('now') WHERE id=?`, depID) //nolint
			db.Exec(`UPDATE services SET status='deploying', updated_at=datetime('now') WHERE id=?`, svcID) //nolint
			Run(db, jwtSecret, depID, svcID, userID)
		}()
		return
	}
	select {
	case jobCh <- deployJob{db: db, jwtSecret: jwtSecret, depID: depID, svcID: svcID, userID: userID}:
		// successfully queued
	default:
		// Queue full — fail the deployment so the user gets immediate feedback
		db.Exec( //nolint
			`UPDATE deployments SET status='failed',
			  error_message='deployment queue is full — please wait for current deployments to complete',
			  finished_at=datetime('now') WHERE id=?`, depID)
		slog.Warn("deployment queue full", "dep_id", depID)
	}
}

// ─── Public entry point ───────────────────────────────────────────────────────

// Run executes a real deployment asynchronously. Call from a goroutine.
func Run(db *sql.DB, jwtSecret string, depID, svcID, userID int64) {
	log := &logBuf{}

	// Flush log buffer to DB every 500 ms so the SSE log stream shows real-time
	// progress even while the deployment is still running.
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
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
	var repoURL, repoBranch, repoFolder, framework, buildCmd, startCmd, svcName string
	var projectID int64
	var appPort int
	var hostPortNull sql.NullInt64
	err := db.QueryRow(
		`SELECT project_id, name, repo_url, repo_branch, repo_folder, framework, build_command, start_command,
		        app_port, host_port
		 FROM services WHERE id=?`, svcID,
	).Scan(&projectID, &svcName, &repoURL, &repoBranch, &repoFolder, &framework, &buildCmd, &startCmd,
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

	// ── 1b. Fetch deployment-level deploy_type and artifact_path ─────────────
	var deployType, artifactPath string
	db.QueryRow(`SELECT deploy_type, artifact_path FROM deployments WHERE id=?`, depID). //nolint
		Scan(&deployType, &artifactPath)
	isArtifact := deployType == "artifact" && artifactPath != ""

	// ── 2. Source: either git clone or artifact extraction ───────────────────
	workDir, err := os.MkdirTemp(buildTmpDir(), fmt.Sprintf("fd-dep-%d-*", depID))
	if err != nil {
		log.add("ERROR: create work dir: %v", err)
		markFailed(db, depID, svcID, log.text())
		return
	}
	defer os.RemoveAll(workDir)

	var sshKeyFile string // used only for git path

	if isArtifact {
		log.add("[artifact] extracting %s", filepath.Base(artifactPath))
		if extractErr := extractArtifact(artifactPath, workDir, log); extractErr != nil {
			log.add("ERROR: artifact extraction failed: %v", extractErr)
			markFailed(db, depID, svcID, log.text())
			return
		}
		log.add("[artifact] extracted successfully")
	} else {
		// ── 2a. Inject GitHub App installation token for HTTPS GitHub repos ──────
		// First try the GitHub App installation token (server-wide, preferred).
		// If the App is not configured or the URL is unchanged, fall back to the
		// deploying user's personal GitHub OAuth token.
		repoURL = injectGitHubAppToken(context.Background(), db, repoURL, log)
		if strings.HasPrefix(repoURL, "https://github.com/") {
			// App token not injected — try user OAuth token
			repoURL = injectUserOAuthToken(context.Background(), db, userID, repoURL, log)
		}

		// ── 2b. SSH key setup for private / SSH repos ─────────────────────────
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

		// ── 2c. Clone ────────────────────────────────────────────────────────────
		log.add("[clone] git clone --depth 1 --branch %s %s", repoBranch, maskURL(repoURL))
		cloneErr := gitClone(workDir, sshKeyFile, repoURL, repoBranch, log)
		if cloneErr != nil {
			// Retry without explicit branch (use repo default)
			log.add("[clone] branch %q not found — retrying with default branch", repoBranch)
			os.RemoveAll(workDir)
			workDir2, _ := os.MkdirTemp(buildTmpDir(), fmt.Sprintf("fd-dep-%d-*", depID))
			workDir = workDir2
			if err2 := gitCloneDefault(workDir, sshKeyFile, repoURL, log); err2 != nil {
				log.add("ERROR: git clone failed: %v", err2)
				markFailed(db, depID, svcID, log.text())
				return
			}
		}

		// Capture the actual commit SHA and update the deployment record
		if sha, err := gitRevParse(workDir); err == nil && sha != "" {
			db.Exec(`UPDATE deployments SET commit_sha=? WHERE id=?`, sha, depID) //nolint
			log.add("[clone] commit %s", sha)
		}

		// ── 2d. Apply repo_folder (monorepo / subdirectory deployments) ──────────
		if strings.TrimSpace(repoFolder) != "" {
			subDir := filepath.Join(workDir, filepath.Clean(repoFolder))
			// Security: ensure the resolved path is still inside workDir
			if !strings.HasPrefix(subDir, workDir) {
				log.add("ERROR: repo_folder %q escapes the repo root — aborting", repoFolder)
				markFailed(db, depID, svcID, log.text())
				return
			}
			if _, statErr := os.Stat(subDir); os.IsNotExist(statErr) {
				log.add("ERROR: folder %q does not exist in the repository", repoFolder)
				markFailed(db, depID, svcID, log.text())
				return
			}
			log.add("[clone] deploying from subfolder: %s", repoFolder)
			workDir = subDir
		}
	}

	// ── 4. Detect Dockerfile presence and fill config gaps ───────────────────
	dockerfilePath := filepath.Join(workDir, "Dockerfile")
	repoHasDockerfile := true
	if _, statErr := os.Stat(dockerfilePath); os.IsNotExist(statErr) {
		repoHasDockerfile = false
	}

	// When the repo has no Dockerfile, run the static analyser so any fields
	// the user left blank (framework, build_command, start_command, app_port)
	// are filled with sensible defaults derived from the actual repo content.
	if !repoHasDockerfile {
		detected := detect.Detect(workDir)
		if framework == "" && detected.Framework != "" && detected.Framework != "unknown" {
			framework = detected.Framework
			log.add("[detect] auto-detected framework: %s", framework)
		}
		if strings.TrimSpace(buildCmd) == "" && detected.BuildCommand != "" {
			buildCmd = detected.BuildCommand
		}
		if strings.TrimSpace(startCmd) == "" && detected.StartCommand != "" {
			startCmd = detected.StartCommand
		}
		if appPort <= 0 && detected.AppPort > 0 {
			appPort = detected.AppPort
		}
	}

	// ── 5. Host build step (only when repo ships its own Dockerfile) ──────────
	// Package-manager commands (pip install, npm install, cargo build, …) must
	// execute inside the correct runtime image, not on the host server which
	// may not have those runtimes installed. When we auto-generate the
	// Dockerfile the build_command is embedded as a Dockerfile RUN instruction
	// so it runs inside the base image. Only run on the host when the repo
	// ships its own Dockerfile, where the user may rely on host-built artefacts.
	if strings.TrimSpace(buildCmd) != "" && repoHasDockerfile {
		log.add("[build] %s", buildCmd)
		if err := runShell(workDir, sshKeyFile, buildCmd, log); err != nil {
			log.add("ERROR: build command failed: %v", err)
			markFailed(db, depID, svcID, log.text())
			return
		}
	}

	// ── 6. Ensure Dockerfile ──────────────────────────────────────────────────
	if !repoHasDockerfile {
		df := generateDockerfile(workDir, framework, buildCmd, startCmd, appPort)
		if writeErr := os.WriteFile(dockerfilePath, []byte(df), 0644); writeErr != nil {
			log.add("ERROR: write generated Dockerfile: %v", writeErr)
			markFailed(db, depID, svcID, log.text())
			return
		}
		multiStage := strings.Contains(df, "AS builder")
		if multiStage {
			log.add("[dockerfile] generated multi-stage Dockerfile for framework=%q (builder → slim runtime)", framework)
		} else {
			log.add("[dockerfile] generated Dockerfile for framework=%q build=%q", framework, buildCmd)
		}
	} else {
		log.add("[dockerfile] using Dockerfile from repository")
	}

	// ── 6. Inject env vars as .env for build-time access (Next.js, CRA, etc.) ──
	// We write the service's env vars to .env / .env.local before podman build.
	// This allows frameworks that read .env at build time (Next.js, Vite, etc.)
	// to access DATABASE_URL and other vars during `npm run build` / `tsc`.
	// The .env file is included via COPY . . in the Dockerfile.
	if writeErr := writeEnvFileForBuild(db, svcID, projectID, jwtSecret, workDir); writeErr != nil {
		log.add("[env] warning: could not write .env for build: %v", writeErr)
	} else {
		log.add("[env] injected env vars into build context")
	}

	// ── 6b. Podman build ──────────────────────────────────────────────────────
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
		log.add("[podman] stopping container %s (grace=10s, hard limit=20s)", cName)
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 20*time.Second)
		podmanCmdCtx(stopCtx, "stop", "--time", "10", cName).Run() //nolint
		stopCancel()
		podmanCmd("rm", "-f", cName).Run() //nolint
		log.add("[podman] container %s removed", cName)
	}

	// ── 8. Collect env vars ───────────────────────────────────────────────────
	envArgs := collectEnvArgs(db, svcID, jwtSecret)
	// Auto-inject connection URLs for running databases in the same project.
	envArgs = append(envArgs, collectProjectDBEnvArgs(db, projectID, jwtSecret)...)
	mountArgs := collectProjectDBMountArgs(db, projectID)

	// ── 8b. Ensure the per-project network exists ─────────────────────────────
	// Microservices in the same project communicate over a dedicated project
	// network using their service/database aliases. This avoids shared-pod port
	// conflicts when multiple containers inside one project listen on 3000/8080.
	networkName := projectNetworkName(projectID)
	if netErr := ensureProjectNetwork(projectID); netErr != nil {
		log.add("ERROR: could not create project network %s: %v", networkName, netErr)
		markFailed(db, depID, svcID, log.text())
		return
	}
	log.add("[network] project network ready: %s", networkName)

	// ── 9. Run new container ──────────────────────────────────────────────────
	runArgs := []string{
		"run", "-d",
		"--name", cName,
		"--restart", "unless-stopped",
		"-p", fmt.Sprintf("%d:%d", hostPort, appPort),
		// Rotate container logs: max 10 MB per file, keep 3 files
		"--log-opt", "max-size=10m",
		"--log-opt", "max-file=3",
	}
	if networkName != "" {
		// Join the project network; the service is reachable by its name alias
		// from other containers in the same project.
		runArgs = append(runArgs, "--network", networkName, "--network-alias", svcName)
	}
	runArgs = append(runArgs, mountArgs...)
	runArgs = append(runArgs, envArgs...)
	runArgs = append(runArgs, imageName)

	log.add("[podman] podman run -d --name %s -p %d:%d --network %s %s", cName, hostPort, appPort, networkName, imageName)
	out, err := podmanCmd(runArgs...).CombinedOutput()
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

	// Update Caddy so the service is reachable via its registered domains.
	go caddy.Reload(db)

	// Tag the freshly built image as the stable snapshot so it can be used as
	// a fallback if a future deployment build fails.
	if !usingFallback {
		if tagErr := podmanCmd("tag", imageName, stableImage).Run(); tagErr != nil {
			log.add("[podman] warning: could not tag stable image: %v", tagErr)
		} else {
			log.add("[podman] saved %s as stable snapshot", stableImage)
			db.Exec(`UPDATE services SET last_image=? WHERE id=?`, stableImage, svcID) //nolint
		}
		// Prune old deployment-tagged images for this service (dep-N tags that
		// are no longer needed). Keep only the current dep-N and :stable.
		// This reclaims disk space after every successful deployment.
		go func() {
			pruneOldDepImages(svcID, depID)
			// Prune only dangling (untagged) images left over from multi-stage
			// builds. We intentionally do NOT use -a so that base images like
			// node:20-alpine and python:3.12-slim remain cached for the next
			// deployment; they would otherwise be re-pulled every build.
			podmanCmd("image", "prune", "-f").Run() //nolint
		}()
	}

	go trimOldDeployLogs(db, svcID)
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
	// Prune dangling (untagged) images left behind by the failed build.
	// Use -f only (not -a) so cached base images are not evicted.
	go podmanCmd("image", "prune", "-f").Run() //nolint
	go trimOldDeployLogs(db, svcID)
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

// gitRevParse returns the full commit SHA (HEAD) of the cloned repo.
func gitRevParse(workDir string) (string, error) {
	out, err := exec.Command("git", "-C", workDir, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ─── GitHub App installation token ───────────────────────────────────────────
// injectGitHubAppToken rewrites an https://github.com/... URL to include an
// installation access token so git clone can authenticate to private repos.
// Returns the original URL unchanged if GitHub App is not configured or if
// the token exchange fails.
func injectGitHubAppToken(ctx context.Context, db *sql.DB, repoURL string, log *logBuf) string {
	if !strings.HasPrefix(repoURL, "https://github.com/") {
		return repoURL // SSH or non-GitHub URL — skip
	}
	var appID, pkPEM, installID string
	err := db.QueryRowContext(ctx,
		`SELECT app_id, private_key_pem, installation_id FROM github_app_config WHERE id = 1`,
	).Scan(&appID, &pkPEM, &installID)
	if err != nil {
		return repoURL // GitHub App not configured — try unauthenticated
	}
	if appID == "" || pkPEM == "" || installID == "" {
		return repoURL
	}
	token, err := githubAppInstallToken(ctx, appID, pkPEM, installID)
	if err != nil {
		log.add("[clone] WARNING: GitHub App token unavailable (%v) — trying unauthenticated clone", err)
		return repoURL
	}
	// https://github.com/owner/repo.git → https://x-access-token:<token>@github.com/owner/repo.git
	rest := strings.TrimPrefix(repoURL, "https://github.com/")
	authedURL := "https://x-access-token:" + token + "@github.com/" + rest
	log.add("[clone] using GitHub App installation token for authentication")
	return authedURL
}

// InjectGitHubAppToken is the exported version of injectGitHubAppToken for use
// from other packages (e.g. the detect handler). It rewrites an
// https://github.com/... URL to carry an installation access token.
// Returns the original URL unchanged on any error.
func InjectGitHubAppToken(ctx context.Context, db *sql.DB, repoURL string) string {
	return injectGitHubAppToken(ctx, db, repoURL, &logBuf{})
}

// injectUserOAuthToken rewrites an https://github.com/... URL to include the
// deploying user's stored GitHub OAuth token. This is the fallback when no
// GitHub App installation token is available.
// Returns the original URL unchanged if the user has no token stored.
func injectUserOAuthToken(ctx context.Context, db *sql.DB, userID int64, repoURL string, log *logBuf) string {
	if !strings.HasPrefix(repoURL, "https://github.com/") {
		return repoURL
	}
	var token string
	if err := db.QueryRowContext(ctx,
		`SELECT github_access_token FROM users WHERE id=?`, userID,
	).Scan(&token); err != nil || token == "" {
		// No OAuth token — try unauthenticated (public repos only)
		return repoURL
	}
	// https://github.com/owner/repo.git → https://oauth2:<token>@github.com/owner/repo.git
	rest := strings.TrimPrefix(repoURL, "https://github.com/")
	log.add("[clone] using GitHub OAuth token for authentication")
	return "https://oauth2:" + token + "@github.com/" + rest
}

// githubAppInstallToken exchanges the App JWT for a short-lived installation token.
func githubAppInstallToken(ctx context.Context, appID, privateKeyPEM, installID string) (string, error) {
	privKey, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(privateKeyPEM))
	if err != nil {
		return "", fmt.Errorf("invalid RSA private key: %w", err)
	}
	now := time.Now()
	appTok, err := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iat": now.Add(-60 * time.Second).Unix(), // 60s backdate for clock skew
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": appID,
	}).SignedString(privKey)
	if err != nil {
		return "", fmt.Errorf("sign app JWT: %w", err)
	}
	apiURL := fmt.Sprintf("https://api.github.com/app/installations/%s/access_tokens", installID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+appTok)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("GitHub returned %d: %s", resp.StatusCode, body)
	}
	var result struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &result); err != nil || result.Token == "" {
		return "", fmt.Errorf("unexpected GitHub response: %s", body)
	}
	return result.Token, nil
}

func containerExists(name string) bool {
	out, err := podmanCmd("ps", "-a", "--filter", "name=^"+name+"$", "--format", "{{.Names}}").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), name)
}

func collectEnvArgs(db *sql.DB, svcID int64, jwtSecret string) []string {
	rows, err := db.Query(
		`SELECT key, value, is_secret FROM env_variables WHERE service_id=?`, svcID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var args []string
	for rows.Next() {
		var k, v string
		var isSecret int
		if rows.Scan(&k, &v, &isSecret) == nil {
			// Decrypt secrets before injecting into container environment
			if isSecret == 1 && strings.HasPrefix(v, "fdenc:") {
				plain, err := appCrypto.Decrypt(v[len("fdenc:"):], jwtSecret)
				if err == nil {
					v = plain
				}
			}
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

// generateDockerfile creates a Dockerfile for repos that don't include one.
// It handles Python (pip/poetry/pipenv) and Node (npm/yarn/pnpm) specifically
// so the right runtime image is used and dependencies are installed correctly
// inside the container rather than on the host server.
func generateDockerfile(workDir, framework, buildCmd, startCmd string, appPort int) string {
	fw := strings.ToLower(strings.TrimSpace(framework))
	baseImage := pickBaseImage(fw)
	if appPort <= 0 {
		appPort = 8080
	}
	if startCmd == "" {
		startCmd = "./app"
	}

	isPython := fw == "python" || fw == "django" || fw == "flask" || fw == "fastapi" ||
		fw == "tornado" || fw == "aiohttp" || fw == "starlette" || fw == "sanic" || fw == "bottle"
	isNode := fw == "nodejs" || fw == "nextjs" || fw == "nuxt" || fw == "remix" ||
		fw == "express" || fw == "fastify" || fw == "react" || fw == "vue" ||
		fw == "svelte" || fw == "vite" || fw == "static" || fw == "angular" ||
		fw == "nestjs" || fw == "koa" || fw == "hapi" || fw == "gatsby"

	var sb strings.Builder

	switch {
	// ── Node: multi-stage ────────────────────────────────────────────────────
	// Stage 1 (builder): full node image — install deps, run build.
	// Stage 2 (runner): slim node-alpine — copy only what's needed to run.
	// This cuts typical Next.js / React images from 1–2 GB down to ~150–250 MB.
	case isNode:
		sb.WriteString("FROM " + baseImage + " AS builder\n")
		sb.WriteString("WORKDIR /app\n")
		sb.WriteString("COPY . .\n")
		if strings.TrimSpace(buildCmd) != "" && looksLikeNpmInstall(buildCmd) {
			sb.WriteString("RUN " + buildCmd + "\n")
		} else {
			sb.WriteString("RUN npm install\n")
			if strings.TrimSpace(buildCmd) != "" {
				sb.WriteString("RUN " + buildCmd + "\n")
			}
		}
		// Remove devDependencies after build to shrink node_modules before copy
		sb.WriteString("RUN npm prune --production 2>/dev/null || true\n")

		// Final stage: copy the entire /app from the pruned builder.
		// A single COPY is safe for every Node output layout:
		//   .next       → Next.js
		//   dist        → Vite / Angular / NestJS / most CJS TypeScript compilers
		//   build       → Create React App / Remix
		//   public      → Gatsby static output
		//   (in-place)  → plain Express/Fastify/Koa — no build output dir
		// Selective per-directory copies are avoided because Dockerfile COPY
		// fails with "no such file or directory" when the source path doesn't
		// exist in the builder stage (e.g. /app/dist on a Next.js build).
		sb.WriteString("\nFROM " + baseImage + "\n")
		sb.WriteString("WORKDIR /app\n")
		sb.WriteString("COPY --from=builder /app .\n")
		sb.WriteString(fmt.Sprintf("EXPOSE %d\n", appPort))
		sb.WriteString(`CMD ["/bin/sh", "-c", "` + strings.ReplaceAll(startCmd, `"`, `\"`) + `"]` + "\n")

	// ── Python: multi-stage ──────────────────────────────────────────────────
	// Stage 1 (builder): full python image with gcc/build-tools — install deps
	//   into a venv so they can be cleanly copied.
	// Stage 2 (runner): slim python image — copy only the venv + app code.
	case isPython:
		sb.WriteString("FROM " + baseImage + " AS builder\n")
		sb.WriteString("WORKDIR /app\n")
		if aptPkgs := detectPythonAptDeps(workDir); len(aptPkgs) > 0 {
			sb.WriteString("RUN apt-get update && apt-get install -y --no-install-recommends " +
				strings.Join(aptPkgs, " ") +
				" && rm -rf /var/lib/apt/lists/*\n")
		}
		// Always create an isolated venv for clean copying
		sb.WriteString("RUN python -m venv /venv\n")
		sb.WriteString("ENV PATH=\"/venv/bin:$PATH\"\n")
		sb.WriteString("COPY . .\n")
		pipInstallCmd := buildCmd
		remainingBuild := ""
		if !looksLikePipInstall(buildCmd) {
			pipInstallCmd = "pip install --no-cache-dir -r requirements.txt"
			remainingBuild = buildCmd
		}
		sb.WriteString("RUN " + pipInstallCmd + "\n")
		if strings.TrimSpace(remainingBuild) != "" {
			sb.WriteString("RUN " + remainingBuild + "\n")
		}
		if extra := pythonServerPipInstall(workDir, startCmd); extra != "" {
			sb.WriteString("RUN " + extra + "\n")
		}

		// Final stage: no compiler or build tools — just the venv and app source
		sb.WriteString("\nFROM " + baseImage + "\n")
		sb.WriteString("WORKDIR /app\n")
		sb.WriteString("COPY --from=builder /venv /venv\n")
		sb.WriteString("COPY --from=builder /app .\n")
		sb.WriteString("ENV PATH=\"/venv/bin:$PATH\"\n")
		sb.WriteString(fmt.Sprintf("EXPOSE %d\n", appPort))
		sb.WriteString(`CMD ["/bin/sh", "-c", "` + strings.ReplaceAll(startCmd, `"`, `\"`) + `"]` + "\n")

	// ── Default: single-stage (Go, Ruby, PHP, etc.) ──────────────────────────
	default:
		sb.WriteString("FROM " + baseImage + "\n")
		sb.WriteString("WORKDIR /app\n")
		sb.WriteString("COPY . .\n")
		if strings.TrimSpace(buildCmd) != "" {
			sb.WriteString("RUN " + buildCmd + "\n")
		}
		sb.WriteString(fmt.Sprintf("EXPOSE %d\n", appPort))
		sb.WriteString(`CMD ["/bin/sh", "-c", "` + strings.ReplaceAll(startCmd, `"`, `\"`) + `"]` + "\n")
	}

	return sb.String()
}

// detectPythonAptDeps scans the repo's dependency files and returns the
// apt packages required to build any C-extension wheels found there.
// python:3.12-slim ships no compiler, pkg-config, or dev headers.
func detectPythonAptDeps(workDir string) []string {
	var content string
	for _, f := range []string{"requirements.txt", "requirements-dev.txt", "Pipfile", "pyproject.toml", "setup.py", "setup.cfg"} {
		if data, err := os.ReadFile(filepath.Join(workDir, f)); err == nil {
			content += strings.ToLower(string(data)) + "\n"
		}
	}
	if content == "" {
		return nil
	}

	var pkgs []string
	seen := map[string]bool{}
	add := func(p ...string) {
		for _, pkg := range p {
			if !seen[pkg] {
				seen[pkg] = true
				pkgs = append(pkgs, pkg)
			}
		}
	}

	needsBuildTools := false

	// MySQL client (mysqlclient, flask-mysqldb, mysql-connector-python)
	if strings.Contains(content, "mysqlclient") || strings.Contains(content, "flask-mysqldb") ||
		strings.Contains(content, "mysql-connector") || strings.Contains(content, "pymysql") {
		add("pkg-config", "default-libmysqlclient-dev")
		needsBuildTools = true
	}

	// PostgreSQL (psycopg2 source build; psycopg2-binary is a pre-built wheel)
	if strings.Contains(content, "psycopg2") && !strings.Contains(content, "psycopg2-binary") {
		add("libpq-dev")
		needsBuildTools = true
	}

	// XML / HTML parsing
	if strings.Contains(content, "lxml") {
		add("libxml2-dev", "libxslt1-dev")
		needsBuildTools = true
	}

	// Cryptography, CFFI, PyOpenSSL
	if strings.Contains(content, "cryptography") || strings.Contains(content, "pyopenssl") ||
		strings.Contains(content, "cffi") {
		add("libssl-dev", "libffi-dev")
		needsBuildTools = true
	}

	// Image processing
	if strings.Contains(content, "pillow") || strings.Contains(content, "\"pil\"") {
		add("libjpeg-dev", "zlib1g-dev")
		needsBuildTools = true
	}

	// uWSGI
	if strings.Contains(content, "uwsgi") {
		needsBuildTools = true
	}

	// Numpy / scipy with non-wheel source builds (rare but possible)
	if strings.Contains(content, "scipy") {
		add("gfortran", "libopenblas-dev")
		needsBuildTools = true
	}

	// Any C extension that wasn't already covered needs gcc at minimum
	if needsBuildTools {
		// Prepend gcc so it appears first in the apt install list
		final := []string{"gcc"}
		for _, p := range pkgs {
			if p != "gcc" {
				final = append(final, p)
			}
		}
		return final
	}
	return nil
}

// pythonServerPipInstall returns a "pip install ..." command string for the
// WSGI/ASGI server referenced in startCmd when that server is not already
// listed in the project's dependency files. Returns "" when nothing is needed.
func pythonServerPipInstall(workDir, startCmd string) string {
	// Read combined content of all dependency declaration files
	var content string
	for _, f := range []string{"requirements.txt", "requirements-dev.txt", "Pipfile", "pyproject.toml", "setup.py", "setup.cfg"} {
		if data, err := os.ReadFile(filepath.Join(workDir, f)); err == nil {
			content += strings.ToLower(string(data)) + "\n"
		}
	}
	cmd := strings.ToLower(startCmd)
	switch {
	case strings.Contains(cmd, "gunicorn") && !strings.Contains(content, "gunicorn"):
		return "pip install --no-cache-dir gunicorn"
	case strings.Contains(cmd, "uvicorn") && !strings.Contains(content, "uvicorn"):
		return "pip install --no-cache-dir 'uvicorn[standard]'"
	case strings.Contains(cmd, "hypercorn") && !strings.Contains(content, "hypercorn"):
		return "pip install --no-cache-dir hypercorn"
	case strings.Contains(cmd, "daphne") && !strings.Contains(content, "daphne"):
		return "pip install --no-cache-dir daphne"
	case strings.Contains(cmd, "waitress") && !strings.Contains(content, "waitress"):
		return "pip install --no-cache-dir waitress"
	}
	return ""
}

// looksLikePipInstall reports whether cmd is a Python dependency-install
// invocation (pip, pip3, python -m pip, poetry install, pipenv install).
func looksLikePipInstall(cmd string) bool {
	c := strings.ToLower(strings.TrimSpace(cmd))
	return strings.HasPrefix(c, "pip ") || strings.HasPrefix(c, "pip3 ") ||
		strings.Contains(c, "python -m pip ") ||
		strings.HasPrefix(c, "poetry install") ||
		strings.HasPrefix(c, "pipenv install")
}

// looksLikeNpmInstall reports whether cmd begins with a Node dependency-
// install invocation (npm install/ci, yarn install, pnpm install).
func looksLikeNpmInstall(cmd string) bool {
	c := strings.ToLower(strings.TrimSpace(cmd))
	return strings.HasPrefix(c, "npm install") || strings.HasPrefix(c, "npm ci") ||
		strings.HasPrefix(c, "yarn install") || strings.HasPrefix(c, "pnpm install")
}

// ─── Per-project network helpers ─────────────────────────────────────────────

func projectPodName(projectID int64) string {
	return fmt.Sprintf("fd-pod-%d", projectID)
}

func projectNetworkName(projectID int64) string {
	return fmt.Sprintf("fd-proj-%d", projectID)
}

// PodmanCmd returns an exec.Cmd for podman with the correct rootless
// environment. Exported for use by the startup reconcile goroutine in main.go
// so it reads/writes the same podman store as the deployment pipeline.
func PodmanCmd(args ...string) *exec.Cmd {
	return podmanCmd(args...)
}

// CheckNetworkingBackend verifies that Podman named-network support is
// functional.  It creates a temporary test network, confirms it is visible,
// then removes it.  If anything fails it returns a descriptive error that
// includes remediation instructions for the most common distributions.
//
// Called once at server startup (non-fatal: only a warning is emitted).
func CheckNetworkingBackend() error {
	testNet := fmt.Sprintf("fd-netcheck-%d", time.Now().UnixNano())

	// Create
	out, err := podmanCmd("network", "create", testNet).CombinedOutput()
	if err != nil {
		outStr := strings.TrimSpace(string(out))
		return fmt.Errorf("podman network create failed (%v): %s\n\n%s",
			err, outStr, classifyNetworkError(outStr))
	}

	// Inspect — verify it actually persisted
	if verErr := podmanCmd("network", "inspect", testNet).Run(); verErr != nil {
		podmanCmd("network", "rm", testNet).Run() //nolint
		return fmt.Errorf(
			"podman network create appeared to succeed but inspect failed (%v)\n"+
				"This usually means the networking backend (netavark) is missing.\n"+
				"  RHEL 9 / AlmaLinux / Rocky: sudo dnf install -y netavark aardvark-dns\n"+
				"  Ubuntu / Debian:             sudo apt-get install -y netavark",
			verErr)
	}

	// Run a tiny container on the network.  create/inspect only validate the
	// network JSON, but 'podman run --network' is the operation that exercises
	// rootless netns setup, cgroup-manager interaction and port-forward helper
	// startup.  This catches the exact class of failures that previously slipped
	// past startup checks and only showed up during deployments.
	runOut, runErr := podmanCmd("run", "--rm", "--network", testNet, "docker.io/library/alpine", "true").CombinedOutput()
	if runErr != nil {
		podmanCmd("network", "rm", testNet).Run() //nolint
		outStr := strings.TrimSpace(string(runOut))
		return fmt.Errorf("podman run --network %s failed (%v): %s\n\n%s",
			testNet, runErr, outStr, classifyNetworkError(outStr))
	}

	// Remove
	podmanCmd("network", "rm", testNet).Run() //nolint
	return nil
}

// classifyNetworkError inspects podman error output and returns a targeted
// remediation message.  Callers should append it to their error string.
func classifyNetworkError(out string) string {
	switch {
	case strings.Contains(out, "permission denied"):
		// Podman uses getpwuid() to find the container-storage home dir,
		// ignoring the $HOME env var.  If /etc/passwd has the wrong home
		// (e.g. /home/featherdeploy instead of /var/lib/featherdeploy),
		// Podman cannot create ~/.local/share/containers and fails here.
		return "Container storage is inaccessible — the service user's home in\n" +
			"/etc/passwd is likely wrong (podman ignores $HOME, it calls getpwuid()).\n" +
			"  sudo usermod -d /var/lib/featherdeploy featherdeploy\n" +
			"  sudo mkdir -p /var/lib/featherdeploy\n" +
			"  sudo chown -R featherdeploy:featherdeploy /var/lib/featherdeploy\n" +
			"  sudo systemctl restart featherdeploy"
	case strings.Contains(out, "no such file or directory") &&
		(strings.Contains(out, "runtime") || strings.Contains(out, "XDG")):
		// XDG_RUNTIME_DIR/containers doesn't exist — created by the service's
		// ExecStartPre before the main process starts.
		rtDir := fmt.Sprintf("/run/user/%d", os.Getuid())
		return "XDG_RUNTIME_DIR (" + rtDir + ") does not exist.\n" +
			"It is created automatically when the service starts via its systemd unit.\n" +
			"  sudo mkdir -p " + rtDir + "\n" +
			"  sudo chown featherdeploy:featherdeploy " + rtDir + "\n" +
			"  sudo systemctl restart featherdeploy"
	default:
		return "Named networks require the netavark networking back-end:\n" +
			"  RHEL 9 / AlmaLinux / Rocky: sudo dnf install -y netavark aardvark-dns\n" +
			"  Ubuntu / Debian:             sudo apt-get install -y netavark\n" +
			"  Then restart: sudo systemctl restart featherdeploy"
	}
}

// ensureProjectNetwork guarantees the per-project podman bridge network exists
// AND is ready for 'podman run --network' to use it.
//
// Key design decisions:
//
//  1. 'podman network inspect' only reads the JSON config file from disk.
//     It does NOT verify the network is wired into libpod's runtime state.
//     A network can pass inspect but fail 'podman run --network' if the
//     libpod DB was cleared (e.g. after 'podman system migrate' on update)
//     while the config JSON survived.
//
//  2. Therefore: if NO containers are actively using the network, we always
//     remove+recreate it so both the JSON and the libpod runtime state are
//     freshly consistent.  This is safe — no containers means no disruption —
//     and eliminates the entire class of "network not found" split-brain.
//
//  3. After network create, we poll inspect for up to 5 s so we never hand
//     back to the caller before the network is confirmed visible.
//     We then require a tiny container to start on the network as the final
//     readiness check, because inspect can succeed before run is actually
//     able to resolve the network in the runtime state.
func ensureProjectNetwork(projectID int64) error {
	// Serialize all network operations for this project to prevent races
	// between concurrent deployments and the startup reconcile goroutine.
	mu := projectNetworkLock(projectID)
	mu.Lock()
	defer mu.Unlock()

	name := projectNetworkName(projectID)

	// Check whether any containers are actively running on this network.
	// 'podman ps --filter network' queries live runtime state, not just config JSON.
	ctrOut, _ := podmanCmd("ps", "-q", "--filter", "network="+name).Output()
	if strings.TrimSpace(string(ctrOut)) != "" {
		// Live containers are on this network — it is definitely healthy.
		return nil
	}

	// No running containers: remove whatever state exists (stale JSON, stale
	// libpod entry) and recreate from scratch so the runtime is always fresh.
	podmanCmd("network", "rm", "-f", name).Run() //nolint
	// Give netavark/libpod time to finish tearing down old state.
	time.Sleep(500 * time.Millisecond)

	out, err := podmanCmd("network", "create", name).CombinedOutput()
	if err != nil {
		// Race: another goroutine created it between our rm and create.
		if strings.Contains(strings.ToLower(string(out)), "already exists") {
			return nil
		}
		hint := ""
		if strings.Contains(err.Error(), "127") || strings.Contains(string(out), "127") {
			hint = "\n  Fix (RHEL/AlmaLinux/Rocky): sudo dnf install -y netavark aardvark-dns" +
				"\n  Fix (Ubuntu/Debian):         sudo apt-get install -y netavark"
		}
		return fmt.Errorf("podman network create %s: %v — %s%s", name, err, strings.TrimSpace(string(out)), hint)
	}

	// Poll up to 5 s for inspect to confirm both JSON and runtime state are ready.
	// netavark bridges are wired asynchronously on some kernels.
	for i := 0; i < 10; i++ {
		if podmanCmd("network", "inspect", name).Run() == nil {
			// Extra settle before the caller passes it to 'podman run'.
			time.Sleep(300 * time.Millisecond)
			// Final readiness check: a container must actually start on the
			// network. If this fails with "network not found" we back off and
			// retry, because the runtime state is still catching up.
			runOut, runErr := podmanCmd("run", "--rm", "--network", name, "docker.io/library/alpine", "true").CombinedOutput()
			if runErr == nil {
				return nil
			}
			outStr := strings.TrimSpace(string(runOut))
			if strings.Contains(outStr, "network not found") || strings.Contains(outStr, "unable to find network") {
				time.Sleep(1 * time.Second)
				continue
			}
			return fmt.Errorf("podman run --network %s failed during readiness check: %v — %s", name, runErr, outStr)
		}
		time.Sleep(500 * time.Millisecond)
	}

	lsOut, _ := podmanCmd("network", "ls").CombinedOutput()
	return fmt.Errorf(
		"network %s still not visible after 5s\npodman network ls:\n%s\nLikely cause: netavark not installed.\n  sudo dnf install -y netavark aardvark-dns",
		name, strings.TrimSpace(string(lsOut)))
}

// DBNetworkAlias converts a database or service name (slug) into a valid
// container network alias (lowercase alphanum + hyphens).
func DBNetworkAlias(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return b.String()
}

// DBEnvKey converts a name to the environment-variable key suffix.
// e.g. "my-db" → "MY_DB" (used as prefix for the auto-injected _URL var).
func DBEnvKey(name string) string {
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

// DBConnectionURL builds the internal connection URL for a database using the
// container alias as hostname (valid within the project podman network).
func DBConnectionURL(dbType, dbName, dbUser, clearPassword, alias string) string {
	esc := urlEscapeCredential
	switch dbType {
	case "postgres":
		return fmt.Sprintf("postgresql://%s:%s@%s:5432/%s",
			esc(dbUser), esc(clearPassword), alias, dbName)
	case "mysql":
		return fmt.Sprintf("mysql://%s:%s@%s:3306/%s",
			esc(dbUser), esc(clearPassword), alias, dbName)
	case "sqlite":
		return fmt.Sprintf("sqlite:///%s", strings.TrimPrefix(filepath.Join(sqliteMountTarget(alias), sqliteDatabaseFilename(dbName)), "/"))
	}
	return ""
}

// DBPublicConnectionURL builds the external connection URL using the host's
// port binding (valid from outside the project network).
func DBPublicConnectionURL(dbType, dbName, dbUser, clearPassword string, hostPort int) string {
	esc := urlEscapeCredential
	switch dbType {
	case "postgres":
		return fmt.Sprintf("postgresql://%s:%s@HOST:%d/%s",
			esc(dbUser), esc(clearPassword), hostPort, dbName)
	case "mysql":
		return fmt.Sprintf("mysql://%s:%s@HOST:%d/%s",
			esc(dbUser), esc(clearPassword), hostPort, dbName)
	}
	return ""
}

func sqliteMountTarget(name string) string {
	return filepath.ToSlash(filepath.Join("/var/lib/featherdeploy/sqlite", DBNetworkAlias(name)))
}

func sqliteDatabaseFilename(dbName string) string {
	lowerName := strings.ToLower(dbName)
	if strings.HasSuffix(lowerName, ".sqlite") || strings.HasSuffix(lowerName, ".db") {
		return dbName
	}
	return dbName + ".sqlite"
}

func dbVolumeName(dbID int64) string {
	return fmt.Sprintf("fd-db-%d-data", dbID)
}

func urlEscapeCredential(s string) string {
	return strings.NewReplacer(
		":", "%3A", "@", "%40", "/", "%2F",
		"?", "%3F", "#", "%23",
	).Replace(s)
}

// collectProjectDBEnvArgs queries all running databases in the same project
// and returns ["-e", "MYDB_URL=...", ...] args for podman run.
// This auto-injects connection URLs into sibling service containers.
func collectProjectDBEnvArgs(db *sql.DB, projectID int64, jwtSecret string) []string {
	rows, err := db.Query(
		`SELECT id, name, db_type, db_name, db_user, db_password
		 FROM databases WHERE project_id=? AND status='running'`, projectID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var args []string
	for rows.Next() {
		var dbID int64
		var name, dbType, dbName, dbUser, encPass string
		if rows.Scan(&dbID, &name, &dbType, &dbName, &dbUser, &encPass) != nil {
			continue
		}
		clearPass := encPass
		if strings.HasPrefix(encPass, "fdenc:") {
			if p, decErr := appCrypto.Decrypt(encPass[len("fdenc:"):], jwtSecret); decErr == nil {
				clearPass = p
			}
		}
		alias := DBNetworkAlias(name)
		_ = dbID
		connURL := DBConnectionURL(dbType, dbName, dbUser, clearPass, alias)
		if connURL == "" {
			continue
		}
		envKey := DBEnvKey(name) + "_URL"
		args = append(args, "-e", envKey+"="+connURL)
	}
	return args
}

func collectProjectDBMountArgs(db *sql.DB, projectID int64) []string {
	rows, err := db.Query(
		`SELECT id, name, db_type
		 FROM databases WHERE project_id=? AND status='running'`, projectID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var args []string
	for rows.Next() {
		var dbID int64
		var name, dbType string
		if rows.Scan(&dbID, &name, &dbType) != nil {
			continue
		}
		if dbType != "sqlite" {
			continue
		}
		args = append(args, "-v", dbVolumeName(dbID)+":"+sqliteMountTarget(name))
	}
	return args
}

// ─── Database container management ───────────────────────────────────────────

// StartDatabase pulls the database image and starts the container in the
// project's network. Safe to call for both new and stopped databases.
func StartDatabase(db *sql.DB, jwtSecret string, dbID int64) error {
	var projectID int64
	var name, dbType, dbVersion, dbName, dbUser, encPass string
	var hostPortNull sql.NullInt64
	var networkPublic int
	err := db.QueryRow(
		`SELECT project_id, name, db_type, db_version, db_name, db_user, db_password,
		        host_port, network_public
		 FROM databases WHERE id=?`, dbID,
	).Scan(&projectID, &name, &dbType, &dbVersion, &dbName, &dbUser, &encPass,
		&hostPortNull, &networkPublic)
	if err != nil {
		return fmt.Errorf("load database config: %w", err)
	}

	clearPass := encPass
	if strings.HasPrefix(encPass, "fdenc:") {
		if p, decErr := appCrypto.Decrypt(encPass[len("fdenc:"):], jwtSecret); decErr == nil {
			clearPass = p
		}
	}
	if dbType == "sqlite" {
		if err := ensureDatabaseVolume(dbID); err != nil {
			db.Exec(`UPDATE databases SET status='error', last_error=?, updated_at=datetime('now') WHERE id=?`, err.Error(), dbID) //nolint
			return err
		}
		db.Exec( //nolint
			`UPDATE databases SET status='running', container_id='', host_port=NULL, last_error='', updated_at=datetime('now') WHERE id=?`,
			dbID)
		return nil
	}

	// Ensure the project network exists.
	if netErr := ensureProjectNetwork(projectID); netErr != nil {
		errMsg := fmt.Sprintf("project network unavailable: %v", netErr)
		db.Exec(`UPDATE databases SET status='error', last_error=?, updated_at=datetime('now') WHERE id=?`, errMsg, dbID) //nolint
		return fmt.Errorf("start database: ensure project network: %w", netErr)
	}

	cName := fmt.Sprintf("fd-db-%d", dbID)
	networkName := projectNetworkName(projectID)
	alias := DBNetworkAlias(name)
	image := dbImageName(dbType, dbVersion)

	db.Exec(`UPDATE databases SET status='starting', updated_at=datetime('now') WHERE id=?`, dbID) //nolint

	// Pull the image only when it is missing locally to avoid re-downloading a
	// cached database image on every start.
	if image != "" && !podmanImageExists(image) {
		slog.Info("pulling database image", "db_id", dbID, "image", image)
		if out, pullErr := podmanCmd("pull", image).CombinedOutput(); pullErr != nil {
			errMsg := fmt.Sprintf("pull image %s: %v — %s", image, pullErr, strings.TrimSpace(string(out)))
			db.Exec(`UPDATE databases SET status='error', last_error=?, updated_at=datetime('now') WHERE id=?`, errMsg, dbID) //nolint
			return fmt.Errorf("pull database image %s: %v — %s", image, pullErr, strings.TrimSpace(string(out)))
		}
	}

	// Stop/remove any existing container from a previous start.
	if containerExists(cName) {
		stopCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		podmanCmdCtx(stopCtx, "stop", "--time", "10", cName).Run() //nolint
		cancel()
		podmanCmd("rm", "-f", cName).Run() //nolint
	}

	// Auto-assign host port for public databases.
	hostPort := int(hostPortNull.Int64)
	if networkPublic == 1 && hostPort <= 0 {
		hostPort = 15000 + int(dbID)
		db.Exec(`UPDATE databases SET host_port=? WHERE id=?`, hostPort, dbID) //nolint
	}

	// Persist data in a named volume so it survives container re-creation.
	volumeName := dbVolumeName(dbID)
	if err := ensureDatabaseVolume(dbID); err != nil {
		db.Exec(`UPDATE databases SET status='error', last_error=?, updated_at=datetime('now') WHERE id=?`, err.Error(), dbID) //nolint
		return err
	}
	mountPath := dbDataMountPath(dbType)

	runArgs := []string{
		"run", "-d",
		"--name", cName,
		"--restart", "unless-stopped",
		"--network-alias", alias,
		"--log-opt", "max-size=10m",
		"--log-opt", "max-file=3",
		"-v", volumeName + ":" + mountPath,
	}
	if networkName != "" {
		runArgs = append(runArgs, "--network", networkName)
	}
	// Public databases are additionally bound to a host port for external access.
	if networkPublic == 1 && hostPort > 0 {
		internalPort := dbInternalPort(dbType)
		runArgs = append(runArgs, "-p", fmt.Sprintf("%d:%d", hostPort, internalPort))
	}
	// Inject the DB engine's environment variables (credentials, database name).
	runArgs = append(runArgs, dbContainerEnvArgs(dbType, dbName, dbUser, clearPass)...)
	runArgs = append(runArgs, image)

	out, err := podmanCmd(runArgs...).CombinedOutput()
	if err != nil {
		errMsg := fmt.Sprintf("container start failed: %v — %s", err, strings.TrimSpace(string(out)))
		db.Exec(`UPDATE databases SET status='error', last_error=?, updated_at=datetime('now') WHERE id=?`, errMsg, dbID) //nolint
		return fmt.Errorf("podman run database %s: %v — %s", cName, err, strings.TrimSpace(string(out)))
	}
	containerID := strings.TrimSpace(string(out))
	db.Exec( //nolint
		`UPDATE databases SET status='running', container_id=?, last_error='', updated_at=datetime('now') WHERE id=?`,
		containerID, dbID)
	slog.Info("database container started", "db_id", dbID, "container", cName)
	return nil
}

// StopDatabase stops and removes the database container without deleting the
// data volume, so it can be restarted later with all data intact.
func StopDatabase(db *sql.DB, dbID int64) error {
	var projectID int64
	var dbType string
	if err := db.QueryRow(`SELECT project_id, db_type FROM databases WHERE id=?`, dbID).Scan(&projectID, &dbType); err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("load database type: %w", err)
	}
	if dbType == "sqlite" {
		db.Exec( //nolint
			`UPDATE databases SET status='stopped', container_id='', host_port=NULL, updated_at=datetime('now') WHERE id=?`, dbID)
		CleanupProjectRuntimeIfUnused(db, projectID) //nolint
		slog.Info("sqlite database marked stopped", "db_id", dbID)
		return nil
	}
	cName := fmt.Sprintf("fd-db-%d", dbID)
	if err := removeContainerIfExists(cName); err != nil {
		return err
	}
	db.Exec( //nolint
		`UPDATE databases SET status='stopped', container_id='', updated_at=datetime('now') WHERE id=?`, dbID)
	CleanupProjectRuntimeIfUnused(db, projectID) //nolint
	slog.Info("database container stopped", "db_id", dbID)
	return nil
}

func DeleteDatabase(db *sql.DB, dbID int64, purgeData bool) error {
	var projectID int64
	if err := db.QueryRow(`SELECT project_id FROM databases WHERE id=?`, dbID).Scan(&projectID); err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("load database project: %w", err)
	}
	if err := StopDatabase(db, dbID); err != nil {
		return err
	}
	if !purgeData {
		CleanupProjectRuntimeIfUnused(db, projectID) //nolint
		return nil
	}
	if err := deleteDatabaseVolume(dbID); err != nil {
		return err
	}
	CleanupProjectRuntimeIfUnused(db, projectID) //nolint
	return nil
}

func DeleteServiceRuntime(db *sql.DB, projectID, svcID int64) error {
	cName := fmt.Sprintf("fd-svc-%d", svcID)
	if err := removeContainerIfExists(cName); err != nil {
		return err
	}
	if err := deleteServiceImages(svcID); err != nil {
		return err
	}
	CleanupProjectRuntimeIfUnused(db, projectID) //nolint
	return nil
}

func CreateDatabaseBackup(db *sql.DB, jwtSecret string, dbID int64) (string, string, error) {
	var name, dbType, status string
	err := db.QueryRow(
		`SELECT name, db_type, status FROM databases WHERE id=?`, dbID,
	).Scan(&name, &dbType, &status)
	if err != nil {
		return "", "", fmt.Errorf("load database backup metadata: %w", err)
	}

	wasRunning := status == "running" && dbType != "sqlite"
	if wasRunning {
		if err := StopDatabase(db, dbID); err != nil {
			return "", "", err
		}
	}

	tmpFile, err := os.CreateTemp("", fmt.Sprintf("fd-db-%d-*.tar", dbID))
	if err != nil {
		if wasRunning {
			_ = StartDatabase(db, jwtSecret, dbID)
		}
		return "", "", fmt.Errorf("create temp backup file: %w", err)
	}
	defer tmpFile.Close()

	cmd := podmanCmd("volume", "export", dbVolumeName(dbID))
	cmd.Stdout = tmpFile
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		os.Remove(tmpFile.Name()) //nolint
		if wasRunning {
			_ = StartDatabase(db, jwtSecret, dbID)
		}
		return "", "", fmt.Errorf("export database volume: %v — %s", err, strings.TrimSpace(stderr.String()))
	}

	if wasRunning {
		if err := StartDatabase(db, jwtSecret, dbID); err != nil {
			os.Remove(tmpFile.Name()) //nolint
			return "", "", fmt.Errorf("restart database after backup: %w", err)
		}
	}

	downloadName := fmt.Sprintf("%s-%s-backup-%s.tar",
		DBNetworkAlias(name), dbType, time.Now().UTC().Format("20060102-150405"))
	return tmpFile.Name(), downloadName, nil
}

// GetDatabaseLogs returns the last 200 lines of stdout+stderr from the
// database container. Returns an error when the container doesn't exist
// (e.g. start failed before the container was created).
func GetDatabaseLogs(dbID int64) (string, error) {
	cName := fmt.Sprintf("fd-db-%d", dbID)
	if !containerExists(cName) {
		return "", fmt.Errorf("container %s does not exist (start may have failed before container creation)", cName)
	}
	out, err := podmanCmd("logs", "--tail", "200", cName).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("podman logs %s: %w", cName, err)
	}
	return string(out), nil
}

// UpdateDatabase persists configuration changes (db_version, network_public)
// for a database record. A restart is required for the new settings to take
// effect on the running container.
func UpdateDatabase(db *sql.DB, dbID int64, dbVersion string, networkPublic bool) error {
	npInt := 0
	if networkPublic {
		npInt = 1
	}
	_, err := db.Exec(
		`UPDATE databases SET db_version=?, network_public=?, updated_at=datetime('now') WHERE id=?`,
		dbVersion, npInt, dbID)
	return err
}

// dbImageName returns the full image reference for a database type + version tag.
func dbImageName(dbType, version string) string {
	if version == "" || version == "latest" {
		version = "latest"
	}
	// Use fully-qualified docker.io/library/ names to avoid short-name
	// resolution failures on systems without unqualified-search-registries
	// configured in /etc/containers/registries.conf (e.g. RHEL/Fedora defaults).
	switch dbType {
	case "postgres":
		return "docker.io/library/postgres:" + version
	case "mysql":
		return "docker.io/library/mysql:" + version
	case "sqlite":
		return ""
	default:
		return ""
	}
}

// dbInternalPort returns the default listening port for a database type.
func dbInternalPort(dbType string) int {
	switch dbType {
	case "postgres":
		return 5432
	case "mysql":
		return 3306
	default:
		return 5432
	}
}

// dbDataMountPath returns the internal data directory for persistent volume mounts.
func dbDataMountPath(dbType string) string {
	switch dbType {
	case "postgres":
		return "/var/lib/postgresql/data"
	case "mysql":
		return "/var/lib/mysql"
	case "sqlite":
		return "/data"
	default:
		return "/data"
	}
}

// dbContainerEnvArgs returns -e KEY=VALUE pairs for the DB engine container.
// Redis credentials are handled via a command override, not env vars.
func dbContainerEnvArgs(dbType, dbName, dbUser, clearPass string) []string {
	switch dbType {
	case "postgres":
		return []string{
			"-e", "POSTGRES_DB=" + dbName,
			"-e", "POSTGRES_USER=" + dbUser,
			"-e", "POSTGRES_PASSWORD=" + clearPass,
		}
	case "mysql":
		return []string{
			"-e", "MYSQL_DATABASE=" + dbName,
			"-e", "MYSQL_USER=" + dbUser,
			"-e", "MYSQL_PASSWORD=" + clearPass,
			"-e", "MYSQL_ROOT_PASSWORD=" + clearPass,
		}
	default:
		return nil
	}
}

func ensureDatabaseVolume(dbID int64) error {
	out, err := podmanCmd("volume", "create", "--ignore", dbVolumeName(dbID)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("create database volume %s: %v — %s", dbVolumeName(dbID), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func deleteDatabaseVolume(dbID int64) error {
	out, err := podmanCmd("volume", "rm", "-f", dbVolumeName(dbID)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("remove database volume %s: %v — %s", dbVolumeName(dbID), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// podmanCmd creates a rootless podman command run as the featherdeploy service
// user. The service user has /etc/subuid + /etc/subgid entries and the systemd
// unit provides HOME + XDG_RUNTIME_DIR so rootless podman has a valid user
// store and networking namespace. No sudo required.
// podmanEnv returns an environment slice for podman sub-processes.
// It inherits the current process environment but forces the correct values
// for HOME (container image/network storage) and XDG_RUNTIME_DIR (socket/
// network namespace path).
//
// XDG_RUNTIME_DIR is ALWAYS computed from os.Getuid() — the real numeric
// UID of the running process.  Reading $XDG_RUNTIME_DIR from the parent
// environment is unreliable: if the systemd %U specifier failed (its
// documented fallback is 0), the inherited value is /run/user/0, causing
// every podman call to log "not owned by the current user" and fail build.
//
// DBUS_SESSION_BUS_ADDRESS is stripped so podman and its helpers (slirp4netns,
// crun) don't try to contact a non-existent user dbus socket and fail with
// "connect: permission denied".
func podmanEnv() []string {
	raw := os.Environ()
	env := make([]string, 0, len(raw)+6)
	for _, e := range raw {
		k := strings.SplitN(e, "=", 2)[0]
		switch {
		case k == "HOME", k == "XDG_RUNTIME_DIR", k == "XDG_CONFIG_HOME", k == "XDG_DATA_HOME", k == "XDG_CACHE_HOME", k == "CONTAINER_HOST", k == "DOCKER_HOST":
			continue
		case strings.HasPrefix(k, "DBUS_"):
			continue
		}
		env = append(env, e)
	}
	home := os.Getenv("HOME")
	if home == "" {
		home = "/var/lib/featherdeploy"
	}
	// Compute from the actual runtime UID — always correct regardless of what
	// the parent process inherited as $XDG_RUNTIME_DIR.
	rtDir := fmt.Sprintf("/run/user/%d", os.Getuid())
	
	// Explicitly set all XDG paths based on home to prevent split-brain issues
	// where systemd-provided XDG_CONFIG_HOME or XDG_DATA_HOME causes 'podman run'
	// to look in different directories than 'podman network create'.
	return append(env, 
		"HOME="+home, 
		"XDG_RUNTIME_DIR="+rtDir,
		"XDG_CONFIG_HOME="+home+"/.config",
		"XDG_DATA_HOME="+home+"/.local/share",
		"XDG_CACHE_HOME="+home+"/.cache",
		"DBUS_SESSION_BUS_ADDRESS=",
	)
}

func podmanCmd(args ...string) *exec.Cmd {
	home := os.Getenv("HOME")
	if home == "" {
		home = "/var/lib/featherdeploy"
	}
	// Podman 4.x stores runRoot/tmpDir in its SQLite DB and overrides the
	// configured values on every invocation ("Overriding run root ... from
	// database").  Passing --root and --runroot explicitly on the CLI takes
	// precedence over the DB so a stale database from an old installer version
	// can never redirect netavark state to a wrong path (causing "network not
	// found" even when the network JSON exists and 'network inspect' succeeds).
	graphRoot := filepath.Join(home, ".local", "share", "containers", "storage")
	networkCfgDir := filepath.Join(graphRoot, "networks")
	runRoot := fmt.Sprintf("/run/user/%d/containers", os.Getuid())
	cmd := exec.Command("podman", append([]string{"--cgroup-manager", "cgroupfs", "--root", graphRoot, "--runroot", runRoot, "--network-config-dir", networkCfgDir}, args...)...)
	cmd.Env = podmanEnv()
	return cmd
}

// podmanCmdCtx is like podmanCmd but accepts a context for timeout control.
func podmanCmdCtx(ctx context.Context, args ...string) *exec.Cmd {
	home := os.Getenv("HOME")
	if home == "" {
		home = "/var/lib/featherdeploy"
	}
	graphRoot := filepath.Join(home, ".local", "share", "containers", "storage")
	networkCfgDir := filepath.Join(graphRoot, "networks")
	runRoot := fmt.Sprintf("/run/user/%d/containers", os.Getuid())
	cmd := exec.CommandContext(ctx, "podman", append([]string{"--cgroup-manager", "cgroupfs", "--root", graphRoot, "--runroot", runRoot, "--network-config-dir", networkCfgDir}, args...)...)
	cmd.Env = podmanEnv()
	return cmd
}

// podmanBuild builds a container image using rootless podman.
// --pull=missing tells podman to use the locally-cached base image when it
// already exists, and only contact the registry when the image is absent.
// This avoids re-pulling node:20-alpine / python:3.12-slim on every build,
// which saves bandwidth and minutes per deployment.
func podmanBuild(dir string, log *logBuf, imageName string) error {
	cmd := podmanCmd("build", "--pull=missing", "-t", imageName, ".")
	cmd.Dir = dir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	out := strings.TrimSpace(buf.String())
	for _, line := range strings.Split(out, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			log.add("  %s", t)
		}
	}
	return err
}

// podmanImageExists returns true if the named image exists in the rootless podman store.
func podmanImageExists(image string) bool {
	return podmanCmd("image", "exists", image).Run() == nil
}

func removeContainerIfExists(name string) error {
	if !containerExists(name) {
		return nil
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if out, err := podmanCmdCtx(stopCtx, "stop", "--time", "10", name).CombinedOutput(); err != nil {
		return fmt.Errorf("stop container %s: %v — %s", name, err, strings.TrimSpace(string(out)))
	}
	if out, err := podmanCmd("rm", "-f", name).CombinedOutput(); err != nil {
		return fmt.Errorf("remove container %s: %v — %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func deleteServiceImages(svcID int64) error {
	prefix := fmt.Sprintf("featherdeploy/svc-%d", svcID)
	out, err := podmanCmd("images", "--format", "{{.Repository}}:{{.Tag}}", "--filter", "reference="+prefix+"*").Output()
	if err != nil {
		return fmt.Errorf("list service images: %w", err)
	}
	for _, img := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		img = strings.TrimSpace(img)
		if img == "" {
			continue
		}
		if rmOut, rmErr := podmanCmd("rmi", "-f", img).CombinedOutput(); rmErr != nil {
			return fmt.Errorf("remove image %s: %v — %s", img, rmErr, strings.TrimSpace(string(rmOut)))
		}
	}
	return nil
}

func CleanupProjectRuntimeIfUnused(db *sql.DB, projectID int64) {
	if projectID == 0 {
		return
	}
	var serviceCount int
	db.QueryRow(`SELECT COUNT(*) FROM services WHERE project_id=?`, projectID).Scan(&serviceCount) //nolint
	var databaseCount int
	db.QueryRow(`SELECT COUNT(*) FROM databases WHERE project_id=?`, projectID).Scan(&databaseCount) //nolint
	if serviceCount > 0 || databaseCount > 0 {
		return
	}
	podmanCmd("pod", "rm", "-f", projectPodName(projectID)).Run()       //nolint
	podmanCmd("network", "rm", "-f", projectNetworkName(projectID)).Run() //nolint
}

// trimOldDeployLogs nullifies the deploy_log column for all but the most recent
// maxKeepLogs deployments for the given service. This prevents the database
// from growing unboundedly as build output (npm install, podman build, …) can
// easily be 100–300 KB per deployment.
const maxKeepLogs = 5

func trimOldDeployLogs(db *sql.DB, svcID int64) {
	db.Exec(`
		UPDATE deployments
		SET    deploy_log = ''
		WHERE  service_id = ?
		  AND  deploy_log != ''
		  AND  id NOT IN (
			  SELECT id FROM deployments
			  WHERE  service_id = ?
			  ORDER  BY id DESC
			  LIMIT  ?
		  )`, svcID, svcID, maxKeepLogs) //nolint
}

// pruneOldDepImages removes old featherdeploy/svc-N:dep-M images for the given
// service, keeping only the current deployment image and the :stable tag.
// Called asynchronously after a successful deployment to reclaim disk space.
func pruneOldDepImages(svcID, currentDepID int64) {
	prefix := fmt.Sprintf("featherdeploy/svc-%d", svcID)
	current := fmt.Sprintf("%s:dep-%d", prefix, currentDepID)
	stable := fmt.Sprintf("%s:stable", prefix)

	out, err := podmanCmd("images", "--format", "{{.Repository}}:{{.Tag}}", "--filter",
		"reference="+prefix+"*").Output()
	if err != nil {
		return
	}
	for _, img := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		img = strings.TrimSpace(img)
		if img == "" || img == current || img == stable {
			continue
		}
		// Only remove dep-N images for this service
		if strings.HasPrefix(img, prefix+":dep-") {
			podmanCmd("rmi", "-f", img).Run() //nolint
			slog.Info("[deploy] pruned old image", "image", img)
		}
	}
}

func pickBaseImage(framework string) string {
	fw := strings.ToLower(framework)
	switch {
	case fw == "nextjs" || fw == "nuxt" || fw == "remix" || fw == "nodejs" || fw == "express" || fw == "fastify" ||
		fw == "nestjs" || fw == "koa" || fw == "hapi" || fw == "gatsby" || fw == "angular":
		return "node:20-alpine"
	case fw == "react" || fw == "vue" || fw == "svelte" || fw == "vite" || fw == "static":
		return "node:20-alpine"
	case fw == "django" || fw == "flask" || fw == "fastapi" || fw == "python" ||
		fw == "tornado" || fw == "aiohttp" || fw == "starlette" || fw == "sanic" || fw == "bottle":
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

// writeEnvFileForBuild writes service env vars (and project DB connection URLs)
// to .env and .env.local in workDir so that frameworks which read env vars at
// build time (Next.js, Vite, Create React App, etc.) can access them during
// `npm run build`. Secret values are decrypted before writing.
// Both .env and .env.local are written so Next.js and other tools pick them up.
func writeEnvFileForBuild(db *sql.DB, svcID, projectID int64, jwtSecret, workDir string) error {
	rows, err := db.Query(
		`SELECT key, value, is_secret FROM env_variables WHERE service_id=?`, svcID)
	if err != nil {
		return nil // no vars is fine
	}
	defer rows.Close()

	var sb strings.Builder
	for rows.Next() {
		var k, v string
		var isSecret int
		if rows.Scan(&k, &v, &isSecret) != nil {
			continue
		}
		if isSecret == 1 && strings.HasPrefix(v, "fdenc:") {
			plain, decErr := appCrypto.Decrypt(v[len("fdenc:"):], jwtSecret)
			if decErr != nil {
				continue // skip undecryptable vars
			}
			v = plain
		}
		// Quote values that contain special shell chars
		sb.WriteString(k + "=" + v + "\n")
	}
	// Also inject project database connection URLs so build tools (Prisma, etc.)
	// can access them at build time.
	for _, pair := range collectProjectDBEnvArgs(db, projectID, jwtSecret) {
		// collectProjectDBEnvArgs returns ["-e", "KEY=VALUE", "-e", "KEY=VALUE", ...]
		// skip the "-e" flag entries
		if pair == "-e" {
			continue
		}
		sb.WriteString(pair + "\n")
	}
	content := []byte(sb.String())
	if len(content) == 0 {
		return nil
	}
	// Write both .env and .env.local (Next.js prefers .env.local)
	os.WriteFile(filepath.Join(workDir, ".env"), content, 0600)       //nolint
	os.WriteFile(filepath.Join(workDir, ".env.local"), content, 0600) //nolint
	return nil
}

// extractArtifact extracts a .zip, .tar.gz, or .tgz archive into destDir.
// Files are extracted with path sanitisation to prevent zip-slip attacks.
func extractArtifact(archivePath, destDir string, log *logBuf) error {
	name := strings.ToLower(filepath.Base(archivePath))
	switch {
	case strings.HasSuffix(name, ".zip"):
		return extractZip(archivePath, destDir, log)
	case strings.HasSuffix(name, ".tar.gz"), strings.HasSuffix(name, ".tgz"):
		return extractTarGz(archivePath, destDir, log)
	default:
		return fmt.Errorf("unsupported archive format: %s (only .zip, .tar.gz, .tgz are supported)", name)
	}
}

func extractZip(archivePath, destDir string, log *logBuf) error {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	defer r.Close()
	for _, f := range r.File {
		// Sanitise path: strip ../ sequences and absolute paths
		cleanName := filepath.ToSlash(f.Name)
		if strings.Contains(cleanName, "..") {
			log.add("[artifact] skipping unsafe path: %s", f.Name)
			continue
		}
		target := filepath.Join(destDir, cleanName)
		// Security: verify target is within destDir
		if !strings.HasPrefix(target, destDir+string(os.PathSeparator)) && target != destDir {
			log.add("[artifact] skipping path escaping destination: %s", f.Name)
			continue
		}
		if f.FileInfo().IsDir() {
			os.MkdirAll(target, 0755) //nolint
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return fmt.Errorf("mkdir %q: %w", filepath.Dir(target), err)
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("open entry %q: %w", f.Name, err)
		}
		out, err := os.Create(target)
		if err != nil {
			rc.Close()
			return fmt.Errorf("create %q: %w", target, err)
		}
		_, copyErr := io.Copy(out, io.LimitReader(rc, 500<<20)) // 500 MB per-file cap
		out.Close()
		rc.Close()
		if copyErr != nil {
			return fmt.Errorf("write %q: %w", target, copyErr)
		}
	}
	return nil
}

func extractTarGz(archivePath, destDir string, log *logBuf) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open tar.gz: %w", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar read: %w", err)
		}
		// Sanitise path
		cleanName := filepath.ToSlash(hdr.Name)
		if strings.Contains(cleanName, "..") {
			log.add("[artifact] skipping unsafe path: %s", hdr.Name)
			continue
		}
		target := filepath.Join(destDir, cleanName)
		if !strings.HasPrefix(target, destDir+string(os.PathSeparator)) && target != destDir {
			log.add("[artifact] skipping path escaping destination: %s", hdr.Name)
			continue
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			os.MkdirAll(target, 0755) //nolint
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return fmt.Errorf("mkdir %q: %w", filepath.Dir(target), err)
			}
			out, err := os.Create(target)
			if err != nil {
				return fmt.Errorf("create %q: %w", target, err)
			}
			_, copyErr := io.Copy(out, io.LimitReader(tr, 500<<20))
			out.Close()
			if copyErr != nil {
				return fmt.Errorf("write %q: %w", target, copyErr)
			}
		}
	}
	return nil
}
