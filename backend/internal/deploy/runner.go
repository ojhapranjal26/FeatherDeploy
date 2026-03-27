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
	var repoURL, repoBranch, repoFolder, framework, buildCmd, startCmd string
	var appPort int
	var hostPortNull sql.NullInt64
	err := db.QueryRow(
		`SELECT repo_url, repo_branch, repo_folder, framework, build_command, start_command,
		        app_port, host_port
		 FROM services WHERE id=?`, svcID,
	).Scan(&repoURL, &repoBranch, &repoFolder, &framework, &buildCmd, &startCmd,
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

	// ── 2. Inject GitHub App installation token for HTTPS GitHub repos ──────────
	repoURL = injectGitHubAppToken(context.Background(), db, repoURL, log)

	// ── 3. SSH key setup for private / SSH repos ──────────────────────────────
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

	// Capture the actual commit SHA and update the deployment record
	if sha, err := gitRevParse(workDir); err == nil && sha != "" {
		db.Exec(`UPDATE deployments SET commit_sha=? WHERE id=?`, sha, depID) //nolint
		log.add("[clone] commit %s", sha)
	}

	// ── 3b. Apply repo_folder (monorepo / subdirectory deployments) ───────────
	// When a subfolder is configured, treat that folder as the build root.
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
	if writeErr := writeEnvFileForBuild(db, svcID, jwtSecret, workDir); writeErr != nil {
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
	runArgs = append(runArgs, envArgs...)
	runArgs = append(runArgs, imageName)

	log.add("[podman] podman run -d --name %s -p %d:%d %s", cName, hostPort, appPort, imageName)
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
		go pruneOldDepImages(svcID, depID)
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

// podmanCmd creates a rootful podman command executed via `sudo -n`.
//
// Rootless podman (running as the featherdeploy service account) requires the
// kernel to allow unprivileged user-namespace creation and calls newuidmap,
// both of which are disabled or broken on many VPS / VM kernels. Running podman
// as root via sudo sidesteps every user-namespace restriction. Images are
// stored in /var/lib/containers (root's persistent storage) and are visible to
// all subsequent sudo podman calls.
//
// Prerequisite: build.sh installs the sudoers rule
//   featherdeploy ALL=(root) NOPASSWD: /usr/bin/podman
func podmanCmd(args ...string) *exec.Cmd {
	// sudo -n: non-interactive mode — fail immediately if a password would be
	// prompted (should never happen with NOPASSWD in place).
	full := make([]string, 0, 2+len(args))
	full = append(full, "-n", "podman")
	full = append(full, args...)
	return exec.Command("sudo", full...)
}

// podmanCmdCtx is like podmanCmd but accepts a context for timeout control.
func podmanCmdCtx(ctx context.Context, args ...string) *exec.Cmd {
	full := make([]string, 0, 2+len(args))
	full = append(full, "-n", "podman")
	full = append(full, args...)
	return exec.CommandContext(ctx, "sudo", full...)
}

// podmanBuild builds a container image using rootful podman (via sudo).
func podmanBuild(dir string, log *logBuf, imageName string) error {
	cmd := podmanCmd("build", "-t", imageName, ".")
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

// podmanImageExists returns true if the named image exists in the rootful podman store.
func podmanImageExists(image string) bool {
	return podmanCmd("image", "exists", image).Run() == nil
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

// writeEnvFileForBuild writes service env vars to .env and .env.local in
// workDir so that frameworks which read env vars at build time (Next.js,
// Vite, Create React App, etc.) can access them during `npm run build`.
// Secret values are decrypted before writing.
// Both .env and .env.local are written so Next.js and other tools pick them up.
func writeEnvFileForBuild(db *sql.DB, svcID int64, jwtSecret, workDir string) error {
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
	content := []byte(sb.String())
	if len(content) == 0 {
		return nil
	}
	// Write both .env and .env.local (Next.js prefers .env.local)
	os.WriteFile(filepath.Join(workDir, ".env"), content, 0600)        //nolint
	os.WriteFile(filepath.Join(workDir, ".env.local"), content, 0600)  //nolint
	return nil
}
