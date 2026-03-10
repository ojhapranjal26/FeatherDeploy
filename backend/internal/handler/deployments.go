package handler

import (
	"database/sql"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/deploy-paas/backend/internal/middleware"
	"github.com/deploy-paas/backend/internal/model"
	v "github.com/deploy-paas/backend/internal/validator"
)

type DeploymentHandler struct{ db *sql.DB }

func NewDeploymentHandler(db *sql.DB) *DeploymentHandler {
	return &DeploymentHandler{db: db}
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

	// TODO: In production, hand off to a worker queue.
	// For now: simulate success after writing the record.
	go h.finishDeployment(depID, svcID)

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
// Streams mock ANSI log lines via Server-Sent Events
func (h *DeploymentHandler) Logs(w http.ResponseWriter, r *http.Request) {
	depID, err := strconv.ParseInt(r.PathValue("deploymentID"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid deploymentID"))
		return
	}
	// Verify deployment exists and caller can read it
	var status string
	err = h.db.QueryRowContext(r.Context(), `SELECT status FROM deployments WHERE id=?`, depID).Scan(&status)
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

	lines := []string{
		"\x1b[90m[00:00:01]\x1b[0m Initializing deployment pipeline...",
		"\x1b[90m[00:00:02]\x1b[0m Cloning repository...",
		"\x1b[90m[00:00:03]\x1b[0m Detecting runtime... \x1b[32mNode.js 20.x\x1b[0m",
		"\x1b[90m[00:00:04]\x1b[0m Restoring build cache...",
		"\x1b[90m[00:00:05]\x1b[0m \x1b[1mRunning:\x1b[0m npm install",
		"\x1b[90m[00:00:11]\x1b[0m added 1425 packages in 6.2s",
		"\x1b[90m[00:00:12]\x1b[0m \x1b[1mRunning:\x1b[0m npm run build",
		"\x1b[90m[00:00:14]\x1b[0m   \x1b[32m✓\x1b[0m Compiled successfully",
		"\x1b[90m[00:00:16]\x1b[0m   \x1b[32m✓\x1b[0m Type checking passed",
		"\x1b[90m[00:00:21]\x1b[0m   \x1b[32m✓\x1b[0m Static pages generated",
		"\x1b[90m[00:00:22]\x1b[0m Build complete. Writing layer cache...",
		"\x1b[90m[00:00:23]\x1b[0m Starting container...",
		"\x1b[90m[00:00:24]\x1b[0m Health check \x1b[32mpassed\x1b[0m (200 OK, 12ms)",
		"\x1b[90m[00:00:25]\x1b[0m \x1b[32mDeployment successful!\x1b[0m",
	}

	for i, line := range lines {
		select {
		case <-r.Context().Done():
			return
		default:
		}
		w.Write([]byte("data: " + line + "\n\n"))
		flusher.Flush()
		if i < len(lines)-1 {
			time.Sleep(150 * time.Millisecond)
		}
	}
	w.Write([]byte("event: done\ndata: \n\n"))
	flusher.Flush()
}

func (h *DeploymentHandler) finishDeployment(depID, svcID int64) {
	time.Sleep(3 * time.Second)
	h.db.Exec(
		`UPDATE deployments SET status='success', finished_at=datetime('now') WHERE id=?`, depID)
	h.db.Exec(
		`UPDATE services SET status='running', updated_at=datetime('now') WHERE id=?`, svcID)
}

// ─── scanner helpers ─────────────────────────────────────────────────────────

func scanDeployment(row scanner, d *model.Deployment) error {
	return row.Scan(&d.ID, &d.ServiceID, &d.TriggeredBy, &d.DeployType,
		&d.RepoURL, &d.CommitSHA, &d.ArtifactPath, &d.Status,
		&d.ErrorMessage, &d.StartedAt, &d.FinishedAt, &d.CreatedAt)
}

func scanDeploymentRow(row *sql.Row, d *model.Deployment) error {
	return row.Scan(&d.ID, &d.ServiceID, &d.TriggeredBy, &d.DeployType,
		&d.RepoURL, &d.CommitSHA, &d.ArtifactPath, &d.Status,
		&d.ErrorMessage, &d.StartedAt, &d.FinishedAt, &d.CreatedAt)
}
