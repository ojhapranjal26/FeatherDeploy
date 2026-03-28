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

type UserHandler struct{ db *sql.DB }

func NewUserHandler(db *sql.DB) *UserHandler { return &UserHandler{db: db} }

// GET /api/admin/users  — superadmin + admin only
func (h *UserHandler) List(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.QueryContext(r.Context(),
		`SELECT id, email, name, role, created_at, updated_at FROM users ORDER BY id`)
	if err != nil {
		slog.Error("list users", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	defer rows.Close()

	users := make([]model.User, 0)
	for rows.Next() {
		var u model.User
		if err := rows.Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.CreatedAt, &u.UpdatedAt); err != nil {
			continue
		}
		users = append(users, u)
	}
	writeJSON(w, http.StatusOK, users)
}

// PATCH /api/admin/users/{userID}/role  — superadmin only
func (h *UserHandler) UpdateRole(w http.ResponseWriter, r *http.Request) {
	targetID, err := strconv.ParseInt(r.PathValue("userID"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid userID"))
		return
	}

	caller := middleware.GetClaims(r.Context())
	if caller.UserID == targetID {
		writeJSON(w, http.StatusForbidden, errMap("cannot change your own role"))
		return
	}

	var req model.UpdateUserRoleRequest
	if !v.DecodeAndValidate(w, r, &req) {
		return
	}

	res, err := h.db.ExecContext(r.Context(),
		`UPDATE users SET role=?, updated_at=datetime('now') WHERE id=?`, req.Role, targetID)
	if err != nil {
		slog.Error("update user role", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeJSON(w, http.StatusNotFound, errMap("user not found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// DELETE /api/admin/users/{userID}  — superadmin only
func (h *UserHandler) Delete(w http.ResponseWriter, r *http.Request) {
	targetID, err := strconv.ParseInt(r.PathValue("userID"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid userID"))
		return
	}
	caller := middleware.GetClaims(r.Context())
	if caller.UserID == targetID {
		writeJSON(w, http.StatusForbidden, errMap("cannot delete yourself"))
		return
	}
	res, err := h.db.ExecContext(r.Context(), `DELETE FROM users WHERE id=?`, targetID)
	if err != nil {
		slog.Error("delete user", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeJSON(w, http.StatusNotFound, errMap("user not found"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func errMap(msg string) map[string]string { return map[string]string{"error": msg} }

// GET /api/users/lookup?email=xxx  — any authenticated user
// Returns basic public info about a registered user so project owners can
// look up someone by email before adding them as a project member.
func (h *UserHandler) Lookup(w http.ResponseWriter, r *http.Request) {
	email := r.URL.Query().Get("email")
	if email == "" {
		writeJSON(w, http.StatusBadRequest, errMap("email query param required"))
		return
	}
	var u struct {
		ID    int64  `json:"id"`
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id, email, name FROM users WHERE email=?`, email,
	).Scan(&u.ID, &u.Email, &u.Name)
	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusNotFound, errMap("user not found"))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	writeJSON(w, http.StatusOK, u)
}

