package handler

import (
	"database/sql"
	"log/slog"
	"net/http"
	"time"
)

type DashboardHandler struct{ db *sql.DB }

func NewDashboardHandler(db *sql.DB) *DashboardHandler { return &DashboardHandler{db: db} }

type dashService struct {
	ID        int64  `json:"id"`
	ProjectID int64  `json:"project_id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
}

type dashDeployment struct {
	ID          int64     `json:"id"`
	ServiceID   int64     `json:"service_id"`
	ServiceName string    `json:"service_name"`
	Status      string    `json:"status"`
	CommitSHA   string    `json:"commit_sha,omitempty"`
	DeployType  string    `json:"deploy_type"`
	CreatedAt   time.Time `json:"created_at"`
}

// GET /api/dashboard
// Returns aggregate stats, all service statuses, and the 10 most recent deployments.
func (h *DashboardHandler) Stats(w http.ResponseWriter, r *http.Request) {
	type response struct {
		TotalProjects     int              `json:"total_projects"`
		TotalServices     int              `json:"total_services"`
		RunningServices   int              `json:"running_services"`
		TotalDeployments  int              `json:"total_deployments"`
		FailedDeployments int              `json:"failed_deployments"`
		Services          []dashService    `json:"services"`
		RecentDeployments []dashDeployment `json:"recent_deployments"`
	}

	var res response

	h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM projects`).Scan(&res.TotalProjects)
	h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM services`).Scan(&res.TotalServices)
	h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM services WHERE status='running'`).Scan(&res.RunningServices)
	h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM deployments`).Scan(&res.TotalDeployments)
	h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM deployments WHERE status='failed'`).Scan(&res.FailedDeployments)

	// All services (id, project_id, name, status)
	rows, err := h.db.QueryContext(r.Context(),
		`SELECT id, project_id, name, status FROM services ORDER BY project_id, name`)
	if err != nil {
		slog.Error("dashboard services", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	defer rows.Close()
	res.Services = make([]dashService, 0)
	for rows.Next() {
		var s dashService
		if err := rows.Scan(&s.ID, &s.ProjectID, &s.Name, &s.Status); err == nil {
			res.Services = append(res.Services, s)
		}
	}

	// 10 most recent deployments with service name joined
	depRows, err := h.db.QueryContext(r.Context(),
		`SELECT d.id, d.service_id, COALESCE(s.name,''), d.status, d.commit_sha, d.deploy_type, d.created_at
		 FROM deployments d
		 LEFT JOIN services s ON s.id = d.service_id
		 ORDER BY d.created_at DESC LIMIT 10`)
	if err != nil {
		slog.Error("dashboard deployments", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	defer depRows.Close()
	res.RecentDeployments = make([]dashDeployment, 0)
	for depRows.Next() {
		var d dashDeployment
		if err := depRows.Scan(&d.ID, &d.ServiceID, &d.ServiceName, &d.Status, &d.CommitSHA, &d.DeployType, &d.CreatedAt); err == nil {
			res.RecentDeployments = append(res.RecentDeployments, d)
		}
	}

	writeJSON(w, http.StatusOK, res)
}
