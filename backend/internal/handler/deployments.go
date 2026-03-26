package handler

import (
	"bufio"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/ojhapranjal26/featherdeploy/backend/internal/deploy"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/middleware"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/model"
	v "github.com/ojhapranjal26/featherdeploy/backend/internal/validator"
)

type DeploymentHandler struct {
	db        *sql.DB
	jwtSecret string
}

func NewDeploymentHandler(db *sql.DB, jwtSecret string) *DeploymentHandler {
	return &DeploymentHandler{db: db, jwtSecret: jwtSecret}
}

// GET /api/projects/{projectID}/services/{serviceID}/deployments
func (h *DeploymentHandler) List(w http.ResponseWriter, r *http.Request) {
	svcID, err := strconv.ParseInt(r.PathValue("serviceID"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid serviceID"))
		return
	}
	limit := 20
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}
	rows, err := h.db.QueryContext(r.Context(),
		`SELECT id, service_id, triggered_by, deploy_type, repo_url, commit_sha,
		        artifact_path, status, error_message, started_at, finished_at, created_at
		 FROM deployments WHERE service_id=? ORDER BY created_at DESC LIMIT ?`,
		svcID, limit)
	if err != nil {
		slog.Error("list deployments", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	defer rows.Close()
	deps := make([]model.Deployment, 0)
	for rows.Next() {
		var d model.Deployment
		if err := scanDeployment(rows, &d); err == nil {
			deps = append(deps, d)
		}
	}
	writeJSON(w, http.StatusOK, deps)
}

// POST /api/projects/{projectID}/services/{serviceID}/deployments
func (h *DeploymentHandler) Trigger(w http.ResponseWriter, r *http.Request) {
	svcID, err := strconv.ParseInt(r.PathValue("serviceID"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid serviceID"))
		return
	}
	var req model.TriggerDeployRequest
	if !v.DecodeAndValidate(w, r, &req) {
		return
	}
	claims := middleware.GetClaims(r.Context())

	// Insert deployment record
	now := time.Now().UTC()
	res, err := h.db.ExecContext(r.Context(),
		`INSERT INTO deployments
		  (service_id, triggered_by, deploy_type, repo_url, commit_sha, artifact_path, status, started_at)
		 VALUES (?,?,?,?,?,?,?,?)`,
		svcID, claims.UserID, req.DeployType, req.RepoURL, req.CommitSHA, req.ArtifactPath,
		"running", now)
	if err != nil {
		slog.Error("trigger deployment", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	depID, _ := res.LastInsertId()

	// Mark service as deploying
	h.db.ExecContext(r.Context(),
		`UPDATE services SET status='deploying', updated_at=datetime('now') WHERE id=?`, svcID)

	go deploy.Run(h.db, h.jwtSecret, depID, svcID, claims.UserID)

	writeJSON(w, http.StatusCreated, map[string]any{"deployment_id": depID, "status": "running"})
}

// GET /api/projects/{projectID}/services/{serviceID}/deployments/{deploymentID}
func (h *DeploymentHandler) Get(w http.ResponseWriter, r *http.Request) {
	depID, err := strconv.ParseInt(r.PathValue("deploymentID"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid deploymentID"))
		return
	}
	row := h.db.QueryRowContext(r.Context(),
		`SELECT id, service_id, triggered_by, deploy_type, repo_url, commit_sha,
		        artifact_path, status, error_message, started_at, finished_at, created_at
		 FROM deployments WHERE id=?`, depID)
	var d model.Deployment
	if err := scanDeploymentRow(row, &d); err == sql.ErrNoRows {
		writeJSON(w, http.StatusNotFound, errMap("deployment not found"))
		return
	} else if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// GET /api/projects/{projectID}/services/{serviceID}/deployments/{deploymentID}/logs
// Streams real deployment logs via Server-Sent Events, polling the deploy_log DB column.
func (h *DeploymentHandler) Logs(w http.ResponseWriter, r *http.Request) {
	depID, err := strconv.ParseInt(r.PathValue("deploymentID"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid deploymentID"))
		return
	}
	// Verify deployment exists
	var dummy string
	err = h.db.QueryRowContext(r.Context(), `SELECT id FROM deployments WHERE id=?`, depID).Scan(&dummy)
	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusNotFound, errMap("deployment not found"))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, errMap("streaming not supported"))
		return
	}

	// Stream log lines by polling deploy_log column until deployment finishes.
	// We track the number of *non-empty* lines already sent so we only emit
	// new content on each tick.  A keep-alive SSE comment (": ping") is sent
	// on every tick even when there are no new log lines — this prevents
	// Caddy/nginx from closing the connection during long-running steps.
	sentLines := 0
	sendLine := func(line string) {
		fmt.Fprintf(w, "data: %s\n\n", line)
		flusher.Flush()
	}
	sendPing := func() {
		fmt.Fprint(w, ": ping\n\n")
		flusher.Flush()
	}

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}

		var deployLog, status string
		qErr := h.db.QueryRowContext(r.Context(),
			`SELECT COALESCE(deploy_log,''), status FROM deployments WHERE id=?`, depID,
		).Scan(&deployLog, &status)
		if qErr != nil {
			return
		}

		// Collect all non-empty lines
		allLines := strings.Split(deployLog, "\n")
		var nonEmpty []string
		for _, l := range allLines {
			if strings.TrimSpace(l) != "" {
				nonEmpty = append(nonEmpty, l)
			}
		}

		// Emit only lines we haven't sent yet
		if len(nonEmpty) > sentLines {
			for _, line := range nonEmpty[sentLines:] {
				sendLine(line)
			}
			sentLines = len(nonEmpty)
		} else if sentLines == 0 && deployLog == "" {
			// Nothing yet — send a waiting indicator (not counted in sentLines)
			sendLine("Waiting for deployment to start...")
			sendPing()
		} else {
			sendPing()
		}

		// Done when not still running
		if status != "running" {
			fmt.Fprint(w, "event: done\ndata: \n\n")
			flusher.Flush()
			return
		}
	}
}

// ─── scanner helpers ─────────────────────────────────────────────────────────

// GET /api/projects/{projectID}/services/{serviceID}/container-logs
// Streams live stdout+stderr of the running container via Server-Sent Events.
// The stream ends naturally when the container exits or the client disconnects.
func (h *DeploymentHandler) ContainerLogs(w http.ResponseWriter, r *http.Request) {
	svcID, err := strconv.ParseInt(r.PathValue("serviceID"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid serviceID"))
		return
	}

	// Verify the service exists (container may not be running yet)
	var dummy int64
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT id FROM services WHERE id=?`, svcID).Scan(&dummy); err == sql.ErrNoRows {
		writeJSON(w, http.StatusNotFound, errMap("service not found"))
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, errMap("streaming not supported"))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	sendLine := func(line string) {
		fmt.Fprintf(w, "data: %s\n\n", strings.ReplaceAll(line, "\r", ""))
		flusher.Flush()
	}
	sendDone := func() {
		fmt.Fprint(w, "event: done\ndata: \n\n")
		flusher.Flush()
	}

	cName := fmt.Sprintf("fd-svc-%d", svcID)

	// Use os.Pipe so that when the child exits its write-end is closed,
	// causing the scanner to return EOF and exit the loop cleanly.
	rp, wp, pipeErr := os.Pipe()
	if pipeErr != nil {
		sendLine("[error: cannot create pipe]")
		sendDone()
		return
	}

	// exec.CommandContext sends SIGKILL to the process when ctx is cancelled
	// (i.e. client disconnects), which closes the child's write end of the pipe.
	cmd := exec.CommandContext(r.Context(),
		"sudo", "-n", "podman", "logs", "-f", "--tail=100", cName)
	cmd.Stdout = wp
	cmd.Stderr = wp

	if err := cmd.Start(); err != nil {
		rp.Close()
		wp.Close()
		sendLine(fmt.Sprintf("[error: %v]", err))
		sendDone()
		return
	}
	// Close parent's write end so the pipe gets EOF once the child exits.
	wp.Close()

	scanner := bufio.NewScanner(rp)
	scanner.Buffer(make([]byte, 256*1024), 256*1024) // guard against very long lines
	for scanner.Scan() {
		sendLine(scanner.Text())
	}

	rp.Close()
	cmd.Wait() //nolint:errcheck
	sendDone()
}

func scanDeployment(row scanner, d *model.Deployment) error {
	var finishedAt sql.NullTime
	err := row.Scan(&d.ID, &d.ServiceID, &d.TriggeredBy, &d.DeployType,
		&d.RepoURL, &d.CommitSHA, &d.ArtifactPath, &d.Status,
		&d.ErrorMessage, &d.StartedAt, &finishedAt, &d.CreatedAt)
	if err != nil {
		return err
	}
	if finishedAt.Valid {
		t := finishedAt.Time
		d.FinishedAt = &t
	}
	return nil
}

func scanDeploymentRow(row *sql.Row, d *model.Deployment) error {
	var finishedAt sql.NullTime
	err := row.Scan(&d.ID, &d.ServiceID, &d.TriggeredBy, &d.DeployType,
		&d.RepoURL, &d.CommitSHA, &d.ArtifactPath, &d.Status,
		&d.ErrorMessage, &d.StartedAt, &finishedAt, &d.CreatedAt)
	if err != nil {
		return err
	}
	if finishedAt.Valid {
		t := finishedAt.Time
		d.FinishedAt = &t
	}
	return nil
}
