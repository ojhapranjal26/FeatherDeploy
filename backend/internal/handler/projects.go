package handler

import (
	"database/sql"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/ojhapranjal26/featherdeploy/backend/internal/middleware"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/model"
	v "github.com/ojhapranjal26/featherdeploy/backend/internal/validator"
)

type ProjectHandler struct{ db *sql.DB }

func NewProjectHandler(db *sql.DB) *ProjectHandler { return &ProjectHandler{db: db} }

// GET /api/projects
// superadmin/admin: all projects; user: only their member projects
func (h *ProjectHandler) List(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())

	var (
		rows *sql.Rows
		err  error
	)
	if claims.Role == model.RoleSuperAdmin || claims.Role == model.RoleAdmin {
		rows, err = h.db.QueryContext(r.Context(),
			`SELECT p.id, p.name, p.description, p.owner_id, p.created_at, p.updated_at,
			        (SELECT COUNT(*) FROM services s WHERE s.project_id=p.id) AS service_count
			 FROM projects p ORDER BY p.created_at DESC`)
	} else {
		rows, err = h.db.QueryContext(r.Context(),
			`SELECT p.id, p.name, p.description, p.owner_id, p.created_at, p.updated_at,
			        (SELECT COUNT(*) FROM services s WHERE s.project_id=p.id) AS service_count
			 FROM projects p
			 JOIN project_members pm ON pm.project_id=p.id AND pm.user_id=?
			 ORDER BY p.created_at DESC`, claims.UserID)
	}
	if err != nil {
		slog.Error("list projects", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	defer rows.Close()

	projects := make([]model.Project, 0)
	for rows.Next() {
		var p model.Project
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.OwnerID,
			&p.CreatedAt, &p.UpdatedAt, &p.ServiceCount); err != nil {
			continue
		}
		projects = append(projects, p)
	}
	writeJSON(w, http.StatusOK, projects)
}

// POST /api/projects
func (h *ProjectHandler) Create(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())

	var req model.CreateProjectRequest
	if !v.DecodeAndValidate(w, r, &req) {
		return
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(r.Context(),
		`INSERT INTO projects (name, description, owner_id) VALUES (?,?,?)`,
		req.Name, req.Description, claims.UserID)
	if err != nil {
		if isUnique(err) {
			writeJSON(w, http.StatusConflict, errMap("project name already exists"))
			return
		}
		slog.Error("create project", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	projectID, _ := res.LastInsertId()

	// Add creator as owner in project_members
	_, err = tx.ExecContext(r.Context(),
		`INSERT INTO project_members (project_id, user_id, role) VALUES (?,?,?)`,
		projectID, claims.UserID, "owner")
	if err != nil {
		slog.Error("add project member", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	if err := tx.Commit(); err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}

	var p model.Project
	h.db.QueryRowContext(r.Context(),
		`SELECT id, name, description, owner_id, created_at, updated_at FROM projects WHERE id=?`,
		projectID).Scan(&p.ID, &p.Name, &p.Description, &p.OwnerID, &p.CreatedAt, &p.UpdatedAt)
	writeJSON(w, http.StatusCreated, p)
}

// GET /api/projects/{projectID}
func (h *ProjectHandler) Get(w http.ResponseWriter, r *http.Request) {
	projectID, err := strconv.ParseInt(r.PathValue("projectID"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid projectID"))
		return
	}
	var p model.Project
	err = h.db.QueryRowContext(r.Context(),
		`SELECT p.id, p.name, p.description, p.owner_id, p.created_at, p.updated_at,
		        (SELECT COUNT(*) FROM services s WHERE s.project_id=p.id) AS service_count
		 FROM projects p WHERE p.id=?`, projectID,
	).Scan(&p.ID, &p.Name, &p.Description, &p.OwnerID, &p.CreatedAt, &p.UpdatedAt, &p.ServiceCount)
	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusNotFound, errMap("project not found"))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// PATCH /api/projects/{projectID}
func (h *ProjectHandler) Update(w http.ResponseWriter, r *http.Request) {
	projectID, err := strconv.ParseInt(r.PathValue("projectID"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid projectID"))
		return
	}

	var req model.UpdateProjectRequest
	if !v.DecodeAndValidate(w, r, &req) {
		return
	}

	_, err = h.db.ExecContext(r.Context(),
		`UPDATE projects SET name=COALESCE(NULLIF(?,0), name),
		  description=?, updated_at=datetime('now') WHERE id=?`,
		req.Name, req.Description, projectID)
	if err != nil {
		if isUnique(err) {
			writeJSON(w, http.StatusConflict, errMap("project name already exists"))
			return
		}
		slog.Error("update project", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	h.Get(w, r)
}

// DELETE /api/projects/{projectID}
func (h *ProjectHandler) Delete(w http.ResponseWriter, r *http.Request) {
	projectID, err := strconv.ParseInt(r.PathValue("projectID"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid projectID"))
		return
	}
	res, err := h.db.ExecContext(r.Context(), `DELETE FROM projects WHERE id=?`, projectID)
	if err != nil {
		slog.Error("delete project", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeJSON(w, http.StatusNotFound, errMap("project not found"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /api/projects/{projectID}/members
func (h *ProjectHandler) ListMembers(w http.ResponseWriter, r *http.Request) {
	projectID, err := strconv.ParseInt(r.PathValue("projectID"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid projectID"))
		return
	}
	rows, err := h.db.QueryContext(r.Context(),
		`SELECT u.id, u.email, u.name, u.role, pm.role AS project_role
		 FROM users u JOIN project_members pm ON pm.user_id=u.id
		 WHERE pm.project_id=? ORDER BY u.id`, projectID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	defer rows.Close()
	type Member struct {
		model.User
		ProjectRole string `json:"project_role"`
	}
	members := make([]Member, 0)
	for rows.Next() {
		var m Member
		rows.Scan(&m.ID, &m.Email, &m.Name, &m.Role, &m.ProjectRole)
		members = append(members, m)
	}
	writeJSON(w, http.StatusOK, members)
}

// POST /api/projects/{projectID}/members
func (h *ProjectHandler) AddMember(w http.ResponseWriter, r *http.Request) {
	projectID, err := strconv.ParseInt(r.PathValue("projectID"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid projectID"))
		return
	}
	var req model.AssignProjectMemberRequest
	if !v.DecodeAndValidate(w, r, &req) {
		return
	}
	_, err = h.db.ExecContext(r.Context(),
		`INSERT INTO project_members (project_id, user_id, role) VALUES (?,?,?)
		 ON CONFLICT(project_id, user_id) DO UPDATE SET role=excluded.role`,
		projectID, req.UserID, req.Role)
	if err != nil {
		slog.Error("add member", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// DELETE /api/projects/{projectID}/members/{userID}
func (h *ProjectHandler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	projectID, _ := strconv.ParseInt(r.PathValue("projectID"), 10, 64)
	userID, err := strconv.ParseInt(r.PathValue("userID"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid userID"))
		return
	}
	h.db.ExecContext(r.Context(),
		`DELETE FROM project_members WHERE project_id=? AND user_id=?`, projectID, userID)
	w.WriteHeader(http.StatusNoContent)
}

