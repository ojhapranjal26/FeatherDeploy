package handler

import (
	"database/sql"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/middleware"
)

// SessionsHandler manages device/session listing and revocation.
type SessionsHandler struct {
	db *sql.DB
}

func NewSessionsHandler(db *sql.DB) *SessionsHandler {
	return &SessionsHandler{db: db}
}

// sessionRow is the shape returned to the frontend.
type sessionRow struct {
	ID        string `json:"id"`
	UserAgent string `json:"user_agent"`
	IPAddress string `json:"ip_address"`
	CreatedAt string `json:"created_at"`
	LastSeen  string `json:"last_seen"`
	IsCurrent bool   `json:"is_current"`
}

// GET /api/auth/sessions
// Returns all non-revoked, non-expired sessions for the current user.
func (h *SessionsHandler) List(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())

	rows, err := h.db.QueryContext(r.Context(),
		`SELECT id, user_agent, ip_address, created_at, last_seen
		 FROM user_sessions
		 WHERE user_id=? AND revoked=0 AND expires_at > datetime('now')
		 ORDER BY last_seen DESC`,
		claims.UserID,
	)
	if err != nil {
		slog.Error("sessions list", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	defer rows.Close()

	sessions := make([]sessionRow, 0)
	for rows.Next() {
		var s sessionRow
		if err := rows.Scan(&s.ID, &s.UserAgent, &s.IPAddress, &s.CreatedAt, &s.LastSeen); err != nil {
			continue
		}
		s.IsCurrent = s.ID == claims.ID
		sessions = append(sessions, s)
	}
	writeJSON(w, http.StatusOK, sessions)
}

// DELETE /api/auth/sessions/{sessionID}
// Revokes (logs out) a single session owned by the current user.
// The current session cannot be revoked via this endpoint; use POST /api/auth/logout instead.
func (h *SessionsHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	sessionID := chi.URLParam(r, "sessionID")

	if sessionID == "" {
		writeJSON(w, http.StatusBadRequest, errMap("missing session ID"))
		return
	}
	if sessionID == claims.ID {
		writeJSON(w, http.StatusBadRequest, errMap("use logout to end the current session"))
		return
	}

	res, err := h.db.ExecContext(r.Context(),
		`UPDATE user_sessions SET revoked=1 WHERE id=? AND user_id=?`,
		sessionID, claims.UserID,
	)
	if err != nil {
		slog.Error("session revoke", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeJSON(w, http.StatusNotFound, errMap("session not found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

// DELETE /api/auth/sessions/others
// Revokes all sessions for the current user except the current one.
func (h *SessionsHandler) RevokeOthers(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())

	_, err := h.db.ExecContext(r.Context(),
		`UPDATE user_sessions SET revoked=1 WHERE user_id=? AND id != ?`,
		claims.UserID, claims.ID,
	)
	if err != nil {
		slog.Error("session revoke-others", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
