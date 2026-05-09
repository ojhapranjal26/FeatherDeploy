package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
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
	"github.com/ojhapranjal26/featherdeploy/backend/internal/caddy"
	appDb "github.com/ojhapranjal26/featherdeploy/backend/internal/db"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/deploy"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/handler"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/heartbeat"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/installer"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/mailer"
	mw "github.com/ojhapranjal26/featherdeploy/backend/internal/middleware"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/model"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/netdaemon"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/pki"
	"github.com/ojhapranjal26/featherdeploy/backend/web"
)

const usage = `featherdeploy — self-hosted PaaS panel

Usage:
  featherdeploy                  Run the server (default)
  featherdeploy serve            Run the server (explicit)
  featherdeploy install          Interactive first-time setup wizard (Linux, root)
  featherdeploy update           Update an existing installation in-place (Linux, root)
  featherdeploy logs <name|id>   Show live container logs for a deployed service
                                   e.g.  featherdeploy logs my-app
                                         featherdeploy logs fd-svc-3
  featherdeploy podman <args...> Run any podman command as the service user
                                   e.g.  featherdeploy podman ps -a
                                         featherdeploy podman logs fd-svc-1 -f
  featherdeploy --help           Show this help

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

// svcUser returns the service OS user from the systemd unit, defaulting to "featherdeploy".
func svcUserFromUnit() string {
	data, err := os.ReadFile("/etc/systemd/system/featherdeploy.service")
	if err != nil {
		return "featherdeploy"
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "User=") {
			return strings.TrimPrefix(strings.TrimSpace(line), "User=")
		}
	}
	return "featherdeploy"
}

// runAsSvcUser execs a command as the featherdeploy service user using sudo.
// This allows root (or any user with sudo rights) to operate on rootless
// Podman containers that were created under the service account.
func runAsSvcUser(name string, args ...string) {
	svcUser := svcUserFromUnit()
	// Build: sudo -u <svcUser> -H <name> <args...>
	// -H sets HOME to the service user's home dir so Podman finds its storage.
	full := append([]string{"-u", svcUser, "-H", name}, args...)
	cmd := exec.Command("sudo", full...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// Set XDG_RUNTIME_DIR so rootless Podman works correctly.
	uidOut, _ := exec.Command("id", "-u", svcUser).Output()
	uid := strings.TrimSpace(string(uidOut))
	if uid != "" {
		cmd.Env = append(os.Environ(), "XDG_RUNTIME_DIR=/run/user/"+uid)
	}
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		os.Exit(1)
	}
}

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
		case "podman":
			// Pass-through: run podman as the service user.
			// Usage: featherdeploy podman <podman-args...>
			if len(os.Args) < 3 {
				fmt.Fprintln(os.Stderr, "usage: featherdeploy podman <args...>")
				os.Exit(1)
			}
			runAsSvcUser("podman", os.Args[2:]...)
			return
		case "logs":
			// Convenience: show logs for a deployed service container.
			// Usage: featherdeploy logs <name|id> [extra podman flags]
			//   name can be: my-app, fd-svc-3, or a numeric service id "3"
			if len(os.Args) < 3 {
				fmt.Fprintln(os.Stderr, "usage: featherdeploy logs <service-name-or-id> [podman-log-flags]")
				fmt.Fprintln(os.Stderr, "  e.g. featherdeploy logs my-app")
				fmt.Fprintln(os.Stderr, "       featherdeploy logs fd-svc-3")
				fmt.Fprintln(os.Stderr, "       featherdeploy logs 3 -f --tail 100")
				os.Exit(1)
			}
			target := os.Args[2]
			extra := os.Args[3:]
			// Normalise: "3" → "fd-svc-3", "my-app" → "my-app", "fd-svc-3" → unchanged
			if !strings.HasPrefix(target, "fd-svc-") {
				// Check if it's a plain number → treat as service ID
				allDigits := true
				for _, c := range target {
					if c < '0' || c > '9' {
						allDigits = false
						break
					}
				}
				if allDigits {
					target = "fd-svc-" + target
				}
				// Otherwise keep as-is (user might have named their container differently)
			}
			logArgs := append([]string{"logs", "--tail", "200", target}, extra...)
			runAsSvcUser("podman", logArgs...)
			return
		default:
			// Unknown subcommand — print helpful error instead of accidentally
			// starting a second server instance (which would conflict on port 8080).
			fmt.Fprintf(os.Stderr, "featherdeploy: unknown subcommand %q\n\n", os.Args[1])
			fmt.Fprint(os.Stderr, usage)
			os.Exit(1)
		}
	}

	serve()
}

func serve() {
	// ─── Logging ──────────────────────────────────────────────────────────────
	// Explicitly configure slog to write to stderr so systemd's
	// StandardError=journal reliably captures all log output in the journal.
	// Without this, the default handler routes through Go's log package whose
	// writer can differ across Go versions; writing directly to stderr is
	// unambiguous and always line-flushed by the OS.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

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

	// ─── FDNet: lightweight internal network proxy ─────────────────────────────
	// Replaces Podman named-bridge networks (netavark/aardvark-dns) with a
	// pure-Go TCP proxy that works on every Linux distribution.
	netDaemon := netdaemon.New("/var/lib/featherdeploy/fdnet-state.json")
	netDaemon.ReconcileRegistered()
	defer netDaemon.Stop()
	deploy.SetNetDaemon(netDaemon)
	slog.Info("fdnet: network daemon ready")

	// Sync cluster_port back to the DB for all services whose fdnet proxy was
	// restored from the state file.  This is required so Caddy's buildConfig
	// picks up the correct proxy port (via COALESCE(cluster_port, host_port))
	// after a server restart without waiting for the next deployment.
	go syncFdnetClusterPorts(db, netDaemon)


	// Re-sync database container states after a restart or update.
	// Must run after SetNetDaemon so that re-registered containers can be
	// proxied immediately.  Databases whose container is still running need
	// no intervention — fdnet already restored their TCP proxy above via
	// ReconcileRegistered().  Databases whose container stopped while we were
	// down are attempted a re-start here so services don't see a dead proxy.
	go reconcileDatabaseStates(db)

	// Re-apply iptables ACCEPT rules for databases marked as public.
	// iptables rules are in-memory and lost on every server restart/update,
	// so we need to restore them from the database state each time we start.
	go reconcilePublicDBIPTables(db)

	// Reload Caddy after every process restart so the domain→port mapping in
	// /etc/caddy/featherdeploy-services.caddy is always current with the DB.
	// Without this, if a domain was added while Caddy was running under the
	// previous process the new block would already be on disk; but if the Caddy
	// config was manually cleared or the file was recreated by build.sh the
	// reload here repairs it before any user traffic arrives.
	go caddy.Reload(db)

	// Periodic Caddy sync: re-generate and reload the services file every 60s.
	// This self-heals any situation where the initial reload failed (e.g. Caddy
	// started after featherdeploy, a transient sudo timeout, etc.).
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			caddy.Reload(db)
		}
	}()

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
	}, func(deadNodeID string) {
		slog.Info("failover: migrating services from dead node", "node_id", deadNodeID)
		rows, err := db.Query(`SELECT id FROM services WHERE node_id=?`, deadNodeID)
		if err != nil {
			return
		}
		defer rows.Close()
		for rows.Next() {
			var svcID int64
			if err := rows.Scan(&svcID); err == nil {
				// Trigger a new deployment with target_node_id='auto'
				res, err := db.Exec(`INSERT INTO deployments 
					(service_id, triggered_by, deploy_type, status, target_node_id, deploy_log, created_at)
					VALUES (?, 1, 'git', 'pending', 'auto', 'Failover: migrating from dead node', datetime('now'))`, svcID)
				if err == nil {
					depID, _ := res.LastInsertId()
					deploy.Enqueue(db, *jwtSecret, depID, svcID, 1)
				}
			}
		}
	})
	slog.Info("brain heartbeat started with failover monitor")

	// Start weekly mTLS certificate rotation
	startWeeklyCertRotation(context.Background(), db, *jwtSecret)

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
	dbH := handler.NewDatabaseHandler(db, *jwtSecret, domainFromOrigin(*origin))
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

	sessionsH := handler.NewSessionsHandler(db)
	storageH := handler.NewStorageHandler(db, *jwtSecret)

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
		AllowedHeaders:   []string{"Authorization", "Content-Type", "X-Request-Id", "X-Storage-Key"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	// ─── Public routes ────────────────────────────────────────────────────────
	r.Post("/api/auth/login", authH.Login)

	// Branding is public so the login page can show it before authentication
	r.Get("/api/settings/branding", settingsH.GetBranding)
	// Uploaded logo is public so it can be used in the login page and sidebar
	r.Get("/api/settings/branding/logo", settingsH.GetLogoImage)
	// Timezone is public so the app can apply it before the user logs in
	r.Get("/api/settings/timezone", settingsH.GetTimezone)

	// Invitation accept flow (public — token acts as credential)
	r.Get("/api/invitations/{token}", inviteH.Verify)
	r.Post("/api/invitations/{token}/accept", inviteH.Accept)

	// QR login: init and poll are public (token is the credential)
	r.Post("/api/auth/qr/init", qrH.Init)
	r.Get("/api/auth/qr/{token}/poll", qrH.Poll)

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

	// ── Storage Object API — service key auth (X-Storage-Key), no JWT required ───
	// Services (containers) call these endpoints using their per-service API key.
	// The admin management endpoints (/api/storages/*) still require JWT above.
	r.Get("/api/storage/{storageId}/list", storageH.ObjectList)
	r.Get("/api/storage/{storageId}/objects/*", storageH.ObjectGet)
	r.Put("/api/storage/{storageId}/objects/*", storageH.ObjectPut)
	r.Delete("/api/storage/{storageId}/objects/*", storageH.ObjectDelete)
	r.Post("/api/storage/{storageId}/multipart/init", storageH.MultipartInit)
	r.Put("/api/storage/{storageId}/multipart/{uploadId}/part/{partNumber}", storageH.MultipartUploadPart)
	r.Post("/api/storage/{storageId}/multipart/{uploadId}/complete", storageH.MultipartComplete)
	r.Delete("/api/storage/{storageId}/multipart/{uploadId}", storageH.MultipartAbort)

	// ─── Authenticated routes (with 30s request timeout) ─────────────────────────
	// SSE streaming routes are registered in a separate group below WITHOUT this
	// timeout so long-running builds don't get killed at 30s.
	r.Group(func(r chi.Router) {
		r.Use(mw.Authenticate(*jwtSecret, db))
		r.Use(chiMiddleware.Timeout(30 * time.Second))

		// Self
		r.Get("/api/auth/me", authH.Me)
		r.Post("/api/auth/logout", authH.Logout)

		// Sessions / device management
		r.Get("/api/auth/sessions", sessionsH.List)
		r.Delete("/api/auth/sessions/others", sessionsH.RevokeOthers)
		r.Delete("/api/auth/sessions/{sessionID}", sessionsH.Revoke)

		// ── System / version check (any authenticated user can query) ─────
		r.Get("/api/system/version", systemH.VersionCheck)
		// Self-update is superadmin only — one-click update from the dashboard.
		r.Group(func(r chi.Router) {
			r.Use(mw.RequireRole(model.RoleSuperAdmin))
			r.Post("/api/system/update", systemH.TriggerUpdate)
		})

		// ── Dashboard ──────────────────────────────────────────────────────
		r.Get("/api/dashboard", dashH.Stats)

		// ── User lookup / search (any authenticated user — used for member invites) ─
		r.Get("/api/users/lookup", userH.Lookup)
		r.Get("/api/users/search", userH.Search)

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

		// ── Storages (admin + superadmin) ────────────────────────────────
		r.Group(func(r chi.Router) {
			r.Use(mw.RequireRole(model.RoleSuperAdmin, model.RoleAdmin))
			// Bucket management
			r.Get("/api/storages", storageH.List)
			r.Post("/api/storages", storageH.Create)
			r.Get("/api/storages/{storageId}", storageH.Get)
			r.Delete("/api/storages/{storageId}", storageH.Delete)
			// Service access management
			r.Get("/api/storages/{storageId}/access", storageH.ListAccess)
			r.Post("/api/storages/{storageId}/access", storageH.GrantAccess)
			r.Patch("/api/storages/{storageId}/access/{serviceId}", storageH.UpdateAccess)
			r.Delete("/api/storages/{storageId}/access/{serviceId}", storageH.RevokeAccess)
			r.Post("/api/storages/{storageId}/access/{serviceId}/rotate-key", storageH.RotateServiceKey)
			// Admin file browser & stats
			r.Get("/api/storages/{storageId}/browse", storageH.Browse)
			r.Delete("/api/storages/{storageId}/objects", storageH.AdminDeleteObject)
			r.Get("/api/storages/{storageId}/stats", storageH.Stats)
		})

		// ── Superadmin: platform settings ─────────────────────────────────
		r.Group(func(r chi.Router) {
			r.Use(mw.RequireRole(model.RoleSuperAdmin))
			// Branding
			r.Put("/api/settings/branding", settingsH.SetBranding)
			r.Post("/api/settings/branding/logo", settingsH.UploadLogo)
			// Global timezone
			r.Put("/api/settings/timezone", settingsH.SetTimezone)
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
			r.Post("/api/nodes/{nodeID}/token", nodeH.RegenerateToken)
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
				r.Post("/api/projects/{projectID}/services/{serviceID}/env/bulk", envH.BulkUpsert)
				r.Delete("/api/projects/{projectID}/services/{serviceID}/env/{key}", envH.Delete)
				r.Get("/api/projects/{projectID}/services/{serviceID}/env/{key}/reveal", envH.Reveal)
				// ── Container live stats SSE and Domains ─────────────────
				// NOTE: /stats/stream is an SSE route; registered in no-timeout group below.

				// ── Historical stats ──────────────────────────────────────
				r.Get("/api/projects/{projectID}/services/{serviceID}/stats/history", statsHistH.History)
				r.Get("/api/projects/{projectID}/services/{serviceID}/stats/monthly", statsHistH.MonthlyHistory)

				// ── Databases ────────────────────────────────────────────
				r.Get("/api/projects/{projectID}/databases", dbH.List)
				r.Post("/api/projects/{projectID}/databases", dbH.Create)
				r.Get("/api/projects/{projectID}/databases/{databaseID}", dbH.Get)
				r.Put("/api/projects/{projectID}/databases/{databaseID}", dbH.Update)
				r.Get("/api/projects/{projectID}/databases/{databaseID}/logs", dbH.GetLogs)

				r.Get("/api/projects/{projectID}/databases/{databaseID}/backup", dbH.Backup)
				r.Post("/api/projects/{projectID}/databases/{databaseID}/restore", dbH.Restore)
				r.Delete("/api/projects/{projectID}/databases/{databaseID}", dbH.Delete)
				r.Post("/api/projects/{projectID}/databases/{databaseID}/start", dbH.Start)
				r.Post("/api/projects/{projectID}/databases/{databaseID}/restart", dbH.Restart)
				r.Post("/api/projects/{projectID}/databases/{databaseID}/stop", dbH.Stop)
				r.Post("/api/projects/{projectID}/databases/{databaseID}/password", dbH.ChangePassword)
				r.Post("/api/projects/{projectID}/databases/{databaseID}/public", dbH.TogglePublic)

				// ── QR approve (authenticated) ────────────────────────────
				r.Post("/api/auth/qr/{token}/approve", qrH.Approve)

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
		r.Use(mw.Authenticate(*jwtSecret, db))
		// Global node stats stream
		r.Get("/api/stats/stream", statsH.Stream)
		// Per-service SSE streams (require at least editor project access)
		r.With(mw.RequireProjectAccess(db, "editor")).
			Get("/api/projects/{projectID}/services/{serviceID}/deployments/{deploymentID}/logs", depH.Logs)
		r.With(mw.RequireProjectAccess(db, "editor")).
			Get("/api/projects/{projectID}/services/{serviceID}/container-logs", depH.ContainerLogs)
		r.With(mw.RequireProjectAccess(db, "editor")).
			Get("/api/projects/{projectID}/services/{serviceID}/stats/stream", containerStatsH.Stream)
		// Database container stats stream (no timeout — long-lived SSE)
		r.With(mw.RequireProjectAccess(db, "editor")).
			Get("/api/projects/{projectID}/databases/{databaseID}/stats/stream", dbH.StatsStream)
		// Database startup log stream (no timeout — long-lived SSE)
		r.With(mw.RequireProjectAccess(db, "editor")).
			Get("/api/projects/{projectID}/databases/{databaseID}/start-log/stream", dbH.StartLogStream)
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
		out, inspErr := deploy.PodmanCmd("inspect",
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
			if startOut, startErr := deploy.PodmanCmd("start", cName).CombinedOutput(); startErr == nil {
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

// syncFdnetClusterPorts writes the fdnet clusterPort for each active service
// back to services.cluster_port in the DB.
//
// On a fresh deployment runner.go already does this immediately after fdnet.Register().
// This function handles the restart case: when the server restarts, fdnet loads
// its state file and ReconcileRegistered() restores the TCP proxy goroutines — but
// the cluster_port column in the DB may be NULL for services deployed before the
// column was added, or for services that haven't been redeployed since the fix.
//
// Without this, Caddy's COALESCE(cluster_port, host_port) would fall back to
// host_port (the slirp4netns port-forward), which may not work for services
// beyond the first.
func syncFdnetClusterPorts(db *sql.DB, netD *netdaemon.Daemon) {
	// Read all running services and their project/name so we can look them up
	// in the fdnet registry.
	rows, err := db.Query(
		`SELECT id, project_id, name FROM services WHERE status = 'running'`)
	if err != nil {
		slog.Warn("syncFdnetClusterPorts: query failed", "err", err)
		return
	}
	defer rows.Close()

	type srow struct {
		id        int64
		projectID int64
		name      string
	}
	var svcs []srow
	for rows.Next() {
		var s srow
		if rows.Scan(&s.id, &s.projectID, &s.name) == nil {
			svcs = append(svcs, s)
		}
	}
	rows.Close()

	updated := 0
	for _, s := range svcs {
		cp, ok := netD.Resolve(s.projectID, s.name)
		if !ok || cp == 0 {
			continue // not registered in fdnet — will be fixed on next deploy
		}
		res, err := db.Exec(
			`UPDATE services SET cluster_port=? WHERE id=? AND (cluster_port IS NULL OR cluster_port != ?)`,
			cp, s.id, cp)
		if err == nil {
			if n, _ := res.RowsAffected(); n > 0 {
				updated++
				slog.Info("syncFdnetClusterPorts: updated cluster_port",
					"svc_id", s.id, "name", s.name, "cluster_port", cp)
			}
		}
	}
	if updated > 0 {
		slog.Info("syncFdnetClusterPorts: cluster_port synced", "services", updated)
	}
}

// reconcileDatabaseStates is called once at startup (after SetNetDaemon) in a
// goroutine.  It synchronises the databases.status column with the real Podman
// container state and attempts to restart containers that stopped while the
// previous process was down.
//
// Zero-downtime update path:
//   - Running containers survive the binary restart untouched.
//   - fdnet already restored TCP proxies via ReconcileRegistered() (state file).
//   - This function only corrects stale status rows and tries to revive
//     containers that died while featherdeploy was offline.
func reconcileDatabaseStates(db *sql.DB) {
	rows, err := db.Query(
		`SELECT id, db_type FROM databases WHERE status IN ('running','starting','error')`)
	if err != nil {
		slog.Warn("startup reconcile databases: query", "err", err)
		return
	}
	defer rows.Close()

	type dbRow struct {
		id     int64
		dbType string
	}
	var entries []dbRow
	for rows.Next() {
		var e dbRow
		if rows.Scan(&e.id, &e.dbType) == nil {
			entries = append(entries, e)
		}
	}
	rows.Close()

	for _, e := range entries {
		if e.dbType == "sqlite" {
			// SQLite has no container — it is always "running" as long as its
			// named volume exists (which it does unless the user explicitly deleted it).
			db.Exec( //nolint
				`UPDATE databases SET status='running', updated_at=datetime('now') WHERE id=?`, e.id)
			slog.Info("startup reconcile: sqlite database marked running", "db_id", e.id)
			continue
		}

		cName := fmt.Sprintf("fd-db-%d", e.id)
		out, inspErr := deploy.PodmanCmd("inspect",
			"--format", "{{.State.Status}}", cName).Output()
		if inspErr != nil {
			// Container doesn't exist.
			db.Exec( //nolint
				`UPDATE databases SET status='error', updated_at=datetime('now') WHERE id=?`, e.id)
			slog.Info("startup reconcile: database container missing", "db_id", e.id)
			continue
		}

		state := strings.TrimSpace(string(out))
		wantedStatus := "error"
		if state == "running" {
			wantedStatus = "running"
		} else if state == "exited" || state == "stopped" || state == "created" {
			// Container is stopped — try to bring it back before marking error.
			if startOut, startErr := deploy.PodmanCmd("start", cName).CombinedOutput(); startErr == nil {
				wantedStatus = "running"
				slog.Info("startup reconcile: restarted stopped database container", "db_id", e.id)
			} else {
				slog.Warn("startup reconcile: could not restart database container",
					"db_id", e.id, "output", strings.TrimSpace(string(startOut)))
			}
		}
		db.Exec( //nolint
			`UPDATE databases SET status=?, updated_at=datetime('now') WHERE id=?`,
			wantedStatus, e.id)
		slog.Info("startup reconcile: synced database", "db_id", e.id, "container_state", state, "db_status", wantedStatus)
	}
}

// reconcilePublicDBIPTables re-applies iptables ACCEPT rules for all databases
// that have network_public=1. iptables rules live only in kernel memory and are
// wiped on every server restart or update, so we must restore them from the DB
// state each time the process starts.
func reconcilePublicDBIPTables(db *sql.DB) {
	ipt, err := exec.LookPath("iptables")
	if err != nil {
		return // iptables not available on this host
	}
	sudo, _ := exec.LookPath("sudo")
	iptCmd := func(args ...string) *exec.Cmd {
		if sudo != "" {
			return exec.Command(sudo, append([]string{ipt}, args...)...)
		}
		return exec.Command(ipt, args...)
	}

	rows, err := db.Query(
		`SELECT id, COALESCE(host_port, 0), COALESCE(cluster_port, 0)
		 FROM databases WHERE network_public=1 AND host_port IS NOT NULL`)
	if err != nil {
		slog.Warn("startup reconcile: query public databases", "err", err)
		return
	}
	defer rows.Close()

	restored := 0
	for rows.Next() {
		var dbID int64
		var hostPort, clusterPort int
		if rows.Scan(&dbID, &hostPort, &clusterPort) != nil || hostPort == 0 {
			continue
		}
		// Open both the rootlessport host port and the fdnet cluster port.
		// The cluster port has the most reliable 0.0.0.0 socket (Go net.Listen).
		openPorts := []int{hostPort}
		if clusterPort > 0 && clusterPort != hostPort {
			openPorts = append(openPorts, clusterPort)
		}
		for _, p := range openPorts {
			portStr := strconv.Itoa(p)
			ruleSpec := []string{"-p", "tcp", "--dport", portStr,
				"-m", "comment", "--comment", fmt.Sprintf("featherdeploy db-%d public", dbID),
				"-j", "ACCEPT"}
			checkArgs := append([]string{"-C", "INPUT"}, ruleSpec...)
			if iptCmd(checkArgs...).Run() != nil {
				insertArgs := append([]string{"-I", "INPUT", "1"}, ruleSpec...)
				if out, runErr := iptCmd(insertArgs...).CombinedOutput(); runErr != nil {
					slog.Warn("startup reconcile: could not restore iptables rule for public DB",
						"db_id", dbID, "port", p, "err", runErr, "out", strings.TrimSpace(string(out)))
				} else {
					slog.Info("startup reconcile: restored iptables ACCEPT for public DB",
						"db_id", dbID, "port", p)
					restored++
				}
			}
			// Also ensure UFW allows the port if UFW is active.
			reconcileUFWRule(true, portStr)
		}
	}
	if restored > 0 {
		slog.Info("startup reconcile: iptables rules for public DBs restored", "count", restored)
	}
}

// reconcileUFWRule adds or removes a UFW allow rule for a TCP port when UFW
// is active.  Unlike raw iptables rules, UFW rules are persistent across
// reboots so we only need to add them once, but checking idempotently on
// every startup is harmless.
func reconcileUFWRule(allow bool, port string) {
	ufw, err := exec.LookPath("ufw")
	if err != nil {
		return
	}
	sudo, _ := exec.LookPath("sudo")
	// ufw status requires root — use sudo so the service user gets the real status.
	var statusOut []byte
	if sudo != "" {
		statusOut, _ = exec.Command(sudo, ufw, "status").Output()
	} else {
		statusOut, _ = exec.Command(ufw, "status").Output()
	}
	if !strings.Contains(string(statusOut), "Status: active") {
		return
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
	} else {
		run("delete", "allow", port+"/tcp")
		run("deny", port+"/tcp")
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

func startWeeklyCertRotation(ctx context.Context, db *sql.DB, jwtSecret string) {
	go func() {
		// Initial check on startup
		rotateCerts(db, jwtSecret)

		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				rotateCerts(db, jwtSecret)
			case <-ctx.Done():
				return
			}
		}
	}()
}

func rotateCerts(db *sql.DB, jwtSecret string) {
	// Find nodes needing rotation (rotated more than 7 days ago, or never rotated)
	rows, err := db.Query(`SELECT id, name, ip, port FROM nodes WHERE status='connected' AND (last_rotated_at IS NULL OR last_rotated_at <= datetime('now', '-7 days'))`)
	if err != nil {
		return
	}
	defer rows.Close()

	var caCertPEM, encCAKeyPEM string
	err = db.QueryRow(`SELECT cert_pem, key_pem FROM pki_ca WHERE id=1`).Scan(&caCertPEM, &encCAKeyPEM)
	if err != nil {
		return
	}
	caKeyPEM, _ := pki.DecryptKey(encCAKeyPEM, jwtSecret)
	ca := &pki.CA{CertPEM: caCertPEM, KeyPEM: caKeyPEM}

	for rows.Next() {
		var id int64
		var name, ip string
		var port int
		if err := rows.Scan(&id, &name, &ip, &port); err != nil {
			continue
		}

		slog.Info("pki: rotating certificate for node", "name", name, "ip", ip)
		nodeCert, err := pki.SignNodeCert(ca, name, ip)
		if err != nil {
			slog.Error("pki: failed to sign new cert", "node", name, "err", err)
			continue
		}

		if err := pushCertToNode(ip, port, nodeCert, caCertPEM); err != nil {
			slog.Error("pki: failed to push cert to node", "node", name, "err", err)
			continue
		}

		// Update last_rotated_at
		db.Exec(`UPDATE nodes SET last_rotated_at=datetime('now'), node_cert_pem=? WHERE id=?`, nodeCert.CertPEM, id)
	}
}

func pushCertToNode(ip string, port int, cert *pki.NodeCert, caPEM string) error {
	payload := map[string]string{
		"cert_pem": cert.CertPEM,
		"key_pem":  cert.KeyPEM,
		"ca_pem":   caPEM,
	}
	body, _ := json.Marshal(payload)

	// Build mTLS client using existing certs
	caFile, _ := os.ReadFile("/etc/featherdeploy/ca.crt")
	certFile, _ := os.ReadFile("/etc/featherdeploy/node.crt")
	keyFile, _ := os.ReadFile("/etc/featherdeploy/node.key")

	tlsCfg, err := pki.TLSConfig(string(certFile), string(keyFile), string(caFile))
	if err != nil {
		return err
	}
	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
		Timeout:   10 * time.Second,
	}

	url := fmt.Sprintf("https://%s:%d/api/node/rotate-cert", ip, port)
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}
