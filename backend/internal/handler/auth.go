package handler

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/ojhapranjal26/featherdeploy/backend/internal/auth"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/middleware"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/model"
	v "github.com/ojhapranjal26/featherdeploy/backend/internal/validator"
)

// ─── Login rate limiter ───────────────────────────────────────────────────────
// Simple fixed-window rate limiter keyed by client IP.
// Limit: 10 attempts per IP per minute. Protects the bcrypt verify path.

const (
	loginRateMax    = 10
	loginRatePeriod = time.Minute
)

var (
	loginMu  sync.Mutex
	loginWin = make(map[string]*loginWindow)
)

type loginWindow struct {
	count   int
	resetAt time.Time
}

func loginRateCheck(ip string) bool {
	loginMu.Lock()
	defer loginMu.Unlock()
	now := time.Now()
	// Evict expired entries when the map grows large to prevent unbounded memory use.
	if len(loginWin) > 5000 {
		for k, w := range loginWin {
			if now.After(w.resetAt) {
				delete(loginWin, k)
			}
		}
	}
	w, ok := loginWin[ip]
	if !ok || now.After(w.resetAt) {
		loginWin[ip] = &loginWindow{count: 1, resetAt: now.Add(loginRatePeriod)}
		return true
	}
	w.count++
	return w.count <= loginRateMax
}

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
	// Rate-limit by client IP (10 attempts/minute) to prevent brute-force attacks.
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr // RealIP middleware may have already stripped the port
	}
	if !loginRateCheck(ip) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many login attempts — wait 1 minute and try again"})
		return
	}

	var req model.LoginRequest
	if !v.DecodeAndValidate(w, r, &req) {
		return
	}

	var user model.User
	dbErr := h.db.QueryRowContext(r.Context(),
		`SELECT id, email, name, password_hash, role, github_login FROM users WHERE email=?`, req.Email,
	).Scan(&user.ID, &user.Email, &user.Name, &user.PasswordHash, &user.Role, &user.GitHubLogin)
	if dbErr == sql.ErrNoRows {
		// Timing-safe: hash even on not-found to prevent timing attacks
		auth.CheckPassword("$2a$12$dummy", req.Password) //nolint
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return
	}
	if dbErr != nil {
		slog.Error("query user", "err", dbErr)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	if err := auth.CheckPassword(user.PasswordHash, req.Password); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return
	}

	token, sessionID, err := auth.IssueToken(h.jwtSecret, user.ID, user.Email, user.Role, h.tokenTTL)
	if err != nil {
		slog.Error("issue token", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	// Record the session for device management
	expiresAt := time.Now().UTC().Add(h.tokenTTL).Format("2006-01-02 15:04:05")
	h.db.ExecContext(r.Context(), //nolint
		`INSERT INTO user_sessions (id, user_id, user_agent, ip_address, expires_at)
		 VALUES (?, ?, ?, ?, ?)`,
		sessionID, user.ID, r.UserAgent(), ip, expiresAt)

	writeJSON(w, http.StatusOK, model.TokenResponse{Token: token, User: user})
}

// GET /api/auth/me
func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	var user model.User
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id, email, name, role, github_login, created_at, updated_at FROM users WHERE id=?`, claims.UserID,
	).Scan(&user.ID, &user.Email, &user.Name, &user.Role, &user.GitHubLogin, &user.CreatedAt, &user.UpdatedAt)
	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "user not found"})
		return
	}
	if err != nil {
		slog.Error("Me: scan user", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, user)
}

// POST /api/auth/logout
func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims != nil && claims.ID != "" {
		h.db.ExecContext(r.Context(), //nolint
			`UPDATE user_sessions SET revoked=1 WHERE id=?`, claims.ID)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ─────── helpers ─────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

