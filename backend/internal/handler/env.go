package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/ojhapranjal26/featherdeploy/backend/internal/crypto"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/model"
	v "github.com/ojhapranjal26/featherdeploy/backend/internal/validator"
)

// encryptedPrefix is prepended to values that have been AES-256-GCM encrypted.
// This allows the handler to detect legacy plaintext rows and migrate them
// gracefully without a schema migration.
const encryptedPrefix = "fdenc:"

// EnvHandler handles CRUD operations for per-service environment variables.
// Secret values (is_secret=1) are stored AES-256-GCM encrypted in the
// database; only the encrypted blob (prefixed with encryptedPrefix) is
// persisted. Plaintext is never written for secret variables.
type EnvHandler struct {
	db        *sql.DB
	jwtSecret string // passphrase for AES-256-GCM key derivation
}

func NewEnvHandler(db *sql.DB, jwtSecret string) *EnvHandler {
	return &EnvHandler{db: db, jwtSecret: jwtSecret}
}

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
		// Always mask secret values in list responses — never expose ciphertext or plaintext.
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

	valueToStore := req.Value
	if req.IsSecret {
		// Encrypt secret values before persisting
		enc, err := crypto.Encrypt(req.Value, h.jwtSecret)
		if err != nil {
			slog.Error("encrypt env var", "err", err)
			writeJSON(w, http.StatusInternalServerError, errMap("encryption error"))
			return
		}
		valueToStore = encryptedPrefix + enc
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
		svcID, req.Key, valueToStore, secretInt)
	if err != nil {
		slog.Error("upsert env var", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// POST /api/projects/{projectID}/services/{serviceID}/env/bulk
func (h *EnvHandler) BulkUpsert(w http.ResponseWriter, r *http.Request) {
	svcID, err := strconv.ParseInt(r.PathValue("serviceID"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid serviceID"))
		return
	}
	var items []model.UpsertEnvVarRequest
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&items); err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid JSON: "+err.Error()))
		return
	}
	if len(items) == 0 {
		writeJSON(w, http.StatusBadRequest, errMap("empty list"))
		return
	}
	if len(items) > 200 {
		writeJSON(w, http.StatusBadRequest, errMap("too many variables (max 200)"))
		return
	}

	upserted := 0
	for _, req := range items {
		if req.Key == "" || req.Value == "" {
			continue
		}
		valueToStore := req.Value
		if req.IsSecret {
			enc, encErr := crypto.Encrypt(req.Value, h.jwtSecret)
			if encErr != nil {
				slog.Error("bulk encrypt env var", "key", req.Key, "err", encErr)
				continue
			}
			valueToStore = encryptedPrefix + enc
		}
		secretInt := 0
		if req.IsSecret {
			secretInt = 1
		}
		_, execErr := h.db.ExecContext(r.Context(),
			`INSERT INTO env_variables (service_id, key, value, is_secret, updated_at)
			 VALUES (?,?,?,?,datetime('now'))
			 ON CONFLICT(service_id, key) DO UPDATE
			   SET value=excluded.value, is_secret=excluded.is_secret, updated_at=excluded.updated_at`,
			svcID, req.Key, valueToStore, secretInt)
		if execErr != nil {
			slog.Error("bulk upsert env var", "key", req.Key, "err", execErr)
			continue
		}
		upserted++
	}
	writeJSON(w, http.StatusOK, map[string]int{"upserted": upserted})
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

// GET /api/projects/{projectID}/services/{serviceID}/env/{key}/reveal
// Returns the decrypted plaintext value of a secret env var. Only authenticated
// users with at least editor access to the project may call this.
func (h *EnvHandler) Reveal(w http.ResponseWriter, r *http.Request) {
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

	var value string
	var isSecret int
	err = h.db.QueryRowContext(r.Context(),
		`SELECT value, is_secret FROM env_variables WHERE service_id=? AND key=?`, svcID, key,
	).Scan(&value, &isSecret)
	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusNotFound, errMap("variable not found"))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}

	plaintext := value
	if isSecret == 1 && len(value) > len(encryptedPrefix) && value[:len(encryptedPrefix)] == encryptedPrefix {
		decrypted, decErr := crypto.Decrypt(value[len(encryptedPrefix):], h.jwtSecret)
		if decErr != nil {
			writeJSON(w, http.StatusInternalServerError, errMap("decryption error"))
			return
		}
		plaintext = decrypted
	}

	writeJSON(w, http.StatusOK, map[string]string{"value": plaintext})
}

// GetDecryptedEnv returns all env vars for a service with secret values
// decrypted. This is intended for internal use by the deployment engine when
// injecting environment variables into a container.
func (h *EnvHandler) GetDecryptedEnv(ctx context.Context, serviceID int64) ([]model.EnvVar, error) {
	rows, err := h.db.QueryContext(ctx,
		`SELECT id, service_id, key, value, is_secret, updated_at
		 FROM env_variables WHERE service_id=? ORDER BY key`, serviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var vars []model.EnvVar
	for rows.Next() {
		var ev model.EnvVar
		var secretInt int
		if err := rows.Scan(&ev.ID, &ev.ServiceID, &ev.Key, &ev.Value, &secretInt, &ev.UpdatedAt); err != nil {
			continue
		}
		ev.IsSecret = secretInt == 1
		if ev.IsSecret && len(ev.Value) > len(encryptedPrefix) && ev.Value[:len(encryptedPrefix)] == encryptedPrefix {
			plain, err := crypto.Decrypt(ev.Value[len(encryptedPrefix):], h.jwtSecret)
			if err != nil {
				slog.Error("decrypt env var", "key", ev.Key, "err", err)
				// Return empty string rather than corrupt ciphertext
				ev.Value = ""
			} else {
				ev.Value = plain
			}
		}
		vars = append(vars, ev)
	}
	return vars, nil
}

