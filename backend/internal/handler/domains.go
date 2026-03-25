package handler

import (
	"database/sql"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"

	caddypkg "github.com/ojhapranjal26/featherdeploy/backend/internal/caddy"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/model"
	v "github.com/ojhapranjal26/featherdeploy/backend/internal/validator"
)

type DomainHandler struct{ db *sql.DB }

func NewDomainHandler(db *sql.DB) *DomainHandler { return &DomainHandler{db: db} }

// GET /api/projects/{projectID}/services/{serviceID}/domains
func (h *DomainHandler) List(w http.ResponseWriter, r *http.Request) {
	svcID, err := strconv.ParseInt(r.PathValue("serviceID"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid serviceID"))
		return
	}
	rows, err := h.db.QueryContext(r.Context(),
		`SELECT id, service_id, domain, tls, verified, created_at, updated_at
		 FROM domains WHERE service_id=? ORDER BY created_at`, svcID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	defer rows.Close()
	doms := make([]model.Domain, 0)
	for rows.Next() {
		var d model.Domain
		var tls, verified int
		if err := rows.Scan(&d.ID, &d.ServiceID, &d.Domain, &tls, &verified,
			&d.CreatedAt, &d.UpdatedAt); err != nil {
			continue
		}
		d.TLS = tls == 1
		d.Verified = verified == 1
		doms = append(doms, d)
	}
	writeJSON(w, http.StatusOK, doms)
}

// POST /api/projects/{projectID}/services/{serviceID}/domains
func (h *DomainHandler) Add(w http.ResponseWriter, r *http.Request) {
	svcID, err := strconv.ParseInt(r.PathValue("serviceID"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid serviceID"))
		return
	}
	var req model.AddDomainRequest
	if !v.DecodeAndValidate(w, r, &req) {
		return
	}
	// Normalize domain to lowercase
	req.Domain = strings.ToLower(req.Domain)
	tlsInt := 0
	if req.TLS {
		tlsInt = 1
	}
	res, err := h.db.ExecContext(r.Context(),
		`INSERT INTO domains (service_id, domain, tls) VALUES (?,?,?)`,
		svcID, req.Domain, tlsInt)
	if err != nil {
		if isUnique(err) {
			writeJSON(w, http.StatusConflict, errMap("domain already registered"))
			return
		}
		slog.Error("add domain", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	id, _ := res.LastInsertId()
	row := h.db.QueryRowContext(r.Context(),
		`SELECT id, service_id, domain, tls, verified, created_at, updated_at
		 FROM domains WHERE id=?`, id)
	var d model.Domain
	var tls, verified int
	row.Scan(&d.ID, &d.ServiceID, &d.Domain, &tls, &verified, &d.CreatedAt, &d.UpdatedAt)
	d.TLS = tls == 1
	d.Verified = verified == 1
	writeJSON(w, http.StatusCreated, d)
	go caddypkg.Reload(h.db)
}

// DELETE /api/projects/{projectID}/services/{serviceID}/domains/{domainID}
func (h *DomainHandler) Delete(w http.ResponseWriter, r *http.Request) {
	domainID, err := strconv.ParseInt(r.PathValue("domainID"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid domainID"))
		return
	}
	h.db.ExecContext(r.Context(), `DELETE FROM domains WHERE id=?`, domainID)
	w.WriteHeader(http.StatusNoContent)
	go caddypkg.Reload(h.db)
}

// POST /api/projects/{projectID}/services/{serviceID}/domains/{domainID}/verify
// Performs a DNS lookup to check that the domain resolves to this server's IP.
// Set the SERVER_IP environment variable to the expected IP; if unset any resolved IP marks verified.
func (h *DomainHandler) Verify(w http.ResponseWriter, r *http.Request) {
	domainID, err := strconv.ParseInt(r.PathValue("domainID"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid domainID"))
		return
	}

	var domainName string
	err = h.db.QueryRowContext(r.Context(),
		`SELECT domain FROM domains WHERE id=?`, domainID).Scan(&domainName)
	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusNotFound, errMap("domain not found"))
		return
	} else if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}

	serverIP := os.Getenv("SERVER_IP")

	addrs, dnsErr := net.LookupHost(domainName)
	resolvedIP := ""
	if dnsErr == nil && len(addrs) > 0 {
		resolvedIP = addrs[0]
	}

	verified := resolvedIP != "" && (serverIP == "" || resolvedIP == serverIP)
	if verified {
		h.db.ExecContext(r.Context(),
			`UPDATE domains SET verified=1, updated_at=datetime('now') WHERE id=?`, domainID)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"verified":    verified,
		"resolved_ip": resolvedIP,
		"server_ip":   serverIP,
	})
	go caddypkg.Reload(h.db)
}

