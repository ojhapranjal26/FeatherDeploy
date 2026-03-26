package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"github.com/ojhapranjal26/featherdeploy/backend/internal/auth"
	appDb "github.com/ojhapranjal26/featherdeploy/backend/internal/db"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/handler"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/heartbeat"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/installer"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/mailer"
	mw "github.com/ojhapranjal26/featherdeploy/backend/internal/middleware"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/model"
	"github.com/ojhapranjal26/featherdeploy/backend/web"
)

const usage = `deploypaaas — self-hosted PaaS panel

Usage:
  deploypaaas                    Run the server (default)
  deploypaaas serve              Run the server (explicit)
  deploypaaas install            Interactive first-time setup wizard (Linux, root)
  deploypaaas update             Update an existing installation in-place (Linux, root)
  deploypaaas --help             Show this help

Flags (all overridable via environment variables):
  --rqlite-url    RQLITE_URL        rqlite HTTP URL         (default: http://127.0.0.1:4001)
  --jwt-secret    JWT_SECRET        Token signing secret
  --addr          ADDR              Listen address          (default: :8080)
  --origin        ORIGIN            Allowed CORS origins
  --smtp-host     SMTP_HOST         SMTP server host
  --smtp-port     SMTP_PORT         SMTP server port        (default: 1025)
  --smtp-user     SMTP_USER         SMTP username
  --smtp-pass     SMTP_PASS         SMTP password
  --smtp-from     SMTP_FROM         From address
  --smtp-tls      SMTP_TLS          Use STARTTLS            (default: false)
  --gh-client-id      GITHUB_CLIENT_ID      GitHub OAuth client ID
  --gh-client-secret  GITHUB_CLIENT_SECRET  GitHub OAuth client secret
`

func main() {
	// ── Subcommand dispatch ────────────────────────────────────────────────────
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "install":
			installer.Run()
			return
		case "update":
			installer.RunUpdate()
			return
		case "serve":
			// Remove "serve" from args so flag.Parse works normally
			os.Args = append(os.Args[:1], os.Args[2:]...)
		case "--help", "-h", "help":
			fmt.Fprint(os.Stderr, usage)
			os.Exit(0)
		}
	}

	serve()
}

func serve() {
	// ─── Config via flags / env ───────────────────────────────────────────────
	rqliteURL := envOrFlag("RQLITE_URL", "rqlite-url", "http://127.0.0.1:4001", "rqlite HTTP URL")
	envFilePath := envOrFlag("ENV_FILE", "env-file", "/etc/featherdeploy/featherdeploy.env", "Path to the env file shared with nodes")
	jwtSecret := envOrFlag("JWT_SECRET", "jwt-secret", "change-me-in-prod!", "JWT signing secret")
	addr := envOrFlag("ADDR", "addr", ":8080", "Listen address")
	origin := envOrFlag("ORIGIN", "origin", "http://localhost:5173,http://localhost:5174", "Comma-separated allowed CORS origins")

	// SMTP (optional — dev-log mode when SMTP_HOST is empty)
	smtpHost := envOrFlag("SMTP_HOST", "smtp-host", "", "SMTP host (leave blank for dev log mode)")
	smtpPort := envOrFlag("SMTP_PORT", "smtp-port", "1025", "SMTP port")
	smtpUser := envOrFlag("SMTP_USER", "smtp-user", "", "SMTP username")
	smtpPass := envOrFlag("SMTP_PASS", "smtp-pass", "", "SMTP password")
	smtpFrom := envOrFlag("SMTP_FROM", "smtp-from", "", "SMTP from address")
	smtpTLS := envOrFlag("SMTP_TLS", "smtp-tls", "false", "Use STARTTLS for SMTP")

	// GitHub OAuth
	ghClientID := envOrFlag("GITHUB_CLIENT_ID", "gh-client-id", "", "GitHub OAuth client ID")
	ghClientSecret := envOrFlag("GITHUB_CLIENT_SECRET", "gh-client-secret", "", "GitHub OAuth client secret")

	flag.Parse()

	if *jwtSecret == "change-me-in-prod!" {
		slog.Warn("JWT_SECRET is using the default value — set a strong secret in production")
	}

	// ─── Database ─────────────────────────────────────────────────────────────
	// Retry connecting to rqlite for up to 60s — rqlite may still be starting
	// after a system reboot even with After=rqlite.service in the unit file.
	var db *sql.DB
	for attempt := 1; attempt <= 12; attempt++ {
		var err error
		db, err = appDb.OpenRqlite(*rqliteURL)
		if err == nil {
			break
		}
		if attempt == 12 {
			slog.Error("open database: giving up after 12 attempts", "err", err)
			os.Exit(1)
		}
		slog.Warn("rqlite not ready, retrying", "attempt", attempt, "err", err)
		time.Sleep(5 * time.Second)
	}
	defer db.Close()
	slog.Info("database ready", "rqlite", *rqliteURL)

	// Seed default superadmin on first run (no users in DB yet)
	seedSuperAdmin(db)

	// ─── Brain heartbeat + SSH key ──────────────────────────────────────
	// Generate / load cluster SSH keypair (for passwordless node access)
	if err := ensureSSHKey(db); err != nil {
		slog.Warn("SSH key setup", "err", err)
	}

	// Start brain heartbeat: writes cluster_state every 10s so nodes know we're alive
	brainAddr := *addr
	if strings.HasPrefix(brainAddr, ":") {
		brainAddr = "http://127.0.0.1" + brainAddr
	} else {
		brainAddr = "http://" + brainAddr
	}
	heartbeat.StartBrain(context.Background(), db, "main", brainAddr, func() heartbeat.BrainStats {
		return collectServerStats()
	})
	slog.Info("brain heartbeat started")

	// ─── Handlers ─────────────────────────────────────────────────────────────
	smtpPortInt, _ := strconv.Atoi(*smtpPort)
	m := mailer.New(mailer.Config{
		Host:     *smtpHost,
		Port:     smtpPortInt,
		Username: *smtpUser,
		Password: *smtpPass,
		From:     *smtpFrom,
		UseTLS:   *smtpTLS == "true",
	})

	authH := handler.NewAuthHandler(db, *jwtSecret, 24*time.Hour)
	userH := handler.NewUserHandler(db)
	projH := handler.NewProjectHandler(db)
	svcH := handler.NewServiceHandler(db)
	depH := handler.NewDeploymentHandler(db, *jwtSecret)
	envH := handler.NewEnvHandler(db, *jwtSecret)
	domainH := handler.NewDomainHandler(db)
	inviteH := handler.NewInvitationHandler(db, m, *jwtSecret, 24*time.Hour, *origin)
	ghH := handler.NewGitHubHandler(db, *ghClientID, *ghClientSecret, *origin)
	ghAppH := handler.NewGitHubAppHandler(db)
	sshH := handler.NewSSHKeyHandler(db, *jwtSecret)
	dashH := handler.NewDashboardHandler(db)
	detectH := handler.NewDetectHandler(db, *jwtSecret)
	nodeH := handler.NewNodeHandler(db, *jwtSecret, *envFilePath, "/usr/local/bin/featherdeploy-node", domainFromOrigin(*origin))
	if err := nodeH.EnsureCA(); err != nil {
		slog.Warn("CA init warning", "err", err)
	}
	settingsH := handler.NewSettingsHandler(db)
	statsH := handler.NewStatsHandler(db)
	containerStatsH := handler.NewContainerStatsHandler()

	// ─── Router ──────────────────────────────────────────────────────────────
	r := chi.NewRouter()

	r.Use(chiMiddleware.RealIP)
	r.Use(chiMiddleware.RequestID)
	r.Use(chiMiddleware.Logger)
	r.Use(chiMiddleware.Recoverer)
	r.Use(chiMiddleware.Timeout(30 * time.Second))
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   splitOrigins(*origin),
		AllowedMethods:   []string{"GET", "POST", "PATCH", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Authorization", "Content-Type", "X-Request-Id"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	// ─── Public routes ────────────────────────────────────────────────────────
	r.Post("/api/auth/login", authH.Login)

	// Branding is public so the login page can show it before authentication
	r.Get("/api/settings/branding", settingsH.GetBranding)

	// Invitation accept flow (public — token acts as credential)
	r.Get("/api/invitations/{token}", inviteH.Verify)
	r.Post("/api/invitations/{token}/accept", inviteH.Accept)

	// GitHub App status is public (frontend uses it to show connect button)
	r.Get("/api/github-app/status", ghAppH.Status)

	// Node join flow — the join token serves as the credential
	r.Get("/api/nodes/{token}/join-script", nodeH.JoinScript)
	r.Post("/api/nodes/{token}/complete-join", nodeH.CompleteJoin)
	r.Get("/api/nodes/binary", nodeH.BinaryDownload)
	r.Get("/api/nodes/server-binary", nodeH.ServerBinaryDownload)
	r.Get("/api/nodes/ca-cert", nodeH.CACert)

	// ─── Authenticated routes ─────────────────────────────────────────────────
	r.Group(func(r chi.Router) {
		r.Use(mw.Authenticate(*jwtSecret))

		// Self
		r.Get("/api/auth/me", authH.Me)

		// ── Dashboard ──────────────────────────────────────────────────────
		r.Get("/api/dashboard", dashH.Stats)

		// ── Admin: user management ─────────────────────────────────────────
		r.Group(func(r chi.Router) {
			r.Use(mw.RequireRole(model.RoleSuperAdmin, model.RoleAdmin))
			r.Get("/api/admin/users", userH.List)

			// Invitation management
			r.Get("/api/admin/invitations", inviteH.List)
			r.Post("/api/admin/invitations", inviteH.Create)
			r.Delete("/api/admin/invitations/{invitationID}", inviteH.Revoke)
		})
		r.Group(func(r chi.Router) {
			r.Use(mw.RequireRole(model.RoleSuperAdmin))
			r.Patch("/api/admin/users/{userID}/role", userH.UpdateRole)
			r.Delete("/api/admin/users/{userID}", userH.Delete)
		})

		// ── GitHub OAuth ───────────────────────────────────────────────────
		// Status is visible to all authenticated users (shows whether their
		// account is connected; users without the role will see "not configured").
		r.Get("/api/github/status", ghH.Status)
		// Connecting/disconnecting GitHub and listing repos requires at least
		// the admin role — regular users cannot integrate their own GitHub.
		r.Group(func(r chi.Router) {
			r.Use(mw.RequireRole(model.RoleSuperAdmin, model.RoleAdmin))
			r.Get("/api/github/auth", ghH.AuthURL)
			r.Get("/api/github/callback", ghH.Callback)
			r.Delete("/api/github/disconnect", ghH.Disconnect)
			r.Get("/api/github/repos", ghH.ListRepos)
		})

		// ── GitHub App ────────────────────────────────────────────────────
		r.Get("/api/github-app/repos", ghAppH.ListRepos)
		r.Group(func(r chi.Router) {
			r.Use(mw.RequireRole(model.RoleSuperAdmin))
			r.Get("/api/github-app/config", ghAppH.GetConfig)
			r.Post("/api/github-app/config", ghAppH.SetConfig)
			r.Delete("/api/github-app/config", ghAppH.DeleteConfig)
			// Branding write requires superadmin
			r.Put("/api/settings/branding", settingsH.SetBranding)
		})

		// ── Live stats SSE stream (no 30s timeout — long-lived connection) ──
		// Use a dedicated sub-router so we can remove the Timeout middleware.
		r.With(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				// Strip request context deadline set by chi Timeout middleware
				next.ServeHTTP(w, req)
			})
		}).Get("/api/stats/stream", statsH.Stream)

		// ── Cluster brain info (any authenticated user) ───────────────────
		r.Get("/api/cluster/brain", nodeH.ClusterBrain)

		// ── Nodes (superadmin / admin only) ──────────────────────────────
		r.Group(func(r chi.Router) {
			r.Use(mw.RequireRole(model.RoleSuperAdmin, model.RoleAdmin))
			r.Get("/api/nodes", nodeH.List)
			r.Post("/api/nodes", nodeH.Add)
			r.Delete("/api/nodes/{nodeID}", nodeH.Delete)
			r.Post("/api/nodes/{nodeID}/ping", nodeH.Ping)
			r.Get("/api/nodes/{nodeID}/ssh-command", nodeH.SSHCommand)
		})

		// ── SSH Keys ──────────────────────────────────────────────────────
		r.Get("/api/ssh-keys", sshH.List)
		r.Post("/api/ssh-keys/generate", sshH.Generate)
		r.Post("/api/ssh-keys/import", sshH.Import)
		r.Delete("/api/ssh-keys/{keyID}", sshH.Delete)
		r.Get("/api/ssh-keys/{keyID}/private", sshH.ExportPrivate)

		// ── Projects ──────────────────────────────────────────────────────
		r.Get("/api/projects", projH.List)
		r.Post("/api/projects", projH.Create)

		r.Group(func(r chi.Router) {
			// viewer minimum for GET; editor+ for mutations
			r.Use(mw.RequireProjectAccess(db, "viewer"))
			r.Get("/api/projects/{projectID}", projH.Get)

			r.Group(func(r chi.Router) {
				r.Use(mw.RequireProjectAccess(db, "editor"))
				r.Patch("/api/projects/{projectID}", projH.Update)
			})

			r.Group(func(r chi.Router) {
				r.Use(mw.RequireProjectAccess(db, "owner"))
				r.Delete("/api/projects/{projectID}", projH.Delete)
				r.Get("/api/projects/{projectID}/members", projH.ListMembers)
				r.Post("/api/projects/{projectID}/members", projH.AddMember)
				r.Delete("/api/projects/{projectID}/members/{userID}", projH.RemoveMember)
			})

			// ── Services ────────────────────────────────────────────────
			r.Get("/api/projects/{projectID}/services", svcH.List)

			r.Group(func(r chi.Router) {
				r.Use(mw.RequireProjectAccess(db, "editor"))
				r.Post("/api/projects/{projectID}/services", svcH.Create)

				r.Get("/api/projects/{projectID}/services/{serviceID}", svcH.Get)
				r.Patch("/api/projects/{projectID}/services/{serviceID}", svcH.Update)
				r.Delete("/api/projects/{projectID}/services/{serviceID}", svcH.Delete)

				// ── Stack detection ──────────────────────────────────────
				r.Post("/api/projects/{projectID}/services/{serviceID}/detect", detectH.Detect)

				// ── Deployments ─────────────────────────────────────────
				r.Get("/api/projects/{projectID}/services/{serviceID}/deployments", depH.List)
				r.Post("/api/projects/{projectID}/services/{serviceID}/deployments", depH.Trigger)
				r.Get("/api/projects/{projectID}/services/{serviceID}/deployments/{deploymentID}", depH.Get)
				r.Get("/api/projects/{projectID}/services/{serviceID}/deployments/{deploymentID}/logs", depH.Logs)
				r.Get("/api/projects/{projectID}/services/{serviceID}/container-logs", depH.ContainerLogs)

				// ── Env vars ─────────────────────────────────────────────
				r.Get("/api/projects/{projectID}/services/{serviceID}/env", envH.List)
				r.Put("/api/projects/{projectID}/services/{serviceID}/env", envH.Upsert)
				r.Delete("/api/projects/{projectID}/services/{serviceID}/env/{key}", envH.Delete)

				// ── Container live stats SSE ──────────────────────────────
				r.Get("/api/projects/{projectID}/services/{serviceID}/stats/stream", containerStatsH.Stream)

				// ── Domains ──────────────────────────────────────────────
				r.Get("/api/projects/{projectID}/services/{serviceID}/domains", domainH.List)
				r.Post("/api/projects/{projectID}/services/{serviceID}/domains", domainH.Add)
				r.Delete("/api/projects/{projectID}/services/{serviceID}/domains/{domainID}", domainH.Delete)
				r.Post("/api/projects/{projectID}/services/{serviceID}/domains/{domainID}/verify", domainH.Verify)
			})
		})
	})

	// ─── Health check (no auth) ───────────────────────────────────────────────
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})

	// ─── Embedded frontend SPA ────────────────────────────────────────────────
	staticFS, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		slog.Error("embed dist", "err", err)
		os.Exit(1)
	}
	r.Get("/*", spaHandler(staticFS))

	slog.Info("server starting", "addr", *addr)
	if err := http.ListenAndServe(*addr, r); err != nil {
		slog.Error("server exited", "err", err)
		os.Exit(1)
	}
}

// spaHandler serves the embedded React SPA, falling back to index.html for
// any path that doesn't match a real file (client-side routing support).
func spaHandler(static fs.FS) http.HandlerFunc {
	server := http.FileServer(http.FS(static))
	return func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := static.Open(path); err != nil {
			// Path not found — serve index.html for client-side routing
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/"
			server.ServeHTTP(w, r2)
			return
		}
		server.ServeHTTP(w, r)
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func envOrFlag(envKey, flagName, def, usage string) *string {
	val := def
	if v := os.Getenv(envKey); v != "" {
		val = v
	}
	return flag.String(flagName, val, usage)
}

func splitOrigins(s string) []string {
	parts := []string{}
	for _, p := range splitComma(s) {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return parts
}

// domainFromOrigin extracts the host (no scheme) from the first CORS origin.
// e.g. "http://example.com,http://localhost:5173" → "example.com"
func domainFromOrigin(origins string) string {
	if origins == "" {
		return "localhost"
	}
	first := strings.SplitN(origins, ",", 2)[0]
	first = strings.TrimPrefix(strings.TrimPrefix(first, "https://"), "http://")
	return first
}

func splitComma(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

// seedSuperAdmin inserts a default superadmin user when the users table is empty.
// Credentials are printed to stdout so the operator can change them immediately.
func seedSuperAdmin(db *sql.DB) {
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count); err != nil || count > 0 {
		return
	}
	hash, err := auth.HashPassword("Admin@123456")
	if err != nil {
		slog.Error("seed superadmin: hash password", "err", err)
		return
	}
	_, err = db.Exec(
		`INSERT INTO users (email, name, password_hash, role) VALUES (?,?,?,?)`,
		"admin@deploypaaas.local", "Platform Admin", hash, "superadmin",
	)
	if err != nil {
		slog.Error("seed superadmin: insert", "err", err)
		return
	}
	fmt.Println(`
  ╔══════════════════════════════════════════════════════╗
  ║  DEFAULT SUPERADMIN CREATED — CHANGE IMMEDIATELY!   ║
  ║  Email   : admin@deploypaaas.local                  ║
  ║  Password: Admin@123456                             ║
  ╚══════════════════════════════════════════════════════╝`)
	slog.Warn("seeded default superadmin — change credentials immediately!",
		"email", "admin@deploypaaas.local",
		"password", "Admin@123456",
	)
}

// ensureSSHKey generates an SSH keypair at /etc/featherdeploy/ssh_id (ed25519)
// if one doesn't exist, then stores the public key in cluster_state for
// distribution to worker nodes.
func ensureSSHKey(db *sql.DB) error {
	keyPath := "/etc/featherdeploy/ssh_id"
	pubPath := keyPath + ".pub"

	// Generate keypair if not present
	if _, err := os.Stat(keyPath); os.IsNotExist(err) {
		if err := os.MkdirAll("/etc/featherdeploy", 0700); err != nil {
			return err
		}
		if out, err := exec.Command("ssh-keygen",
			"-t", "ed25519",
			"-f", keyPath,
			"-N", "",
			"-C", "featherdeploy-cluster",
		).CombinedOutput(); err != nil {
			slog.Warn("ssh-keygen failed (SSH passwordless access will be unavailable)",
				"err", err, "out", string(out))
			return nil // non-fatal
		}
		slog.Info("generated cluster SSH keypair", "path", keyPath)
	}

	// Read public key and store in DB
	pubKey, err := os.ReadFile(pubPath)
	if err != nil {
		return nil // non-fatal
	}
	if err := heartbeat.SetSSHPublicKey(db, strings.TrimSpace(string(pubKey))); err != nil {
		slog.Warn("store SSH public key in DB", "err", err)
	}
	return nil
}

// collectServerStats reads /proc/meminfo and statvfs for the main server's
// current resource usage.  Returns empty stats on non-Linux systems.
func collectServerStats() heartbeat.BrainStats {
	var s heartbeat.BrainStats
	// RAM from /proc/meminfo
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			var key string
			var val uint64
			fmt.Sscanf(line, "%s %d", &key, &val)
			switch key {
			case "MemTotal:":
				s.RAMTotal = int64(val * 1024)
			case "MemAvailable:":
				s.RAMUsed = s.RAMTotal - int64(val*1024)
			}
		}
	}
	// CPU: simple 1-second sample via /proc/stat
	s.CPU = 0 // lightweight: leave as 0 from main server side; nodes calculate their own
	// Disk: root filesystem
	s.DiskUsed, s.DiskTotal = diskUsage("/")
	return s
}

func diskUsage(path string) (used, total int64) {
	if out, err := exec.Command("df", "-B1", "--output=used,size", path).Output(); err == nil {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) >= 2 {
			parts := strings.Fields(lines[1])
			if len(parts) >= 2 {
				fmt.Sscanf(parts[0], "%d", &used)
				fmt.Sscanf(parts[1], "%d", &total)
			}
		}
	}
	return
}
