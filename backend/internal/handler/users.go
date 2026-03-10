package handler

import (
	"database/sql"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/deploy-paas/backend/internal/middleware"
	"github.com/deploy-paas/backend/internal/model"
	v "github.com/deploy-paas/backend/internal/validator"
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
