package handler

import (
	"database/sql"
	"encoding/json"
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
		// Only http/https URLs are allowed (or empty to clear the logo).
		if u != "" && !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			writeJSON(w, http.StatusBadRequest, errMap("logo_url must start with http:// or https://"))
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
