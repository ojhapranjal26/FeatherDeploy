package handler

import (
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"os/exec"
	"strconv"

	caddypkg "github.com/ojhapranjal26/featherdeploy/backend/internal/caddy"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/model"
	v "github.com/ojhapranjal26/featherdeploy/backend/internal/validator"
)

type ServiceHandler struct{ db *sql.DB }

func NewServiceHandler(db *sql.DB) *ServiceHandler { return &ServiceHandler{db: db} }

// GET /api/projects/{projectID}/services
func (h *ServiceHandler) List(w http.ResponseWriter, r *http.Request) {
	projectID, err := strconv.ParseInt(r.PathValue("projectID"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid projectID"))
		return
	}
	rows, err := h.db.QueryContext(r.Context(),
		`SELECT id, project_id, name, description, deploy_type, repo_url, repo_branch,
		        framework, build_command, start_command, app_port, COALESCE(host_port, 0),
		        status, container_id, created_at, updated_at
		 FROM services WHERE project_id=? ORDER BY created_at DESC`, projectID)
	if err != nil {
		slog.Error("list services", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	defer rows.Close()
	svcs := make([]model.Service, 0)
	for rows.Next() {
		var s model.Service
		if err := scanService(rows, &s); err == nil {
			svcs = append(svcs, s)
		}
	}
	writeJSON(w, http.StatusOK, svcs)
}

// POST /api/projects/{projectID}/services
func (h *ServiceHandler) Create(w http.ResponseWriter, r *http.Request) {
	projectID, err := strconv.ParseInt(r.PathValue("projectID"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid projectID"))
		return
	}
	var req model.CreateServiceRequest
	if !v.DecodeAndValidate(w, r, &req) {
		return
	}
	if req.AppPort == 0 {
		req.AppPort = 8080
	}
	if req.RepoBranch == "" {
		req.RepoBranch = "main"
	}
	res, err := h.db.ExecContext(r.Context(),
		`INSERT INTO services
		  (project_id, name, description, deploy_type, repo_url, repo_branch,
		   framework, build_command, start_command, app_port, host_port)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		projectID, req.Name, req.Description, req.DeployType, req.RepoURL,
		req.RepoBranch, req.Framework, req.BuildCommand, req.StartCommand,
		req.AppPort, nullInt(req.HostPort))
	if err != nil {
		if isUnique(err) {
			writeJSON(w, http.StatusConflict, errMap("service name already exists in project"))
			return
		}
		slog.Error("create service", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	id, _ := res.LastInsertId()
	h.getByID(w, r, id)
}

// GET /api/projects/{projectID}/services/{serviceID}
func (h *ServiceHandler) Get(w http.ResponseWriter, r *http.Request) {
	svcID, err := parseServiceID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid serviceID"))
		return
	}
	h.getByID(w, r, svcID)
}

// PATCH /api/projects/{projectID}/services/{serviceID}
func (h *ServiceHandler) Update(w http.ResponseWriter, r *http.Request) {
	projectID, _ := strconv.ParseInt(r.PathValue("projectID"), 10, 64)
	svcID, err := parseServiceID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid serviceID"))
		return
	}
	var req model.UpdateServiceRequest
	if !v.DecodeAndValidate(w, r, &req) {
		return
	}
	_, err = h.db.ExecContext(r.Context(),
		`UPDATE services SET
		   name         = CASE WHEN ? != '' THEN ? ELSE name END,
		   description  = ?,
		   repo_url     = CASE WHEN ? != '' THEN ? ELSE repo_url END,
		   repo_branch  = CASE WHEN ? != '' THEN ? ELSE repo_branch END,
		   framework    = ?,
		   build_command= ?,
		   start_command= ?,
		   app_port     = CASE WHEN ? > 0 THEN ? ELSE app_port END,
		   host_port    = CASE WHEN ? > 0 THEN ? ELSE host_port END,
		   updated_at   = datetime('now')
		 WHERE id=? AND project_id=?`,
		req.Name, req.Name,
		req.Description,
		req.RepoURL, req.RepoURL,
		req.RepoBranch, req.RepoBranch,
		req.Framework, req.BuildCommand, req.StartCommand,
		req.AppPort, req.AppPort,
		req.HostPort, req.HostPort,
		svcID, projectID)
	if err != nil {
		slog.Error("update service", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	h.getByID(w, r, svcID)
}

// DELETE /api/projects/{projectID}/services/{serviceID}
func (h *ServiceHandler) Delete(w http.ResponseWriter, r *http.Request) {
	projectID, _ := strconv.ParseInt(r.PathValue("projectID"), 10, 64)
	svcID, err := parseServiceID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid serviceID"))
		return
	}

	// Verify service exists and belongs to project
	var containerID sql.NullString
	err = h.db.QueryRowContext(r.Context(),
		`SELECT container_id FROM services WHERE id=? AND project_id=?`, svcID, projectID,
	).Scan(&containerID)
	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusNotFound, errMap("service not found"))
		return
	} else if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}

	// ── Cascade delete in DB ──────────────────────────────────────────────────
	h.db.ExecContext(r.Context(), `DELETE FROM domains WHERE service_id=?`, svcID)       //nolint
	h.db.ExecContext(r.Context(), `DELETE FROM env_variables WHERE service_id=?`, svcID) //nolint
	h.db.ExecContext(r.Context(), `DELETE FROM deployments WHERE service_id=?`, svcID)   //nolint
	h.db.ExecContext(r.Context(), `DELETE FROM services WHERE id=?`, svcID)              //nolint

	// ── Update Caddy (domain removed) ────────────────────────────────────────
	go caddypkg.Reload(h.db)

	// ── Stop and remove the container ─────────────────────────────────────────
	cName := fmt.Sprintf("fd-svc-%d", svcID)
	go func() {
		exec.Command("sudo", "-n", "podman", "stop", "--time", "10", cName).Run() //nolint
		exec.Command("sudo", "-n", "podman", "rm", "-f", cName).Run()             //nolint
		// Remove associated images to free disk space
		stableImage := fmt.Sprintf("featherdeploy/svc-%d:stable", svcID)
		exec.Command("sudo", "-n", "podman", "rmi", "-f", stableImage).Run() //nolint
		// Remove any per-deployment tagged images
		out, err := exec.Command("sudo", "-n", "podman", "images",
			"--format", "{{.Repository}}:{{.Tag}}",
			"--filter", fmt.Sprintf("reference=featherdeploy/svc-%d", svcID),
		).Output()
		if err == nil {
			for _, img := range splitLines(string(out)) {
				if img != "" {
					exec.Command("sudo", "-n", "podman", "rmi", "-f", img).Run() //nolint
				}
			}
		}
		slog.Info("service cleanup complete", "svc_id", svcID, "container", cName)
	}()

	w.WriteHeader(http.StatusNoContent)
}

// splitLines splits a newline-separated string, trimming whitespace.
func splitLines(s string) []string {
	var out []string
	for _, l := range splitNewline(s) {
		if t := trimString(l); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func splitNewline(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func trimString(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\r') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func (h *ServiceHandler) getByID(w http.ResponseWriter, r *http.Request, id int64) {
	row := h.db.QueryRowContext(r.Context(),
		`SELECT id, project_id, name, description, deploy_type, repo_url, repo_branch,
		        framework, build_command, start_command, app_port, COALESCE(host_port, 0),
		        status, container_id, created_at, updated_at
		 FROM services WHERE id=?`, id)
	var s model.Service
	if err := scanServiceRow(row, &s); err == sql.ErrNoRows {
		writeJSON(w, http.StatusNotFound, errMap("service not found"))
		return
	} else if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	writeJSON(w, http.StatusOK, s)
}

type scanner interface {
	Scan(dest ...any) error
}

func scanService(row scanner, s *model.Service) error {
	return row.Scan(&s.ID, &s.ProjectID, &s.Name, &s.Description,
		&s.DeployType, &s.RepoURL, &s.RepoBranch, &s.Framework,
		&s.BuildCommand, &s.StartCommand, &s.AppPort, &s.HostPort,
		&s.Status, &s.ContainerID, &s.CreatedAt, &s.UpdatedAt)
}

func scanServiceRow(row *sql.Row, s *model.Service) error {
	return row.Scan(&s.ID, &s.ProjectID, &s.Name, &s.Description,
		&s.DeployType, &s.RepoURL, &s.RepoBranch, &s.Framework,
		&s.BuildCommand, &s.StartCommand, &s.AppPort, &s.HostPort,
		&s.Status, &s.ContainerID, &s.CreatedAt, &s.UpdatedAt)
}

func parseServiceID(r *http.Request) (int64, error) {
	return strconv.ParseInt(r.PathValue("serviceID"), 10, 64)
}

func nullInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}
