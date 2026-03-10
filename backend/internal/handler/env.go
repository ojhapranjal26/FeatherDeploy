package handler

import (
	"database/sql"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/deploy-paas/backend/internal/model"
	v "github.com/deploy-paas/backend/internal/validator"
)

type EnvHandler struct{ db *sql.DB }

func NewEnvHandler(db *sql.DB) *EnvHandler { return &EnvHandler{db: db} }

// GET /api/projects/{projectID}/services/{serviceID}/env
func (h *EnvHandler) List(w http.ResponseWriter, r *http.Request) {
	svcID, err := strconv.ParseInt(r.PathValue("serviceID"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid serviceID"))
		return
	}
	rows, err := h.db.QueryContext(r.Context(),
		`SELECT id, service_id, key, value, is_secret, updated_at
		 FROM env_variables WHERE service_id=? ORDER BY key`, svcID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	defer rows.Close()
	vars := make([]model.EnvVar, 0)
	for rows.Next() {
		var ev model.EnvVar
		var secretInt int
		if err := rows.Scan(&ev.ID, &ev.ServiceID, &ev.Key, &ev.Value, &secretInt, &ev.UpdatedAt); err != nil {
			continue
		}
		ev.IsSecret = secretInt == 1
		// Mask secret values in list
		if ev.IsSecret {
			ev.Value = ""
		}
		vars = append(vars, ev)
	}
	writeJSON(w, http.StatusOK, vars)
}

// PUT /api/projects/{projectID}/services/{serviceID}/env
func (h *EnvHandler) Upsert(w http.ResponseWriter, r *http.Request) {
	svcID, err := strconv.ParseInt(r.PathValue("serviceID"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid serviceID"))
		return
	}
	var req model.UpsertEnvVarRequest
	if !v.DecodeAndValidate(w, r, &req) {
		return
	}
	secretInt := 0
	if req.IsSecret {
		secretInt = 1
	}
	_, err = h.db.ExecContext(r.Context(),
		`INSERT INTO env_variables (service_id, key, value, is_secret, updated_at)
		 VALUES (?,?,?,?,datetime('now'))
		 ON CONFLICT(service_id, key) DO UPDATE
		   SET value=excluded.value, is_secret=excluded.is_secret, updated_at=excluded.updated_at`,
		svcID, req.Key, req.Value, secretInt)
	if err != nil {
		slog.Error("upsert env var", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// DELETE /api/projects/{projectID}/services/{serviceID}/env/{key}
func (h *EnvHandler) Delete(w http.ResponseWriter, r *http.Request) {
	svcID, err := strconv.ParseInt(r.PathValue("serviceID"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid serviceID"))
		return
	}
	key := r.PathValue("key")
	if key == "" {
		writeJSON(w, http.StatusBadRequest, errMap("missing key"))
		return
	}
	h.db.ExecContext(r.Context(),
		`DELETE FROM env_variables WHERE service_id=? AND key=?`, svcID, key)
	w.WriteHeader(http.StatusNoContent)
}
