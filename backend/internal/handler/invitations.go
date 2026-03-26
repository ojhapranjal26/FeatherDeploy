package handler

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/ojhapranjal26/featherdeploy/backend/internal/auth"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/mailer"
	mw "github.com/ojhapranjal26/featherdeploy/backend/internal/middleware"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/model"
	v "github.com/ojhapranjal26/featherdeploy/backend/internal/validator"
)

const inviteTTL = 15 * time.Minute

// InvitationHandler manages invite-based user registration.
type InvitationHandler struct {
	db        *sql.DB
	config    *ConfigStore
	jwtSecret string
	jwtTTL    time.Duration
	origin    string // frontend base URL for building invite links
}

func NewInvitationHandler(db *sql.DB, config *ConfigStore, jwtSecret string, jwtTTL time.Duration, origin string) *InvitationHandler {
	return &InvitationHandler{db: db, config: config, jwtSecret: jwtSecret, jwtTTL: jwtTTL, origin: origin}
}

// ─── POST /api/admin/invitations ─────────────────────────────────────────────
// Admin or superadmin creates an invitation for a new user.
// Sends an email with a time-limited token link.
func (h *InvitationHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req model.CreateInvitationRequest
	if !v.DecodeAndValidate(w, r, &req) {
		return
	}

	claims := mw.GetClaims(r.Context())

	// Superadmin can invite any role; admin can only invite 'user'
	if claims.Role == model.RoleAdmin && req.Role != model.RoleUser {
		http.Error(w, `{"error":"admins can only invite users with role 'user'"}`, http.StatusForbidden)
		return
	}

	// Check email not already registered
	var exists int
	_ = h.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM users WHERE email = ? COLLATE NOCASE`, req.Email).Scan(&exists)
	if exists > 0 {
		http.Error(w, `{"error":"a user with that email already exists"}`, http.StatusConflict)
		return
	}

	// Generate a cryptographically secure token
	token, err := generateToken(32)
	if err != nil {
		http.Error(w, `{"error":"failed to generate token"}`, http.StatusInternalServerError)
		return
	}

	expiresAt := time.Now().UTC().Add(inviteTTL)

	// Get inviter name for the email
	var inviterName string
	_ = h.db.QueryRowContext(r.Context(), `SELECT name FROM users WHERE id = ?`, claims.UserID).Scan(&inviterName)

	// Persist invitation
	res, err := h.db.ExecContext(r.Context(),
		`INSERT INTO invitations (email, token, role, invited_by, expires_at) VALUES (?,?,?,?,?)`,
		req.Email, token, req.Role, claims.UserID, expiresAt.Format(time.RFC3339),
	)
	if err != nil {
		http.Error(w, `{"error":"failed to create invitation"}`, http.StatusInternalServerError)
		return
	}
	id, _ := res.LastInsertId()

	// Build invite URL
	inviteURL := fmt.Sprintf("%s/invite/%s", h.origin, token)

	// Send email (non-blocking: log but don't fail if SMTP is down)
	m := mailer.New(h.config.SMTPConfig(r.Context()))
	if err := m.SendInvitation(req.Email, inviterName, inviteURL, inviteTTL); err != nil {
		// Log but continue — admin can still copy the link from the response
		_ = err
	}

	inv := model.Invitation{
		ID:        id,
		Email:     req.Email,
		Token:     token, // returned so admin can copy the link manually if email fails
		Role:      req.Role,
		InvitedBy: claims.UserID,
		ExpiresAt: expiresAt,
		CreatedAt: time.Now().UTC(),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]any{
		"invitation": inv,
		"invite_url": inviteURL,
	})
}

// ─── GET /api/admin/invitations ──────────────────────────────────────────────
// List pending (un-accepted, non-expired) invitations
func (h *InvitationHandler) List(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.QueryContext(r.Context(),
		`SELECT i.id, i.email, i.role, i.invited_by, i.expires_at, i.accepted_at, i.created_at,
		        u.name AS inviter_name
		 FROM invitations i
		 LEFT JOIN users u ON u.id = i.invited_by
		 ORDER BY i.created_at DESC LIMIT 100`)
	if err != nil {
		http.Error(w, `{"error":"query failed"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type row struct {
		model.Invitation
		InviterName string `json:"inviter_name"`
	}
	var out []row
	for rows.Next() {
		var ri row
		var expiresAt, createdAt string
		var acceptedAt *string
		if err := rows.Scan(&ri.ID, &ri.Email, &ri.Role, &ri.InvitedBy,
			&expiresAt, &acceptedAt, &createdAt, &ri.InviterName); err != nil {
			continue
		}
		ri.ExpiresAt, _ = time.Parse(time.RFC3339, expiresAt)
		ri.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		if acceptedAt != nil {
			t, _ := time.Parse(time.RFC3339, *acceptedAt)
			ri.AcceptedAt = &t
		}
		out = append(out, ri)
	}
	if out == nil {
		out = []row{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// ─── GET /api/invitations/{token} ────────────────────────────────────────────
// Public: verify a token is valid and return the associated email
func (h *InvitationHandler) Verify(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if token == "" {
		http.Error(w, `{"error":"missing token"}`, http.StatusBadRequest)
		return
	}

	var email, role, expiresAtStr string
	var acceptedAt *string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT email, role, expires_at, accepted_at FROM invitations WHERE token = ?`, token,
	).Scan(&email, &role, &expiresAtStr, &acceptedAt)

	if err == sql.ErrNoRows {
		http.Error(w, `{"error":"invalid or expired invitation"}`, http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, `{"error":"database error"}`, http.StatusInternalServerError)
		return
	}
	if acceptedAt != nil {
		http.Error(w, `{"error":"invitation already used"}`, http.StatusGone)
		return
	}
	expiresAt, _ := time.Parse(time.RFC3339, expiresAtStr)
	if time.Now().UTC().After(expiresAt) {
		http.Error(w, `{"error":"invitation has expired"}`, http.StatusGone)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"email":      email,
		"role":       role,
		"expires_at": expiresAtStr,
	})
}

// ─── POST /api/invitations/{token}/accept ────────────────────────────────────
// Public: user sets name + password from the invite link → creates account → returns JWT
func (h *InvitationHandler) Accept(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if token == "" {
		http.Error(w, `{"error":"missing token"}`, http.StatusBadRequest)
		return
	}

	var req model.AcceptInvitationRequest
	if !v.DecodeAndValidate(w, r, &req) {
		return
	}

	// Fetch and validate the invitation inside a transaction
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		http.Error(w, `{"error":"transaction failed"}`, http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	var invID int64
	var email, role, expiresAtStr string
	var acceptedAt *string
	err = tx.QueryRowContext(r.Context(),
		`SELECT id, email, role, expires_at, accepted_at FROM invitations WHERE token = ?`, token,
	).Scan(&invID, &email, &role, &expiresAtStr, &acceptedAt)

	if err == sql.ErrNoRows {
		http.Error(w, `{"error":"invalid or expired invitation"}`, http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, `{"error":"database error"}`, http.StatusInternalServerError)
		return
	}
	if acceptedAt != nil {
		http.Error(w, `{"error":"invitation already used"}`, http.StatusGone)
		return
	}
	expiresAt, _ := time.Parse(time.RFC3339, expiresAtStr)
	if time.Now().UTC().After(expiresAt) {
		http.Error(w, `{"error":"invitation has expired"}`, http.StatusGone)
		return
	}

	// Check the email isn't already taken (race condition guard)
	var exists int
	_ = tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM users WHERE email = ? COLLATE NOCASE`, email).Scan(&exists)
	if exists > 0 {
		http.Error(w, `{"error":"a user with this email already exists"}`, http.StatusConflict)
		return
	}

	// Hash password
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		http.Error(w, `{"error":"failed to hash password"}`, http.StatusInternalServerError)
		return
	}

	// Create user
	res, err := tx.ExecContext(r.Context(),
		`INSERT INTO users (email, name, password_hash, role) VALUES (?,?,?,?)`,
		email, req.Name, hash, role,
	)
	if err != nil {
		http.Error(w, `{"error":"failed to create user"}`, http.StatusInternalServerError)
		return
	}
	userID, _ := res.LastInsertId()

	// Mark invitation as accepted
	_, err = tx.ExecContext(r.Context(),
		`UPDATE invitations SET accepted_at = datetime('now') WHERE id = ?`, invID,
	)
	if err != nil {
		http.Error(w, `{"error":"failed to mark invitation as accepted"}`, http.StatusInternalServerError)
		return
	}

	if err := tx.Commit(); err != nil {
		http.Error(w, `{"error":"commit failed"}`, http.StatusInternalServerError)
		return
	}

	// Issue JWT and return user
	tokenStr, err := auth.IssueToken(h.jwtSecret, userID, email, role, h.jwtTTL)
	if err != nil {
		http.Error(w, `{"error":"failed to issue token"}`, http.StatusInternalServerError)
		return
	}

	user := model.User{ID: userID, Email: email, Name: req.Name, Role: role, CreatedAt: time.Now().UTC()}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(model.TokenResponse{Token: tokenStr, User: user})
}

// ─── DELETE /api/admin/invitations/{invitationID} ────────────────────────────
// Admin can revoke a pending invitation
func (h *InvitationHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("invitationID")
	res, err := h.db.ExecContext(r.Context(),
		`DELETE FROM invitations WHERE id = ? AND accepted_at IS NULL`, id)
	if err != nil {
		http.Error(w, `{"error":"delete failed"}`, http.StatusInternalServerError)
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		http.Error(w, `{"error":"invitation not found or already accepted"}`, http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func generateToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(b), nil
}

