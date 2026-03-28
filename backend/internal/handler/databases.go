package handler

import (
	cryptoRand "crypto/rand"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"

	appCrypto "github.com/ojhapranjal26/featherdeploy/backend/internal/crypto"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/deploy"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/model"
	v "github.com/ojhapranjal26/featherdeploy/backend/internal/validator"
)

// DatabaseHandler handles CRUD + lifecycle operations for managed database containers.
type DatabaseHandler struct {
	db        *sql.DB
	jwtSecret string
}

var (
	dbIdentifierRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]{0,63}$`)
	imageTagRe     = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`)
)

func NewDatabaseHandler(db *sql.DB, jwtSecret string) *DatabaseHandler {
	return &DatabaseHandler{db: db, jwtSecret: jwtSecret}
}

// GET /api/projects/{projectID}/databases
func (h *DatabaseHandler) List(w http.ResponseWriter, r *http.Request) {
	projectID, err := strconv.ParseInt(r.PathValue("projectID"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid projectID"))
		return
	}
	rows, err := h.db.QueryContext(r.Context(),
		`SELECT id, project_id, name, db_type, db_version, db_name, db_user,
		        COALESCE(host_port, 0), status, container_id, network_public, created_at, updated_at
		 FROM databases WHERE project_id=? ORDER BY created_at DESC`, projectID)
	if err != nil {
		slog.Error("list databases", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	defer rows.Close()
	dbs := make([]model.Database, 0)
	for rows.Next() {
		var d model.Database
		var npInt int
		if err := rows.Scan(
			&d.ID, &d.ProjectID, &d.Name, &d.DBType, &d.DBVersion,
			&d.DBName, &d.DBUser, &d.HostPort, &d.Status, &d.ContainerID,
			&npInt, &d.CreatedAt, &d.UpdatedAt,
		); err == nil {
			d.NetworkPublic = npInt == 1
			d.EnvVarName = deploy.DBEnvKey(d.Name) + "_URL"
			dbs = append(dbs, d)
		}
	}
	writeJSON(w, http.StatusOK, dbs)
}

// POST /api/projects/{projectID}/databases
func (h *DatabaseHandler) Create(w http.ResponseWriter, r *http.Request) {
	projectID, err := strconv.ParseInt(r.PathValue("projectID"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid projectID"))
		return
	}
	var req model.CreateDatabaseRequest
	if !v.DecodeAndValidate(w, r, &req) {
		return
	}
	if !isSupportedDatabaseType(req.DBType) {
		writeJSON(w, http.StatusBadRequest, errMap("unsupported database type"))
		return
	}
	if req.DBVersion != "" && !imageTagRe.MatchString(req.DBVersion) {
		writeJSON(w, http.StatusBadRequest, errMap("invalid database version"))
		return
	}

	// Defaults
	if req.DBVersion == "" {
		if req.DBType == model.DatabaseTypeSQLite {
			req.DBVersion = "3"
		} else {
			req.DBVersion = "latest"
		}
	}
	if req.DBName == "" {
		req.DBName = defaultDatabaseIdentifier(req.Name)
	}
	if req.DBUser == "" && req.DBType != model.DatabaseTypeSQLite {
		req.DBUser = defaultDatabaseIdentifier(req.Name)
	}
	if !dbIdentifierRe.MatchString(req.DBName) {
		writeJSON(w, http.StatusBadRequest, errMap("invalid database name identifier"))
		return
	}
	if req.DBType != model.DatabaseTypeSQLite && !dbIdentifierRe.MatchString(req.DBUser) {
		writeJSON(w, http.StatusBadRequest, errMap("invalid database user identifier"))
		return
	}
	if req.DBType == model.DatabaseTypeSQLite && req.NetworkPublic {
		writeJSON(w, http.StatusBadRequest, errMap("sqlite databases cannot be exposed publicly"))
		return
	}
	// Auto-generate a cryptographically random password when none is provided.
	if req.DBType != model.DatabaseTypeSQLite && req.DBPassword == "" {
		req.DBPassword = genSecurePassword(24)
	}

	encPass := ""
	if req.DBType != model.DatabaseTypeSQLite {
		// Encrypt the password before persisting.
		encPass, err = appCrypto.Encrypt(req.DBPassword, h.jwtSecret)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errMap("failed to encrypt password"))
			return
		}
		encPass = "fdenc:" + encPass
	}

	npInt := 0
	if req.NetworkPublic {
		npInt = 1
	}

	res, err := h.db.ExecContext(r.Context(),
		`INSERT INTO databases
		  (project_id, name, db_type, db_version, db_name, db_user, db_password, network_public)
		 VALUES (?,?,?,?,?,?,?,?)`,
		projectID, req.Name, req.DBType, req.DBVersion,
		req.DBName, req.DBUser, encPass, npInt)
	if err != nil {
		if isUnique(err) {
			writeJSON(w, http.StatusConflict, errMap("database name already exists in project"))
			return
		}
		slog.Error("create database", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	dbID, _ := res.LastInsertId()

	// Auto-start the database container asynchronously.
	go func() {
		if err := deploy.StartDatabase(h.db, h.jwtSecret, dbID); err != nil {
			slog.Error("auto-start database", "db_id", dbID, "err", err)
		}
	}()

	h.getByID(w, r, projectID, dbID)
}

// GET /api/projects/{projectID}/databases/{databaseID}
func (h *DatabaseHandler) Get(w http.ResponseWriter, r *http.Request) {
	projectID, err := strconv.ParseInt(r.PathValue("projectID"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid projectID"))
		return
	}
	dbID, err := strconv.ParseInt(r.PathValue("databaseID"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid databaseID"))
		return
	}
	h.getByID(w, r, projectID, dbID)
}

// GET /api/projects/{projectID}/databases/{databaseID}/backup
func (h *DatabaseHandler) Backup(w http.ResponseWriter, r *http.Request) {
	projectID, err := strconv.ParseInt(r.PathValue("projectID"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid projectID"))
		return
	}
	dbID, err := strconv.ParseInt(r.PathValue("databaseID"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid databaseID"))
		return
	}
	if !h.databaseExists(r, projectID, dbID) {
		writeJSON(w, http.StatusNotFound, errMap("database not found"))
		return
	}

	backupPath, downloadName, err := deploy.CreateDatabaseBackup(h.db, h.jwtSecret, dbID)
	if err != nil {
		slog.Error("backup database", "db_id", dbID, "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("failed to create database backup"))
		return
	}
	defer os.Remove(backupPath) //nolint

	f, err := os.Open(backupPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("failed to read database backup"))
		return
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("failed to stat database backup"))
		return
	}

	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", downloadName))
	http.ServeContent(w, r, downloadName, stat.ModTime(), f)
}

// DELETE /api/projects/{projectID}/databases/{databaseID}
func (h *DatabaseHandler) Delete(w http.ResponseWriter, r *http.Request) {
	projectID, err := strconv.ParseInt(r.PathValue("projectID"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid projectID"))
		return
	}
	dbID, err := strconv.ParseInt(r.PathValue("databaseID"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid databaseID"))
		return
	}

	if !h.databaseExists(r, projectID, dbID) {
		writeJSON(w, http.StatusNotFound, errMap("database not found"))
		return
	}

	if err := deploy.DeleteDatabase(h.db, dbID, true); err != nil {
		slog.Error("delete database assets", "db_id", dbID, "err", err)
		writeJSON(w, http.StatusConflict, errMap("failed to delete database data; stop dependent services and try again"))
		return
	}

	if _, err := h.db.ExecContext(r.Context(),
		`DELETE FROM databases WHERE id=?`, dbID); err != nil {
		slog.Error("delete database", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	deploy.CleanupProjectRuntimeIfUnused(h.db, projectID)
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/projects/{projectID}/databases/{databaseID}/start
func (h *DatabaseHandler) Start(w http.ResponseWriter, r *http.Request) {
	dbID, err := strconv.ParseInt(r.PathValue("databaseID"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid databaseID"))
		return
	}
	go func() {
		if err := deploy.StartDatabase(h.db, h.jwtSecret, dbID); err != nil {
			slog.Error("start database", "db_id", dbID, "err", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "starting"})
}

// POST /api/projects/{projectID}/databases/{databaseID}/stop
func (h *DatabaseHandler) Stop(w http.ResponseWriter, r *http.Request) {
	dbID, err := strconv.ParseInt(r.PathValue("databaseID"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid databaseID"))
		return
	}
	go deploy.StopDatabase(h.db, dbID) //nolint
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "stopping"})
}

// ── Internal helpers ──────────────────────────────────────────────────────────

func (h *DatabaseHandler) getByID(w http.ResponseWriter, r *http.Request, projectID, dbID int64) {
	var d model.Database
	var npInt int
	var encPass string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id, project_id, name, db_type, db_version, db_name, db_user, db_password,
		        COALESCE(host_port, 0), status, container_id, network_public, created_at, updated_at
		 FROM databases WHERE id=? AND project_id=?`, dbID, projectID,
	).Scan(
		&d.ID, &d.ProjectID, &d.Name, &d.DBType, &d.DBVersion, &d.DBName, &d.DBUser, &encPass,
		&d.HostPort, &d.Status, &d.ContainerID, &npInt, &d.CreatedAt, &d.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusNotFound, errMap("database not found"))
		return
	}
	if err != nil {
		slog.Error("get database", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	d.NetworkPublic = npInt == 1

	// Decrypt password and build connection URLs for the response.
	clearPass := encPass
	if strings.HasPrefix(encPass, "fdenc:") {
		if p, decErr := appCrypto.Decrypt(encPass[len("fdenc:"):], h.jwtSecret); decErr == nil {
			clearPass = p
		}
	}
	alias := deploy.DBNetworkAlias(d.Name)
	d.ConnectionURL = deploy.DBConnectionURL(d.DBType, d.DBName, d.DBUser, clearPass, alias)
	if d.NetworkPublic && d.HostPort > 0 {
		d.PublicConnectionURL = deploy.DBPublicConnectionURL(
			d.DBType, d.DBName, d.DBUser, clearPass, d.HostPort)
	}
	d.EnvVarName = deploy.DBEnvKey(d.Name) + "_URL"

	writeJSON(w, http.StatusOK, d)
}

func (h *DatabaseHandler) databaseExists(r *http.Request, projectID, dbID int64) bool {
	var count int
	h.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM databases WHERE id=? AND project_id=?`, dbID, projectID,
	).Scan(&count) //nolint
	return count > 0
}

func defaultDatabaseIdentifier(name string) string {
	return strings.ReplaceAll(name, "-", "_")
}

func isSupportedDatabaseType(dbType string) bool {
	switch dbType {
	case model.DatabaseTypePostgres, model.DatabaseTypeMySQL, model.DatabaseTypeSQLite:
		return true
	default:
		return false
	}
}

// genSecurePassword generates a random alphanumeric password of length n
// using crypto/rand for security.
func genSecurePassword(n int) string {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	raw := make([]byte, n)
	if _, err := cryptoRand.Read(raw); err != nil {
		// crypto/rand failure is extremely unlikely; fall back to a fixed value
		// so the caller always gets a non-empty password.
		return fmt.Sprintf("fd%020d", n)
	}
	result := make([]byte, n)
	for i, b := range raw {
		result[i] = chars[int(b)%len(chars)]
	}
	return string(result)
}
