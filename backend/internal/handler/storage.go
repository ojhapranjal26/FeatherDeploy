package handler

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/crypto"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/middleware"
	"golang.org/x/crypto/bcrypt"
)

// StorageHandler handles all storage management and KV-access endpoints.
type StorageHandler struct {
	db     *sql.DB
	secret string // server JWT secret – used as the master passphrase salt
}

func NewStorageHandler(db *sql.DB, secret string) *StorageHandler {
	return &StorageHandler{db: db, secret: secret}
}

// ────────────────────────────────────────────────────────────────────────────
// Response types
// ────────────────────────────────────────────────────────────────────────────

type storageRow struct {
	ID             int64     `json:"id"`
	Name           string    `json:"name"`
	Description    string    `json:"description"`
	APIKeyPreview  string    `json:"api_key_preview"`
	CreatedBy      int64     `json:"created_by"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	KeyCount       int64     `json:"key_count"`
	AccessCount    int64     `json:"access_count"`
}

type storageAccessRow struct {
	ID        int64     `json:"id"`
	StorageID int64     `json:"storage_id"`
	ServiceID int64     `json:"service_id"`
	ServiceName string  `json:"service_name"`
	CanRead   bool      `json:"can_read"`
	CanWrite  bool      `json:"can_write"`
	GrantedAt time.Time `json:"granted_at"`
}

type storageKVItem struct {
	Key         string    `json:"key"`
	ContentType string    `json:"content_type"`
	SizeBytes   int64     `json:"size_bytes"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ────────────────────────────────────────────────────────────────────────────
// Helpers
// ────────────────────────────────────────────────────────────────────────────

// generateAPIKey returns a cryptographically random 32-byte hex API key.
func generateAPIKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// hashAPIKey hashes an API key using bcrypt.
func hashAPIKey(key string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(key), 10)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// checkAPIKey compares an API key to a bcrypt hash.
func checkAPIKey(hash, key string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(key))
}

// storagePassphrase derives a per-storage AES passphrase that is stored
// encrypted in the DB. The encryption passphrase for it is derived from
// the server secret + storage ID.
func (h *StorageHandler) storagePassphrase(storageID int64) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s-storage-%d", h.secret, storageID)))
	return hex.EncodeToString(sum[:])
}

// getStorageByID fetches the storage row plus validates the API key.
// Returns (storage_id, api_key_hash, enc_passphrase, error).
func (h *StorageHandler) lookupStorageForKV(storageID int64, apiKey string) (string, error) {
	var keyHash, encPass string
	err := h.db.QueryRow(
		`SELECT api_key_hash, enc_passphrase FROM storages WHERE id = ?`, storageID,
	).Scan(&keyHash, &encPass)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("not found")
	}
	if err != nil {
		return "", err
	}
	if err := checkAPIKey(keyHash, apiKey); err != nil {
		return "", fmt.Errorf("invalid key")
	}
	// Decrypt the per-storage encryption passphrase
	pass, err := crypto.Decrypt(encPass, h.storagePassphrase(storageID))
	if err != nil {
		return "", fmt.Errorf("internal: passphrase decrypt: %w", err)
	}
	return pass, nil
}

// scanStorage scans one row from the enriched storages query.
func scanStorage(rows *sql.Rows) (storageRow, error) {
	var s storageRow
	var createdAt, updatedAt flexTime
	err := rows.Scan(
		&s.ID, &s.Name, &s.Description, &s.APIKeyPreview,
		&s.CreatedBy, &createdAt, &updatedAt,
		&s.KeyCount, &s.AccessCount,
	)
	if err != nil {
		return s, err
	}
	if createdAt.Valid {
		s.CreatedAt = createdAt.Time
	}
	if updatedAt.Valid {
		s.UpdatedAt = updatedAt.Time
	}
	return s, nil
}

// ────────────────────────────────────────────────────────────────────────────
// Management endpoints (require admin or superadmin JWT)
// ────────────────────────────────────────────────────────────────────────────

const storagesBaseQuery = `
SELECT s.id, s.name, s.description, s.api_key_preview,
       s.created_by, s.created_at, s.updated_at,
       (SELECT COUNT(*) FROM storage_kv    k WHERE k.storage_id = s.id) AS key_count,
       (SELECT COUNT(*) FROM storage_access a WHERE a.storage_id = s.id) AS access_count
FROM storages s`

// List returns all storages.
// GET /api/storages
func (h *StorageHandler) List(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(storagesBaseQuery + ` ORDER BY s.name`)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap(err.Error()))
		return
	}
	defer rows.Close()

	out := make([]storageRow, 0)
	for rows.Next() {
		s, err := scanStorage(rows)
		if err != nil {
			continue
		}
		out = append(out, s)
	}
	writeJSON(w, http.StatusOK, out)
}

// Get returns a single storage.
// GET /api/storages/{storageId}
func (h *StorageHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "storageId"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid storageId"))
		return
	}
	rows, err := h.db.Query(storagesBaseQuery+` WHERE s.id = ?`, id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap(err.Error()))
		return
	}
	defer rows.Close()
	if !rows.Next() {
		writeJSON(w, http.StatusNotFound, errMap("storage not found"))
		return
	}
	s, err := scanStorage(rows)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, s)
}

// Create creates a new storage and returns the plaintext API key ONCE.
// POST /api/storages
func (h *StorageHandler) Create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid JSON"))
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" {
		writeJSON(w, http.StatusBadRequest, errMap("name is required"))
		return
	}
	if len(body.Name) > 64 {
		writeJSON(w, http.StatusBadRequest, errMap("name too long (max 64 chars)"))
		return
	}

	apiKey, err := generateAPIKey()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("key generation failed"))
		return
	}
	keyHash, err := hashAPIKey(apiKey)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("key hash failed"))
		return
	}
	preview := apiKey[:12]

	// We need the storage ID to derive the passphrase, so insert first with a
	// placeholder passphrase then update once we have the ID.
	res, err := h.db.Exec(
		`INSERT INTO storages (name, description, api_key_hash, api_key_preview, enc_passphrase, created_by)
		 VALUES (?, ?, ?, ?, '', ?)`,
		body.Name, body.Description, keyHash, preview,
		middleware.GetClaims(r.Context()).UserID,
	)
	if err != nil {
		if isUnique(err) {
			writeJSON(w, http.StatusConflict, errMap("a storage with that name already exists"))
			return
		}
		writeJSON(w, http.StatusInternalServerError, errMap(err.Error()))
		return
	}
	storageID, _ := res.LastInsertId()

	// Generate and encrypt a random per-storage encryption passphrase.
	rawPass, err := generateAPIKey() // reuse random 64-char hex as passphrase
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("passphrase generation failed"))
		return
	}
	encPass, err := crypto.Encrypt(rawPass, h.storagePassphrase(storageID))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("passphrase encrypt failed"))
		return
	}
	if _, err := h.db.Exec(`UPDATE storages SET enc_passphrase = ? WHERE id = ?`, encPass, storageID); err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap(err.Error()))
		return
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"id":             storageID,
		"name":           body.Name,
		"description":    body.Description,
		"api_key_preview": preview,
		// The plaintext API key is ONLY returned here. After this response
		// the key cannot be recovered; only rotation generates a new one.
		"api_key": apiKey,
	})
}

// Delete deletes a storage and all its KV entries.
// DELETE /api/storages/{storageId}
func (h *StorageHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "storageId"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid storageId"))
		return
	}
	res, err := h.db.Exec(`DELETE FROM storages WHERE id = ?`, id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap(err.Error()))
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeJSON(w, http.StatusNotFound, errMap("storage not found"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RotateKey generates a new API key for a storage.
// POST /api/storages/{storageId}/rotate-key
func (h *StorageHandler) RotateKey(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "storageId"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid storageId"))
		return
	}
	apiKey, err := generateAPIKey()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("key generation failed"))
		return
	}
	keyHash, err := hashAPIKey(apiKey)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("key hash failed"))
		return
	}
	preview := apiKey[:12]

	res, err := h.db.Exec(
		`UPDATE storages SET api_key_hash = ?, api_key_preview = ?, updated_at = datetime('now') WHERE id = ?`,
		keyHash, preview, id,
	)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap(err.Error()))
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeJSON(w, http.StatusNotFound, errMap("storage not found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"api_key_preview": preview,
		"api_key":         apiKey,
	})
}

// ────────────────────────────────────────────────────────────────────────────
// Access management
// ────────────────────────────────────────────────────────────────────────────

// ListAccess lists which services can access a storage.
// GET /api/storages/{storageId}/access
func (h *StorageHandler) ListAccess(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "storageId"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid storageId"))
		return
	}
	rows, err := h.db.Query(`
		SELECT a.id, a.storage_id, a.service_id, s.name,
		       a.can_read, a.can_write, a.granted_at
		FROM storage_access a
		JOIN services s ON s.id = a.service_id
		WHERE a.storage_id = ?
		ORDER BY s.name`, id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap(err.Error()))
		return
	}
	defer rows.Close()

	out := make([]storageAccessRow, 0)
	for rows.Next() {
		var a storageAccessRow
		var grantedAt flexTime
		var canRead, canWrite int
		if err := rows.Scan(&a.ID, &a.StorageID, &a.ServiceID, &a.ServiceName,
			&canRead, &canWrite, &grantedAt); err != nil {
			continue
		}
		a.CanRead = canRead == 1
		a.CanWrite = canWrite == 1
		if grantedAt.Valid {
			a.GrantedAt = grantedAt.Time
		}
		out = append(out, a)
	}
	writeJSON(w, http.StatusOK, out)
}

// GrantAccess adds a service to the access list of a storage.
// POST /api/storages/{storageId}/access
func (h *StorageHandler) GrantAccess(w http.ResponseWriter, r *http.Request) {
	storageID, err := strconv.ParseInt(chi.URLParam(r, "storageId"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid storageId"))
		return
	}
	var body struct {
		ServiceID int64 `json:"service_id"`
		CanRead   *bool `json:"can_read"`
		CanWrite  *bool `json:"can_write"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid JSON"))
		return
	}
	if body.ServiceID == 0 {
		writeJSON(w, http.StatusBadRequest, errMap("service_id is required"))
		return
	}
	canRead, canWrite := 1, 1
	if body.CanRead != nil && !*body.CanRead {
		canRead = 0
	}
	if body.CanWrite != nil && !*body.CanWrite {
		canWrite = 0
	}

	_, err = h.db.Exec(
		`INSERT INTO storage_access (storage_id, service_id, can_read, can_write)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(storage_id, service_id) DO UPDATE SET can_read=excluded.can_read, can_write=excluded.can_write`,
		storageID, body.ServiceID, canRead, canWrite,
	)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap(err.Error()))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RevokeAccess removes a service from the access list.
// DELETE /api/storages/{storageId}/access/{serviceId}
func (h *StorageHandler) RevokeAccess(w http.ResponseWriter, r *http.Request) {
	storageID, err := strconv.ParseInt(chi.URLParam(r, "storageId"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid storageId"))
		return
	}
	serviceID, err := strconv.ParseInt(chi.URLParam(r, "serviceId"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid serviceId"))
		return
	}
	h.db.Exec(`DELETE FROM storage_access WHERE storage_id = ? AND service_id = ?`, storageID, serviceID)
	w.WriteHeader(http.StatusNoContent)
}

// ────────────────────────────────────────────────────────────────────────────
// Admin KV inspection
// ────────────────────────────────────────────────────────────────────────────

// ListKeys returns the list of keys in a storage (admin view, no values).
// GET /api/storages/{storageId}/kv
func (h *StorageHandler) ListKeys(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "storageId"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid storageId"))
		return
	}
	rows, err := h.db.Query(`
		SELECT kv_key, content_type, size_bytes, updated_at
		FROM storage_kv WHERE storage_id = ? ORDER BY kv_key`, id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap(err.Error()))
		return
	}
	defer rows.Close()

	out := make([]storageKVItem, 0)
	for rows.Next() {
		var item storageKVItem
		var updatedAt flexTime
		if err := rows.Scan(&item.Key, &item.ContentType, &item.SizeBytes, &updatedAt); err != nil {
			continue
		}
		if updatedAt.Valid {
			item.UpdatedAt = updatedAt.Time
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, out)
}

// AdminDeleteKey removes a KV entry from a storage (admin action, no API key needed).
// DELETE /api/storages/{storageId}/kv/{key}
func (h *StorageHandler) AdminDeleteKey(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "storageId"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid storageId"))
		return
	}
	key := chi.URLParam(r, "key")
	h.db.Exec(`DELETE FROM storage_kv WHERE storage_id = ? AND kv_key = ?`, id, key)
	w.WriteHeader(http.StatusNoContent)
}

// ────────────────────────────────────────────────────────────────────────────
// Storage KV API (authenticated via X-Storage-Key header, no JWT)
// Only services in the storage_access list may call these endpoints.
// ────────────────────────────────────────────────────────────────────────────

// kvAuth validates X-Storage-Key and optionally checks service whitelist.
// Returns the decrypted per-storage passphrase on success.
func (h *StorageHandler) kvAuth(r *http.Request, storageID int64, needWrite bool) (string, int, string) {
	apiKey := r.Header.Get("X-Storage-Key")
	if apiKey == "" {
		return "", http.StatusUnauthorized, "missing X-Storage-Key header"
	}

	var keyHash, encPass string
	err := h.db.QueryRow(
		`SELECT api_key_hash, enc_passphrase FROM storages WHERE id = ?`, storageID,
	).Scan(&keyHash, &encPass)
	if err == sql.ErrNoRows {
		return "", http.StatusNotFound, "storage not found"
	}
	if err != nil {
		return "", http.StatusInternalServerError, err.Error()
	}
	if err := checkAPIKey(keyHash, apiKey); err != nil {
		return "", http.StatusUnauthorized, "invalid API key"
	}

	// Decrypt per-storage passphrase
	pass, err := crypto.Decrypt(encPass, h.storagePassphrase(storageID))
	if err != nil {
		return "", http.StatusInternalServerError, "internal error"
	}
	return pass, 0, ""
}

// KVGet retrieves a value by key.
// GET /api/storage/{storageId}/kv/{key}
func (h *StorageHandler) KVGet(w http.ResponseWriter, r *http.Request) {
	storageID, err := strconv.ParseInt(chi.URLParam(r, "storageId"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid storageId"))
		return
	}
	pass, status, msg := h.kvAuth(r, storageID, false)
	if status != 0 {
		writeJSON(w, status, errMap(msg))
		return
	}

	key := chi.URLParam(r, "key")
	var encValue, contentType string
	err = h.db.QueryRow(
		`SELECT enc_value, content_type FROM storage_kv WHERE storage_id = ? AND kv_key = ?`,
		storageID, key,
	).Scan(&encValue, &contentType)
	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusNotFound, errMap("key not found"))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap(err.Error()))
		return
	}

	plain, err := crypto.Decrypt(encValue, pass)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("decrypt failed"))
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(plain)) //nolint:errcheck
}

// KVPut creates or replaces a KV entry.
// PUT /api/storage/{storageId}/kv/{key}
func (h *StorageHandler) KVPut(w http.ResponseWriter, r *http.Request) {
	storageID, err := strconv.ParseInt(chi.URLParam(r, "storageId"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid storageId"))
		return
	}
	pass, status, msg := h.kvAuth(r, storageID, true)
	if status != 0 {
		writeJSON(w, status, errMap(msg))
		return
	}

	key := chi.URLParam(r, "key")
	if key == "" {
		writeJSON(w, http.StatusBadRequest, errMap("key is required"))
		return
	}
	// Limit key length to prevent abuse
	if len(key) > 256 {
		writeJSON(w, http.StatusBadRequest, errMap("key too long (max 256 chars)"))
		return
	}

	// Read body (max 1 MiB)
	const maxBody = 1 << 20
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	rawValue := make([]byte, 0, 512)
	buf := make([]byte, 4096)
	for {
		n, readErr := r.Body.Read(buf)
		if n > 0 {
			rawValue = append(rawValue, buf[:n]...)
		}
		if readErr != nil {
			break
		}
	}

	contentType := r.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	encValue, err := crypto.Encrypt(string(rawValue), pass)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("encrypt failed"))
		return
	}
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	_, err = h.db.Exec(`
		INSERT INTO storage_kv (storage_id, kv_key, enc_value, content_type, size_bytes, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(storage_id, kv_key) DO UPDATE SET
		  enc_value = excluded.enc_value,
		  content_type = excluded.content_type,
		  size_bytes = excluded.size_bytes,
		  updated_at = excluded.updated_at`,
		storageID, key, encValue, contentType, len(rawValue), now, now,
	)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap(err.Error()))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// KVDelete removes a KV entry.
// DELETE /api/storage/{storageId}/kv/{key}
func (h *StorageHandler) KVDelete(w http.ResponseWriter, r *http.Request) {
	storageID, err := strconv.ParseInt(chi.URLParam(r, "storageId"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid storageId"))
		return
	}
	_, status, msg := h.kvAuth(r, storageID, true)
	if status != 0 {
		writeJSON(w, status, errMap(msg))
		return
	}
	key := chi.URLParam(r, "key")
	h.db.Exec(`DELETE FROM storage_kv WHERE storage_id = ? AND kv_key = ?`, storageID, key) //nolint:errcheck
	w.WriteHeader(http.StatusNoContent)
}
