package handler

import (
	cryptoRand "crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	appCrypto "github.com/ojhapranjal26/featherdeploy/backend/internal/crypto"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/deploy"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/model"
	v "github.com/ojhapranjal26/featherdeploy/backend/internal/validator"
)

// DatabaseHandler handles CRUD + lifecycle operations for managed database containers.
type DatabaseHandler struct {
	db          *sql.DB
	jwtSecret   string
	serverHost  string // public hostname/IP used in public connection URLs (e.g. panel.intelectio.art)
	dbSubdomain string // subdomain for public DB URLs (e.g. db.intelectio.art)
}

var (
	dbIdentifierRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]{0,63}$`)
	imageTagRe     = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`)
)

func NewDatabaseHandler(db *sql.DB, jwtSecret, serverHost string) *DatabaseHandler {
	dbSub := dbSubdomainFromHost(serverHost)
	return &DatabaseHandler{db: db, jwtSecret: jwtSecret, serverHost: serverHost, dbSubdomain: dbSub}
}

// dbSubdomainFromHost derives a database subdomain from the panel's public host.
// panel.intelectio.art → db.intelectio.art
// localhost             → localhost  (kept as-is for dev)
// 1.2.3.4              → 1.2.3.4    (IPs kept as-is)
func dbSubdomainFromHost(host string) string {
	// Strip port if present (shouldn't be, but be safe)
	if idx := strings.LastIndex(host, ":"); idx >= 0 {
		host = host[:idx]
	}
	// Is it an IPv4 address?
	if isIPv4(host) {
		return host
	}
	parts := strings.Split(host, ".")
	if len(parts) > 2 {
		// e.g. panel.intelectio.art → db.intelectio.art
		return "db." + strings.Join(parts[1:], ".")
	}
	// bare domain or localhost
	if host == "localhost" || host == "" {
		return host
	}
	return "db." + host
}

func isIPv4(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		for _, c := range p {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}

// GET /api/projects/{projectID}/databases
func (h *DatabaseHandler) List(w http.ResponseWriter, r *http.Request) {
	projectID, err := strconv.ParseInt(r.PathValue("projectID"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid projectID"))
		return
	}
	rows, err := h.db.QueryContext(r.Context(),
		`SELECT id, project_id, name, db_type, db_version, db_name, db_user, db_password,
		        COALESCE(host_port, 0), COALESCE(cluster_port, 0), status, container_id, network_public, last_error, created_at, updated_at
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
		var encPass string
		if err := rows.Scan(
			&d.ID, &d.ProjectID, &d.Name, &d.DBType, &d.DBVersion,
			&d.DBName, &d.DBUser, &encPass, &d.HostPort, &d.ClusterPort, &d.Status, &d.ContainerID,
			&npInt, &d.LastError, &d.CreatedAt, &d.UpdatedAt,
		); err == nil {
			d.NetworkPublic = npInt == 1
			d.EnvVarName = deploy.DBEnvKey(d.Name) + "_URL"
			// Decrypt password and build connection URLs so the frontend can
			// display copy buttons on the project page without an extra GET.
			clearPass := encPass
			if strings.HasPrefix(encPass, "fdenc:") {
				if p, decErr := appCrypto.Decrypt(encPass[len("fdenc:"):], h.jwtSecret); decErr == nil {
					clearPass = p
				}
			}
			alias := deploy.DBNetworkAlias(d.Name)
			d.ConnectionURL = deploy.DBConnectionURL(d.DBType, d.DBName, d.DBUser, clearPass, alias)
			if d.NetworkPublic && d.HostPort > 0 {
				publicHost := h.dbSubdomain
				publicPort := d.HostPort
				if d.ClusterPort > 0 {
					publicPort = d.ClusterPort
				}
				d.PublicConnectionURL = deploy.DBPublicConnectionURL(d.DBType, d.DBName, d.DBUser, clearPass, publicHost, publicPort)
			}
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
	// MySQL does not allow "root" as the MYSQL_USER — the docker-entrypoint
	// rejects it and the container fails to initialize.
	if req.DBType == model.DatabaseTypeMySQL && strings.EqualFold(req.DBUser, "root") {
		writeJSON(w, http.StatusBadRequest, errMap("mysql user cannot be 'root'; choose a different username"))
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

	res, err := h.db.ExecContext(r.Context(),
		`INSERT INTO databases
		  (project_id, name, db_type, db_version, db_name, db_user, db_password, network_public)
		 VALUES (?,?,?,?,?,?,?,0)`,
		projectID, req.Name, req.DBType, req.DBVersion,
		req.DBName, req.DBUser, encPass)
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

// POST /api/projects/{projectID}/databases/{databaseID}/restore
// Accepts a multipart/form-data upload with a single "file" field containing
// a .tar volume backup (as produced by the Backup endpoint). The database is
// stopped, the volume is replaced with the backup content, then the database
// is restarted.
func (h *DatabaseHandler) Restore(w http.ResponseWriter, r *http.Request) {
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

	// Limit the multipart parse to 32 MB in-memory; the rest spills to disk
	// automatically, so large backups are handled without OOM risk.
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("failed to parse multipart form"))
		return
	}
	uploadFile, _, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("missing 'file' field in form"))
		return
	}
	defer uploadFile.Close()

	// Stream the upload to a temp file so we can pass a path to podman.
	tmp, err := os.CreateTemp("", fmt.Sprintf("fd-db-%d-restore-*.tar", dbID))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("failed to create temp file"))
		return
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, uploadFile); err != nil {
		tmp.Close()
		writeJSON(w, http.StatusInternalServerError, errMap("failed to write backup data"))
		return
	}
	tmp.Close()

	if err := deploy.RestoreDatabaseBackup(h.db, h.jwtSecret, dbID, tmp.Name()); err != nil {
		slog.Error("restore database", "db_id", dbID, "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("failed to restore database: "+err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "restored"})
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

// POST /api/projects/{projectID}/databases/{databaseID}/restart
// Recreates the container from scratch using the stored configuration.
// Safe to call on a running, stopped, or crash-looping database.
func (h *DatabaseHandler) Restart(w http.ResponseWriter, r *http.Request) {
	dbID, err := strconv.ParseInt(r.PathValue("databaseID"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid databaseID"))
		return
	}
	go func() {
		if err := deploy.StartDatabase(h.db, h.jwtSecret, dbID); err != nil {
			slog.Error("restart database", "db_id", dbID, "err", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "restarting"})
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

// PUT /api/projects/{projectID}/databases/{databaseID}
func (h *DatabaseHandler) Update(w http.ResponseWriter, r *http.Request) {
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

	var req model.UpdateDatabaseRequest
	if !v.DecodeAndValidate(w, r, &req) {
		return
	}
	if req.DBVersion != "" && !imageTagRe.MatchString(req.DBVersion) {
		writeJSON(w, http.StatusBadRequest, errMap("invalid database version"))
		return
	}

	var dbType, currentVersion string
	h.db.QueryRowContext(r.Context(), `SELECT db_type, db_version FROM databases WHERE id=?`, dbID). //nolint
		Scan(&dbType, &currentVersion)
	version := req.DBVersion
	if version == "" {
		version = currentVersion
	}

	if err := deploy.UpdateDatabase(h.db, dbID, version); err != nil {
		slog.Error("update database", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	h.getByID(w, r, projectID, dbID)
}

// POST /api/projects/{projectID}/databases/{databaseID}/password
// Changes the database password live inside the running container, then
// updates the stored connection URL. Dependent services must be restarted
// to pick up the new credentials.
func (h *DatabaseHandler) ChangePassword(w http.ResponseWriter, r *http.Request) {
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
	var req model.ChangePasswordRequest
	if !v.DecodeAndValidate(w, r, &req) {
		return
	}
	if err := deploy.ChangeDBPassword(h.db, h.jwtSecret, dbID, req.NewPassword); err != nil {
		slog.Error("change database password", "db_id", dbID, "err", err)
		writeJSON(w, http.StatusBadRequest, errMap(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                      true,
		"services_restart_needed": true,
		"message":                 "Password changed. Restart dependent services to apply the new connection URL.",
	})
}

// POST /api/projects/{projectID}/databases/{databaseID}/public
// Toggles whether the database is reachable from the internet on its host
// port.  When enabled an iptables ACCEPT rule is added specifically for
// the database's host port (before the broad DROP rules) so external TCP
// clients (PgAdmin, TablePlus, etc.) can connect with the usual credentials.
// When disabled the rule is removed.
func (h *DatabaseHandler) TogglePublic(w http.ResponseWriter, r *http.Request) {
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

	var req struct {
		Public bool `json:"public"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid request body"))
		return
	}

	// Fetch current state — also fetch `name` so we can resolve the fdnet
	// cluster port (which gives a more reliable 0.0.0.0 socket than rootlessport).
	var dbType, dbName, dbUser, encPass, dbContainerName string
	var hostPort, clusterPort int
	var npInt int
	err = h.db.QueryRowContext(r.Context(),
		`SELECT name, db_type, db_name, db_user, db_password,
		        COALESCE(host_port, 0), COALESCE(cluster_port, 0), network_public
		 FROM databases WHERE id=? AND project_id=?`, dbID, projectID,
	).Scan(&dbContainerName, &dbType, &dbName, &dbUser, &encPass,
		&hostPort, &clusterPort, &npInt)
	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusNotFound, errMap("database not found"))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	if hostPort == 0 {
		if dbType == "sqlite" {
			writeJSON(w, http.StatusBadRequest, errMap("SQLite databases are file-based and cannot be exposed publicly"))
		} else {
			writeJSON(w, http.StatusBadRequest, errMap("database has no host port assigned; start it first"))
		}
		return
	}

	ipt, lookErr := exec.LookPath("iptables")
	if lookErr != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("iptables not available on this server"))
		return
	}

	// Run iptables via sudo so the featherdeploy service user (non-root) has
	// the required privileges.  sudo must be available and have a NOPASSWD rule
	// for the iptables binary (written by the installer).
	sudoPath, _ := exec.LookPath("sudo")
	iptCmd := func(args ...string) *exec.Cmd {
		if sudoPath != "" {
			return exec.Command(sudoPath, append([]string{ipt}, args...)...)
		}
		return exec.Command(ipt, args...)
	}

	// If the fdnet cluster port isn't stored in the DB yet, try to resolve it
	// live from the running daemon.
	if clusterPort == 0 && deploy.NetDaemon != nil {
		alias := deploy.DBNetworkAlias(dbContainerName)
		if cp, ok := deploy.NetDaemon.Resolve(projectID, alias); ok {
			clusterPort = cp
			// Persist it so future calls don't need the live lookup.
			h.db.ExecContext(r.Context(), //nolint
				`UPDATE databases SET cluster_port=? WHERE id=?`, clusterPort, dbID)
		}
	}

	// Open/close iptables + UFW rules for BOTH the rootlessport host port AND
	// the fdnet cluster port. The fdnet cluster port has a guaranteed
	// 0.0.0.0 binding (net.Listen in Go) and is unaffected by rootlessport
	// quirks, making it the primary external-access port. The host port is
	// opened as a fallback for direct access.
	openPorts := []int{hostPort}
	if clusterPort > 0 && clusterPort != hostPort {
		openPorts = append(openPorts, clusterPort)
	}
	for _, p := range openPorts {
		portStr := strconv.Itoa(p)
		acceptSpec := []string{"-p", "tcp", "--dport", portStr,
			"-m", "comment", "--comment", fmt.Sprintf("featherdeploy db-%d public", dbID),
			"-j", "ACCEPT"}
		// Explicit DROP rule used when public access is disabled. This is
		// necessary because many VPS kernels have a default INPUT policy of
		// ACCEPT, meaning removing the ACCEPT rule alone would not block
		// traffic — we need a specific DROP to override the default.
		dropSpec := []string{"-p", "tcp", "--dport", portStr,
			"-m", "comment", "--comment", fmt.Sprintf("featherdeploy db-%d block", dbID),
			"-j", "DROP"}
		if req.Public {
			// Remove any stale DROP rule for this port before adding ACCEPT.
			iptCmd(append([]string{"-D", "INPUT"}, dropSpec...)...).Run() //nolint
			// Add ACCEPT if not already present.
			checkArgs := append([]string{"-C", "INPUT"}, acceptSpec...)
			if iptCmd(checkArgs...).Run() != nil {
				insertArgs := append([]string{"-I", "INPUT", "1"}, acceptSpec...)
				if out, runErr := iptCmd(insertArgs...).CombinedOutput(); runErr != nil {
					slog.Error("iptables: add public DB rule", "port", p, "err", runErr, "out", string(out))
					// Non-fatal — continue trying other ports.
				}
			}
		} else {
			// Remove ACCEPT rule.
			iptCmd(append([]string{"-D", "INPUT"}, acceptSpec...)...).Run() //nolint
			// Add DROP so the port is explicitly blocked on default-ACCEPT chains.
			checkDropArgs := append([]string{"-C", "INPUT"}, dropSpec...)
			if iptCmd(checkDropArgs...).Run() != nil {
				appendArgs := append([]string{"-A", "INPUT"}, dropSpec...)
				if out, runErr := iptCmd(appendArgs...).CombinedOutput(); runErr != nil {
					slog.Error("iptables: add block DB rule", "port", p, "err", runErr, "out", string(out))
				}
			}
		}
		// Also manage UFW so rules survive UFW reloads/reboots.
		applyUFWRule(req.Public, portStr)
	}
	if req.Public {
		// Persist all open-port rules to disk so they survive a reboot even
		// if iptables-persistent is installed without UFW.
		persistIPTablesRules()
	}

	npVal := 0
	if req.Public {
		npVal = 1
	}
	if _, err := h.db.ExecContext(r.Context(),
		`UPDATE databases SET network_public=?, updated_at=datetime('now') WHERE id=?`, npVal, dbID,
	); err != nil {
		slog.Error("toggle public db", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}

	// Persist iptables rules.
	persistIPTablesRules()

	// Build the public connection URL for the response.
	// Prefer the fdnet cluster port (reliable 0.0.0.0 socket) over the
	// rootlessport host port.  Use the db. subdomain so DBeaver users can
	// create a DNS A/CNAME record for it that is NOT behind a Cloudflare/CDN
	// proxy (which would strip non-HTTP ports).
	clearPass := encPass
	if strings.HasPrefix(encPass, "fdenc:") {
		if p, decErr := appCrypto.Decrypt(encPass[len("fdenc:"):], h.jwtSecret); decErr == nil {
			clearPass = p
		}
	}
	publicURL := ""
	if req.Public {
		publicHost := h.dbSubdomain
		publicPort := hostPort
		if clusterPort > 0 {
			publicPort = clusterPort
		}
		publicURL = deploy.DBPublicConnectionURL(dbType, dbName, dbUser, clearPass, publicHost, publicPort)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                    true,
		"network_public":        req.Public,
		"public_connection_url": publicURL,
	})
}

// persistIPTablesRules saves the current iptables ruleset to disk.
func persistIPTablesRules() {
	sudo, _ := exec.LookPath("sudo")
	saveCmd := func() *exec.Cmd {
		if sudo != "" {
			return exec.Command(sudo, "iptables-save")
		}
		return exec.Command("iptables-save")
	}
	// Debian/Ubuntu
	if err := os.MkdirAll("/etc/iptables", 0755); err == nil {
		if out, err := saveCmd().Output(); err == nil {
			os.WriteFile("/etc/iptables/rules.v4", out, 0640) //nolint
			return
		}
	}
	// RHEL/Fedora
	if err := os.MkdirAll("/etc/sysconfig", 0755); err == nil {
		if out, err := saveCmd().Output(); err == nil {
			os.WriteFile("/etc/sysconfig/iptables", out, 0640) //nolint
		}
	}
}

// applyUFWRule adds or removes a UFW allow rule for the given TCP port.
// UFW is active on many Ubuntu/Debian servers and periodically rewrites the
// iptables INPUT chain; without a matching UFW rule the raw iptables ACCEPT
// we insert is wiped on every 'ufw reload' or reboot.  UFW rules persist
// across reboots on their own, so this complements (not replaces) the
// iptables INSERT we do for the current session.
func applyUFWRule(allow bool, port string) {
	ufw, err := exec.LookPath("ufw")
	if err != nil {
		return // UFW not installed
	}
	sudo, _ := exec.LookPath("sudo")
	// 'ufw status' requires root — run via sudo so the featherdeploy service
	// user (non-root) gets the real status instead of always "inactive".
	var statusOut []byte
	if sudo != "" {
		statusOut, _ = exec.Command(sudo, ufw, "status").Output()
	} else {
		statusOut, _ = exec.Command(ufw, "status").Output()
	}
	if !strings.Contains(string(statusOut), "Status: active") {
		return // UFW not active on this host
	}
	run := func(args ...string) {
		if sudo != "" {
			exec.Command(sudo, append([]string{ufw}, args...)...).Run() //nolint
		} else {
			exec.Command(ufw, args...).Run() //nolint
		}
	}
	if allow {
		run("allow", port+"/tcp")
		slog.Info("ufw: allowed public DB port", "port", port)
	} else {
		// 'ufw deny' blocks even if the rule is not present, whereas
		// 'ufw delete allow' would error if the allow rule doesn't exist.
		// We delete the allow rule first, then deny for safety.
		run("delete", "allow", port+"/tcp") // ignore error if not present
		run("deny", port+"/tcp")
		slog.Info("ufw: blocked public DB port", "port", port)
	}
}

// GET /api/projects/{projectID}/databases/{databaseID}/logs
func (h *DatabaseHandler) GetLogs(w http.ResponseWriter, r *http.Request) {
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

	var lastError, startLog string
	h.db.QueryRowContext(r.Context(), `SELECT last_error, COALESCE(start_log,'') FROM databases WHERE id=?`, dbID).Scan(&lastError, &startLog) //nolint

	containerName := fmt.Sprintf("fd-db-%d", dbID)
	logs, logsErr := deploy.GetDatabaseLogs(dbID)
	if logsErr != nil {
		// Container not found — return the stored startup error instead
		writeJSON(w, http.StatusOK, map[string]any{
			"container": containerName,
			"logs":      lastError,
			"start_log": startLog,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"container": containerName,
		"logs":      logs,
		"start_log": startLog,
	})
}

// GET /api/projects/{projectID}/databases/{databaseID}/start-log/stream
// SSE stream that tails the start_log column while the database container is starting.
func (h *DatabaseHandler) StartLogStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, errMap("streaming not supported"))
		return
	}
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

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	initialSkip := 0
	if s := r.URL.Query().Get("skip"); s != "" {
		if n, convErr := strconv.Atoi(s); convErr == nil && n > 0 {
			initialSkip = n
		}
	}
	sentLines := initialSkip
	sendLine := func(line string) {
		safe := strings.ReplaceAll(line, "\r", "")
		fmt.Fprintf(w, "data: %s\n\n", safe)
		flusher.Flush()
	}
	sendPing := func() {
		fmt.Fprint(w, ": ping\n\n")
		flusher.Flush()
	}

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}

		var startLog, status string
		if qErr := h.db.QueryRowContext(r.Context(),
			`SELECT COALESCE(start_log,''), status FROM databases WHERE id=?`, dbID,
		).Scan(&startLog, &status); qErr != nil {
			return
		}

		allLines := strings.Split(startLog, "\n")
		var nonEmpty []string
		for _, l := range allLines {
			if strings.TrimSpace(l) != "" {
				nonEmpty = append(nonEmpty, l)
			}
		}
		if len(nonEmpty) > sentLines {
			for _, line := range nonEmpty[sentLines:] {
				sendLine(line)
			}
			sentLines = len(nonEmpty)
		} else {
			sendPing()
		}

		// Terminal states — the start attempt is complete (success or fail)
		if status == "running" || status == "error" || status == "stopped" {
			fmt.Fprint(w, "event: done\ndata: \n\n")
			flusher.Flush()
			return
		}
	}
}

// GET /api/projects/{projectID}/databases/{databaseID}/stats/stream
// SSE stream of live resource stats for the database container.
func (h *DatabaseHandler) StatsStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, errMap("streaming not supported"))
		return
	}
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

	cName := fmt.Sprintf("fd-db-%d", dbID)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	send := func() {
		ev := CollectContainerStats(cName)
		ev.Timestamp = time.Now().UnixMilli()
		data, _ := json.Marshal(ev)
		fmt.Fprintf(w, "event: stats\ndata: %s\n\n", data)
		flusher.Flush()
	}

	send() // immediate first event

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			send()
		}
	}
}

// ── Internal helpers ──────────────────────────────────────────────────────────

func (h *DatabaseHandler) getByID(w http.ResponseWriter, r *http.Request, projectID, dbID int64) {
	var d model.Database
	var npInt int
	var encPass string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id, project_id, name, db_type, db_version, db_name, db_user, db_password,
		        COALESCE(host_port, 0), COALESCE(cluster_port, 0), status, container_id, network_public, last_error, created_at, updated_at
		 FROM databases WHERE id=? AND project_id=?`, dbID, projectID,
	).Scan(
		&d.ID, &d.ProjectID, &d.Name, &d.DBType, &d.DBVersion, &d.DBName, &d.DBUser, &encPass,
		&d.HostPort, &d.ClusterPort, &d.Status, &d.ContainerID, &npInt, &d.LastError, &d.CreatedAt, &d.UpdatedAt,
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
		publicHost := h.dbSubdomain
		publicPort := d.HostPort
		if d.ClusterPort > 0 {
			publicPort = d.ClusterPort
		}
		d.PublicConnectionURL = deploy.DBPublicConnectionURL(d.DBType, d.DBName, d.DBUser, clearPass, publicHost, publicPort)
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
