package handler

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/auth"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/middleware"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/model"
)

const (
	qrTokenBytes = 32            // → 64-char hex string (256-bit entropy)
	qrCodeTTL    = 5 * time.Minute
	qrSessionTTL = 60 * time.Minute
)

// QRAuthHandler handles QR-code-based login.
//
// Flow:
//  1. Login page (unauthenticated) calls POST /api/auth/qr/init → receives {qr_token, expires_at}.
//  2. Login page renders a QR code whose URL is /qr-approve/{qr_token}.
//  3. An already-authenticated device opens that URL and calls POST /api/auth/qr/{token}/approve.
//  4. Login page polls GET /api/auth/qr/{token}/poll until status=="approved", then uses the JWT.
type QRAuthHandler struct {
	db        *sql.DB
	jwtSecret string
}

func NewQRAuthHandler(db *sql.DB, jwtSecret string) *QRAuthHandler {
	return &QRAuthHandler{db: db, jwtSecret: jwtSecret}
}

// POST /api/auth/qr/init — PUBLIC
// Creates a pending QR session (no user associated yet).
func (h *QRAuthHandler) Init(w http.ResponseWriter, r *http.Request) {
	b := make([]byte, qrTokenBytes)
	if _, err := rand.Read(b); err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("could not generate token"))
		return
	}
	token := hex.EncodeToString(b)
	qrExp := time.Now().UTC().Add(qrCodeTTL)

	// Prune expired rows opportunistically to keep the table small
	h.db.ExecContext(r.Context(), //nolint
		`DELETE FROM qr_login_tokens WHERE status='expired' OR qr_expires_at < datetime('now','-10 minutes')`)

	_, err := h.db.ExecContext(r.Context(),
		`INSERT INTO qr_login_tokens (token, qr_expires_at) VALUES (?, ?)`,
		token, qrExp.Format("2006-01-02 15:04:05"))
	if err != nil {
		slog.Error("qr init", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"qr_token":   token,
		"expires_at": qrExp.UnixMilli(),
	})
}

// GET /api/auth/qr/{token}/poll — PUBLIC
// Login page polls this until status is "approved", then logs in with the returned JWT.
func (h *QRAuthHandler) Poll(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	if len(token) != qrTokenBytes*2 {
		writeJSON(w, http.StatusBadRequest, errMap("invalid token"))
		return
	}

	var status, sessionToken string
	var qrExpires time.Time
	err := h.db.QueryRowContext(r.Context(),
		`SELECT status, qr_expires_at, session_token FROM qr_login_tokens WHERE token=?`, token,
	).Scan(&status, &qrExpires, &sessionToken)
	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusNotFound, errMap("not found"))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}

	// Expire tokens that are still pending but past the QR TTL
	if status == "pending" {
		if time.Now().UTC().After(qrExpires.UTC()) {
			h.db.ExecContext(r.Context(), //nolint
				`UPDATE qr_login_tokens SET status='expired' WHERE token=?`, token)
			writeJSON(w, http.StatusGone, errMap("QR code expired"))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "pending"})
		return
	}

	if status == "expired" {
		writeJSON(w, http.StatusGone, errMap("QR code expired"))
		return
	}

	// status == "approved" — fetch the user and clear the row (single-use)
	var user model.User
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT u.id, u.email, u.name, u.role
		 FROM qr_login_tokens q JOIN users u ON u.id = q.user_id
		 WHERE q.token=?`, token,
	).Scan(&user.ID, &user.Email, &user.Name, &user.Role); err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}

	// Delete the token row so it cannot be polled again (single-use credential)
	h.db.ExecContext(r.Context(), //nolint
		`DELETE FROM qr_login_tokens WHERE token=?`, token)

	writeJSON(w, http.StatusOK, map[string]any{
		"status": "approved",
		"token":  sessionToken,
		"user":   user,
	})
}

// POST /api/auth/qr/{token}/approve — AUTHENTICATED
// The already-logged-in device approves the pending QR session,
// issues a JWT for that session, and stores it for the login-page poll.
func (h *QRAuthHandler) Approve(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	token := chi.URLParam(r, "token")
	if len(token) != qrTokenBytes*2 {
		writeJSON(w, http.StatusBadRequest, errMap("invalid token"))
		return
	}

	var status string
	var qrExpires time.Time
	err := h.db.QueryRowContext(r.Context(),
		`SELECT status, qr_expires_at FROM qr_login_tokens WHERE token=?`, token,
	).Scan(&status, &qrExpires)
	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusNotFound, errMap("QR code not found"))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}

	if status == "approved" {
		writeJSON(w, http.StatusConflict, errMap("QR code has already been approved"))
		return
	}
	if status == "expired" {
		writeJSON(w, http.StatusGone, errMap("QR code has expired"))
		return
	}

	if time.Now().UTC().After(qrExpires.UTC()) {
		h.db.ExecContext(r.Context(), //nolint
			`UPDATE qr_login_tokens SET status='expired' WHERE token=?`, token)
		writeJSON(w, http.StatusGone, errMap("QR code has expired"))
		return
	}

	// Fetch the approving user to embed in the issued JWT
	var user model.User
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT id, email, name, role FROM users WHERE id=?`, claims.UserID,
	).Scan(&user.ID, &user.Email, &user.Name, &user.Role); err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}

	sessionJWT, sessionID, err := auth.IssueToken(h.jwtSecret, user.ID, user.Email, user.Role, qrSessionTTL)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("could not issue session"))
		return
	}

	// Record the session for device management
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if ip == "" {
		ip = r.RemoteAddr
	}
	expiresAt := time.Now().UTC().Add(qrSessionTTL).Format("2006-01-02 15:04:05")
	h.db.ExecContext(r.Context(), //nolint
		`INSERT INTO user_sessions (id, user_id, user_agent, ip_address, expires_at)
		 VALUES (?, ?, ?, ?, ?)`,
		sessionID, user.ID, r.UserAgent(), ip, expiresAt)

	if _, err = h.db.ExecContext(r.Context(),
		`UPDATE qr_login_tokens SET status='approved', user_id=?, session_token=? WHERE token=?`,
		claims.UserID, sessionJWT, token); err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"status": "approved"})
}
