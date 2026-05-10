package handler

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/crypto"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/middleware"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/coordination"
	"golang.org/x/crypto/bcrypt"
)

// StorageHandler handles all storage management and object-storage endpoints.
// Data is stored on disk at STORAGE_DATA_DIR/{storageId}/{objectPath}.
// Files are encrypted with AES-256-CTR; the 16-byte nonce is prepended to each file.
type StorageHandler struct {
	db     *sql.DB
	secret string
	etcd   *coordination.Client
}

func NewStorageHandler(db *sql.DB, secret string, etcd *coordination.Client) *StorageHandler {
	return &StorageHandler{db: db, secret: secret, etcd: etcd}
}

// ── Configuration ─────────────────────────────────────────────────────────────

// StorageDataDir returns the root directory for all storage data.
func StorageDataDir() string {
	if d := os.Getenv("STORAGE_DATA_DIR"); d != "" {
		return d
	}
	return "/var/lib/featherdeploy/storage"
}

func storageRoot(storageID int64) string {
	return filepath.Join(StorageDataDir(), strconv.FormatInt(storageID, 10))
}

func multipartRoot(storageID int64) string {
	return filepath.Join(storageRoot(storageID), ".multipart")
}

// ── Encryption ────────────────────────────────────────────────────────────────

func (h *StorageHandler) storagePassphrase(storageID int64) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s-storage-%d", h.secret, storageID)))
	return hex.EncodeToString(sum[:])
}

func objEncKey(masterPass string) []byte {
	sum := sha256.Sum256([]byte(masterPass + ":obj-v1"))
	return sum[:]
}

// encryptStream encrypts src into dst with AES-256-CTR.
// On-disk format: [16-byte nonce][ciphertext].
func encryptStream(dst io.Writer, src io.Reader, key []byte) (int64, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return 0, err
	}
	if _, err := dst.Write(nonce); err != nil {
		return 0, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return 0, err
	}
	stream := cipher.NewCTR(block, nonce)
	w := &cipher.StreamWriter{S: stream, W: dst}
	return io.Copy(w, src)
}

// decryptStream decrypts [nonce][ciphertext] from src into dst.
func decryptStream(dst io.Writer, src io.Reader, key []byte) (int64, error) {
	nonce := make([]byte, 16)
	if _, err := io.ReadFull(src, nonce); err != nil {
		return 0, fmt.Errorf("read nonce: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return 0, err
	}
	stream := cipher.NewCTR(block, nonce)
	r := &cipher.StreamReader{S: stream, R: src}
	return io.Copy(dst, r)
}

// ── Key helpers ───────────────────────────────────────────────────────────────

func generateKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func hashKey(key string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(key), 10)
	return string(h), err
}

// ── Service authentication ────────────────────────────────────────────────────

func (h *StorageHandler) serviceAuth(r *http.Request, storageID int64, needWrite bool) (masterPass string, serviceID int64, httpStatus int, errMsg string) {
	svcKey := r.Header.Get("X-Storage-Key")
	if svcKey == "" {
		return "", 0, http.StatusUnauthorized, "missing X-Storage-Key header"
	}
	if len(svcKey) < 12 {
		return "", 0, http.StatusUnauthorized, "invalid key"
	}
	preview := svcKey[:12]

	var keyHash, encPass string
	var canRead, canWrite int
	var svcID int64
	err := h.db.QueryRow(`
		SELECT a.service_id, a.service_key_hash, a.can_read, a.can_write, s.enc_passphrase
		FROM storage_access a
		JOIN storages s ON s.id = a.storage_id
		WHERE a.storage_id = ? AND a.service_key_preview = ?
	`, storageID, preview).Scan(&svcID, &keyHash, &canRead, &canWrite, &encPass)
	if err == sql.ErrNoRows {
		return "", 0, http.StatusUnauthorized, "invalid key"
	}
	if err != nil {
		return "", 0, http.StatusInternalServerError, err.Error()
	}
	if err := bcrypt.CompareHashAndPassword([]byte(keyHash), []byte(svcKey)); err != nil {
		return "", 0, http.StatusUnauthorized, "invalid key"
	}
	if needWrite && canWrite == 0 {
		return "", 0, http.StatusForbidden, "write access denied"
	}
	if !needWrite && canRead == 0 {
		return "", 0, http.StatusForbidden, "read access denied"
	}
	pass, err := crypto.Decrypt(encPass, h.storagePassphrase(storageID))
	if err != nil {
		return "", 0, http.StatusInternalServerError, "internal error"
	}
	return pass, svcID, 0, ""
}

// ── Path sanitization ─────────────────────────────────────────────────────────

func sanitizePath(raw string) (string, error) {
	clean := filepath.ToSlash(filepath.Clean("/" + strings.TrimPrefix(raw, "/")))
	clean = strings.TrimPrefix(clean, "/")
	if clean == "" || clean == "." {
		return "", fmt.Errorf("path cannot be empty")
	}
	for _, part := range strings.Split(clean, "/") {
		if part == ".." {
			return "", fmt.Errorf("path traversal not allowed")
		}
		if part == ".multipart" {
			return "", fmt.Errorf("reserved path component")
		}
	}
	return clean, nil
}

// ── Bandwidth tracking ────────────────────────────────────────────────────────

func (h *StorageHandler) trackBandwidth(storageID, serviceID, bytesRead, bytesWritten int64) {
	if serviceID == 0 || (bytesRead == 0 && bytesWritten == 0) {
		return
	}
	period := time.Now().UTC().Format("2006-01")
	h.db.Exec(`
		INSERT INTO storage_bandwidth (storage_id, service_id, period, bytes_read, bytes_written)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(storage_id, service_id, period) DO UPDATE SET
		  bytes_read    = bytes_read    + excluded.bytes_read,
		  bytes_written = bytes_written + excluded.bytes_written
	`, storageID, serviceID, period, bytesRead, bytesWritten) //nolint:errcheck
}

// ── Response types ────────────────────────────────────────────────────────────

type storageRow struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	SizeBytes   int64     `json:"size_bytes"`
	CreatedBy   int64     `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	AccessCount int64     `json:"access_count"`
}

type storageAccessRow struct {
	ID                int64     `json:"id"`
	StorageID         int64     `json:"storage_id"`
	ServiceID         int64     `json:"service_id"`
	ServiceName       string    `json:"service_name"`
	CanRead           bool      `json:"can_read"`
	CanWrite          bool      `json:"can_write"`
	ServiceKeyPreview string    `json:"service_key_preview"`
	GrantedAt         time.Time `json:"granted_at"`
}

type objectEntry struct {
	Path      string    `json:"path"`
	Size      int64     `json:"size"`
	UpdatedAt time.Time `json:"updated_at"`
}

type bandwidthEntry struct {
	ServiceID    int64  `json:"service_id"`
	ServiceName  string `json:"service_name"`
	Period       string `json:"period"`
	BytesRead    int64  `json:"bytes_read"`
	BytesWritten int64  `json:"bytes_written"`
}

// ── Management: List / Get / Create / Delete ──────────────────────────────────

// List returns all storages with usage stats.
// GET /api/storages
func (h *StorageHandler) List(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(`
		SELECT s.id, s.name, s.description, s.size_bytes, s.created_by,
		       s.created_at, s.updated_at,
		       (SELECT COUNT(*) FROM storage_access a WHERE a.storage_id = s.id) AS access_count
		FROM storages s ORDER BY s.name
	`)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap(err.Error()))
		return
	}
	defer rows.Close()
	out := make([]storageRow, 0)
	for rows.Next() {
		var s storageRow
		var ca, ua flexTime
		if err := rows.Scan(&s.ID, &s.Name, &s.Description, &s.SizeBytes, &s.CreatedBy,
			&ca, &ua, &s.AccessCount); err != nil {
			continue
		}
		if ca.Valid {
			s.CreatedAt = ca.Time
		}
		if ua.Valid {
			s.UpdatedAt = ua.Time
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
	var s storageRow
	var ca, ua flexTime
	err = h.db.QueryRow(`
		SELECT s.id, s.name, s.description, s.size_bytes, s.created_by,
		       s.created_at, s.updated_at,
		       (SELECT COUNT(*) FROM storage_access a WHERE a.storage_id = s.id) AS access_count
		FROM storages s WHERE s.id = ?
	`, id).Scan(&s.ID, &s.Name, &s.Description, &s.SizeBytes, &s.CreatedBy,
		&ca, &ua, &s.AccessCount)
	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusNotFound, errMap("storage not found"))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap(err.Error()))
		return
	}
	if ca.Valid {
		s.CreatedAt = ca.Time
	}
	if ua.Valid {
		s.UpdatedAt = ua.Time
	}
	writeJSON(w, http.StatusOK, s)
}

// Create creates a new storage bucket.
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

	res, err := h.db.Exec(
		`INSERT INTO storages (name, description, api_key_hash, api_key_preview, enc_passphrase, created_by)
		 VALUES (?, ?, '', '', '', ?)`,
		body.Name, body.Description, middleware.GetClaims(r.Context()).UserID,
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

	rawPass, err := generateKey()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("passphrase generation failed"))
		return
	}
	encPass, err := crypto.Encrypt(rawPass, h.storagePassphrase(storageID))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("passphrase encrypt failed"))
		return
	}

	dataPath := storageRoot(storageID)
	if err := os.MkdirAll(dataPath, 0750); err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("could not create storage directory"))
		return
	}

	if _, err := h.db.Exec(
		`UPDATE storages SET enc_passphrase = ?, data_path = ? WHERE id = ?`,
		encPass, dataPath, storageID,
	); err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap(err.Error()))
		return
	}

	if h.etcd != nil {
		nodeIP := "127.0.0.1"
		if dIP := os.Getenv("SERVER_IP"); dIP != "" {
			nodeIP = dIP
		}
		go h.etcd.RegisterStorage(r.Context(), 0, body.Name, nodeIP)
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"id":           storageID,
		"name":         body.Name,
		"description":  body.Description,
		"size_bytes":   0,
		"access_count": 0,
	})
}

// Delete removes a storage and its disk data.
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
	go os.RemoveAll(storageRoot(id)) //nolint:errcheck
	w.WriteHeader(http.StatusNoContent)
}

// ── Management: Access ────────────────────────────────────────────────────────

// ListAccess lists services with access to a storage.
// GET /api/storages/{storageId}/access
func (h *StorageHandler) ListAccess(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "storageId"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid storageId"))
		return
	}
	rows, err := h.db.Query(`
		SELECT a.id, a.storage_id, a.service_id, svc.name,
		       a.can_read, a.can_write, a.service_key_preview, a.granted_at
		FROM storage_access a
		JOIN services svc ON svc.id = a.service_id
		WHERE a.storage_id = ? ORDER BY svc.name
	`, id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap(err.Error()))
		return
	}
	defer rows.Close()
	out := make([]storageAccessRow, 0)
	for rows.Next() {
		var a storageAccessRow
		var ga flexTime
		var canRead, canWrite int
		if err := rows.Scan(&a.ID, &a.StorageID, &a.ServiceID, &a.ServiceName,
			&canRead, &canWrite, &a.ServiceKeyPreview, &ga); err != nil {
			continue
		}
		a.CanRead = canRead == 1
		a.CanWrite = canWrite == 1
		if ga.Valid {
			a.GrantedAt = ga.Time
		}
		out = append(out, a)
	}
	writeJSON(w, http.StatusOK, out)
}

// GrantAccess grants a service access and returns its one-time API key.
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

	svcKey, err := generateKey()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("key generation failed"))
		return
	}
	keyHash, err := hashKey(svcKey)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("key hash failed"))
		return
	}
	keyPreview := svcKey[:12]
	encSvcKey, err := crypto.Encrypt(svcKey, h.secret)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("key encrypt failed"))
		return
	}

	_, err = h.db.Exec(`
		INSERT INTO storage_access
		  (storage_id, service_id, can_read, can_write,
		   service_key_hash, service_key_preview, enc_service_key)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(storage_id, service_id) DO UPDATE SET
		  can_read            = excluded.can_read,
		  can_write           = excluded.can_write,
		  service_key_hash    = excluded.service_key_hash,
		  service_key_preview = excluded.service_key_preview,
		  enc_service_key     = excluded.enc_service_key
	`, storageID, body.ServiceID, canRead, canWrite, keyHash, keyPreview, encSvcKey)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap(err.Error()))
		return
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"service_key":         svcKey,
		"service_key_preview": keyPreview,
		"can_read":            canRead == 1,
		"can_write":           canWrite == 1,
	})
}

// UpdateAccess changes read/write permissions for a service.
// PATCH /api/storages/{storageId}/access/{serviceId}
func (h *StorageHandler) UpdateAccess(w http.ResponseWriter, r *http.Request) {
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
	var body struct {
		CanRead  *bool `json:"can_read"`
		CanWrite *bool `json:"can_write"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid JSON"))
		return
	}
	if body.CanRead != nil {
		v := 0
		if *body.CanRead {
			v = 1
		}
		h.db.Exec(`UPDATE storage_access SET can_read = ? WHERE storage_id = ? AND service_id = ?`, //nolint:errcheck
			v, storageID, serviceID)
	}
	if body.CanWrite != nil {
		v := 0
		if *body.CanWrite {
			v = 1
		}
		h.db.Exec(`UPDATE storage_access SET can_write = ? WHERE storage_id = ? AND service_id = ?`, //nolint:errcheck
			v, storageID, serviceID)
	}
	w.WriteHeader(http.StatusNoContent)
}

// RotateServiceKey rotates the per-service API key.
// POST /api/storages/{storageId}/access/{serviceId}/rotate-key
func (h *StorageHandler) RotateServiceKey(w http.ResponseWriter, r *http.Request) {
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

	svcKey, err := generateKey()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("key generation failed"))
		return
	}
	keyHash, err := hashKey(svcKey)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("key hash failed"))
		return
	}
	keyPreview := svcKey[:12]
	encSvcKey, err := crypto.Encrypt(svcKey, h.secret)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("key encrypt failed"))
		return
	}

	res, err := h.db.Exec(`
		UPDATE storage_access
		SET service_key_hash = ?, service_key_preview = ?, enc_service_key = ?
		WHERE storage_id = ? AND service_id = ?
	`, keyHash, keyPreview, encSvcKey, storageID, serviceID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap(err.Error()))
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeJSON(w, http.StatusNotFound, errMap("access entry not found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"service_key":         svcKey,
		"service_key_preview": keyPreview,
	})
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
	h.db.Exec(`DELETE FROM storage_access WHERE storage_id = ? AND service_id = ?`, //nolint:errcheck
		storageID, serviceID)
	w.WriteHeader(http.StatusNoContent)
}

// ── Management: Browse & Stats ────────────────────────────────────────────────

// Browse lists objects with optional prefix (admin view).
// GET /api/storages/{storageId}/browse?prefix=folder/
func (h *StorageHandler) Browse(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "storageId"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid storageId"))
		return
	}
	prefix := normalizePrefix(r.URL.Query().Get("prefix"))
	entries, err := listObjects(id, prefix)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

// AdminDeleteObject deletes a specific object (admin action).
// DELETE /api/storages/{storageId}/objects?path=folder/file.txt
func (h *StorageHandler) AdminDeleteObject(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "storageId"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid storageId"))
		return
	}
	rawPath := r.URL.Query().Get("path")
	if rawPath == "" {
		writeJSON(w, http.StatusBadRequest, errMap("path query param required"))
		return
	}
	objPath, err := sanitizePath(rawPath)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap(err.Error()))
		return
	}
	filePath := filepath.Join(storageRoot(id), filepath.FromSlash(objPath))
	info, statErr := os.Stat(filePath)
	if os.IsNotExist(statErr) {
		writeJSON(w, http.StatusNotFound, errMap("object not found"))
		return
	}
	if statErr != nil {
		writeJSON(w, http.StatusInternalServerError, errMap(statErr.Error()))
		return
	}
	encSize := info.Size()
	if err := os.Remove(filePath); err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap(err.Error()))
		return
	}
	go cleanEmptyDirs(storageRoot(id), filepath.Dir(filePath))
	plainSize := encSize - 16
	if plainSize < 0 {
		plainSize = 0
	}
	h.db.Exec(`UPDATE storages SET size_bytes = MAX(0, size_bytes - ?) WHERE id = ?`, //nolint:errcheck
		plainSize, id)
	w.WriteHeader(http.StatusNoContent)
}

// Stats returns usage statistics for a storage bucket.
// GET /api/storages/{storageId}/stats
func (h *StorageHandler) Stats(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "storageId"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid storageId"))
		return
	}
	var sizeBytes int64
	var name string
	if err := h.db.QueryRow(`SELECT name, size_bytes FROM storages WHERE id = ?`, id).
		Scan(&name, &sizeBytes); err == sql.ErrNoRows {
		writeJSON(w, http.StatusNotFound, errMap("storage not found"))
		return
	}
	objCount := countObjects(id)
	rows, err := h.db.Query(`
		SELECT b.service_id, svc.name, b.period, b.bytes_read, b.bytes_written
		FROM storage_bandwidth b
		JOIN services svc ON svc.id = b.service_id
		WHERE b.storage_id = ?
		ORDER BY b.period DESC, svc.name
	`, id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap(err.Error()))
		return
	}
	defer rows.Close()
	bw := make([]bandwidthEntry, 0)
	for rows.Next() {
		var e bandwidthEntry
		if err := rows.Scan(&e.ServiceID, &e.ServiceName, &e.Period, &e.BytesRead, &e.BytesWritten); err != nil {
			continue
		}
		bw = append(bw, e)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"id":           id,
		"name":         name,
		"size_bytes":   sizeBytes,
		"object_count": objCount,
		"bandwidth":    bw,
	})
}

// ── Object API (authenticated via X-Storage-Key) ──────────────────────────────

// ObjectList lists objects (service view).
// GET /api/storage/{storageId}/list?prefix=folder/
func (h *StorageHandler) ObjectList(w http.ResponseWriter, r *http.Request) {
	storageID, err := strconv.ParseInt(chi.URLParam(r, "storageId"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid storageId"))
		return
	}
	if _, _, status, msg := h.serviceAuth(r, storageID, false); status != 0 {
		writeJSON(w, status, errMap(msg))
		return
	}
	prefix := normalizePrefix(r.URL.Query().Get("prefix"))
	entries, err := listObjects(storageID, prefix)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

// ObjectGet streams a decrypted object.
// GET /api/storage/{storageId}/objects/*
func (h *StorageHandler) ObjectGet(w http.ResponseWriter, r *http.Request) {
	storageID, err := strconv.ParseInt(chi.URLParam(r, "storageId"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid storageId"))
		return
	}
	masterPass, serviceID, status, msg := h.serviceAuth(r, storageID, false)
	if status != 0 {
		writeJSON(w, status, errMap(msg))
		return
	}
	rawPath := chi.URLParam(r, "*")
	objPath, err := sanitizePath(rawPath)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap(err.Error()))
		return
	}
	filePath := filepath.Join(storageRoot(storageID), filepath.FromSlash(objPath))
	f, err := os.Open(filePath)
	if os.IsNotExist(err) {
		writeJSON(w, http.StatusNotFound, errMap("object not found"))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap(err.Error()))
		return
	}
	defer f.Close()
	info, _ := f.Stat()
	plainSize := info.Size() - 16
	if plainSize < 0 {
		plainSize = 0
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filepath.Base(objPath)))
	w.Header().Set("Content-Length", strconv.FormatInt(plainSize, 10))
	w.WriteHeader(http.StatusOK)
	n, _ := decryptStream(w, f, objEncKey(masterPass))
	go h.trackBandwidth(storageID, serviceID, n, 0)
}

// ObjectPut uploads and encrypts an object.
// PUT /api/storage/{storageId}/objects/*
func (h *StorageHandler) ObjectPut(w http.ResponseWriter, r *http.Request) {
	storageID, err := strconv.ParseInt(chi.URLParam(r, "storageId"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid storageId"))
		return
	}
	masterPass, serviceID, status, msg := h.serviceAuth(r, storageID, true)
	if status != 0 {
		writeJSON(w, status, errMap(msg))
		return
	}
	rawPath := chi.URLParam(r, "*")
	objPath, err := sanitizePath(rawPath)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap(err.Error()))
		return
	}
	finalPath := filepath.Join(storageRoot(storageID), filepath.FromSlash(objPath))
	if err := os.MkdirAll(filepath.Dir(finalPath), 0750); err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("could not create path"))
		return
	}

	tmpFile, err := os.CreateTemp(filepath.Dir(finalPath), ".upload-*")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("could not create temp file"))
		return
	}
	tmpPath := tmpFile.Name()
	defer func() {
		tmpFile.Close()
		os.Remove(tmpPath)
	}()

	oldSize := int64(0)
	if info, err := os.Stat(finalPath); err == nil {
		if s := info.Size() - 16; s > 0 {
			oldSize = s
		}
	}

	plainWritten, err := encryptStream(tmpFile, r.Body, objEncKey(masterPass))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("encrypt failed"))
		return
	}
	tmpFile.Close()
	if err := os.Rename(tmpPath, finalPath); err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("could not store object"))
		return
	}
	delta := plainWritten - oldSize
	h.db.Exec(`UPDATE storages SET size_bytes = MAX(0, size_bytes + ?), updated_at = datetime('now') WHERE id = ?`, //nolint:errcheck
		delta, storageID)
	go h.trackBandwidth(storageID, serviceID, 0, plainWritten)
	w.WriteHeader(http.StatusNoContent)
}

// ObjectDelete removes an object.
// DELETE /api/storage/{storageId}/objects/*
func (h *StorageHandler) ObjectDelete(w http.ResponseWriter, r *http.Request) {
	storageID, err := strconv.ParseInt(chi.URLParam(r, "storageId"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid storageId"))
		return
	}
	if _, _, status, msg := h.serviceAuth(r, storageID, true); status != 0 {
		writeJSON(w, status, errMap(msg))
		return
	}
	rawPath := chi.URLParam(r, "*")
	objPath, err := sanitizePath(rawPath)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap(err.Error()))
		return
	}
	filePath := filepath.Join(storageRoot(storageID), filepath.FromSlash(objPath))
	info, statErr := os.Stat(filePath)
	if os.IsNotExist(statErr) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	encSize := info.Size()
	if err := os.Remove(filePath); err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap(err.Error()))
		return
	}
	go cleanEmptyDirs(storageRoot(storageID), filepath.Dir(filePath))
	plainSize := encSize - 16
	if plainSize < 0 {
		plainSize = 0
	}
	h.db.Exec(`UPDATE storages SET size_bytes = MAX(0, size_bytes - ?) WHERE id = ?`, //nolint:errcheck
		plainSize, storageID)
	w.WriteHeader(http.StatusNoContent)
}

// ── Multipart Upload ──────────────────────────────────────────────────────────

type multipartMeta struct {
	StorageID int64  `json:"storage_id"`
	ObjPath   string `json:"obj_path"`
	CreatedAt string `json:"created_at"`
}

// MultipartInit initiates a multipart upload.
// POST /api/storage/{storageId}/multipart/init?path=folder/file.txt
func (h *StorageHandler) MultipartInit(w http.ResponseWriter, r *http.Request) {
	storageID, err := strconv.ParseInt(chi.URLParam(r, "storageId"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid storageId"))
		return
	}
	if _, _, status, msg := h.serviceAuth(r, storageID, true); status != 0 {
		writeJSON(w, status, errMap(msg))
		return
	}
	rawPath := r.URL.Query().Get("path")
	objPath, err := sanitizePath(rawPath)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap(err.Error()))
		return
	}
	uploadID, err := generateKey()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("upload ID generation failed"))
		return
	}
	uploadDir := filepath.Join(multipartRoot(storageID), uploadID)
	if err := os.MkdirAll(uploadDir, 0750); err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("could not create upload dir"))
		return
	}
	meta := multipartMeta{StorageID: storageID, ObjPath: objPath, CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	metaJSON, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(uploadDir, "meta.json"), metaJSON, 0640); err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("could not write upload meta"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"upload_id": uploadID})
}

// MultipartUploadPart uploads one part.
// PUT /api/storage/{storageId}/multipart/{uploadId}/part/{partNumber}
func (h *StorageHandler) MultipartUploadPart(w http.ResponseWriter, r *http.Request) {
	storageID, err := strconv.ParseInt(chi.URLParam(r, "storageId"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid storageId"))
		return
	}
	if _, _, status, msg := h.serviceAuth(r, storageID, true); status != 0 {
		writeJSON(w, status, errMap(msg))
		return
	}
	uploadID := chi.URLParam(r, "uploadId")
	partNum := chi.URLParam(r, "partNumber")
	pn, err := strconv.Atoi(partNum)
	if err != nil || pn < 1 || pn > 10000 {
		writeJSON(w, http.StatusBadRequest, errMap("invalid partNumber (1-10000)"))
		return
	}
	uploadDir := filepath.Join(multipartRoot(storageID), uploadID)
	if _, err := os.Stat(uploadDir); os.IsNotExist(err) {
		writeJSON(w, http.StatusNotFound, errMap("upload not found"))
		return
	}
	partPath := filepath.Join(uploadDir, fmt.Sprintf("part-%05d", pn))
	f, err := os.Create(partPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("could not create part file"))
		return
	}
	defer f.Close()
	if _, err := io.Copy(f, r.Body); err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("failed to write part"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// MultipartComplete assembles parts, encrypts, and finalizes the object.
// POST /api/storage/{storageId}/multipart/{uploadId}/complete
func (h *StorageHandler) MultipartComplete(w http.ResponseWriter, r *http.Request) {
	storageID, err := strconv.ParseInt(chi.URLParam(r, "storageId"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid storageId"))
		return
	}
	masterPass, serviceID, status, msg := h.serviceAuth(r, storageID, true)
	if status != 0 {
		writeJSON(w, status, errMap(msg))
		return
	}
	uploadID := chi.URLParam(r, "uploadId")
	uploadDir := filepath.Join(multipartRoot(storageID), uploadID)
	metaData, err := os.ReadFile(filepath.Join(uploadDir, "meta.json"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, errMap("upload not found"))
		return
	}
	var meta multipartMeta
	if err := json.Unmarshal(metaData, &meta); err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("corrupt upload meta"))
		return
	}

	entries, err := os.ReadDir(uploadDir)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap(err.Error()))
		return
	}
	var parts []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "part-") {
			parts = append(parts, filepath.Join(uploadDir, e.Name()))
		}
	}
	sort.Strings(parts)
	if len(parts) == 0 {
		writeJSON(w, http.StatusBadRequest, errMap("no parts uploaded"))
		return
	}

	finalPath := filepath.Join(storageRoot(storageID), filepath.FromSlash(meta.ObjPath))
	if err := os.MkdirAll(filepath.Dir(finalPath), 0750); err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("could not create path"))
		return
	}
	tmpFile, err := os.CreateTemp(filepath.Dir(finalPath), ".mp-complete-*")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("could not create temp file"))
		return
	}
	tmpPath := tmpFile.Name()

	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		for _, partPath := range parts {
			pf, err := os.Open(partPath)
			if err != nil {
				pw.CloseWithError(err)
				return
			}
			if _, err := io.Copy(pw, pf); err != nil {
				pf.Close()
				pw.CloseWithError(err)
				return
			}
			pf.Close()
		}
	}()

	oldSize := int64(0)
	if info, err := os.Stat(finalPath); err == nil {
		if s := info.Size() - 16; s > 0 {
			oldSize = s
		}
	}
	plainWritten, err := encryptStream(tmpFile, pr, objEncKey(masterPass))
	tmpFile.Close()
	if err != nil {
		os.Remove(tmpPath)
		writeJSON(w, http.StatusInternalServerError, errMap("encrypt failed"))
		return
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		os.Remove(tmpPath)
		writeJSON(w, http.StatusInternalServerError, errMap("could not finalize object"))
		return
	}
	os.RemoveAll(uploadDir) //nolint:errcheck
	delta := plainWritten - oldSize
	h.db.Exec(`UPDATE storages SET size_bytes = MAX(0, size_bytes + ?), updated_at = datetime('now') WHERE id = ?`, //nolint:errcheck
		delta, storageID)
	go h.trackBandwidth(storageID, serviceID, 0, plainWritten)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"path":       meta.ObjPath,
		"size_bytes": plainWritten,
	})
}

// MultipartAbort cancels an in-progress multipart upload.
// DELETE /api/storage/{storageId}/multipart/{uploadId}
func (h *StorageHandler) MultipartAbort(w http.ResponseWriter, r *http.Request) {
	storageID, err := strconv.ParseInt(chi.URLParam(r, "storageId"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid storageId"))
		return
	}
	if _, _, status, msg := h.serviceAuth(r, storageID, true); status != 0 {
		writeJSON(w, status, errMap(msg))
		return
	}
	uploadID := chi.URLParam(r, "uploadId")
	os.RemoveAll(filepath.Join(multipartRoot(storageID), uploadID)) //nolint:errcheck
	w.WriteHeader(http.StatusNoContent)
}

// ── Disk helpers ──────────────────────────────────────────────────────────────

func listObjects(storageID int64, prefix string) ([]objectEntry, error) {
	root := storageRoot(storageID)
	if err := os.MkdirAll(root, 0750); err != nil {
		return nil, err
	}
	out := make([]objectEntry, 0)
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkerr error) error {
		if walkerr != nil || info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, ".multipart") {
			return filepath.SkipDir
		}
		if prefix != "" && !strings.HasPrefix(rel, prefix) {
			return nil
		}
		plainSize := info.Size() - 16
		if plainSize < 0 {
			plainSize = 0
		}
		out = append(out, objectEntry{Path: rel, Size: plainSize, UpdatedAt: info.ModTime().UTC()})
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return out, nil
}

func countObjects(storageID int64) int {
	root := storageRoot(storageID)
	count := 0
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error { //nolint:errcheck
		if err != nil || info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if !strings.HasPrefix(filepath.ToSlash(rel), ".multipart") {
			count++
		}
		return nil
	})
	return count
}

func cleanEmptyDirs(root, dir string) {
	for {
		if dir == root || !strings.HasPrefix(dir, root) {
			break
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			break
		}
		if err := os.Remove(dir); err != nil {
			break
		}
		dir = filepath.Dir(dir)
	}
}

func normalizePrefix(prefix string) string {
	if prefix == "" {
		return ""
	}
	p := filepath.ToSlash(filepath.Clean(prefix))
	if p == "." {
		return ""
	}
	p = strings.TrimPrefix(p, "/")
	if p != "" && !strings.HasSuffix(p, "/") {
		p += "/"
	}
	return p
}

// ── Deploy runner helper ──────────────────────────────────────────────────────

// GetStorageEnvArgs returns podman -e flags for all storages a service can access.
// Injected env vars per storage:
//
//	STORAGE_{NAME}_KEY       = per-service plaintext API key
//	STORAGE_{NAME}_BUCKET    = storage name
//	STORAGE_{NAME}_ENDPOINT  = internal URL to reach the storage API
func GetStorageEnvArgs(db *sql.DB, serverSecret string, svcID int64) []string {
	rows, err := db.Query(`
		SELECT st.id, st.name, sa.enc_service_key
		FROM storage_access sa
		JOIN storages st ON st.id = sa.storage_id
		WHERE sa.service_id = ? AND sa.enc_service_key != ''
	`, svcID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var args []string
	for rows.Next() {
		var stID int64
		var stName, encSvcKey string
		if err := rows.Scan(&stID, &stName, &encSvcKey); err != nil {
			continue
		}
		plainKey, err := crypto.Decrypt(encSvcKey, serverSecret)
		if err != nil {
			continue
		}
		upper := storageEnvName(stName)
		args = append(args,
			"-e", fmt.Sprintf("STORAGE_%s_KEY=%s", upper, plainKey),
			"-e", fmt.Sprintf("STORAGE_%s_BUCKET=%s", upper, stName),
			"-e", fmt.Sprintf("STORAGE_%s_ENDPOINT=http://10.0.2.2:8080/api/storage/%d", upper, stID),
		)
	}
	return args
}

func storageEnvName(s string) string {
	s = strings.ToUpper(s)
	var b strings.Builder
	for _, c := range s {
		if (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			b.WriteRune(c)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

func (h *StorageHandler) replicateObject(storageID int64, objPath, localFilePath, method, svcKey string) {
	// Find all other connected nodes
	rows, err := h.db.Query("SELECT ip, port FROM nodes WHERE status='connected'")
	if err != nil {
		return
	}
	defer rows.Close()

	var targets []string
	for rows.Next() {
		var ip string
		var port int
		if err := rows.Scan(&ip, &port); err == nil {
			targets = append(targets, fmt.Sprintf("https://%s:%d", ip, port))
		}
	}

	// Also check brain (main)
	var brainAddr string
	_ = h.db.QueryRow("SELECT brain_addr FROM cluster_state LIMIT 1").Scan(&brainAddr)
	if brainAddr != "" {
		targets = append(targets, brainAddr)
	}

	for _, t := range targets {
		url := fmt.Sprintf("%s/api/storage/%d/objects/%s", t, storageID, objPath)

		var body io.Reader
		if method == "PUT" {
			f, err := os.Open(localFilePath)
			if err != nil {
				continue
			}
			defer f.Close()
			body = f
		}

		req, err := http.NewRequest(method, url, body)
		if err != nil {
			continue
		}
		req.Header.Set("X-Storage-Key", svcKey)
		req.Header.Set("X-Storage-Replication", "true")

		// Use mTLS if possible
		client := http.DefaultClient

		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
		}
	}
}
