package handler

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// SettingsHandler manages platform-wide settings stored in system_settings
// as well as encrypted application config in app_settings.
type SettingsHandler struct {
	db     *sql.DB
	config *ConfigStore
}

func NewSettingsHandler(db *sql.DB, config *ConfigStore) *SettingsHandler {
	return &SettingsHandler{db: db, config: config}
}

// GET /api/settings/branding — public endpoint, used by the login page before auth.
func (h *SettingsHandler) GetBranding(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.QueryContext(r.Context(),
		`SELECT setting_key, value FROM system_settings WHERE setting_key IN ('company_name','logo_url')`,
	)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	defer rows.Close()

	resp := map[string]string{"company_name": "", "logo_url": ""}
	for rows.Next() {
		var k, v string
		if rows.Scan(&k, &v) == nil {
			resp[k] = v
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// PUT /api/settings/branding — superadmin only (enforced at router level via RequireRole).
func (h *SettingsHandler) SetBranding(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CompanyName *string `json:"company_name"`
		LogoURL     *string `json:"logo_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid JSON"))
		return
	}

	if req.CompanyName != nil {
		name := strings.TrimSpace(*req.CompanyName)
		if len(name) > 120 {
			writeJSON(w, http.StatusBadRequest, errMap("company_name must be ≤ 120 characters"))
			return
		}
		if _, err := h.db.ExecContext(r.Context(),
			`INSERT INTO system_settings(setting_key,value,updated_at) VALUES('company_name',?,datetime('now'))
		 ON CONFLICT(setting_key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
			name,
		); err != nil {
			writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
			return
		}
	}

	if req.LogoURL != nil {
		u := strings.TrimSpace(*req.LogoURL)
		if len(u) > 2048 {
			writeJSON(w, http.StatusBadRequest, errMap("logo_url must be ≤ 2048 characters"))
			return
		}
		// Allow external http/https URLs, the internal uploaded-logo path, or empty (to clear).
		if u != "" && !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") && u != "/api/settings/branding/logo" {
			writeJSON(w, http.StatusBadRequest, errMap("logo_url must be an http(s) URL or empty"))
			return
		}
		if _, err := h.db.ExecContext(r.Context(),
			`INSERT INTO system_settings(setting_key,value,updated_at) VALUES('logo_url',?,datetime('now'))
		 ON CONFLICT(setting_key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
			u,
		); err != nil {
			writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ─── SMTP settings ───────────────────────────────────────────────────────────

// ─── Logo upload / serve ────────────────────────────────────────────────────

// GET /api/settings/branding/logo — public
// Serves the uploaded logo image stored in system_settings.
func (h *SettingsHandler) GetLogoImage(w http.ResponseWriter, r *http.Request) {
	var data, mime string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT value FROM system_settings WHERE setting_key = 'logo_data'`,
	).Scan(&data)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if h.db.QueryRowContext(r.Context(),
		`SELECT value FROM system_settings WHERE setting_key = 'logo_mime'`,
	).Scan(&mime) != nil {
		mime = "image/jpeg"
	}
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		http.Error(w, "invalid image data", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(raw) //nolint:errcheck
}

// POST /api/settings/branding/logo — superadmin only
// Accepts multipart/form-data with a "logo" file (max 2 MB, images only).
// Stores the image in system_settings and sets logo_url to the internal path.
func (h *SettingsHandler) UploadLogo(w http.ResponseWriter, r *http.Request) {
	const maxBytes = 2 << 20 // 2 MB
	if err := r.ParseMultipartForm(maxBytes); err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("file too large (max 2 MB)"))
		return
	}
	file, _, err := r.FormFile("logo")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("missing 'logo' file field"))
		return
	}
	defer file.Close()

	raw, err := io.ReadAll(io.LimitReader(file, maxBytes))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("failed to read file"))
		return
	}

	// Detect content-type from actual bytes (not browser-supplied header).
	contentType := http.DetectContentType(raw)
	allowed := map[string]bool{
		"image/jpeg": true,
		"image/png":  true,
		"image/gif":  true,
		"image/webp": true,
	}
	if !allowed[contentType] {
		// DetectContentType returns text/plain for SVG; check raw bytes.
		snippet := strings.TrimSpace(string(raw[:min(512, len(raw))]))
		if strings.HasPrefix(snippet, "<svg") || strings.HasPrefix(snippet, "<?xml") {
			contentType = "image/svg+xml"
		} else {
			writeJSON(w, http.StatusBadRequest, errMap("unsupported file type — use JPEG, PNG, GIF, WebP, or SVG"))
			return
		}
	}

	encoded := base64.StdEncoding.EncodeToString(raw)
	const logoPath = "/api/settings/branding/logo"
	for _, pair := range [][2]string{
		{"logo_data", encoded},
		{"logo_mime", contentType},
		{"logo_url", logoPath},
	} {
		if _, err := h.db.ExecContext(r.Context(),
			`INSERT INTO system_settings(setting_key,value,updated_at) VALUES(?,?,datetime('now'))
			 ON CONFLICT(setting_key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
			pair[0], pair[1],
		); err != nil {
			writeJSON(w, http.StatusInternalServerError, errMap("failed to store logo"))
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"logo_url": logoPath})
}

// GET /api/settings/smtp — superadmin only
// Returns display-safe SMTP status (host/port/from/tls visible; credentials as bool).
func (h *SettingsHandler) GetSMTPStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.config.SMTPStatus(r.Context()))
}

// POST /api/settings/smtp — superadmin only
// Stores (and encrypts) SMTP credentials. All fields are optional;
// omitting a field leaves the existing stored value unchanged.
// Send an empty string to clear a specific field.
func (h *SettingsHandler) SetSMTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Host string `json:"host"`
		Port string `json:"port"`
		User string `json:"user"`
		Pass string `json:"pass"`
		From string `json:"from"`
		TLS  string `json:"tls"` // "true" / "false" / ""
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid JSON"))
		return
	}
	if err := h.config.SetSMTP(r.Context(), req.Host, req.Port, req.User, req.Pass, req.From, req.TLS); err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("failed to save SMTP settings: "+err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, h.config.SMTPStatus(r.Context()))
}

// DELETE /api/settings/smtp — superadmin only
// Removes all stored SMTP settings (the server falls back to env-var defaults).
func (h *SettingsHandler) DeleteSMTP(w http.ResponseWriter, r *http.Request) {
	if err := h.config.DeleteSMTP(r.Context()); err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("failed to remove SMTP settings"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ─── GitHub OAuth settings ────────────────────────────────────────────────────

// GET /api/settings/github-oauth — superadmin only
func (h *SettingsHandler) GetGitHubOAuthStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.config.GitHubOAuthStatus(r.Context()))
}

// POST /api/settings/github-oauth — superadmin only
func (h *SettingsHandler) SetGitHubOAuth(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid JSON"))
		return
	}
	if req.ClientID == "" && req.ClientSecret == "" {
		writeJSON(w, http.StatusBadRequest, errMap("provide client_id and/or client_secret"))
		return
	}
	if err := h.config.SetGitHubOAuth(r.Context(), req.ClientID, req.ClientSecret); err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("failed to save GitHub OAuth settings: "+err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, h.config.GitHubOAuthStatus(r.Context()))
}

// DELETE /api/settings/github-oauth — superadmin only
func (h *SettingsHandler) DeleteGitHubOAuth(w http.ResponseWriter, r *http.Request) {
	if err := h.config.DeleteGitHubOAuth(r.Context()); err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("failed to remove GitHub OAuth settings"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
