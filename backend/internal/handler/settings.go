package handler

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
)

// SettingsHandler manages platform-wide settings stored in system_settings.
type SettingsHandler struct {
	db *sql.DB
}

func NewSettingsHandler(db *sql.DB) *SettingsHandler {
	return &SettingsHandler{db: db}
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
