package handler

import (
	"context"
	"database/sql"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

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
// Uses Cloudflare's 1.1.1.1:53 directly to avoid systemd-resolved stub issues.
// SERVER_IP env var overrides auto-detection; if unset, the host's public IP is
// detected via a UDP routing trick.
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

	// Determine the expected server IP.
	// Prefer the SERVER_IP env var (set by the installer); fall back to
	// auto-detecting the host's public-facing IP via UDP routing trick.
	serverIP := os.Getenv("SERVER_IP")
	if serverIP == "" {
		serverIP = detectOwnPublicIP()
	}

	// Use Cloudflare's public resolver directly (1.1.1.1:53).
	// The system default resolver on Ubuntu 22.04+ is the systemd-resolved stub
	// at 127.0.0.53 which can fail or return stale results from within a
	// restricted systemd unit.  Hitting 1.1.1.1 directly is always reliable.
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: 5 * time.Second}
			// Try Cloudflare first, fall back to Google
			for _, server := range []string{"1.1.1.1:53", "8.8.8.8:53"} {
				conn, err := d.DialContext(ctx, "udp", server)
				if err == nil {
					return conn, nil
				}
			}
			return nil, context.DeadlineExceeded
		},
	}

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	addrs, dnsErr := resolver.LookupHost(ctx, domainName)

	resolvedIP := ""
	var dnsErrStr string
	if dnsErr != nil {
		dnsErrStr = dnsErr.Error()
		slog.Warn("dns verify: lookup failed", "domain", domainName, "err", dnsErr)
	} else if len(addrs) > 0 {
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
		"dns_error":   dnsErrStr,
	})
	go caddypkg.Reload(h.db)
}

// detectOwnPublicIP uses a UDP "connect" trick to determine the host's
// public-facing IP address without sending any packets.  The OS routing
// table picks the interface that would be used to reach 1.1.1.1, and
// LocalAddr() on the resulting UDP conn gives us that interface's IP.
func detectOwnPublicIP() string {
	conn, err := net.DialTimeout("udp", "1.1.1.1:80", 2*time.Second)
	if err != nil {
		return ""
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}

