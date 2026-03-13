package handler

import (
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
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
	// Lines already sent are tracked by index so we only emit new lines.
	var sentLines int
	sendLine := func(line string) {
		fmt.Fprintf(w, "data: %s\n\n", line)
		flusher.Flush()
	}

	ticker := time.NewTicker(1 * time.Second)
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

		// Emit any new lines
		if deployLog != "" {
			allLines := strings.Split(deployLog, "\n")
			for i := sentLines; i < len(allLines); i++ {
				if allLines[i] != "" {
					sendLine(allLines[i])
				}
			}
			sentLines = len(allLines)
		} else if sentLines == 0 {
			// Nothing yet — show a waiting message once
			sendLine("Waiting for deployment to start...")
			sentLines = -1 // sentinel: waiting message sent
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

