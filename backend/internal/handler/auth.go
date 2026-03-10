package handler

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/deploy-paas/backend/internal/auth"
	"github.com/deploy-paas/backend/internal/middleware"
	"github.com/deploy-paas/backend/internal/model"
	v "github.com/deploy-paas/backend/internal/validator"
)

type AuthHandler struct {
	db        *sql.DB
	jwtSecret string
	tokenTTL  time.Duration
}

func NewAuthHandler(db *sql.DB, jwtSecret string, tokenTTL time.Duration) *AuthHandler {
	return &AuthHandler{db: db, jwtSecret: jwtSecret, tokenTTL: tokenTTL}
}

// POST /api/auth/login
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req model.LoginRequest
	if !v.DecodeAndValidate(w, r, &req) {
		return
	}

	var user model.User
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id, email, name, password_hash, role, github_login FROM users WHERE email=?`, req.Email,
	).Scan(&user.ID, &user.Email, &user.Name, &user.PasswordHash, &user.Role, &user.GitHubLogin)
	if err == sql.ErrNoRows {
		// Timing-safe: hash even on not-found to prevent timing attacks
		auth.CheckPassword("$2a$12$dummy", req.Password) //nolint
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return
	}
	if err != nil {
		slog.Error("query user", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	if err := auth.CheckPassword(user.PasswordHash, req.Password); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return
	}

	token, err := auth.IssueToken(h.jwtSecret, user.ID, user.Email, user.Role, h.tokenTTL)
	if err != nil {
		slog.Error("issue token", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	writeJSON(w, http.StatusOK, model.TokenResponse{Token: token, User: user})
}

// GET /api/auth/me
func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	var user model.User
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id, email, name, role, github_login, created_at, updated_at FROM users WHERE id=?`, claims.UserID,
	).Scan(&user.ID, &user.Email, &user.Name, &user.Role, &user.GitHubLogin, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "user not found"})
		return
	}
	writeJSON(w, http.StatusOK, user)
}

// ─────── helpers ─────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
