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
	"crypto/tls"
	"database/sql"
	"encoding/json"
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
	"strconv"
	"strings"
	"sync"
	"time"

	appCrypto "github.com/ojhapranjal26/featherdeploy/backend/internal/crypto"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/pki"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/caddy"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/detect"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/netdaemon"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/coordination"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/transfer"
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

	dbQueueOnce sync.Once
	dbJobCh     chan databaseJob
)

// networkMu serializes all per-project network operations (create / rm / inspect)
// across concurrent deployments and the startup reconcile goroutine.
// Using a per-project striped lock avoids blocking unrelated projects.
var (
	networkMuMap sync.Map // key: int64 projectID → *sync.Mutex
)

// NetDaemon is the in-process TCP proxy daemon.  Set once at startup via
// SetNetDaemon so the deployment pipeline can register and deregister services
// without importing the full netdaemon package in callers.
var NetDaemon *netdaemon.Daemon
var EtcdClient *coordination.Client

// SetEtcdClient injects the coordination client instance.
func SetEtcdClient(c *coordination.Client) { EtcdClient = c }

// cgroupResourcesOnce / cgroupResourcesOK cache whether the host supports
// cgroup v2 cpu+memory controllers in rootless Podman containers.
// On cgroup v1 (or cgroup v2 without user delegation), passing --cpus/--memory
// to `podman run` causes crun to fail immediately (exit 127 in < 1 ms) before
// the container process ever starts.  We detect this once and skip those flags
// when they are not supported.
var (
	cgroupResourcesOnce sync.Once
	cgroupResourcesOK   bool
)

// cgroupV2ResourcesAvailable returns true when the running process's own
// cgroup has the cpu and memory controllers enabled in its subtree_control,
// meaning child cgroups (i.e. Podman containers) can use --cpus/--memory.
//
// Why not just check /sys/fs/cgroup/cgroup.controllers?
// That file lists controllers available at the ROOT cgroup — it is always
// populated on a cgroup v2 system regardless of delegation. What matters
// for rootless Podman running inside a systemd service is whether the
// SERVICE's own cgroup has Delegate=yes set. Without Delegate=yes, systemd
// keeps cpu/memory out of the service's cgroup.subtree_control, and crun
// cannot create sub-cgroups with those controllers → exit 127.
func cgroupV2ResourcesAvailable() bool {
	cgroupResourcesOnce.Do(func() {
		// 1. Find our own cgroup path via /proc/self/cgroup.
		//    On cgroupv2 the entry is "0::<path>"; absent on cgroupv1/hybrid.
		cgroupData, err := os.ReadFile("/proc/self/cgroup")
		if err != nil {
			cgroupResourcesOK = false
			return
		}
		var myPath string
		for _, line := range strings.Split(strings.TrimSpace(string(cgroupData)), "\n") {
			if strings.HasPrefix(line, "0::") {
				myPath = strings.TrimSpace(strings.TrimPrefix(line, "0::"))
				break
			}
		}
		if myPath == "" {
			// No unified hierarchy entry → cgroupv1 or hybrid mode.
			cgroupResourcesOK = false
			return
		}

		// 2. Read cgroup.subtree_control for our own cgroup.
		//    This tells us whether cpu+memory are enabled for CHILD cgroups
		//    (which is exactly what Podman needs to apply resource limits).
		subtreeFile := filepath.Join("/sys/fs/cgroup", myPath, "cgroup.subtree_control")
		ctrl, err := os.ReadFile(subtreeFile)
		if err != nil {
			// Our own cgroup doesn't have subtree_control — likely no delegation.
			cgroupResourcesOK = false
			return
		}
		s := string(ctrl)
		cgroupResourcesOK = strings.Contains(s, "cpu") && strings.Contains(s, "memory")
	})
	return cgroupResourcesOK
}

// SetNetDaemon injects the daemon instance.  Call once from main after
// netdaemon.New() and ReconcileRegistered() have returned.
func SetNetDaemon(d *netdaemon.Daemon) { NetDaemon = d }

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
func InitQueue(db *sql.DB, jwtSecret string, concurrency int) {
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
			go safeDeployWorker()
		}
		slog.Info("deployment queue started", "workers", concurrency)

		// Start a background janitor to re-enqueue orphaned 'pending' jobs.
		// This handles cases where the server was restarted or a job was dropped.
		go janitor(db, jwtSecret)
	})
}

type databaseJob struct {
	db        *sql.DB
	jwtSecret string
	taskID    int64
	dbID      int64
}

func InitDatabaseQueue(db *sql.DB, jwtSecret string, concurrency int) {
	dbQueueOnce.Do(func() {
		if concurrency < 1 {
			concurrency = 1
		}
		dbJobCh = make(chan databaseJob, 512)
		for i := 0; i < concurrency; i++ {
			go safeDatabaseWorker()
		}
		slog.Info("database task queue started", "workers", concurrency)
	})
}

func safeDatabaseWorker() {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("database worker panic recovered — respawning", "panic", r)
			go safeDatabaseWorker()
		}
	}()

	for job := range dbJobCh {
		// Transition: pending → running
		job.db.Exec(`UPDATE database_tasks SET status='running', started_at=datetime('now'), updated_at=datetime('now') WHERE id=?`, job.taskID)

		err := RunDatabaseTask(job.db, job.jwtSecret, job.taskID, job.dbID)
		
		finishedAt := time.Now().UTC().Format("2006-01-02 15:04:05")
		if err != nil {
			job.db.Exec(`UPDATE database_tasks SET status='failed', error_message=?, finished_at=?, updated_at=datetime('now') WHERE id=?`, err.Error(), finishedAt, job.taskID)
		} else {
			job.db.Exec(`UPDATE database_tasks SET status='completed', progress=100, finished_at=?, updated_at=datetime('now') WHERE id=?`, finishedAt, job.taskID)
		}
	}
}

func EnqueueDatabaseTask(db *sql.DB, jwtSecret string, taskID, dbID int64) {
	if dbJobCh == nil {
		go func() {
			finishedAt := time.Now().UTC().Format("2006-01-02 15:04:05")
			db.Exec(`UPDATE database_tasks SET status='running', started_at=datetime('now'), updated_at=datetime('now') WHERE id=?`, taskID)
			err := RunDatabaseTask(db, jwtSecret, taskID, dbID)
			if err != nil {
				db.Exec(`UPDATE database_tasks SET status='failed', error_message=?, finished_at=?, updated_at=datetime('now') WHERE id=?`, err.Error(), finishedAt, taskID)
			} else {
				db.Exec(`UPDATE database_tasks SET status='completed', progress=100, finished_at=?, updated_at=datetime('now') WHERE id=?`, finishedAt, taskID)
			}
		}()
		return
	}
	select {
	case dbJobCh <- databaseJob{db: db, jwtSecret: jwtSecret, taskID: taskID, dbID: dbID}:
	default:
		db.Exec(`UPDATE database_tasks SET status='failed', error_message='database task queue is full', finished_at=datetime('now') WHERE id=?`, taskID)
	}
}

func janitor(db *sql.DB, jwtSecret string) {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		// Cleanup: mark any jobs that have been 'running' for more than 45 minutes as failed.
		// These are likely orphaned by a previous crash or a logic error that bypassed the 30m timeout.
		now := time.Now().UTC().Format("2006-01-02 15:04:05")
		_, _ = db.Exec(`UPDATE deployments 
			SET status='failed', finished_at=?, error_message='deployment timed out (orphaned)'
			WHERE status='running' AND started_at < datetime('now', '-45 minutes')`, now)

		// Prune old logs for all services to keep DB size manageable.
		var svcIDs []int64
		rows, err := db.Query(`SELECT id FROM services`)
		if err == nil {
			for rows.Next() {
				var id int64
				if err := rows.Scan(&id); err == nil {
					svcIDs = append(svcIDs, id)
				}
			}
			rows.Close()
		}
		for _, id := range svcIDs {
			trimOldDeployLogs(db, id)
		}
	}
}

func safeDeployWorker() {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("deploy worker panic recovered — respawning", "panic", r)
			go safeDeployWorker() // respawn
		}
	}()

	for job := range jobCh {
		// Transition: pending → running
		job.db.Exec( //nolint
			`UPDATE deployments SET status='running', started_at=datetime('now') WHERE id=?`, job.depID)
		job.db.Exec( //nolint
			`UPDATE services SET status='deploying', updated_at=datetime('now') WHERE id=?`, job.svcID)
		
		// Each deployment has a global timeout of 30 minutes to prevent a hung
		// build or clone from blocking a worker indefinitely.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		Run(ctx, job.db, job.jwtSecret, job.depID, job.svcID, job.userID)
		cancel()
	}
}

// isAlreadyBuilding returns true if the service has another deployment
// currently pending or running that is NOT 'stale' (started/created in the last 10 mins).
func isAlreadyBuilding(db *sql.DB, svcID, excludeDepID int64) bool {
	var count int
	// We ignore jobs older than 10 minutes because they are likely orphaned or stuck.
	// The 30-minute global timeout in Run() will eventually mark them as failed,
	// but we want to allow new deployments to proceed if the previous one is clearly not progressing.
	_ = db.QueryRow(
		`SELECT COUNT(*) FROM deployments 
		 WHERE service_id=? AND status IN ('pending', 'running') 
		 AND id != ? AND created_at > datetime('now', '-10 minutes')`,
		svcID, excludeDepID,
	).Scan(&count)
	return count > 0
}

// Enqueue queues a deployment for execution. The deployment must already exist in
// the DB with status='pending'. A worker transitions it to 'running' when it is
// picked up. If the queue channel is full, the deployment is failed immediately.
func Enqueue(db *sql.DB, jwtSecret string, depID, svcID, userID int64) {
	if isAlreadyBuilding(db, svcID, depID) {
		markFailed(db, depID, svcID, "A deployment for this service is already in progress.")
		return
	}

	if jobCh == nil {
		// Fallback: queue not initialised — run directly in a goroutine
		go func() {
			db.Exec(`UPDATE deployments SET status='running', started_at=datetime('now') WHERE id=?`, depID) //nolint
			db.Exec(`UPDATE services SET status='deploying', updated_at=datetime('now') WHERE id=?`, svcID) //nolint
			Run(context.Background(), db, jwtSecret, depID, svcID, userID)
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
func Run(ctx context.Context, db *sql.DB, jwtSecret string, depID, svcID, userID int64) {
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
	var targetNodeID string
	var oldNodeIDNull, oldCIDNull sql.NullString

	err := db.QueryRow(
		`SELECT s.project_id, s.name, s.repo_url, s.repo_branch, s.repo_folder, s.framework, s.build_command, s.start_command,
		        s.app_port, s.host_port, d.target_node_id, s.node_id, s.container_id
		 FROM services s
		 JOIN deployments d ON d.service_id = s.id
		 WHERE d.id=?`, depID,
	).Scan(&projectID, &svcName, &repoURL, &repoBranch, &repoFolder, &framework, &buildCmd, &startCmd,
		&appPort, &hostPortNull, &targetNodeID, &oldNodeIDNull, &oldCIDNull)
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

	// ── 1a. Select Node ──────────────────────────────────────────────────────
	// Only the Brain (local host) should perform scheduling.
	// If we are already running on a worker, we skip this.
	myID, _ := os.Hostname()
	// Check if this node is the intended target
	actualNodeID, schedErr := SelectTargetNode(db, targetNodeID)
	if schedErr != nil {
		log.add("ERROR: scheduling failed: %v", schedErr)
		markFailed(db, depID, svcID, log.text())
		return
	}


	db.Exec(`UPDATE deployments SET node_id=? WHERE id=?`, actualNodeID, depID)
	db.Exec(`UPDATE services SET node_id=? WHERE id=?`, actualNodeID, svcID)
	log.add("[scheduler] deploying locally on node %q", actualNodeID)

	// ── 1b. Stop existing container (possibly on another node) ───────────────
	oldCID := oldCIDNull.String
	oldNodeID := oldNodeIDNull.String

	if oldCID != "" {
		if oldNodeID != "" && oldNodeID != actualNodeID && oldNodeID != "main" {
			log.add("[orchestrator] migration detected: stopping container %s on old node %s...", oldCID[:12], oldNodeID)
			if err := stopContainerOnNode(db, oldNodeID, oldCID); err != nil {
				log.add("[orchestrator] warning: could not stop old container: %v", err)
			}
		} else {
			log.add("[podman] stopping existing local container %s...", oldCID[:12])
			removeContainerIfExistsCtx(ctx, oldCID)
		}
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
		repoURL = injectGitHubAppToken(ctx, db, repoURL, log)
		if strings.HasPrefix(repoURL, "https://github.com/") {
			// App token not injected — try user OAuth token
			repoURL = injectUserOAuthToken(ctx, db, userID, repoURL, log)
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
		cloneErr := gitClone(ctx, workDir, sshKeyFile, repoURL, repoBranch, log)
		if cloneErr != nil {
			// Check if we were cancelled by the global timeout
			if ctx.Err() != nil {
				log.add("ERROR: deployment timed out during git clone")
				markFailed(db, depID, svcID, log.text())
				return
			}
			// Retry without explicit branch (use repo default)
			log.add("[clone] branch %q not found — retrying with default branch", repoBranch)
			os.RemoveAll(workDir)
			workDir2, _ := os.MkdirTemp(buildTmpDir(), fmt.Sprintf("fd-dep-%d-*", depID))
			workDir = workDir2
			if err2 := gitCloneDefault(ctx, workDir, sshKeyFile, repoURL, log); err2 != nil {
				log.add("ERROR: git clone failed: %v", err2)
				markFailed(db, depID, svcID, log.text())
				return
			}
		}

		// Capture the actual commit SHA and update the deployment record
		if sha, err := gitRevParse(ctx, workDir); err == nil && sha != "" {
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
		// Only run detection if key fields are missing.
		if framework == "" || strings.TrimSpace(startCmd) == "" {
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
	}

	// ── 4a. Persist configuration ───────────────────────────────────────────
	// Save the effective configuration back to the service record.
	// This ensures future deploys use these settings, and changes are visible in the UI.
	db.Exec(`UPDATE services SET framework=?, build_command=?, start_command=?, app_port=? WHERE id=?`,
		framework, buildCmd, startCmd, appPort, svcID)

	// ── 5. Host build step (only when repo ships its own Dockerfile) ──────────
	// Package-manager commands (pip install, npm install, cargo build, …) must
	// execute inside the correct runtime image, not on the host server which
	// may not have those runtimes installed. When we auto-generate the
	// Dockerfile the build_command is embedded as a Dockerfile RUN instruction
	// so it runs inside the base image. Only run on the host when the repo
	// ships its own Dockerfile, where the user may rely on host-built artefacts.
	if strings.TrimSpace(buildCmd) != "" && repoHasDockerfile {
		log.add("[build] %s", buildCmd)
		if err := runShell(ctx, workDir, sshKeyFile, buildCmd, log); err != nil {
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

	// ── 6a. Dispatch to worker node ───────────────────────────────────────────
	if actualNodeID != "main" && actualNodeID != myID {
		// Optimization: If the worker node is connected and healthy, we can choose to
		// either transfer a pre-built artifact or let the node clone directly.
		// For now, we use artifact transfer by default for maximum reliability across
		// different node environments, but we prioritize the tunnel for the transfer.
		
		log.add("[orchestrator] packaging fully resolved source tree for transfer to %s", actualNodeID)
		
		tarPath := filepath.Join(buildTmpDir(), fmt.Sprintf("artifact-transfer-%d.tar.gz", depID))
		if err := compressWorkDir(workDir, tarPath); err != nil {
			log.add("ERROR: failed to package source tree: %v", err)
			markFailed(db, depID, svcID, log.text())
			return
		}
		defer os.Remove(tarPath)

		destPath := fmt.Sprintf("/var/lib/featherdeploy/artifacts/svc-%d/dep-%d.tar.gz", svcID, depID)
		if err := SendArtifactToNode(db, actualNodeID, depID, tarPath, destPath); err != nil {
			// Fallback: If artifact transfer fails (e.g. timeout), tell the node to try cloning directly.
			log.add("[orchestrator] artifact transfer failed, instructing node to clone directly from git...")
			if err := dispatchToNode(db, actualNodeID, depID, svcID, userID, jwtSecret); err != nil {
				log.add("ERROR: dispatch failed: %v", err)
				markFailed(db, depID, svcID, log.text())
				return
			}
			return
		}

		db.Exec(`UPDATE deployments SET deploy_type='artifact', artifact_path=? WHERE id=?`, destPath, depID)
		
		log.add("[scheduler] dispatching deployment to node %q", actualNodeID)
		if err := dispatchToNode(db, actualNodeID, depID, svcID, userID, jwtSecret); err != nil {
			log.add("ERROR: dispatch failed: %v", err)
			markFailed(db, depID, svcID, log.text())
			return
		}
		return
	}

	// ── 6b. Podman build ──────────────────────────────────────────────────────
	imageName := fmt.Sprintf("featherdeploy/svc-%d:dep-%d", svcID, depID)
	stableImage := fmt.Sprintf("featherdeploy/svc-%d:stable", svcID)
	log.add("[podman] building image %s", imageName)
	buildErr := podmanBuild(ctx, workDir, log, imageName)
	var usingFallback bool
	if buildErr != nil {
		if ctx.Err() != nil {
			log.add("ERROR: deployment timed out during podman build")
			markFailed(db, depID, svcID, log.text())
			return
		}
		log.add("ERROR: podman build failed: %v", buildErr)
		// ── Fallback: use last stable image if available ──────────────────────
		var lastImage string
		db.QueryRow(`SELECT last_image FROM services WHERE id=?`, svcID).Scan(&lastImage) //nolint
		if lastImage != "" && podmanImageExistsCtx(ctx, lastImage) {
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
	if containerExistsCtx(ctx, cName) {
		log.add("[podman] stopping container %s (grace=10s, hard limit=20s)", cName)
		stopCtx, stopCancel := context.WithTimeout(ctx, 20*time.Second)
		podmanCmdCtx(stopCtx, "stop", "--time", "10", cName).Run() //nolint
		stopCancel()
		podmanCmdCtx(ctx, "rm", "-f", cName).Run() //nolint
		log.add("[podman] container %s removed", cName)
	}

	// ── 8. Collect env vars ───────────────────────────────────────────────────
	envArgs := collectEnvArgs(db, svcID, jwtSecret)
	// Rewrite database alias URLs in user-supplied env vars so they resolve
	// inside slirp4netns (e.g. @ssc:5432/ → @10.0.2.2:clusterPort/).
	var firstDBConnURL string
	envArgs, firstDBConnURL = rewriteAndInjectDBEnvArgs(db, projectID, jwtSecret, envArgs)
	// Auto-inject DATABASE_URL if the user hasn't set one.
	if firstDBConnURL != "" && !sliceContainsKeyPrefix(envArgs, "DATABASE_URL=") {
		envArgs = append(envArgs, "-e", "DATABASE_URL="+firstDBConnURL)
	}
	// Auto-inject connection URLs for running databases in the same project.
	envArgs = append(envArgs, collectProjectDBEnvArgs(db, projectID, jwtSecret)...)
	mountArgs := collectProjectDBMountArgs(db, projectID)

	// ── 8b. Inject env vars for project-peer services via fdnet ───────────────
	// FDNet replaces Podman named-bridge networks: each container uses
	// slirp4netns (universally available) and service-to-service discovery is
	// provided by injecting <SVC>_HOST / <SVC>_PORT / <SVC>_URL env vars that
	// route through the in-process TCP proxy daemon.
	if NetDaemon != nil {
		envArgs = append(envArgs, NetDaemon.EnvVarsForPeers(projectID, svcName)...)
	}

	// ── 8c. Inject storage env vars ──────────────────────────────────────────
	// For each storage this service has access to, inject:
	//   STORAGE_{NAME}_KEY, STORAGE_{NAME}_BUCKET, STORAGE_{NAME}_ENDPOINT
	envArgs = append(envArgs, collectStorageEnvArgs(db, svcID, jwtSecret)...)

	// ── 9. Run new container ──────────────────────────────────────────────────

	// With slirp4netns the container gets its own network namespace.
	// PORT=hostPort is injected so the app listens on hostPort inside the
	// container; -p 127.0.0.1:hostPort:hostPort tells rootlessport to bind
	// 127.0.0.1:hostPort in the system's network namespace and forward to
	// the container's hostPort. fdnet dials 127.0.0.1:hostPort and reaches
	// the app through rootlessport.
	runArgs := []string{
		"run", "-d",
		// --replace atomically stops and removes any existing container with the
		// same name before creating the new one.  This is the safety net for the
		// rare case where the explicit stop/rm in step 7 above fails silently
		// (e.g. the container is in a transitional state), which would otherwise
		// cause podman run to fail with exit 125 "name already in use".
		"--replace",
		"--name", cName,
		"--restart", "unless-stopped",
		// NOTE: --log-opt max-size/max-file is intentionally omitted.
		// In rootless Podman, log rotation via conmon requires a minimum conmon
		// version and correct cgroup delegation. When unsupported, it causes
		// the container's stdout/stderr to not be captured (empty podman logs)
		// and can produce spurious exit-127 failures for the container process.
		// The default k8s-file driver without rotation is sufficient and reliable.
		// Use the k8s-file log driver explicitly so podman logs always works in
		// rootless mode. Without this, some distributions default to journald
		// which may be inaccessible to the rootless featherdeploy user.
		"--log-driver", "k8s-file",
	}
	if appPort <= 0 {
		appPort = 8080
	}
	// Apply slirp4netns networking.
	runArgs = append(runArgs, netdaemon.NetworkArgs()...)

	// IMPORTANT: bind on 0.0.0.0 (all interfaces), NOT 127.0.0.1.
	// rootlessport reliably binds on all interfaces; with the 127.0.0.1
	// restriction it sometimes silently fails to bind due to nftables/netavark
	// rules conflicting with loopback forwarding, leaving the port unreachable
	// with i/o timeout instead of connection refused.
	// External access to the host port range is blocked by iptables INPUT DROP
	// rules installed by build.sh, so only localhost (fdnet/Caddy) can reach it.
	runArgs = append(runArgs, "-p", fmt.Sprintf("%d:%d", hostPort, appPort))
	runArgs = append(runArgs, mountArgs...)
	// Inject PORT so apps honour the allocated host port inside the container.
	runArgs = append(runArgs, envArgs...)
	runArgs = append(runArgs, "-e", fmt.Sprintf("PORT=%d", appPort))
	runArgs = append(runArgs, imageName)

	log.add("[podman] podman run -d --name %s --network slirp4netns -p %d:%d -e PORT=%d %s", cName, hostPort, appPort, appPort, imageName)
	out, err := podmanCmdCtx(ctx, runArgs...).CombinedOutput()
	if err != nil {
		log.add("ERROR: podman run failed: %v\n%s", err, strings.TrimSpace(string(out)))
		markFailed(db, depID, svcID, log.text())
		return
	}
	newContainerID := strings.TrimSpace(string(out))

	// Register with fdnet so sibling services can reach this container via
	// their <SVC>_HOST / <SVC>_PORT env vars.
	// With host networking, fdnet dials 127.0.0.1:hostPort directly.
	clusterPortVal := 0
	if NetDaemon != nil {
		if cp, regErr := NetDaemon.Register(projectID, svcName, "127.0.0.1", newContainerID, hostPort, appPort); regErr != nil {
			log.add("[fdnet] warning: could not register service %q: %v", svcName, regErr)
		} else {
			clusterPortVal = cp
			log.add("[fdnet] service %q registered (hostPort=%d clusterPort=%d)", svcName, hostPort, cp)
			// Persist the clusterPort so Caddy's buildConfig can route through the
			// fdnet Go TCP proxy (a real kernel socket on 0.0.0.0:cp) instead of
			// directly to the slirp4netns userspace port-forward (127.0.0.1:hostPort).
			// This eliminates 502 errors caused by slirp4netns helper binding failures.
			db.Exec(`UPDATE services SET cluster_port=? WHERE id=?`, cp, svcID) //nolint
		}
	}

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

	// Cluster discovery registration (Etcd)
	// For worker nodes: register node_id+port so Caddy can route via tunnel proxy.
	// For brain/main: register the actual IP+port via fdnet cluster proxy.
	if EtcdClient != nil {
		regPort := hostPort
		if clusterPortVal > 0 {
			regPort = clusterPortVal
		}
		// actualNodeID is either "main" or "node-N"
		// If this is a remote worker, we register node_id as the "ip" field
		// so Caddy's buildConfig can identify it as a tunnel-routed service.
		regIP := detectNodeIP(db)
		// For non-main nodes, the registered "ip" is the node_id prefixed with "@"
		// so the Caddy config builder can detect it and call EnsureServiceProxy.
		if actualNodeID != "main" {
			regIP = "@" + actualNodeID // e.g. "@node-2"
		}
		discoveryCtx, discoveryCancel := context.WithTimeout(ctx, 10*time.Second)
		if err := EtcdClient.RegisterService(discoveryCtx, projectID, svcName, regIP, regPort); err != nil {
			log.add("[etcd] warning: could not register service for discovery: %v", err)
		} else {
			log.add("[etcd] service registered successfully for discovery (node=%s, port=%d)", actualNodeID, regPort)
		}
		discoveryCancel()
	}

	// Wait for the container's published host port to accept TCP connections
	// before reloading Caddy.  The app now binds hostPort directly, so this
	// probe should succeed as soon as the container has started listening.
	go func(hPort int, dID, sID int64) {
		const (
			maxWait  = 30 * time.Second
			interval = 2 * time.Second
		)
		deadline := time.Now().Add(maxWait)
		for time.Now().Before(deadline) {
			conn, err := net.DialTimeout("tcp",
				fmt.Sprintf("127.0.0.1:%d", hPort), 1*time.Second)
			if err == nil {
				conn.Close()
				// App is up — reload Caddy so traffic starts flowing immediately.
				caddy.PublishRoutes(db)
				return
			}

			time.Sleep(interval)
		}

		// Port never responded in 30 s — append actionable diagnostic and reload anyway.
		// If this happens now it indicates the app did not honor PORT=hostPort or
		// the container exited before binding the socket.
		logs := containerRecentLogs(cName, 30)
		db.Exec( //nolint
			`UPDATE deployments SET deploy_log=deploy_log||? WHERE id=?`,
			fmt.Sprintf(
				"\nWARNING: port %d did not respond within 30 s after container start."+
					"\n  The app is not listening on PORT=%d."+
					"\n  FIX: ensure the start command honors the PORT env var and binds it."+
					"\n  DEBUG: run \"podman logs fd-svc-%d\" on your server to see the startup output."+
					"\n  Last container output:"+
					"\n%s",
					hPort, hPort, sID, logs),
			dID)
		caddy.PublishRoutes(db)
	}(hostPort, depID, svcID)

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

func gitClone(ctx context.Context, workDir, sshKeyFile, repoURL, branch string, log *logBuf) error {
	return runCaptureWithSSH(ctx, workDir, sshKeyFile, log, "git", "clone", "--depth", "1", "--branch", branch, "--", repoURL, workDir)
}

func gitCloneDefault(ctx context.Context, workDir, sshKeyFile, repoURL string, log *logBuf) error {
	return runCaptureWithSSH(ctx, workDir, sshKeyFile, log, "git", "clone", "--depth", "1", "--", repoURL, workDir)
}

// runCapture runs a command, appending its combined output to log.
func runCapture(ctx context.Context, dir string, log *logBuf, name string, args ...string) error {
	return runCaptureWithSSH(ctx, dir, "", log, name, args...)
}

// runCaptureWithSSH is like runCapture but also sets GIT_SSH_COMMAND.
func runCaptureWithSSH(ctx context.Context, dir, sshKeyFile string, log *logBuf, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
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
func runShell(ctx context.Context, dir, sshKeyFile, command string, log *logBuf) error {
	return runCaptureWithSSH(ctx, dir, sshKeyFile, log, "/bin/sh", "-c", command)
}

// gitRevParse returns the full commit SHA (HEAD) of the cloned repo.
func gitRevParse(ctx context.Context, workDir string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", workDir, "rev-parse", "HEAD").Output()
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

func containerExistsCtx(ctx context.Context, name string) bool {
	out, err := podmanCmdCtx(ctx, "ps", "-a", "--filter", "name=^"+name+"$", "--format", "{{.Names}}").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), name)
}

func containerExists(name string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return containerExistsCtx(ctx, name)
}

// containerExitStatus returns (exit_code, true) when the named container has
// stopped or exited, or (0, false) when it is still running or not found.
// Used by the port probe to detect app crashes early instead of waiting 90 s.
func containerExitStatus(name string) (int, bool) {
	out, err := podmanCmd("inspect", "--format", "{{.State.Status}}|{{.State.ExitCode}}", name).Output()
	if err != nil {
		return 0, false
	}
	parts := strings.SplitN(strings.TrimSpace(string(out)), "|", 2)
	if len(parts) < 2 {
		return 0, false
	}
	switch parts[0] {
	case "running", "starting", "created", "":
		return 0, false
	}
	code, _ := strconv.Atoi(parts[1])
	return code, true
}

// containerRecentLogs returns the last n lines of the container's combined
// stdout+stderr with each line prefixed by "  " for deployment log readability.
func containerRecentLogs(name string, lines int) string {
	out, err := podmanCmd("logs", "--tail", strconv.Itoa(lines), name).CombinedOutput()
	if err != nil || len(out) == 0 {
		return "  (no output captured)"
	}
	var sb strings.Builder
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		sb.WriteString("  | ")
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
	return sb.String()
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

// collectStorageEnvArgs returns -e KEY=VALUE pairs for all storage buckets
// the service has access to. Injected per storage:
//
//	STORAGE_{NAME}_KEY      = per-service plaintext API key
//	STORAGE_{NAME}_BUCKET   = storage name
//	STORAGE_{NAME}_ENDPOINT = internal URL (http://10.0.2.2:8080/api/storage/{id})
func collectStorageEnvArgs(db *sql.DB, svcID int64, jwtSecret string) []string {
	rows, err := db.Query(`
		SELECT st.id, st.name, sa.enc_service_key
		FROM storage_access sa
		JOIN storages st ON st.id = sa.storage_id
		WHERE sa.service_id = ? AND sa.enc_service_key != ''
	`, svcID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var args []string
	// Get the current brain address for the storage endpoint
	var brainAddr string
	db.QueryRow(`SELECT brain_addr FROM cluster_state WHERE id=1`).Scan(&brainAddr)
	if brainAddr == "" {
		brainAddr = "http://127.0.0.1:8080"
	}
	// Ensure it has /api/storage suffix
	if strings.HasSuffix(brainAddr, "/") {
		brainAddr = brainAddr[:len(brainAddr)-1]
	}

	for rows.Next() {
		var stID int64
		var stName, encSvcKey string
		if err := rows.Scan(&stID, &stName, &encSvcKey); err != nil {
			continue
		}
		plainKey, err := appCrypto.Decrypt(encSvcKey, jwtSecret)
		if err != nil {
			continue
		}
		upper := storageEnvVarName(stName)

		// The endpoint should be reachable from within the container.
		// If brainAddr is localhost/127.0.0.1, we must use 10.0.2.2 (slirp4netns gateway).
		endpoint := brainAddr + fmt.Sprintf("/api/storage/%d", stID)
		if strings.Contains(endpoint, "127.0.0.1") || strings.Contains(endpoint, "localhost") {
			// Replace with gateway
			endpoint = strings.Replace(endpoint, "127.0.0.1", "10.0.2.2", 1)
			endpoint = strings.Replace(endpoint, "localhost", "10.0.2.2", 1)
		}

		args = append(args,
			"-e", fmt.Sprintf("STORAGE_%s_KEY=%s", upper, plainKey),
			"-e", fmt.Sprintf("STORAGE_%s_BUCKET=%s", upper, stName),
			"-e", fmt.Sprintf("STORAGE_%s_ENDPOINT=%s", upper, endpoint),
		)
	}
	return args
}

func storageEnvVarName(s string) string {
	s = strings.ToUpper(s)
	var b strings.Builder
	for _, c := range s {
		if (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			b.WriteRune(c)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

// sliceContainsKeyPrefix returns true if any element in s has the given prefix.
func sliceContainsKeyPrefix(s []string, prefix string) bool {
	for _, v := range s {
		if strings.HasPrefix(v, prefix) {
			return true
		}
	}
	return false
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
		sb.WriteString(fmt.Sprintf("ENV PORT %d\n", appPort))
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
		sb.WriteString(fmt.Sprintf("ENV PORT %d\n", appPort))
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
		sb.WriteString(fmt.Sprintf("ENV PORT %d\n", appPort))
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
				"This usually means the networking backend (netavark) is missing or aardvark-dns is not installed.\n"+
				"  RHEL 9 / AlmaLinux / Rocky: sudo dnf install -y netavark aardvark-dns\n"+
				"  Ubuntu / Debian:             sudo apt-get install -y netavark aardvark-dns",
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
		// The most common cause of "network not found" on Debian/Ubuntu is
		// aardvark-dns not being installed.  netavark requires aardvark-dns to
		// start the per-network DNS forwarder.  When it's missing, netavark exits
		// 127 ("command not found") and Podman maps this to "unable to find network".
		return "Named bridge networks require both netavark AND aardvark-dns:\n" +
			"  Ubuntu / Debian:             sudo apt-get install -y aardvark-dns netavark\n" +
			"  RHEL 9 / AlmaLinux / Rocky: sudo dnf install -y aardvark-dns netavark\n" +
			"  Then run: sudo featherdeploy update"
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
		// Network not found during run typically means one of two things:
		// 1. netavark is installed but aardvark-dns is MISSING — netavark tries
		//    to exec aardvark-dns for DNS setup, gets ENOENT (exit 127), and
		//    Podman maps this to "unable to find network" (NOT a network lookup
		//    failure, despite the message).  This is the most common cause on
		//    Debian/Ubuntu where aardvark-dns is not a required apt dependency.
		// 2. netavark itself is missing.
		if strings.Contains(err.Error(), "127") || strings.Contains(string(out), "127") ||
			strings.Contains(strings.ToLower(string(out)), "network not found") ||
			strings.Contains(strings.ToLower(string(out)), "unable to find network") {
			hint = "\n  Most likely cause: aardvark-dns is not installed." +
				"\n  Fix (Ubuntu/Debian):         sudo apt-get install -y aardvark-dns" +
				"\n  Fix (RHEL/AlmaLinux/Rocky): sudo dnf install -y aardvark-dns" +
				"\n  Then run: sudo featherdeploy update"
		}
		return fmt.Errorf("podman network create %s: %v — %s%s", name, err, strings.TrimSpace(string(out)), hint)
	}

	// Poll up to 15 s for inspect to confirm both JSON and runtime state are ready.
	// netavark bridges are wired asynchronously on some kernels, and rootless
	// podman can take a few seconds to finish registering a freshly created
	// named network before 'run --network' accepts it.
	for i := 0; i < 30; i++ {
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
		"network %s still not ready after 15s\npodman network ls:\n%s\n"+
			"Most likely cause: the rootless network backend is not fully settled yet,\n"+
			"or netavark cannot find its helper binaries.\n"+
			"  Required packages: netavark + aardvark-dns\n"+
			"  Ubuntu / Debian:   sudo apt-get install -y netavark aardvark-dns slirp4netns\n"+
			"  RHEL / Fedora:     sudo dnf install -y netavark aardvark-dns slirp4netns passt\n"+
			"  Then run: sudo systemctl restart featherdeploy && sudo featherdeploy update",
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

// dbConnectionURLFDNet builds a DB connection URL using an explicit host
// address and port, as used by the fdnet TCP proxy (no DNS alias needed).
func dbConnectionURLFDNet(dbType, dbName, dbUser, clearPassword, host string, port int) string {
	esc := urlEscapeCredential
	switch dbType {
	case "postgres":
		return fmt.Sprintf("postgresql://%s:%s@%s:%d/%s",
			esc(dbUser), esc(clearPassword), host, port, dbName)
	case "mysql":
		return fmt.Sprintf("mysql://%s:%s@%s:%d/%s",
			esc(dbUser), esc(clearPassword), host, port, dbName)
	}
	return ""
}

// DBPublicConnectionURL builds the external connection URL for a database that
// has been made publicly accessible. host is the server's public hostname or
// IP address; port is the externally-reachable port (fdnet cluster port preferred).
func DBPublicConnectionURL(dbType, dbName, dbUser, clearPassword, host string, port int) string {
	esc := urlEscapeCredential
	switch dbType {
	case "postgres":
		return fmt.Sprintf("postgresql://%s:%s@%s:%d/%s",
			esc(dbUser), esc(clearPassword), host, port, dbName)
	case "mysql":
		return fmt.Sprintf("mysql://%s:%s@%s:%d/%s",
			esc(dbUser), esc(clearPassword), host, port, dbName)
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

// rewriteAndInjectDBEnvArgs rewrites database alias hostnames in env args to
// the slirp4netns gateway address so connections resolve inside runtime
// containers (e.g. @ssc:5432/ → @10.0.2.2:clusterPort/).
// It also returns the first reachable DB connection URL for auto-injecting
// DATABASE_URL when the user hasn't set one.
// envArgs is the ["-e", "KEY=VALUE", ...] slice from collectEnvArgs.
func rewriteAndInjectDBEnvArgs(db *sql.DB, projectID int64, jwtSecret string, envArgs []string) ([]string, string) {
	type dbInfo struct {
		alias        string
		internalPort int
		reachablePort int
		connURL       string
	}
	rows, err := db.Query(
		`SELECT id, name, db_type, db_name, db_user, db_password, COALESCE(host_port, 0)
		 FROM databases WHERE project_id=? AND status='running'`, projectID)
	if err != nil {
		return envArgs, ""
	}
	defer rows.Close()

	var dbs []dbInfo
	for rows.Next() {
		var dbID int64
		var name, dbType, dbName, dbUser, encPass string
		var hp int
		if rows.Scan(&dbID, &name, &dbType, &dbName, &dbUser, &encPass, &hp) != nil || hp == 0 {
			continue
		}
		clearPass := encPass
		if strings.HasPrefix(encPass, "fdenc:") {
			if p, decErr := appCrypto.Decrypt(encPass[len("fdenc:"):], jwtSecret); decErr == nil {
				clearPass = p
			}
		}
		alias := DBNetworkAlias(name)
		iport := dbTypeInternalPort(dbType)
		reachablePort := hp
		if NetDaemon != nil {
			if cp, ok := NetDaemon.Resolve(projectID, alias); ok {
				reachablePort = cp
			}
		}
		connURL := dbConnectionURLFDNet(dbType, dbName, dbUser, clearPass, netdaemon.SlirpGateway, reachablePort)
		dbs = append(dbs, dbInfo{alias: alias, internalPort: iport, reachablePort: reachablePort, connURL: connURL})
	}
	if len(dbs) == 0 {
		return envArgs, ""
	}

	// Rewrite alias:internalPort → 10.0.2.2:reachablePort in env var values.
	result := make([]string, len(envArgs))
	copy(result, envArgs)
	for i := 0; i < len(result)-1; i++ {
		if result[i] != "-e" {
			continue
		}
		kv := result[i+1]
		eqIdx := strings.IndexByte(kv, '=')
		if eqIdx < 0 {
			i++
			continue
		}
		v := kv[eqIdx+1:]
		for _, d := range dbs {
			if d.internalPort > 0 {
				old := fmt.Sprintf("@%s:%d", d.alias, d.internalPort)
				repl := fmt.Sprintf("@%s:%d", netdaemon.SlirpGateway, d.reachablePort)
				v = strings.ReplaceAll(v, old, repl)
			}
		}
		result[i+1] = kv[:eqIdx+1] + v
		i++
	}
	return result, dbs[0].connURL
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
		// Prefer fdnet cluster-port URL (works without aardvark-dns / named networks).
		var connURL string
		if NetDaemon != nil {
			if cp, ok := NetDaemon.Resolve(projectID, alias); ok {
				connURL = dbConnectionURLFDNet(dbType, dbName, dbUser, clearPass, netdaemon.SlirpGateway, cp)
			}
		}
		if connURL == "" {
			// Fallback: DNS-alias URL (legacy / non-fdnet environments).
			connURL = DBConnectionURL(dbType, dbName, dbUser, clearPass, alias)
		}
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

// dbLog is a tiny log buffer for the database startup sequence.  Each call to
// add() appends a timestamped line and immediately flushes it to the DB so the
// SSE stream in the handler can deliver live progress to the browser.
type dbLog struct {
	mu    sync.Mutex
	lines []string
	db    *sql.DB
	id    int64
}

func newDBLog(db *sql.DB, id int64) *dbLog { return &dbLog{db: db, id: id} }

func (l *dbLog) add(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	l.mu.Lock()
	l.lines = append(l.lines, line)
	text := strings.Join(l.lines, "\n")
	l.mu.Unlock()
	slog.Info(line)
	// Write to start_log immediately so the SSE stream can tail it.
	l.db.Exec( //nolint
		`UPDATE databases SET start_log=?, updated_at=datetime('now') WHERE id=?`, text, l.id)
}

func (l *dbLog) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

// StartDatabase pulls the database image and starts the container in the
// project's network. Safe to call for both new and stopped databases.
// StartDatabase launches (or re-launches) the container for a managed database.
// It is the top-level entry point; retries are handled internally.
func StartDatabase(db *sql.DB, jwtSecret string, dbID int64) error {
	return startDatabaseRetrying(db, jwtSecret, dbID, 0)
}

// startDatabaseRetrying is the inner implementation of StartDatabase.
// attempt tracks how many times we have already retried after an exit-127;
// it must never exceed maxDBStartAttempts.
const maxDBStartAttempts = 2

func startDatabaseRetrying(db *sql.DB, jwtSecret string, dbID int64, attempt int) error {
	var projectID int64
	var name, dbType, dbVersion, dbName, dbUser, encPass string
	var hostPortNull sql.NullInt64
	var oldNodeID, targetNodeID string
	err := db.QueryRow(
		`SELECT project_id, name, db_type, db_version, db_name, db_user, db_password,
		        host_port, node_id, target_node_id
		 FROM databases WHERE id=?`, dbID,
	).Scan(&projectID, &name, &dbType, &dbVersion, &dbName, &dbUser, &encPass,
		&hostPortNull, &oldNodeID, &targetNodeID)
	if err != nil {
		return fmt.Errorf("load database config: %w", err)
	}

	log := newDBLog(db, dbID)

	// ─── Scheduler ───────────────────────────────────────────────────────────
	h, _ := os.Hostname()
	actualNodeID := h
	if targetNodeID == "auto" {
		targetNodeID, _ = SelectTargetNode(db, "auto")
		db.Exec(`UPDATE databases SET target_node_id=? WHERE id=?`, targetNodeID, dbID)
	}
	if targetNodeID == "" {
		targetNodeID = "main"
	}

	if targetNodeID != actualNodeID && targetNodeID != "main" {
		log.add("[scheduler] dispatching database to node %q", targetNodeID)
		if err := dispatchDatabaseToNode(db, jwtSecret, targetNodeID, dbID); err != nil {
			log.add("ERROR: dispatch failed: %v", err)
			db.Exec(`UPDATE databases SET status='error', last_error=? WHERE id=?`, err.Error(), dbID)
			return err
		}
		return nil
	}

	log.add("[scheduler] starting locally on node %q", actualNodeID)
	db.Exec(`UPDATE databases SET node_id=? WHERE id=?`, actualNodeID, dbID)

	// ─── Migration ───────────────────────────────────────────────────────────
	if oldNodeID != "" && oldNodeID != actualNodeID && oldNodeID != "main" {
		log.add("[orchestrator] migration detected: pulling database data from old node %s...", oldNodeID)
		if err := migrateDatabaseData(db, jwtSecret, oldNodeID, dbID); err != nil {
			log.add("[orchestrator] warning: migration failed: %v", err)
			// Continue anyway? Or fail? Usually better to fail if data is critical.
			log.add("[orchestrator] database may start with empty data!")
		}
	}

	log.add("[db-start] starting database id=%d name=%q type=%s version=%s", dbID, name, dbType, dbVersion)

	// activeDBEngine is the container image engine to use. For MySQL databases
	// on hosts whose CPU does not support x86-64-v2 (SSE4.2/POPCNT), MySQL 8.4+
	// OracleLinux9 images abort immediately with
	//   "Fatal glibc error: CPU does not support x86-64-v2"
	// MariaDB LTS is MySQL-wire-protocol compatible and compiled for older CPUs.
	activeDBEngine := dbType
	if dbType == "mysql" && !cpuSupportsX8664V2() {
		activeDBEngine = "mariadb"
		log.add("[cpu] ⚠ CPU does not support x86-64-v2 — MySQL 8.4+ OracleLinux9 images require SSE4.2/POPCNT")
		log.add("[cpu] Automatically using MariaDB LTS (MySQL-wire-protocol compatible, same connection URL)")
	}

	clearPass := encPass
	if strings.HasPrefix(encPass, "fdenc:") {
		if p, decErr := appCrypto.Decrypt(encPass[len("fdenc:"):], jwtSecret); decErr == nil {
			clearPass = p
		}
	}
	if dbType == "sqlite" {
		log.add("[db-start] SQLite — no container needed, ensuring volume")
		if err := ensureDatabaseVolume(dbID); err != nil {
			log.add("ERROR: volume create failed: %v", err)
			db.Exec(`UPDATE databases SET status='error', last_error=?, updated_at=datetime('now') WHERE id=?`, err.Error(), dbID) //nolint
			return err
		}
		db.Exec( //nolint
			`UPDATE databases SET status='running', container_id='', host_port=NULL, last_error='', updated_at=datetime('now') WHERE id=?`,
			dbID)
		log.add("[db-start] SQLite volume ready — database is running")
		return nil
	}

	cName := fmt.Sprintf("fd-db-%d", dbID)
	alias := DBNetworkAlias(name)
	image := dbImageName(activeDBEngine, dbVersion) // primary Docker Hub ref (for local-cache check)
	log.add("[db-start] container name: %s  image: %s  alias: %s", cName, image, alias)

	db.Exec(`UPDATE databases SET status='starting', updated_at=datetime('now') WHERE id=?`, dbID) //nolint

	// activeImage is the image ref actually passed to podman run. It equals
	// image when Docker Hub is used; it may differ when a fallback registry
	// (e.g. AWS ECR Public) is used because Docker Hub was rate-limited.
	activeImage := image

	if image != "" && !podmanImageExists(image) {
		pulled, pullErr := pullDBImage(log, activeDBEngine, dbVersion)
		if pullErr != nil {
			errMsg := pullErr.Error()
			log.add("ERROR: all registries failed — %s", errMsg)
			db.Exec(`UPDATE databases SET status='error', last_error=?, updated_at=datetime('now') WHERE id=?`, errMsg, dbID) //nolint
			return fmt.Errorf("pull database image: %w", pullErr)
		}
		activeImage = pulled
	} else if image != "" {
		log.add("[pull] image %s already present locally — skipping pull", image)
	}

	// Stop/remove any existing container from a previous start.
	if containerExists(cName) {
		log.add("[podman] existing container found — stopping and removing %s", cName)
		stopCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		podmanCmdCtx(stopCtx, "stop", "--time", "10", cName).Run() //nolint
		cancel()
		podmanCmd("rm", "-f", cName).Run() //nolint
		log.add("[podman] old container removed")
	}

	// Always assign a host port — fdnet needs to proxy connections to the
	// container even for internal (non-public) databases.
	hostPort := int(hostPortNull.Int64)
	if hostPort <= 0 {
		hostPort = 15000 + int(dbID)
		db.Exec(`UPDATE databases SET host_port=? WHERE id=?`, hostPort, dbID) //nolint
	}

	// Persist data in a named volume so it survives container re-creation.
	// The volume is NEVER deleted by Stop or Restart — only an explicit
	// database deletion with purge=true removes it.
	volumeName := dbVolumeName(dbID)
	log.add("[volume] ensuring persistent data volume %s → %s (data survives stop/restart)", volumeName, dbDataMountPath(dbType, dbVersion))
	if err := ensureDatabaseVolume(dbID); err != nil {
		log.add("ERROR: volume ensure failed: %v", err)
		db.Exec(`UPDATE databases SET status='error', last_error=?, updated_at=datetime('now') WHERE id=?`, err.Error(), dbID) //nolint
		return err
	}
	mountPath := dbDataMountPath(dbType, dbVersion)
	mountSpec := dbVolumeMountSpec(volumeName, mountPath)
	entrypoint := dbContainerEntrypoint(dbType)

	internalPort := dbInternalPort(dbType)

	// All databases are bound to 127.0.0.1 only — they are internal resources
	// accessible only to featherdeploy services via the fdnet proxy.
	// Direct external / internet access is intentionally not supported.
	portBind := fmt.Sprintf("127.0.0.1:%d→%d (internal)", hostPort, internalPort)
	// Resource limits require cgroup v2 with cpu+memory controllers available.
	// On cgroup v1 (or cgroup v2 without user delegation), --cpus/--memory cause
	// crun to exit immediately with code 127 before the container process starts,
	// producing a sub-millisecond death loop with no log output.
	// We probe once at runtime and skip those flags if unsupported.
	useLimits := cgroupV2ResourcesAvailable()
	resourceDesc := "--cpus 2.0  --memory 1g"
	if !useLimits {
		resourceDesc = "(resource limits skipped — cgroup v1 or no delegation)"
	}
	cmdArgs := dbContainerCmdArgs(activeDBEngine)
	effectiveCmd := "(image default)"
	if entrypoint != "" && len(cmdArgs) > 0 {
		effectiveCmd = fmt.Sprintf("%s %s", entrypoint, strings.Join(cmdArgs, " "))
	} else if entrypoint != "" {
		effectiveCmd = entrypoint
	}
	log.add("[podman] run  --name %s  --restart on-failure:10  %s  port %s  image %s  cmd: %s",
		cName, resourceDesc, portBind, activeImage, effectiveCmd)

	runArgs := []string{
		"run", "-d",
		// --replace stops+removes any container that already has this name
		// (e.g. a previous crash-looping instance) so podman run never fails
		// with exit 125 "name already in use".
		"--replace",
		"--name", cName,
		// on-failure:10 caps crash loops at 10 restarts. Unlike unless-stopped,
		// this prevents a bad container from pegging CPU forever while still
		// surviving transient failures (OOM, init races). The counter resets on
		// a successful start or an explicit Stop→Start / Restart from the UI.
		"--restart", "on-failure:10",
		// NOTE: --log-opt max-size/max-file is intentionally omitted.
		// In rootless Podman, log rotation via conmon requires a minimum conmon
		// version and correct cgroup delegation. When unsupported, it causes
		// the container's stdout/stderr to not be captured (empty podman logs)
		// and can produce spurious exit-127 failures for the container process.
		// The default k8s-file driver without rotation is sufficient and reliable.
		// Use the k8s-file log driver explicitly so podman logs always works in
		// rootless mode. Without this, some distributions default to journald
		// which may be inaccessible to the rootless featherdeploy user.
		"--log-driver", "k8s-file",
		"-v", mountSpec,
	}
	if useLimits {
		// Cgroup v2 with cpu+memory delegation confirmed — apply resource caps.
		// --memory-swap intentionally omitted so kernel default applies
		// (2× memory = 1 GB swap), preventing OOM kills during first-run initdb.
		runArgs = append(runArgs, "--cpus", "2.0", "--memory", "1g")
	}
	if entrypoint != "" {
		runArgs = append(runArgs, "--entrypoint", entrypoint)
	}
	// Use slirp4netns instead of Podman named networks (no aardvark-dns needed).
	runArgs = append(runArgs, netdaemon.NetworkArgs()...)
	// Bind on 0.0.0.0 (all interfaces) — rootlessport reliably binds this way.
	// Database privacy is enforced by iptables INPUT DROP rules (installed by
	// build.sh) that block external access to the host/cluster port ranges.
	// Databases are also never given a Caddy route, so they are not reachable
	// via any hostname — only services reach them through fdnet cluster ports.
	runArgs = append(runArgs, "-p", fmt.Sprintf("%d:%d", hostPort, internalPort))
	// Inject the DB engine's environment variables (credentials, database name).
	runArgs = append(runArgs, dbContainerEnvArgs(activeDBEngine, dbVersion, dbName, dbUser, clearPass)...)
	runArgs = append(runArgs, activeImage)
	// Append cmd args (script path + engine binary) after the image name.
	// cmdArgs is already computed above for the log line.
	runArgs = append(runArgs, cmdArgs...)

	out, err := podmanCmd(runArgs...).CombinedOutput()
	outStr := strings.TrimSpace(string(out))
	if err != nil {
		errMsg := fmt.Sprintf("container start failed: %v — %s", err, outStr)
		log.add("ERROR: podman run failed (exit %v): %s", err, outStr)
		log.add("ERROR: check 'podman info' and 'podman system info' for storage/cgroup issues")
		db.Exec(`UPDATE databases SET status='error', last_error=?, updated_at=datetime('now') WHERE id=?`, errMsg, dbID) //nolint
		return fmt.Errorf("podman run database %s: %v — %s", cName, err, outStr)
	}
	containerID := strings.TrimSpace(string(out))
	log.add("[podman] container started  id=%s", func() string {
		if len(containerID) > 12 {
			return containerID[:12]
		}
		return containerID
	}())

	// Register with fdnet so sibling services can reach this database.
	if NetDaemon != nil {
		if cp, regErr := NetDaemon.Register(projectID, alias, "127.0.0.1", containerID, hostPort, internalPort); regErr != nil {
			log.add("[fdnet] WARNING: could not register alias %q: %v", alias, regErr)
			slog.Warn("fdnet: could not register database", "db_id", dbID, "alias", alias, "err", regErr)
		} else {
			log.add("[fdnet] registered alias %q → 127.0.0.1:%d (clusterPort=%d)", alias, hostPort, cp)
			slog.Info("fdnet: database registered", "db_id", dbID, "alias", alias, "clusterPort", cp)
			// Persist the cluster port so TogglePublic and startup reconciliation
			// can use it to open iptables/UFW for the fdnet proxy port.
			db.Exec(`UPDATE databases SET cluster_port=? WHERE id=?`, cp, dbID) //nolint
		}
	}

	db.Exec( //nolint
		`UPDATE databases SET status='running', container_id=?, last_error='', updated_at=datetime('now') WHERE id=?`,
		containerID, dbID)

	// Cluster discovery registration (Etcd)
	if EtcdClient != nil {
		nodeIP := detectNodeIP(db)
		// Use the project-unique alias for discovery
		regPort := hostPort
		var cp int
		db.QueryRow(`SELECT cluster_port FROM databases WHERE id=?`, dbID).Scan(&cp)
		if cp > 0 {
			regPort = cp
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = EtcdClient.RegisterDatabase(ctx, projectID, alias, nodeIP, regPort)
		}()
	}
	log.add("[db-start] ✓ database is running — waiting for engine to initialize (this can take 10–30s for first-run)")
	slog.Info("database container started", "db_id", dbID, "container", cName)

	// Background health check: if the container stops shortly after launch,
	// diagnose the cause and optionally retry.
	//
	// Exit-127 here means the container process never became healthy. A very
	// short-lived exit usually means the runtime/entrypoint failed before the
	// engine could emit logs; longer-lived failures tend to be image-layer or
	// engine init problems. We log the distinction, but both are retried with a
	// fresh pull because this environment has already proven to hit rootless
	// startup edge-cases that are not fixed by a single static hint.
	if activeImage != "" {
		go func(cID, pulledImage string) {
			time.Sleep(6 * time.Second)
			inspOut, inspErr := podmanCmd("inspect", "--format",
				"{{.State.Running}}|{{.State.ExitCode}}|{{.RestartCount}}|{{.State.StartedAt.Unix}}|{{.State.FinishedAt.Unix}}", cID).Output()
			if inspErr != nil {
				return // container not found (already removed) — nothing to do
			}
			parts := strings.SplitN(strings.TrimSpace(string(inspOut)), "|", 5)
			if len(parts) < 2 || parts[0] != "false" {
				return // still running, nothing to do
			}
			exitCode := parts[1]
			restarts := ""
			if len(parts) >= 3 {
				restarts = parts[2]
			}

			// Detect whether the container died before its process could start.
			// StartedAt.Unix == FinishedAt.Unix (or delta < 2 s) means crun itself
			// failed (e.g. cgroup resource limit rejected, no Delegate=yes in the
			// systemd unit) — NOT a process-level error from MySQL/Postgres.
			subSecondExit := false
			if len(parts) >= 5 {
				startUnix, e1 := strconv.ParseInt(parts[3], 10, 64)
				finishUnix, e2 := strconv.ParseInt(parts[4], 10, 64)
				if e1 == nil && e2 == nil && finishUnix-startUnix < 2 {
					subSecondExit = true
				}
			}

			log.add("[health] container stopped after start  exitCode=%s  restarts=%s  attempt=%d/%d",
				exitCode, restarts, attempt+1, maxDBStartAttempts+1)

			// Capture whatever the container wrote to stdout/stderr —
			// these are the engine's own error messages and the most
			// useful diagnostic for the user.
			if podLogs, _ := podmanCmd("logs", "--tail", "50", cID).CombinedOutput(); len(podLogs) > 0 {
				for _, line := range strings.Split(strings.TrimSpace(string(podLogs)), "\n") {
					if t := strings.TrimSpace(line); t != "" {
						log.add("[engine] %s", t)
					}
				}
			} else {
				// No log output — ask podman for the OCI runtime error string.
				// This is distinct from the container's own stderr; it contains
				// crun/runc error messages like "exec format error" or "no such file".
				if ociErr, _ := podmanCmd("inspect", "--format", "{{.State.Error}}", cID).Output(); len(ociErr) > 0 {
					if se := strings.TrimSpace(string(ociErr)); se != "" {
						log.add("[health] OCI runtime error: %s", se)
					}
				}
			}

			if exitCode == "127" && subSecondExit {
				log.add("[health] exit 127 in <2 s with no logs — the container process could not be exec'd")
				log.add("[health] this usually means the OCI runtime (crun) failed to start the entrypoint binary")
				log.add("[health] common rootless Podman fixes:")
				log.add("[health]   1) Check subuid/subgid mapping:  grep featherdeploy /etc/subuid /etc/subgid")
				log.add("[health]   2) Verify fuse-overlayfs:        podman info | grep graphDriverName (expect overlay)")
				log.add("[health]   3) Reset overlay storage (WARN: destroys all images/containers):")
				log.add("[health]      podman system reset && podman pull %s", pulledImage)
			}

			if attempt >= maxDBStartAttempts {
				msg := fmt.Sprintf(
					"Container exited with code %s on all %d attempts. "+
						"Podman overlay storage may be corrupt. "+
						"Run as the featherdeploy user: "+
						"podman system reset (WARNING: destroys all containers/images).",
					exitCode, attempt+1)
				log.add("[health] ERROR: %s", msg)
				slog.Error("database start: exceeded retry limit",
					"db_id", dbID, "attempts", attempt+1, "exitCode", exitCode)
				db.Exec( //nolint
					`UPDATE databases SET status='error', last_error=?, updated_at=datetime('now') WHERE id=?`,
					msg, dbID)
				return
			}

			if exitCode != "127" {
				// Non-127 exit: likely an engine startup error (bad config, OOM,
				// port conflict). Don't repull — it won't help. Just log and stop.
				msg := fmt.Sprintf("Container exited with code %s. Check the engine logs above.", exitCode)
				log.add("[health] ERROR: %s", msg)
				db.Exec( //nolint
					`UPDATE databases SET status='error', last_error=?, updated_at=datetime('now') WHERE id=?`,
					msg, dbID)
				return
			}

			// (B) Slow exit-127: entrypoint binary not found — stale overlay layers.
			log.add("[health] exit 127 detected (attempt %d/%d) — image layers may be incomplete, repulling...",
				attempt+1, maxDBStartAttempts+1)
			slog.Warn("database container exited 127 — removing image and repulling",
				"db_id", dbID, "image", pulledImage, "attempt", attempt+1)

			// Step 1: remove the container (it is stopped already but clean up)
			podmanCmd("rm", "-f", cID).Run() //nolint

			// Step 2: REMOVE the image entirely so podman fetches fresh layers.
			// A plain re-pull may reuse the same (broken) cached layers.
			log.add("[health] removing image %s from local storage to force fresh download...", pulledImage)
			if rmOut, rmErr := podmanCmd("image", "rm", "-f", pulledImage).CombinedOutput(); rmErr != nil {
				log.add("[health] WARNING: image remove failed (continuing anyway): %v — %s",
					rmErr, strings.TrimSpace(string(rmOut)))
			} else {
				log.add("[health] image removed from local storage")
			}

			// Step 3: retry the full start sequence (which will re-pull).
			log.add("[health] retrying StartDatabase (attempt %d/%d)...", attempt+2, maxDBStartAttempts+1)
			if retryErr := startDatabaseRetrying(db, jwtSecret, dbID, attempt+1); retryErr != nil {
				log.add("ERROR: retry failed: %v", retryErr)
				slog.Error("database start failed after image repull",
					"db_id", dbID, "attempt", attempt+1, "err", retryErr)
			}
		}(containerID, activeImage)
	}
	return nil
}

// StopDatabase stops and removes the database container without deleting the
// data volume, so it can be restarted later with all data intact.
func StopDatabase(db *sql.DB, dbID int64) error {
	var projectID int64
	var dbType, dbName string
	if err := db.QueryRow(`SELECT project_id, db_type, name FROM databases WHERE id=?`, dbID).Scan(&projectID, &dbType, &dbName); err != nil && err != sql.ErrNoRows {
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
	if NetDaemon != nil {
		NetDaemon.Deregister(projectID, DBNetworkAlias(dbName))
	}
	if EtcdClient != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = EtcdClient.UnregisterDatabase(ctx, projectID, dbName)
		}()
	}
	db.Exec( //nolint
		`UPDATE databases SET status='stopped', container_id='', updated_at=datetime('now') WHERE id=?`, dbID)
	CleanupProjectRuntimeIfUnused(db, projectID) //nolint
	slog.Info("database container stopped", "db_id", dbID)
	return nil
}

func DeleteDatabase(db *sql.DB, dbID int64, purgeData bool) error {
	// Capture db_type and db_version before stopping (StopDatabase removes the row reference)
	var projectID int64
	var dbType, dbVersion string
	if err := db.QueryRow(`SELECT project_id, db_type, db_version FROM databases WHERE id=?`, dbID).
		Scan(&projectID, &dbType, &dbVersion); err != nil && err != sql.ErrNoRows {
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
	// After purging the volume, remove the pulled image if no other database of
	// the same type+version is present (stopped or running). This reclaims disk
	// space without breaking databases that share the same image.
	go deleteDatabaseImageIfUnused(db, dbID, dbType, dbVersion)
	CleanupProjectRuntimeIfUnused(db, projectID) //nolint
	return nil
}

// deleteDatabaseImageIfUnused removes the pulled database image if no other
// database record in the system references the same db_type+db_version.
// It runs in a goroutine after volume deletion so it never blocks the caller.
func deleteDatabaseImageIfUnused(db *sql.DB, excludeID int64, dbType, dbVersion string) {
	if dbType == "sqlite" || dbType == "" {
		return
	}
	image := dbImageName(dbType, dbVersion)
	if image == "" {
		return
	}
	var count int
	db.QueryRow( //nolint
		`SELECT COUNT(*) FROM databases WHERE db_type=? AND db_version=? AND id!=?`,
		dbType, dbVersion, excludeID,
	).Scan(&count)
	if count > 0 {
		// Other databases still use this image — keep it.
		return
	}
	if out, err := podmanCmd("rmi", "-f", image).CombinedOutput(); err != nil {
		slog.Warn("could not remove database image", "image", image, "err", err, "output", strings.TrimSpace(string(out)))
	} else {
		slog.Info("removed unused database image", "image", image)
	}
}

func DeleteServiceRuntime(db *sql.DB, projectID, svcID int64) error {
	cName := fmt.Sprintf("fd-svc-%d", svcID)
	if err := removeContainerIfExists(cName); err != nil {
		return err
	}
	if NetDaemon != nil {
		var svcName string
		db.QueryRow(`SELECT name FROM services WHERE id=?`, svcID).Scan(&svcName) //nolint
		if svcName != "" {
			NetDaemon.Deregister(projectID, svcName)
			if EtcdClient != nil {
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					_ = EtcdClient.UnregisterService(ctx, projectID, svcName)
				}()
			}
		}
	}
	if err := deleteServiceImages(svcID); err != nil {
		return err
	}
	CleanupProjectRuntimeIfUnused(db, projectID) //nolint
	return nil
}

func DatabaseBackupDir() string {
	if d := os.Getenv("DATABASE_BACKUP_DIR"); d != "" {
		return d
	}
	return "/var/lib/featherdeploy/backups/databases"
}

func RunDatabaseTask(db *sql.DB, jwtSecret string, taskID, dbID int64) error {
	var taskType, artifactPath string
	err := db.QueryRow(`SELECT task_type, artifact_path FROM database_tasks WHERE id=?`, taskID).Scan(&taskType, &artifactPath)
	if err != nil {
		return err
	}

	switch taskType {
	case "backup":
		// User downloads are zero-downtime (don't stop the container)
		path, name, err := CreateDatabaseBackup(db, jwtSecret, dbID, false)
		if err != nil {
			return err
		}
		// Move to persistent backup dir
		finalDir := filepath.Join(DatabaseBackupDir(), strconv.FormatInt(dbID, 10))
		if err := os.MkdirAll(finalDir, 0750); err != nil {
			return fmt.Errorf("create backup dir: %w", err)
		}
		finalPath := filepath.Join(finalDir, name)
		if err := os.Rename(path, finalPath); err != nil {
			// fallback to copy if rename fails (e.g. cross-filesystem)
			if err := copyFile(path, finalPath); err != nil {
				return err
			}
			os.Remove(path)
		}
		db.Exec(`UPDATE database_tasks SET artifact_path=?, download_name=? WHERE id=?`, finalPath, name, taskID)
		return nil

	case "restore":
		if artifactPath == "" {
			return fmt.Errorf("no artifact path provided for restore")
		}
		return RestoreDatabaseBackup(db, jwtSecret, dbID, artifactPath)

	case "migrate":
		return migrateDatabaseDataBackground(db, jwtSecret, taskID, dbID)
	}

	return fmt.Errorf("unknown task type: %s", taskType)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func migrateDatabaseDataBackground(db *sql.DB, secret string, taskID, dbID int64) error {
	var oldNodeID string
	err := db.QueryRow(`SELECT node_id FROM databases WHERE id=?`, dbID).Scan(&oldNodeID)
	if err != nil {
		return err
	}
	if oldNodeID == "" || oldNodeID == "main" {
		return nil // nothing to migrate
	}

	var ip string
	var port int
	err = db.QueryRow(`SELECT ip, port FROM nodes WHERE node_id=?`, oldNodeID).Scan(&ip, &port)
	if err != nil {
		return fmt.Errorf("old node %q not found", oldNodeID)
	}

	if tunnelIP, tunnelPort, _ := resolveNodeTunnel(oldNodeID, port); tunnelIP != "" {
		ip = tunnelIP
		port = tunnelPort
	}
	viaTunnel := netdaemon.GlobalTunnel != nil && ip == "127.0.0.1"

	client, scheme := nodeHTTPClient(viaTunnel, 0) // 0 = no timeout for long transfers

	// 1. Trigger backup on old node
	payload := map[string]any{
		"db_id":      dbID,
		"jwt_secret": secret,
	}
	body, _ := json.Marshal(payload)
	url := fmt.Sprintf("%s://%s:%d/api/node/db-backup", scheme, ip, port)
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("backup request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("backup failed (%d): %s", resp.StatusCode, b)
	}

	// 2. Save to temp file with progress tracking
	tmp, err := os.CreateTemp("", "db-mig-*.tar")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	
	totalSize := resp.ContentLength
	
	// Create a wrapper to track progress
	pw := &progressWriter{
		total: totalSize,
		onProgress: func(pct int) {
			db.Exec(`UPDATE database_tasks SET progress=? WHERE id=?`, pct, taskID)
		},
	}
	
	if _, err := io.Copy(tmp, io.TeeReader(resp.Body, pw)); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()

	// 3. Restore locally
	return RestoreDatabaseBackup(db, secret, dbID, tmp.Name())
}

type progressWriter struct {
	total      int64
	written    int64
	lastPct    int
	onProgress func(int)
}

func (pw *progressWriter) Write(p []byte) (int, error) {
	n := len(p)
	pw.written += int64(n)
	if pw.total > 0 {
		pct := int(float64(pw.written) / float64(pw.total) * 100)
		if pct > pw.lastPct {
			pw.lastPct = pct
			if pw.onProgress != nil {
				pw.onProgress(pct)
			}
		}
	}
	return n, nil
}

func CreateDatabaseBackup(db *sql.DB, jwtSecret string, dbID int64, stop bool) (string, string, error) {
	var name, dbType, status string
	err := db.QueryRow(
		`SELECT name, db_type, status FROM databases WHERE id=?`, dbID,
	).Scan(&name, &dbType, &status)
	if err != nil {
		return "", "", fmt.Errorf("load database backup metadata: %w", err)
	}

	wasRunning := status == "running" && dbType != "sqlite" && stop
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

	if err := StreamDatabaseBackup(db, jwtSecret, dbID, stop, tmpFile); err != nil {
		os.Remove(tmpFile.Name())
		return "", "", err
	}

	downloadName := fmt.Sprintf("%s-%s-backup-%s.tar",
		DBNetworkAlias(name), dbType, time.Now().UTC().Format("20060102-150405"))
	
	fi, _ := tmpFile.Stat()
	slog.Info("database backup file created", "db_id", dbID, "size", fi.Size(), "name", downloadName)

	return tmpFile.Name(), downloadName, nil
}

func StreamDatabaseBackup(db *sql.DB, jwtSecret string, dbID int64, stop bool, out io.Writer) error {
	var name, dbType, status string
	err := db.QueryRow(
		`SELECT name, db_type, status FROM databases WHERE id=?`, dbID,
	).Scan(&name, &dbType, &status)
	if err != nil {
		return fmt.Errorf("load database backup metadata: %w", err)
	}

	wasRunning := status == "running" && dbType != "sqlite" && stop
	if wasRunning {
		if err := StopDatabase(db, dbID); err != nil {
			return err
		}
		defer func() {
			_ = StartDatabase(db, jwtSecret, dbID)
		}()
	}

	cmd := podmanCmd("volume", "export", dbVolumeName(dbID))
	cmd.Stdout = out
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("export database volume: %v — %s", err, strings.TrimSpace(stderr.String()))
	}

	return nil
}

// RestoreDatabaseBackup replaces the database volume's contents with the
// supplied .tar backup file (as produced by CreateDatabaseBackup /
// "podman volume export"). The database is stopped before the import and
// restarted afterwards.
//
// backupPath must be the path to a local .tar file that is readable by the
// featherdeploy process. The handler that calls this function is responsible
// for writing the uploaded bytes to a temp file and cleaning it up.
func RestoreDatabaseBackup(db *sql.DB, jwtSecret string, dbID int64, backupPath string) error {
	var name, dbType, status string
	err := db.QueryRow(
		`SELECT name, db_type, status FROM databases WHERE id=?`, dbID,
	).Scan(&name, &dbType, &status)
	if err != nil {
		return fmt.Errorf("load database metadata: %w", err)
	}

	fi, _ := os.Stat(backupPath)
	slog.Info("database restore starting", "db_id", dbID, "size", fi.Size())

	wasRunning := status == "running"
	if wasRunning {
		if err := StopDatabase(db, dbID); err != nil {
			return fmt.Errorf("stop database before restore: %w", err)
		}
	}

	cmd := podmanCmd("volume", "import", dbVolumeName(dbID), backupPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if wasRunning {
			_ = StartDatabase(db, jwtSecret, dbID)
		}
		return fmt.Errorf("import database volume: %v — %s", err, strings.TrimSpace(stderr.String()))
	}

	if wasRunning {
		if err := StartDatabase(db, jwtSecret, dbID); err != nil {
			return fmt.Errorf("restart database after restore: %w", err)
		}
	}
	return nil
}

// GetDatabaseLogs returns the last 200 lines of stdout+stderr from the
// database container. Returns an error when the container doesn't exist
// (e.g. start failed before the container was created).
// When the container exists but has no output (crash loop before any write),
// it returns a diagnostic message with the container's restart count and state.
func GetDatabaseLogs(dbID int64) (string, error) {
	cName := fmt.Sprintf("fd-db-%d", dbID)
	if !containerExists(cName) {
		return "", fmt.Errorf("container %s does not exist (start may have failed before container creation)", cName)
	}
	out, err := podmanCmd("logs", "--tail", "200", cName).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("podman logs %s: %w", cName, err)
	}
	if logStr := strings.TrimSpace(string(out)); logStr != "" {
		return logStr, nil
	}

	// Empty logs — try fetching recent output (last 5 minutes) in case
	// the log driver buffered output that wasn't included in --tail.
	out2, _ := podmanCmd("logs", "--since", "5m", cName).CombinedOutput()
	if logStr := strings.TrimSpace(string(out2)); logStr != "" {
		return logStr, nil
	}

	// Container exists but produced no output — likely crashing before first
	// write, or the engine writes to an internal log file instead of stdout.
	// Inspect to surface the restart count, exit code, and OOM status.
	inspOut, inspErr := podmanCmd("inspect",
		"--format",
		"Status: {{.State.Status}}\nExit code: {{.State.ExitCode}}\nRestarts: {{.RestartCount}}\nOOM killed: {{.State.OOMKilled}}\nError: {{.State.Error}}\nStarted: {{.State.StartedAt}}\nFinished: {{.State.FinishedAt}}",
		cName,
	).Output()
	diag := "[No log output — container is exiting before producing any output.]"
	if inspErr == nil {
		inspStr := strings.TrimSpace(string(inspOut))
		// Specific guidance for exit 127 with no logs — the most common rootless
		// Podman failure indicating the container's init binary couldn't be exec'd.
		exitHint := "\n\nPossible causes:\n" +
			"  • First-run database initialization is slow (wait 30–60s then refresh)\n" +
			"  • Insufficient disk space in the container volume"
		if strings.Contains(inspStr, "Exit code: 127") {
			exitHint = "\n\nExit 127 with no logs — OCI runtime could not exec the entrypoint binary.\n" +
				"This is a rootless Podman environment issue, not a database config error.\n" +
				"Investigate as the featherdeploy user:\n" +
				"  podman info | grep -E 'graphDriver|overlay'\n" +
				"  grep featherdeploy /etc/subuid /etc/subgid\n" +
				"If the overlay storage is corrupt:\n" +
				"  podman system reset   (WARNING: destroys all containers and images)"
		}
		diag = fmt.Sprintf("[No log output]\n\nContainer state:\n%s\n%s\n\n"+
			"For raw container output run as featherdeploy user:\n"+
			"  podman logs %s\n"+
			"  podman inspect %s | grep -i state",
			inspStr, exitHint, cName, cName)
	}
	return diag, nil
}

// UpdateDatabase persists configuration changes (db_version) for a database
// record. A restart is required for the new version to take effect.
func UpdateDatabase(db *sql.DB, dbID int64, dbVersion, targetNodeID string) error {
	_, err := db.Exec(
		`UPDATE databases SET db_version=?, target_node_id=?, updated_at=datetime('now') WHERE id=?`,
		dbVersion, targetNodeID, dbID)
	return err
}

// ChangeDBPassword changes the database user password live in the running
// container using the engine's CLI client (mysql/mariadb/psql), then updates
// the encrypted password in the panel's data store.
// Dependent services must be restarted after this call so they pick up the
// new connection URL that embeds the new password.
func ChangeDBPassword(db *sql.DB, jwtSecret string, dbID int64, newPassword string) error {
	var dbType, dbName, dbUser, encPass, containerID, status string
	if err := db.QueryRow(
		`SELECT db_type, db_name, db_user, db_password, COALESCE(container_id,''), status FROM databases WHERE id=?`, dbID,
	).Scan(&dbType, &dbName, &dbUser, &encPass, &containerID, &status); err != nil {
		return fmt.Errorf("load database: %w", err)
	}

	if dbType != "sqlite" {
		if status != "running" {
			return fmt.Errorf("database is not running (status: %s); start it before changing the password", status)
		}
		// Decrypt the current (old) password to authenticate against the engine.
		oldPass := encPass
		if strings.HasPrefix(encPass, "fdenc:") {
			if p, decErr := appCrypto.Decrypt(encPass[len("fdenc:"):], jwtSecret); decErr == nil {
				oldPass = p
			}
		}
		cName := fmt.Sprintf("fd-db-%d", dbID)
		if execErr := dbExecPasswordChange(cName, dbType, dbUser, oldPass, newPassword); execErr != nil {
			return fmt.Errorf("change password in container: %w", execErr)
		}
	}

	// Encrypt and persist the new password.
	encNew, encErr := appCrypto.Encrypt(newPassword, jwtSecret)
	if encErr != nil {
		return fmt.Errorf("encrypt new password: %w", encErr)
	}
	_, err := db.Exec(
		`UPDATE databases SET db_password=?, updated_at=datetime('now') WHERE id=?`,
		"fdenc:"+encNew, dbID)
	return err
}

// dbExecPasswordChange connects to the running database container and issues
// ALTER USER / ALTER ROLE SQL to change both the app user and superuser passwords.
// It never includes plaintext passwords in process arguments — MYSQL_PWD and
// PGPASSWORD env vars are used instead.
func dbExecPasswordChange(cName, dbType, dbUser, oldPass, newPass string) error {
	// Escape single quotes for safe embedding in SQL string literals.
	newPassSQL := strings.ReplaceAll(newPass, "'", "''")
	switch dbType {
	case "mysql", "mariadb":
		// ALTER USER changes passwords for the app user and both root accounts
		// (root@localhost and root@% are both created by docker-entrypoint.sh).
		sqlStmt := fmt.Sprintf(
			"ALTER USER '%s'@'%%' IDENTIFIED BY '%s';"+
				" ALTER USER 'root'@'%%' IDENTIFIED BY '%s';"+
				" ALTER USER 'root'@'localhost' IDENTIFIED BY '%s';"+
				" FLUSH PRIVILEGES;",
			strings.ReplaceAll(dbUser, "'", "''"), newPassSQL, newPassSQL, newPassSQL)
		// Try 'mariadb' client (MariaDB 11.x) then 'mysql' (MySQL 8.x).
		// MYSQL_PWD env var avoids exposing the password in the process list.
		for _, client := range []string{"mariadb", "mysql"} {
			out, err := podmanCmd("exec",
				"-e", "MYSQL_PWD="+oldPass,
				cName,
				client, "-uroot", "--connect-timeout=10",
				"-e", sqlStmt,
			).CombinedOutput()
			if err == nil {
				return nil
			}
			outStr := strings.TrimSpace(string(out))
			// If the client binary is absent in this image, try the next one.
			if strings.Contains(outStr, "not found") || strings.Contains(outStr, "executable file") {
				continue
			}
			return fmt.Errorf("%s exec failed: %v — %s", client, err, outStr)
		}
		return fmt.Errorf("neither mariadb nor mysql client found in container %s", cName)
	case "postgres":
		// Change the app user and the postgres superuser.
		// PGPASSWORD env var avoids the password appearing in the process list.
		sqlStmt := fmt.Sprintf(
			`ALTER USER "%s" WITH PASSWORD '%s'; ALTER USER postgres WITH PASSWORD '%s';`,
			dbUser, newPassSQL, newPassSQL)
		out, err := podmanCmd("exec",
			"-e", "PGPASSWORD="+oldPass,
			cName,
			"psql", "-U", "postgres", "-c", sqlStmt,
		).CombinedOutput()
		if err != nil {
			return fmt.Errorf("psql exec failed: %v — %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	default:
		return fmt.Errorf("unsupported database type %q for password change", dbType)
	}
}

// dbRegistryImages returns candidate image references in pull-priority order.
// Docker Hub (docker.io/library) is always first — it is the canonical source
// with the widest image selection. AWS ECR Public is a free, unauthenticated
// mirror of Docker Hub official images and is used as a fallback when Docker
// Hub is rate-limited or temporarily unreachable.
func dbRegistryImages(dbType, version string) []string {
	libName, tag := dbImageRefParts(dbType, version)
	if libName == "" || tag == "" {
		return nil // sqlite or unknown — no container image needed
	}
	return []string{
		"docker.io/library/" + libName + ":" + tag,
		// AWS ECR Public mirrors Docker Hub official images at no cost,
		// no authentication, and with higher rate limits — used as fallback
		// when Docker Hub is rate-limited or temporarily unreachable.
		"public.ecr.aws/docker/library/" + libName + ":" + tag,
	}
}

func dbImageRefParts(dbType, version string) (string, string) {
	if version == "" || version == "latest" {
		version = "latest"
	}
	switch dbType {
	case "postgres":
		return "postgres", version
	case "mysql":
		// MySQL 8.4+ dropped Debian variants entirely; all tags are OracleLinux9.
		// Use the plain version tag (e.g. "8.4", "9.6", "latest").
		return "mysql", version
	case "mariadb":
		// CPU-fallback engine: always use the MariaDB LTS release.
		// The user-specified MySQL version is ignored because MariaDB uses its
		// own versioning scheme; "lts" always maps to the current LTS release.
		return "mariadb", "lts"
	default:
		return "", ""
	}
}

// dbImageName returns the primary (Docker Hub) image reference for a database
// type. Kept for callers that only need one canonical ref (e.g. image-exists
// checks, volume-cleanup). Use pullDBImage for actual pulls.
func dbImageName(dbType, version string) string {
	refs := dbRegistryImages(dbType, version)
	if len(refs) == 0 {
		return ""
	}
	return refs[0]
}

// pullDBImage pulls the image for the given database type+version, trying each
// registry in dbRegistryImages priority order until one succeeds. It writes
// progress to the dbLog so the user can see which registry was used.
// Returns the image reference that was successfully pulled (may be a fallback).
func pullDBImage(log *dbLog, dbType, version string) (string, error) {
	refs := dbRegistryImages(dbType, version)
	if len(refs) == 0 {
		return "", fmt.Errorf("no image defined for database type %q", dbType)
	}
	var lastErr error
	for i, ref := range refs {
		if i == 0 {
			log.add("[pull] pulling %s from Docker Hub...", ref)
		} else {
			log.add("[pull] Docker Hub unavailable — trying fallback: %s", ref)
		}
		out, err := podmanCmd("pull", ref).CombinedOutput()
		outStr := strings.TrimSpace(string(out))
		for _, line := range strings.Split(outStr, "\n") {
			if t := strings.TrimSpace(line); t != "" {
				log.add("  %s", t)
			}
		}
		if err == nil {
			if i == 0 {
				log.add("[pull] ✓ pulled successfully from Docker Hub")
			} else {
				log.add("[pull] ✓ pulled from fallback registry (Docker Hub was unavailable)")
			}
			return ref, nil
		}
		log.add("[pull] ERROR: registry %d/%d failed: %v", i+1, len(refs), err)
		lastErr = fmt.Errorf("pull %s: %v — %s", ref, err, outStr)
	}
	return "", fmt.Errorf("all registries failed (tried %d): %w", len(refs), lastErr)
}

// dbInternalPort returns the default listening port for a database type.
func dbInternalPort(dbType string) int {
	switch dbType {
	case "postgres":
		return 5432
	case "mysql", "mariadb":
		return 3306
	default:
		return 5432
	}
}

// dbDataMountPath returns the internal data directory for persistent volume mounts.
func dbDataMountPath(dbType, version string) string {
	switch dbType {
	case "postgres":
		if postgresMajorVersion(version) >= 18 {
			return "/var/lib/postgresql"
		}
		return "/var/lib/postgresql/data"
	case "mysql", "mariadb":
		return "/var/lib/mysql"
	case "sqlite":
		return "/data"
	default:
		return "/data"
	}
}

func dbVolumeMountSpec(volumeName, mountPath string) string {
	return volumeName + ":" + mountPath
}

func dbContainerEntrypoint(dbType string) string {
	switch dbType {
	case "mysql", "postgres", "mariadb":
		// Use /bin/bash as the explicit entrypoint so the OCI runtime exec's a
		// real ELF binary instead of a shell script. In rootless Podman the
		// kernel shebang-interpreter lookup can silently fail (ENOENT → exit 127
		// with no log output) when the user-namespace subuid mapping isn't fully
		// resolved at exec time. Delegating to bash avoids that path entirely.
		return "/bin/bash"
	default:
		return ""
	}
}

// dbContainerEnvArgs returns -e KEY=VALUE pairs for the DB engine container.
func dbContainerEnvArgs(dbType, version, dbName, dbUser, clearPass string) []string {
	switch dbType {
	case "postgres":
		args := []string{
			"-e", "POSTGRES_DB=" + dbName,
			"-e", "POSTGRES_USER=" + dbUser,
			"-e", "POSTGRES_PASSWORD=" + clearPass,
		}
		if major := postgresMajorVersion(version); major >= 18 {
			args = append(args, "-e", fmt.Sprintf("PGDATA=/var/lib/postgresql/%d/docker", major))
		}
		return args
	case "mysql", "mariadb":
		// MYSQL_ROOT_PASSWORD is required; the named user+password are optional.
		// MariaDB ≥10.x also accepts MYSQL_* env vars for backward compatibility.
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

// dbContainerCmdArgs returns the arguments appended after the image name.
// Because dbContainerEntrypoint returns /bin/bash, these become bash argv;
// the first element must be the absolute path to the docker-entrypoint.sh
// script so bash sources+executes it, followed by the engine command.
func dbContainerCmdArgs(dbType string) []string {
	switch dbType {
	case "mysql":
		// /bin/bash /usr/local/bin/docker-entrypoint.sh mysqld
		return []string{"/usr/local/bin/docker-entrypoint.sh", "mysqld"}
	case "mariadb":
		// MariaDB 11.x renamed the daemon binary from mysqld to mariadbd.
		// mysqld is no longer present in MariaDB 11.x — passing it as $1 causes
		// docker-entrypoint.sh line 105 to try `mysqld --verbose --help` which
		// fails with "command not found".
		return []string{"/usr/local/bin/docker-entrypoint.sh", "mariadbd"}
	case "postgres":
		// /bin/bash /usr/local/bin/docker-entrypoint.sh postgres
		return []string{"/usr/local/bin/docker-entrypoint.sh", "postgres"}
	default:
		return nil
	}
}

// cpuSupportsX8664V2 reports whether the host CPU supports the x86-64-v2
// microarchitecture level (requires SSE4.1, SSE4.2, POPCNT).
// MySQL 8.4+ OracleLinux9 images are compiled for x86-64-v2 and abort with
// "Fatal glibc error: CPU does not support x86-64-v2" on older hardware.
// On non-Linux systems (where /proc/cpuinfo is absent) we return true so that
// no unnecessary fallback is triggered.
func cpuSupportsX8664V2() bool {
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return true // non-Linux or unreadable — assume supported
	}
	content := string(data)
	// Check the three key flags that glibc's x86-64-v2 runtime check requires.
	for _, flag := range []string{"sse4_1", "sse4_2", "popcnt"} {
		if !strings.Contains(content, flag) {
			return false
		}
	}
	return true
}

func postgresMajorVersion(version string) int {
	version = strings.TrimSpace(version)
	if version == "" || version == "latest" {
		return 0
	}
	majorPart := version
	if idx := strings.IndexAny(version, ".-"); idx >= 0 {
		majorPart = version[:idx]
	}
	major, err := strconv.Atoi(majorPart)
	if err != nil {
		return 0
	}
	return major
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
// With the service running in a real PAM/logind session (see installer's
// PAMName=login), podman and slirp4netns are expected to use the user session
// normally. Do not strip DBUS_* here: rootless+cgroupv2 networking may require
// a valid session bus/user.slice context.
func podmanEnv() []string {
	raw := os.Environ()
	env := make([]string, 0, len(raw)+5)
	for _, e := range raw {
		k := strings.SplitN(e, "=", 2)[0]
		switch {
		case k == "HOME", k == "XDG_RUNTIME_DIR", k == "XDG_CONFIG_HOME", k == "XDG_DATA_HOME", k == "XDG_CACHE_HOME", k == "CONTAINER_HOST", k == "DOCKER_HOST", k == "PATH":
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
	
	// Ensure Podman helper directories are in PATH so that netavark can find
	// and execute aardvark-dns. Systemd's default PATH does not include them.
	path := os.Getenv("PATH")
	if path == "" {
		path = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	}
	if !strings.Contains(path, "/usr/libexec/podman") {
		path = path + ":/usr/libexec/podman:/usr/lib/podman:/usr/local/lib/podman"
	}

	// Explicitly set all XDG paths based on home to prevent split-brain issues
	// where inherited XDG_CONFIG_HOME or XDG_DATA_HOME causes 'podman run'
	// to look in different directories than 'podman network create'.
	return append(env, 
		"PATH="+path,
		"HOME="+home, 
		"XDG_RUNTIME_DIR="+rtDir,
		"XDG_CONFIG_HOME="+home+"/.config",
		"XDG_DATA_HOME="+home+"/.local/share",
		"XDG_CACHE_HOME="+home+"/.cache",
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
func podmanBuild(ctx context.Context, dir string, log *logBuf, imageName string) error {
	cmd := podmanCmdCtx(ctx, "build", "--pull=missing", "--network=slirp4netns:allow_host_loopback=true", "-t", imageName, ".")
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

func podmanImageExistsCtx(ctx context.Context, image string) bool {
	return podmanCmdCtx(ctx, "image", "exists", image).Run() == nil
}

// podmanImageExists returns true if the named image exists in the rootless podman store.
func podmanImageExists(image string) bool {
	return podmanCmd("image", "exists", image).Run() == nil
}

func removeContainerIfExistsCtx(ctx context.Context, name string) error {
	if !containerExistsCtx(ctx, name) {
		return nil
	}
	// Best-effort graceful stop first: if the container is already stopped or
	// in a crash-restart loop (state = exited/restarting), podman stop will
	// return an error.  We ignore that — podman rm -f handles all states.
	stopCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	podmanCmdCtx(stopCtx, "stop", "--time", "10", name).Run() //nolint
	if out, err := podmanCmdCtx(ctx, "rm", "-f", name).CombinedOutput(); err != nil {
		return fmt.Errorf("remove container %s: %v — %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func removeContainerIfExists(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return removeContainerIfExistsCtx(ctx, name)
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
	podmanCmd("pod", "rm", "-f", projectPodName(projectID)).Run() //nolint
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
	case fw == "laravel" || fw == "symfony":
		return "php:8.2-fpm-alpine"
	case fw == "php" || fw == "slim" || fw == "cakephp" || fw == "codeigniter":
		return "php:8.2-cli-alpine"
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
	// ── 0. Collect build-time DB URLs for alias substitution ─────────────────
	// Users often copy-paste the connection string shown in the UI (e.g.
	// postgresql://user:pass@ssc:5432/db) into their DATABASE_URL env var.
	// That hostname format uses DNS-based routing which doesn't work inside
	// podman build containers (slirp4netns has no DNS for container aliases).
	// We detect this pattern and rewrite it to 10.0.2.2:hostPort, which IS
	// reachable from build containers via the slirp4netns gateway.
	type buildDB struct {
		alias        string
		internalPort int
		hostPort     int
		connURL      string
	}
	var buildDBs []buildDB
	dbRows, _ := db.Query(
		`SELECT id, name, db_type, db_name, db_user, db_password, COALESCE(host_port, 0)
		 FROM databases WHERE project_id=? AND status='running'`, projectID)
	if dbRows != nil {
		defer dbRows.Close()
		for dbRows.Next() {
			var dbID int64
			var name, dbType, dbName, dbUser, encPass string
			var hp int
			if dbRows.Scan(&dbID, &name, &dbType, &dbName, &dbUser, &encPass, &hp) != nil || hp == 0 {
				continue
			}
			clearPass := encPass
			if strings.HasPrefix(encPass, "fdenc:") {
				if p, decErr := appCrypto.Decrypt(encPass[len("fdenc:"):], jwtSecret); decErr == nil {
					clearPass = p
				}
			}
			alias := DBNetworkAlias(name)
			iport := dbTypeInternalPort(dbType)
			connURL := dbConnectionURLFDNet(dbType, dbName, dbUser, clearPass, netdaemon.SlirpGateway, hp)
			buildDBs = append(buildDBs, buildDB{alias: alias, internalPort: iport, hostPort: hp, connURL: connURL})
		}
	}

	rows, err := db.Query(
		`SELECT key, value, is_secret FROM env_variables WHERE service_id=?`, svcID)
	if err != nil {
		return nil // no vars is fine
	}
	defer rows.Close()

	var sb strings.Builder
	userKeys := make(map[string]bool)
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
		// Transparently rewrite alias:internalPort → 10.0.2.2:hostPort in any
		// URL value so build-time tools (drizzle-kit push, prisma migrate, etc.)
		// reach the database through the slirp4netns gateway.
		for _, dbi := range buildDBs {
			if dbi.internalPort > 0 {
				old := fmt.Sprintf("@%s:%d/", dbi.alias, dbi.internalPort)
				repl := fmt.Sprintf("@%s:%d/", netdaemon.SlirpGateway, dbi.hostPort)
				v = strings.ReplaceAll(v, old, repl)
			}
		}
		sb.WriteString(k + "=" + v + "\n")
		userKeys[k] = true
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
	// Auto-inject DATABASE_URL pointing to the first running database so that
	// build tools (drizzle-kit, prisma, etc.) work out of the box without the
	// user having to manually configure a connection string.
	if !userKeys["DATABASE_URL"] && len(buildDBs) > 0 {
		sb.WriteString("DATABASE_URL=" + buildDBs[0].connURL + "\n")
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

// dbTypeInternalPort returns the default listening port for a given database type.
func dbTypeInternalPort(dbType string) int {
	switch dbType {
	case "postgres":
		return 5432
	case "mysql":
		return 3306
	}
	return 0
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

// resolveNodeTunnel looks up the local proxy address for a node API connection.
// The node API always listens on port 7443 regardless of what is stored in the
// DB (the DB port can be 443 from old registrations). We try the DB port first,
// then fall back to 7443.
func resolveNodeTunnel(nodeID string, dbPort int) (ip string, port int, viaTunnel bool) {
	// If it's the brain node, talk to localhost directly (bypassing tunnel)
	if nodeID == "main" || nodeID == "" {
		return "127.0.0.1", dbPort, false
	}

	viaTunnel = false
	if netdaemon.GlobalTunnel == nil {
		return "", dbPort, false
	}

	// For worker nodes, we exclusively use the tunnel. 
	// We prioritize port 443 (standard HTTPS/mTLS) as requested by the user.
	
	// 1. Try to find an existing tunnel proxy for port 443
	proxyAddr := netdaemon.GlobalTunnel.GetNodeProxyAddr(nodeID, 443)
	
	// 2. Fallback: try the requested dbPort (it might be 443 already)
	if proxyAddr == "" && dbPort != 443 {
		proxyAddr = netdaemon.GlobalTunnel.GetNodeProxyAddr(nodeID, dbPort)
	}

	// 3. Last resort: try port 7443
	if proxyAddr == "" && dbPort != 7443 {
		proxyAddr = netdaemon.GlobalTunnel.GetNodeProxyAddr(nodeID, 7443)
	}

	if proxyAddr == "" {
		// If no proxy exists for any management port, the node is effectively unreachable
		// because public ports are blocked on the VPS.
		return "", dbPort, false
	}

	parts := strings.Split(proxyAddr, ":")
	if len(parts) == 2 {
		if p, err := strconv.Atoi(parts[1]); err == nil {
			port = p
		}
	}
	ip = "127.0.0.1"
	viaTunnel = true
	return
}

// nodeHTTPClient builds the appropriate HTTP client for talking to a node.
// When viaTunnel is true the request goes over the yamux tunnel proxy (plain HTTP,
// no mTLS needed because the tunnel itself is the encrypted channel).
// When viaTunnel is false we are talking directly to the node over the public network
// and need a full mTLS client.
func nodeHTTPClient(viaTunnel bool, timeout time.Duration) (*http.Client, string) {
	caPEM, _ := os.ReadFile("/etc/featherdeploy/ca.crt")
	certPEM, _ := os.ReadFile("/etc/featherdeploy/node.crt")
	keyPEM, _ := os.ReadFile("/etc/featherdeploy/node.key")
	tlsCfg, err := pki.TLSConfig(string(certPEM), string(keyPEM), string(caPEM))
	if err != nil {
		slog.Warn("nodeHTTPClient: mTLS config failed, falling back to insecure", "err", err)
		return &http.Client{
			Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
			Timeout:   timeout,
		}, "https"
	}
	if viaTunnel {
		// When routing through the yamux loopback proxy, target hostname is 127.0.0.1.
		// Skip hostname verification while preserving our loaded client certificate to satisfy worker node mTLS requirements.
		tlsCfg.InsecureSkipVerify = true
	}
	return &http.Client{
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
		Timeout:   timeout,
	}, "https"
}

func dispatchToNode(db *sql.DB, nodeID string, depID, svcID, userID int64, secret string) error {
	var ip string
	var port int
	err := db.QueryRow(`SELECT ip, port FROM nodes WHERE node_id=?`, nodeID).Scan(&ip, &port)
	if err != nil {
		return fmt.Errorf("node %q not found in DB: %w", nodeID, err)
	}

	// All inter-node dispatch routes through the persistent tunnel.
	viaTunnel := false
	if tunnelIP, tunnelPort, isTunnel := resolveNodeTunnel(nodeID, port); tunnelIP != "" {
		ip = tunnelIP
		port = tunnelPort
		viaTunnel = isTunnel
	} else {
		// If tunnel is down, worker is unreachable due to restricted VPS ports.
		return fmt.Errorf("node %q is unreachable (tunnel disconnected and public ports blocked)", nodeID)
	}
	
	// Prepare payload
	payload := map[string]any{
		"dep_id":     depID,
		"svc_id":     svcID,
		"user_id":    userID,
		"jwt_secret": secret,
	}
	body, _ := json.Marshal(payload)

	client, scheme := nodeHTTPClient(viaTunnel, 30*time.Second)
	url := fmt.Sprintf("%s://%s:%d/api/node/deploy", scheme, ip, port)
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("post to node: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("node rejected deploy (%d): %s", resp.StatusCode, b)
	}

	return nil
}

func compressWorkDir(workDir, tarball string) error {
	cmd := exec.Command("tar", "-czf", tarball, "-C", workDir, ".")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("tar failed: %v, output: %s", err, out)
	}
	return nil
}

func SendArtifactToNode(db *sql.DB, nodeID string, depID int64, tarballPath string, destPath string) error {
	var ip string
	var port int
	err := db.QueryRow(`SELECT ip, port FROM nodes WHERE node_id=?`, nodeID).Scan(&ip, &port)
	if err != nil {
		return fmt.Errorf("node %q not found in DB: %w", nodeID, err)
	}

	viaTunnel := false
	if tunnelIP, tunnelPort, isTunnel := resolveNodeTunnel(nodeID, port); tunnelIP != "" {
		ip = tunnelIP
		port = tunnelPort
		viaTunnel = isTunnel
	} else {
		return fmt.Errorf("node %q unreachable (tunnel disconnected)", nodeID)
	}

	// Artifact transfers can be large; use a 5-minute timeout.
	client, scheme := nodeHTTPClient(viaTunnel, 5*time.Minute)

	chunker := &transfer.Chunker{FilePath: tarballPath, ChunkSize: transfer.DefaultChunkSize}
	totalChunks, _, err := chunker.ChunkCount()
	if err != nil {
		return err
	}

	for i := 0; i < totalChunks; i++ {
		data, err := chunker.ReadChunk(i)
		if err != nil {
			return fmt.Errorf("read chunk %d: %w", i, err)
		}
		url := fmt.Sprintf("%s://%s:%d/api/node/artifact-chunk/%d/%d", scheme, ip, port, depID, i)
		req, _ := http.NewRequest("POST", url, bytes.NewReader(data))
		req.Header.Set("X-Total-Chunks", strconv.Itoa(totalChunks))
		if destPath != "" {
			req.Header.Set("X-Dest-Path", destPath)
		}
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("post chunk %d: %w", i, err)
		}
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return fmt.Errorf("node rejected chunk %d (%d): %s", i, resp.StatusCode, b)
		}
		resp.Body.Close()
	}

	return nil
}

func stopContainerOnNode(db *sql.DB, nodeID, containerID string) error {
	var ip string
	var port int
	err := db.QueryRow(`SELECT ip, port FROM nodes WHERE node_id=?`, nodeID).Scan(&ip, &port)
	if err != nil {
		return fmt.Errorf("node %q not found: %w", nodeID, err)
	}

	viaTunnel := false
	if tunnelIP, tunnelPort, isTunnel := resolveNodeTunnel(nodeID, port); tunnelIP != "" {
		ip = tunnelIP
		port = tunnelPort
		viaTunnel = isTunnel
	} else {
		return fmt.Errorf("node %q unreachable", nodeID)
	}

	payload := map[string]any{"container_id": containerID}
	body, _ := json.Marshal(payload)

	client, scheme := nodeHTTPClient(viaTunnel, 10*time.Second)
	url := fmt.Sprintf("%s://%s:%d/api/node/stop", scheme, ip, port)
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("post to node: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("node rejected stop (%d): %s", resp.StatusCode, b)
	}

	return nil
}

func dispatchDatabaseToNode(db *sql.DB, secret, nodeID string, dbID int64) error {
	var ip string
	var port int
	err := db.QueryRow(`SELECT ip, port FROM nodes WHERE node_id=?`, nodeID).Scan(&ip, &port)
	if err != nil {
		return fmt.Errorf("node %q not found", nodeID)
	}

	if tunnelIP, tunnelPort, _ := resolveNodeTunnel(nodeID, port); tunnelIP != "" {
		ip = tunnelIP
		port = tunnelPort
	}
	viaTunnel := netdaemon.GlobalTunnel != nil && ip == "127.0.0.1"

	payload := map[string]any{
		"db_id":      dbID,
		"jwt_secret": secret,
	}
	body, _ := json.Marshal(payload)

	client, scheme := nodeHTTPClient(viaTunnel, 30*time.Second)
	url := fmt.Sprintf("%s://%s:%d/api/node/db-start", scheme, ip, port)
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("node rejected db-start (%d): %s", resp.StatusCode, b)
	}
	return nil
}

func migrateDatabaseData(db *sql.DB, secret, oldNodeID string, dbID int64) error {
	var ip string
	var port int
	err := db.QueryRow(`SELECT ip, port FROM nodes WHERE node_id=?`, oldNodeID).Scan(&ip, &port)
	if err != nil {
		return fmt.Errorf("old node %q not found", oldNodeID)
	}

	if tunnelIP, tunnelPort, _ := resolveNodeTunnel(oldNodeID, port); tunnelIP != "" {
		ip = tunnelIP
		port = tunnelPort
	}
	viaTunnel := netdaemon.GlobalTunnel != nil && ip == "127.0.0.1"

	client, scheme := nodeHTTPClient(viaTunnel, 2*time.Minute)

	// 1. Trigger backup on old node
	payload := map[string]any{
		"db_id":      dbID,
		"jwt_secret": secret,
	}
	body, _ := json.Marshal(payload)
	url := fmt.Sprintf("%s://%s:%d/api/node/db-backup", scheme, ip, port)
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("backup request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("backup failed (%d): %s", resp.StatusCode, b)
	}

	// 2. Save to temp file
	tmp, err := os.CreateTemp("", "db-mig-*.tar")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()

	// 3. Restore locally
	return RestoreDatabaseBackup(db, secret, dbID, tmp.Name())
}
func detectNodeIP(db *sql.DB) string {
	// 1. Try to look up our IP from the nodes table using our hostname (for worker nodes)
	if db != nil {
		h, _ := os.Hostname()
		var ip string
		err := db.QueryRow(`SELECT ip FROM nodes WHERE hostname=? OR node_id=?`, h, h).Scan(&ip)
		if err == nil && ip != "" && !isPrivateIP(ip) {
			return ip
		}
	}

	// 2. Check SERVER_IP environment variable (fallback if DB has no mesh IP)
	if ip := os.Getenv("SERVER_IP"); ip != "" && !isPrivateIP(ip) {
		return ip
	}

	// 3. Try external discovery (same as installer)
	urls := []string{"https://ident.me", "https://ifconfig.me/ip", "https://api.ipify.org"}
	for _, u := range urls {
		client := http.Client{Timeout: 3 * time.Second}
		if resp, err := client.Get(u); err == nil {
			if body, err := io.ReadAll(resp.Body); err == nil {
				extIP := strings.TrimSpace(string(body))
				if net.ParseIP(extIP) != nil && !isPrivateIP(extIP) {
					resp.Body.Close()
					return extIP
				}
			}
			resp.Body.Close()
		}
	}

	// 4. Fallback to UDP routing trick
	if conn, err := net.DialTimeout("udp", "1.1.1.1:80", 2*time.Second); err == nil {
		ip := conn.LocalAddr().(*net.UDPAddr).IP.String()
		conn.Close()
		if ip != "" && !isPrivateIP(ip) {
			return ip
		}
	}

	slog.Warn("detectNodeIP: could not find a public IP, falling back to 127.0.0.1")
	return "127.0.0.1"
}

func isPrivateIP(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return true
	}
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
