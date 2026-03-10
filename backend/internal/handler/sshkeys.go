package handler

import (
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	appCrypto "github.com/deploy-paas/backend/internal/crypto"
	mw "github.com/deploy-paas/backend/internal/middleware"
	"github.com/deploy-paas/backend/internal/model"
)

// SSHKeyHandler manages per-user SSH key pairs used for git clone operations.
type SSHKeyHandler struct {
	db        *sql.DB
	jwtSecret string // AES passphrase for encrypting stored private keys
}

func NewSSHKeyHandler(db *sql.DB, jwtSecret string) *SSHKeyHandler {
	return &SSHKeyHandler{db: db, jwtSecret: jwtSecret}
}

// POST /api/ssh-keys/generate
// Generates a new ED25519 key pair. The private key is stored encrypted;
// the public key (OpenSSH format) is returned so the user can add it to GitHub.
func (h *SSHKeyHandler) Generate(w http.ResponseWriter, r *http.Request) {
	claims := mw.GetClaims(r.Context())

	var req model.GenerateSSHKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		req.Name = "default"
	}

	// Generate ED25519 key pair
	pubRaw, privRaw, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		slog.Error("ssh: generate key", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("key generation failed"))
		return
	}

	// Encode private key as OpenSSH PEM block
	privBlock, err := ssh.MarshalPrivateKey(privRaw, "")
	if err != nil {
		slog.Error("ssh: marshal private key", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("private key marshal failed"))
		return
	}
	privPEMStr := string(pem.EncodeToMemory(privBlock))

	// Encrypt private key before storing
	encPriv, err := appCrypto.Encrypt(privPEMStr, h.jwtSecret)
	if err != nil {
		slog.Error("ssh: encrypt private key", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("key encryption failed"))
		return
	}

	// Encode public key as OpenSSH authorized_keys line
	sshPub, err := ssh.NewPublicKey(pubRaw)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("public key encoding failed"))
		return
	}
	pubKeyStr := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
	fingerprint := ssh.FingerprintSHA256(sshPub)

	res, err := h.db.ExecContext(r.Context(),
		`INSERT INTO ssh_keys (user_id, name, public_key, encrypted_priv_key, fingerprint)
		 VALUES (?, ?, ?, ?, ?)`,
		claims.UserID, req.Name, pubKeyStr, encPriv, fingerprint,
	)
	if err != nil {
		if isUnique(err) {
			writeJSON(w, http.StatusConflict, errMap("a key with this fingerprint already exists"))
			return
		}
		slog.Error("ssh: insert key", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	id, _ := res.LastInsertId()
	writeJSON(w, http.StatusCreated, model.SSHKey{
		ID:          id,
		UserID:      claims.UserID,
		Name:        req.Name,
		PublicKey:   pubKeyStr,
		Fingerprint: fingerprint,
		HasPrivate:  true,
		CreatedAt:   time.Now(),
	})
}

// POST /api/ssh-keys/import
// Stores a user-provided public key without any private key.
// Use this when the user already has an SSH key pair and just wants to
// register the public key for reference.
func (h *SSHKeyHandler) Import(w http.ResponseWriter, r *http.Request) {
	claims := mw.GetClaims(r.Context())

	var req model.ImportSSHKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.PublicKey) == "" {
		writeJSON(w, http.StatusBadRequest, errMap("public_key is required"))
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		req.Name = "imported"
	}

	// Validate the public key
	sshPub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(req.PublicKey))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid SSH public key: "+err.Error()))
		return
	}
	fingerprint := ssh.FingerprintSHA256(sshPub)
	pubKeyStr := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))

	res, err := h.db.ExecContext(r.Context(),
		`INSERT INTO ssh_keys (user_id, name, public_key, encrypted_priv_key, fingerprint)
		 VALUES (?, ?, ?, '', ?)`,
		claims.UserID, req.Name, pubKeyStr, fingerprint,
	)
	if err != nil {
		if isUnique(err) {
			writeJSON(w, http.StatusConflict, errMap("this key is already registered"))
			return
		}
		slog.Error("ssh: import key", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	id, _ := res.LastInsertId()
	writeJSON(w, http.StatusCreated, model.SSHKey{
		ID:          id,
		UserID:      claims.UserID,
		Name:        req.Name,
		PublicKey:   pubKeyStr,
		Fingerprint: fingerprint,
		HasPrivate:  false,
		CreatedAt:   time.Now(),
	})
}

// GET /api/ssh-keys
// Lists all SSH keys for the authenticated user.
func (h *SSHKeyHandler) List(w http.ResponseWriter, r *http.Request) {
	claims := mw.GetClaims(r.Context())

	rows, err := h.db.QueryContext(r.Context(),
		`SELECT id, user_id, name, public_key, fingerprint,
		        CASE WHEN encrypted_priv_key != '' THEN 1 ELSE 0 END,
		        created_at
		 FROM ssh_keys WHERE user_id = ? ORDER BY id`,
		claims.UserID,
	)
	if err != nil {
		slog.Error("ssh: list keys", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	defer rows.Close()

	keys := make([]model.SSHKey, 0)
	for rows.Next() {
		var k model.SSHKey
		var hasPrivInt int
		if err := rows.Scan(&k.ID, &k.UserID, &k.Name, &k.PublicKey, &k.Fingerprint, &hasPrivInt, &k.CreatedAt); err != nil {
			continue
		}
		k.HasPrivate = hasPrivInt == 1
		keys = append(keys, k)
	}
	writeJSON(w, http.StatusOK, keys)
}

// DELETE /api/ssh-keys/{keyID}
func (h *SSHKeyHandler) Delete(w http.ResponseWriter, r *http.Request) {
	claims := mw.GetClaims(r.Context())
	keyID, err := strconv.ParseInt(r.PathValue("keyID"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid keyID"))
		return
	}
	res, err := h.db.ExecContext(r.Context(),
		`DELETE FROM ssh_keys WHERE id = ? AND user_id = ?`, keyID, claims.UserID,
	)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeJSON(w, http.StatusNotFound, errMap("key not found"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /api/ssh-keys/{keyID}/private
// Returns the decrypted private key PEM for download.
// Only works for server-generated keys (has_private = true).
func (h *SSHKeyHandler) ExportPrivate(w http.ResponseWriter, r *http.Request) {
	claims := mw.GetClaims(r.Context())
	keyID, err := strconv.ParseInt(r.PathValue("keyID"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid keyID"))
		return
	}

	var encPriv string
	err = h.db.QueryRowContext(r.Context(),
		`SELECT encrypted_priv_key FROM ssh_keys WHERE id = ? AND user_id = ?`,
		keyID, claims.UserID,
	).Scan(&encPriv)
	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusNotFound, errMap("key not found"))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	if encPriv == "" {
		writeJSON(w, http.StatusBadRequest, errMap("no private key stored (this is an imported public-key-only entry)"))
		return
	}
	privPEM, err := appCrypto.Decrypt(encPriv, h.jwtSecret)
	if err != nil {
		slog.Error("ssh: decrypt private key", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("decryption failed"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"private_key": privPEM})
}
