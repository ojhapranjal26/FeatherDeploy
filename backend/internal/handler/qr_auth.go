package handler

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/auth"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/middleware"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/model"
)

const (
	qrTokenBytes    = 32             // → 64-char hex string (256-bit entropy)
	qrCodeTTL       = 5 * time.Minute
	qrSessionMaxMin = 60
)

// QRAuthHandler handles QR-code-based temporary device login.
type QRAuthHandler struct {
	db        *sql.DB
	jwtSecret string
}

func NewQRAuthHandler(db *sql.DB, jwtSecret string) *QRAuthHandler {
	return &QRAuthHandler{db: db, jwtSecret: jwtSecret}
}

// POST /api/auth/qr/generate — authenticated
// Generates a one-time QR token.  The caller chooses the session TTL (≤ 60 min).
func (h *QRAuthHandler) Generate(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())

	var req struct {
		TTLMinutes int `json:"ttl_minutes"`
	}
	req.TTLMinutes = qrSessionMaxMin
	json.NewDecoder(r.Body).Decode(&req) //nolint
	if req.TTLMinutes <= 0 || req.TTLMinutes > qrSessionMaxMin {
		req.TTLMinutes = qrSessionMaxMin
	}

	b := make([]byte, qrTokenBytes)
	if _, err := rand.Read(b); err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("could not generate token"))
		return
	}
	token := hex.EncodeToString(b)
	qrExp := time.Now().UTC().Add(qrCodeTTL)

	_, err := h.db.ExecContext(r.Context(),
		`INSERT INTO qr_login_tokens (token, user_id, ttl_minutes, qr_expires_at)
		 VALUES (?, ?, ?, ?)`,
		token, claims.UserID, req.TTLMinutes, qrExp.Format("2006-01-02 15:04:05"))
	if err != nil {
		slog.Error("qr generate", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"qr_token":    token,
		"expires_at":  qrExp.UnixMilli(),
		"ttl_minutes": req.TTLMinutes,
	})
}

// GET /api/auth/qr/{qrToken}/status — public (polled by the generating device)
// Returns the current status and, for UI context, who generated the token.
func (h *QRAuthHandler) Status(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "qrToken")
	if len(token) != qrTokenBytes*2 {
		writeJSON(w, http.StatusBadRequest, errMap("invalid token"))
		return
	}

	var status, qrExpiresStr string
	var userName, userEmail string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT q.status, q.qr_expires_at, u.name, u.email
		 FROM qr_login_tokens q
		 JOIN users u ON u.id = q.user_id
		 WHERE q.token = ?`, token,
	).Scan(&status, &qrExpiresStr, &userName, &userEmail)
	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusNotFound, errMap("not found"))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}

	if status == "pending" {
		qrExp, _ := time.ParseInLocation("2006-01-02 15:04:05", qrExpiresStr, time.UTC)
		if time.Now().UTC().After(qrExp) {
			h.db.ExecContext(r.Context(), //nolint
				`UPDATE qr_login_tokens SET status='expired' WHERE token=?`, token)
			status = "expired"
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":     status,
		"user_name":  userName,
		"user_email": userEmail,
	})
}

// POST /api/auth/qr/{qrToken}/claim — public (the scanning / other device)
// Validates the QR token, issues a short-lived JWT, and marks the token used.
func (h *QRAuthHandler) Claim(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "qrToken")
	if len(token) != qrTokenBytes*2 {
		writeJSON(w, http.StatusBadRequest, errMap("invalid token"))
		return
	}

	var userID int64
	var status, qrExpiresStr string
	var ttlMinutes int
	err := h.db.QueryRowContext(r.Context(),
		`SELECT user_id, status, ttl_minutes, qr_expires_at
		 FROM qr_login_tokens WHERE token=?`, token,
	).Scan(&userID, &status, &ttlMinutes, &qrExpiresStr)
	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusNotFound, errMap("QR code not found"))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}

	qrExp, _ := time.ParseInLocation("2006-01-02 15:04:05", qrExpiresStr, time.UTC)
	if time.Now().UTC().After(qrExp) || status == "expired" {
		h.db.ExecContext(r.Context(), //nolint
			`UPDATE qr_login_tokens SET status='expired' WHERE token=?`, token)
		writeJSON(w, http.StatusGone, errMap("QR code has expired — please generate a new one"))
		return
	}
	if status == "claimed" {
		writeJSON(w, http.StatusConflict, errMap("QR code has already been used"))
		return
	}

	var user model.User
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT id, email, name, role FROM users WHERE id=?`, userID,
	).Scan(&user.ID, &user.Email, &user.Name, &user.Role); err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}

	ttl := time.Duration(ttlMinutes) * time.Minute
	sessionJWT, err := auth.IssueToken(h.jwtSecret, user.ID, user.Email, user.Role, ttl)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("could not issue session"))
		return
	}

	if _, err = h.db.ExecContext(r.Context(),
		`UPDATE qr_login_tokens SET status='claimed', session_token=? WHERE token=?`,
		sessionJWT, token); err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"token":       sessionJWT,
		"user":        user,
		"ttl_minutes": ttlMinutes,
	})
}
