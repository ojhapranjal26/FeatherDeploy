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
	"github.com/ojhapranjal26/featherdeploy/backend/internal/deploy"
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

	// ─── Startup reconciliation ────────────────────────────────────────────
	// After a restart or update, fix any inconsistent state left by the
	// previous process:
	//  • Deployments stuck in 'running'/'pending' become 'failed' (the worker
	//    that was handling them is gone, so they will never finish).
	//  • Services whose container is actually running get status='running';
	//    services whose container is gone get status='error'.
	go reconcileServiceStates(db)

	// Start the background stats collector: samples every running container once
	// per minute and writes to service_stats for historical analysis.
	go startStatsCollector(db)
	// One worker = fully sequential deployments: when two services are deployed
	// simultaneously they queue up rather than running in parallel. This prevents
	// the build process (npm install, podman build, …) from saturating CPU/RAM
	// on small VPS hosts. Increase via DEPLOY_WORKERS env var if needed.
	workers := 1
	if w := os.Getenv("DEPLOY_WORKERS"); w != "" {
		if n, err := strconv.Atoi(w); err == nil && n > 0 {
			workers = n
		}
	}
	deploy.InitQueue(workers)

	// ─── Brain heartbeat + SSH key
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
	cfgStore := handler.NewConfigStore(
		db,
		*jwtSecret,
		mailer.Config{
			Host:     *smtpHost,
			Port:     smtpPortInt,
			Username: *smtpUser,
			Password: *smtpPass,
			From:     *smtpFrom,
			UseTLS:   *smtpTLS == "true",
		},
		*ghClientID, *ghClientSecret,
	)

	authH := handler.NewAuthHandler(db, *jwtSecret, 24*time.Hour)
	userH := handler.NewUserHandler(db)
	projH := handler.NewProjectHandler(db)
	svcH := handler.NewServiceHandler(db)
	depH := handler.NewDeploymentHandler(db, *jwtSecret)
	envH := handler.NewEnvHandler(db, *jwtSecret)
	domainH := handler.NewDomainHandler(db)
	inviteH := handler.NewInvitationHandler(db, cfgStore, *jwtSecret, 24*time.Hour, *origin)
	ghH := handler.NewGitHubHandler(db, cfgStore, *origin)
	ghAppH := handler.NewGitHubAppHandler(db, *jwtSecret)
	sshH := handler.NewSSHKeyHandler(db, *jwtSecret)
	dashH := handler.NewDashboardHandler(db)
	detectH := handler.NewDetectHandler(db, *jwtSecret)
	nodeH := handler.NewNodeHandler(db, *jwtSecret, *envFilePath, "/usr/local/bin/featherdeploy-node", domainFromOrigin(*origin))
	if err := nodeH.EnsureCA(); err != nil {
		slog.Warn("CA init warning", "err", err)
	}
	settingsH := handler.NewSettingsHandler(db, cfgStore)
	statsH := handler.NewStatsHandler(db)
	containerStatsH := handler.NewContainerStatsHandler()
	statsHistH := handler.NewStatsHistoryHandler(db)
	qrH := handler.NewQRAuthHandler(db, *jwtSecret)
	systemH := handler.NewSystemHandler()

	// ─── Router ──────────────────────────────────────────────────────────────
	r := chi.NewRouter()

	r.Use(chiMiddleware.RealIP)
	r.Use(chiMiddleware.RequestID)
	r.Use(chiMiddleware.Logger)
	r.Use(chiMiddleware.Recoverer)
	// NOTE: Timeout is NOT applied globally — it is added per-group so that
	// long-lived SSE streaming routes can opt out. See authenticated groups below.
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
	// Uploaded logo is public so it can be used in the login page and sidebar
	r.Get("/api/settings/branding/logo", settingsH.GetLogoImage)

	// Invitation accept flow (public — token acts as credential)
	r.Get("/api/invitations/{token}", inviteH.Verify)
	r.Post("/api/invitations/{token}/accept", inviteH.Accept)

	// QR login: status and claim are public (token is the credential)
	r.Get("/api/auth/qr/{qrToken}/status", qrH.Status)
	r.Post("/api/auth/qr/{qrToken}/claim", qrH.Claim)

	// GitHub App status is public (frontend uses it to show connect button)
	r.Get("/api/github-app/status", ghAppH.Status)
	// GitHub App webhook — public (GitHub signs payloads with HMAC-SHA256)
	r.Post("/api/github-app/webhook", ghAppH.Webhook)

	// GitHub OAuth callback — public (browser redirect from GitHub carries no Authorization header).
	// User identity is verified via the gh_oauth_uid cookie set during /api/github/auth.
	r.Get("/api/github/callback", ghH.Callback)

	// Node join flow — the join token serves as the credential
	r.Get("/api/nodes/{token}/join-script", nodeH.JoinScript)
	r.Post("/api/nodes/{token}/complete-join", nodeH.CompleteJoin)
	r.Get("/api/nodes/binary", nodeH.BinaryDownload)
	r.Get("/api/nodes/server-binary", nodeH.ServerBinaryDownload)
	r.Get("/api/nodes/ca-cert", nodeH.CACert)

	// ─── Authenticated routes (with 30s request timeout) ─────────────────────────
	// SSE streaming routes are registered in a separate group below WITHOUT this
	// timeout so long-running builds don't get killed at 30s.
	r.Group(func(r chi.Router) {
		r.Use(mw.Authenticate(*jwtSecret))
		r.Use(chiMiddleware.Timeout(30 * time.Second))

		// Self
		r.Get("/api/auth/me", authH.Me)

		// ── System / version check (any authenticated user can query) ─────
		r.Get("/api/system/version", systemH.VersionCheck)
		// Self-update is superadmin only — one-click update from the dashboard.
		r.Group(func(r chi.Router) {
			r.Use(mw.RequireRole(model.RoleSuperAdmin))
			r.Post("/api/system/update", systemH.TriggerUpdate)
		})

		// ── Dashboard ──────────────────────────────────────────────────────
		r.Get("/api/dashboard", dashH.Stats)

		// ── User lookup (any authenticated user — used for member invites) ─
		r.Get("/api/users/lookup", userH.Lookup)

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

		// ── Superadmin: platform settings ─────────────────────────────────
		r.Group(func(r chi.Router) {
			r.Use(mw.RequireRole(model.RoleSuperAdmin))
			// Branding
			r.Put("/api/settings/branding", settingsH.SetBranding)
			r.Post("/api/settings/branding/logo", settingsH.UploadLogo)
			// SMTP
			r.Get("/api/settings/smtp", settingsH.GetSMTPStatus)
			r.Post("/api/settings/smtp", settingsH.SetSMTP)
			r.Delete("/api/settings/smtp", settingsH.DeleteSMTP)
			// GitHub OAuth credentials
			r.Get("/api/settings/github-oauth", settingsH.GetGitHubOAuthStatus)
			r.Post("/api/settings/github-oauth", settingsH.SetGitHubOAuth)
			r.Delete("/api/settings/github-oauth", settingsH.DeleteGitHubOAuth)
		})

		// ── GitHub OAuth ───────────────────────────────────────────────────
		// All authenticated users can connect/disconnect their GitHub account
		// and browse repos, branches and folder trees.
		r.Get("/api/github/status", ghH.Status)
		r.Get("/api/github/auth", ghH.AuthURL)
		r.Delete("/api/github/disconnect", ghH.Disconnect)
		r.Get("/api/github/repos", ghH.ListRepos)
		r.Get("/api/github/repos/{owner}/{repo}/branches", ghH.ListBranches)
		r.Get("/api/github/repos/{owner}/{repo}/tree", ghH.GetTree)

		// ── GitHub App ────────────────────────────────────────────────────
		r.Get("/api/github-app/repos", ghAppH.ListRepos)
		r.Group(func(r chi.Router) {
			r.Use(mw.RequireRole(model.RoleSuperAdmin))
			r.Get("/api/github-app/config", ghAppH.GetConfig)
			r.Post("/api/github-app/config", ghAppH.SetConfig)
			r.Delete("/api/github-app/config", ghAppH.DeleteConfig)
			r.Get("/api/github-app/webhook-deliveries", ghAppH.WebhookDeliveries)
		})

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
				r.Patch("/api/projects/{projectID}/members/{userID}", projH.UpdateMember)
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
				r.Post("/api/projects/{projectID}/services/{serviceID}/restart", svcH.Restart)

				// ── Stack detection ──────────────────────────────────────
				r.Post("/api/projects/{projectID}/services/{serviceID}/detect", detectH.Detect)

				// ── Deployments ─────────────────────────────────────────
				r.Get("/api/projects/{projectID}/services/{serviceID}/deployments", depH.List)
				r.Post("/api/projects/{projectID}/services/{serviceID}/deployments", depH.Trigger)
				r.Get("/api/projects/{projectID}/services/{serviceID}/deployments/{deploymentID}", depH.Get)
				// NOTE: /logs, /container-logs, and /stats/stream are SSE routes registered
				// in the no-timeout group below.

				// ── Artifact upload ─────────────────────────────────────
				r.Post("/api/projects/{projectID}/services/{serviceID}/upload-artifact", depH.UploadArtifact)

				// ── Env vars ─────────────────────────────────────────────
				r.Get("/api/projects/{projectID}/services/{serviceID}/env", envH.List)
				r.Put("/api/projects/{projectID}/services/{serviceID}/env", envH.Upsert)
				r.Delete("/api/projects/{projectID}/services/{serviceID}/env/{key}", envH.Delete)
				r.Get("/api/projects/{projectID}/services/{serviceID}/env/{key}/reveal", envH.Reveal)
				// ── Container live stats SSE and Domains ─────────────────
				// NOTE: /stats/stream is an SSE route; registered in no-timeout group below.

				// ── Historical stats ──────────────────────────────────────
				r.Get("/api/projects/{projectID}/services/{serviceID}/stats/history", statsHistH.History)
				r.Get("/api/projects/{projectID}/services/{serviceID}/stats/monthly", statsHistH.MonthlyHistory)

				// ── QR generate (authenticated) ───────────────────────────
				r.Post("/api/auth/qr/generate", qrH.Generate)

				// ── Domains ──────────────────────────────────────────────
				r.Get("/api/projects/{projectID}/services/{serviceID}/domains", domainH.List)
				r.Post("/api/projects/{projectID}/services/{serviceID}/domains", domainH.Add)
				r.Delete("/api/projects/{projectID}/services/{serviceID}/domains/{domainID}", domainH.Delete)
				r.Post("/api/projects/{projectID}/services/{serviceID}/domains/{domainID}/verify", domainH.Verify)
			})
		})
	})

	// ─── Authenticated SSE routes (NO timeout — long-lived streaming connections) ──
	// Deployment logs, container logs, and stats streams must not be subject to
	// the 30s timeout applied to regular API routes.  A long build (e.g. Next.js
	// with npm install + tsc + next build) takes several minutes; killing the
	// request context at 30s terminates the SSE stream and causes Caddy to return
	// a 502 to the browser.
	r.Group(func(r chi.Router) {
		r.Use(mw.Authenticate(*jwtSecret))
		// Global node stats stream
		r.Get("/api/stats/stream", statsH.Stream)
		// Per-service SSE streams (require at least editor project access)
		r.With(mw.RequireProjectAccess(db, "editor")).
			Get("/api/projects/{projectID}/services/{serviceID}/deployments/{deploymentID}/logs", depH.Logs)
		r.With(mw.RequireProjectAccess(db, "editor")).
			Get("/api/projects/{projectID}/services/{serviceID}/container-logs", depH.ContainerLogs)
		r.With(mw.RequireProjectAccess(db, "editor")).
			Get("/api/projects/{projectID}/services/{serviceID}/stats/stream", containerStatsH.Stream)
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

// reconcileServiceStates is called once at startup in a goroutine. It marks
// any deployment that was 'running'/'pending' (orphaned by the previous process)
// as 'failed', then checks each service's actual podman container state and
// updates the services.status column accordingly.
func reconcileServiceStates(db *sql.DB) {
	// 1. Mark orphaned in-progress deployments as failed.
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	res, err := db.Exec(
		`UPDATE deployments
		 SET status='failed', finished_at=?, error_message='server restarted before deployment completed'
		 WHERE status IN ('running','pending')`,
		now)
	if err == nil {
		if n, _ := res.RowsAffected(); n > 0 {
			slog.Info("startup reconcile: marked orphaned deployments failed", "count", n)
		}
	}

	// 2. Sync service.status with the actual podman container state.
	rows, err := db.Query(
		`SELECT id FROM services WHERE status IN ('running','deploying','error')`)
	if err != nil {
		slog.Warn("startup reconcile: query services", "err", err)
		return
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()

	for _, svcID := range ids {
		cName := fmt.Sprintf("fd-svc-%d", svcID)
		out, inspErr := exec.Command("podman", "inspect",
			"--format", "{{.State.Status}}", cName).Output()
		if inspErr != nil {
			// Container doesn't exist — mark service as error.
			db.Exec( //nolint
				`UPDATE services SET status='error', updated_at=datetime('now') WHERE id=?`, svcID)
			slog.Info("startup reconcile: container missing", "svc_id", svcID)
			continue
		}
		state := strings.TrimSpace(string(out))
		wantedStatus := "error"
		if state == "running" {
			wantedStatus = "running"
		} else if state == "exited" || state == "stopped" || state == "created" {
			// Container exists but is stopped — attempt a restart before giving up.
			if startOut, startErr := exec.Command("podman", "start", cName).CombinedOutput(); startErr == nil {
				wantedStatus = "running"
				slog.Info("startup reconcile: restarted stopped container", "svc_id", svcID)
			} else {
				slog.Warn("startup reconcile: could not restart container", "svc_id", svcID, "output", strings.TrimSpace(string(startOut)))
			}
		}
		db.Exec( //nolint
			`UPDATE services SET status=?, updated_at=datetime('now') WHERE id=?`,
			wantedStatus, svcID)
		slog.Info("startup reconcile: synced service", "svc_id", svcID, "container_state", state, "db_status", wantedStatus)
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

// collectServerStats reads /proc/meminfo, /proc/stat and statvfs for the main
// server's current resource usage.  Returns empty stats on non-Linux systems.
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
	// CPU: 200 ms sample of /proc/stat
	s.CPU = readServerCPU()
	// Disk: root filesystem
	s.DiskUsed, s.DiskTotal = diskUsage("/")
	return s
}

// readServerCPU returns the CPU utilisation (0–100) as a short /proc/stat diff.
func readServerCPU() float64 {
	type snap struct{ idle, total int64 }
	sample := func() (s snap) {
		data, err := os.ReadFile("/proc/stat")
		if err != nil {
			return
		}
		for _, line := range strings.Split(string(data), "\n") {
			if !strings.HasPrefix(line, "cpu ") {
				continue
			}
			for i, f := range strings.Fields(line)[1:] {
				n, _ := strconv.ParseInt(f, 10, 64)
				s.total += n
				if i == 3 {
					s.idle = n
				}
			}
			return
		}
		return
	}
	a := sample()
	time.Sleep(200 * time.Millisecond)
	b := sample()
	dt := b.total - a.total
	if dt == 0 {
		return 0
	}
	return 100.0 * float64(dt-(b.idle-a.idle)) / float64(dt)
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

// startStatsCollector samples every running container once per minute and
// inserts a row into service_stats for historical analysis.
// Every hour it performs a monthly rollup: raw stats older than 31 days are
// first summarised into service_stats_monthly (hourly averages per calendar
// month) then deleted from the raw table, so storage stays bounded while
// history is preserved indefinitely.
func startStatsCollector(db *sql.DB) {
	collect := func() {
		rows, err := db.Query(`SELECT id FROM services WHERE status='running'`)
		if err != nil {
			return
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if rows.Scan(&id) == nil {
				ids = append(ids, id)
			}
		}
		rows.Close()

		for _, svcID := range ids {
			cName := fmt.Sprintf("fd-svc-%d", svcID)
			ev := handler.CollectContainerStats(cName)
			if ev.Status != "running" {
				continue
			}
			db.Exec( //nolint
				`INSERT INTO service_stats
				 (service_id, cpu_pct, mem_used, mem_total, mem_pct, net_in, net_out, blk_in, blk_out, pids)
				 VALUES (?,?,?,?,?,?,?,?,?,?)`,
				svcID, ev.CPUPct, ev.MemUsed, ev.MemTotal, ev.MemPct,
				ev.NetIn, ev.NetOut, ev.BlkIn, ev.BlkOut, ev.PIDs)
		}
	}

	rollupAndPrune := func() {
		// Roll up the previous calendar month into service_stats_monthly.
		// Uses INSERT OR IGNORE so re-runs after a crash are idempotent.
		now := time.Now().UTC()
		prev := time.Date(now.Year(), now.Month()-1, 1, 0, 0, 0, 0, time.UTC)
		prevYear := prev.Year()
		prevMonth := int(prev.Month())
		db.Exec(`
			INSERT OR IGNORE INTO service_stats_monthly
			    (service_id, year, month, hour,
			     cpu_avg, mem_avg, net_in_avg, net_out_avg, blk_in_avg, blk_out_avg, samples)
			SELECT
			    service_id,
			    CAST(? AS INTEGER),
			    CAST(? AS INTEGER),
			    CAST(strftime('%H', recorded_at) AS INTEGER),
			    AVG(cpu_pct), AVG(mem_pct),
			    AVG(net_in),  AVG(net_out),
			    AVG(blk_in),  AVG(blk_out),
			    COUNT(*)
			FROM service_stats
			WHERE strftime('%Y', recorded_at) = ? AND strftime('%m', recorded_at) = ?
			GROUP BY service_id, strftime('%H', recorded_at)
		`, prevYear, prevMonth,
			fmt.Sprintf("%04d", prevYear),
			fmt.Sprintf("%02d", prevMonth)) //nolint

		// Prune raw data older than 31 days.
		db.Exec(`DELETE FROM service_stats WHERE recorded_at < datetime('now', '-31 days')`) //nolint
		// Expire stale QR login tokens.
		db.Exec(`UPDATE qr_login_tokens SET status='expired' WHERE status='pending' AND qr_expires_at < datetime('now')`) //nolint
		slog.Debug("stats: monthly rollup + prune completed")
	}

	// Immediate first sample, then every 60 s
	collect()

	sampleTicker := time.NewTicker(60 * time.Second)
	pruneTicker := time.NewTicker(1 * time.Hour)
	defer sampleTicker.Stop()
	defer pruneTicker.Stop()

	for {
		select {
		case <-sampleTicker.C:
			collect()
		case <-pruneTicker.C:
			rollupAndPrune()
		}
	}
}
