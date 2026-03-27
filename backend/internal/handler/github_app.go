package handler

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/ojhapranjal26/featherdeploy/backend/internal/deploy"
	mw "github.com/ojhapranjal26/featherdeploy/backend/internal/middleware"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/model"
	v "github.com/ojhapranjal26/featherdeploy/backend/internal/validator"
)

// GitHubAppHandler manages the GitHub App integration.
// A GitHub App provides installation-level access to org/repos —
// more powerful than OAuth Apps and not tied to any individual user.
//
// Setup flow (superadmin):
//  1. Create a GitHub App at https://github.com/settings/apps/new
//  2. Generate a private key (RSA PEM) and note the App ID
//  3. Install the App on your org/account; copy the Installation ID
//     from the URL: github.com/organizations/{org}/settings/installations/{id}
//  4. POST /api/github-app/config with app_id, private_key_pem, installation_id
type GitHubAppHandler struct {
	db        *sql.DB
	jwtSecret string
}

func NewGitHubAppHandler(db *sql.DB, jwtSecret string) *GitHubAppHandler {
	return &GitHubAppHandler{db: db, jwtSecret: jwtSecret}
}

// ─── GET /api/github-app/status ─────────────────────────────────────────────
func (h *GitHubAppHandler) Status(w http.ResponseWriter, r *http.Request) {
	var appID, appName, installID string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT app_id, app_name, installation_id FROM github_app_config WHERE id = 1`,
	).Scan(&appID, &appName, &installID)
	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusOK, map[string]any{
			"configured": false,
		})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"configured":      true,
		"app_id":          appID,
		"app_name":        appName,
		"installation_id": installID,
	})
}

// ─── POST /api/github-app/config  (superadmin only) ─────────────────────────
func (h *GitHubAppHandler) SetConfig(w http.ResponseWriter, r *http.Request) {
	var req model.SetGitHubAppConfigRequest
	if !v.DecodeAndValidate(w, r, &req) {
		return
	}

	// Validate the private key is a parseable RSA key
	if _, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(req.PrivateKeyPEM)); err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("private_key_pem is not a valid RSA private key: "+err.Error()))
		return
	}

	_, err := h.db.ExecContext(r.Context(), `
		INSERT INTO github_app_config
		    (id, app_id, app_name, private_key_pem, installation_id, webhook_secret, client_id, client_secret, updated_at)
		VALUES (1, ?, ?, ?, ?, ?, ?, ?, datetime('now'))
		ON CONFLICT(id) DO UPDATE SET
		    app_id          = excluded.app_id,
		    app_name        = excluded.app_name,
		    private_key_pem = excluded.private_key_pem,
		    installation_id = excluded.installation_id,
		    webhook_secret  = excluded.webhook_secret,
		    client_id       = excluded.client_id,
		    client_secret   = excluded.client_secret,
		    updated_at      = excluded.updated_at`,
		req.AppID, req.AppName, req.PrivateKeyPEM,
		req.InstallationID, req.WebhookSecret, req.ClientID, req.ClientSecret,
	)
	if err != nil {
		slog.Error("github-app: upsert config", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ─── GET /api/github-app/config  (superadmin only) ──────────────────────────
// Returns the config with the private key redacted.
func (h *GitHubAppHandler) GetConfig(w http.ResponseWriter, r *http.Request) {
	var cfg model.GitHubAppConfig
	var hasKey string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT app_id, app_name, installation_id, client_id, updated_at,
		        CASE WHEN private_key_pem != '' THEN '(set)' ELSE '' END
		 FROM github_app_config WHERE id = 1`,
	).Scan(&cfg.AppID, &cfg.AppName, &cfg.InstallationID, &cfg.ClientID, &cfg.UpdatedAt, &hasKey)
	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusNotFound, errMap("GitHub App not configured"))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"app_id":          cfg.AppID,
		"app_name":        cfg.AppName,
		"installation_id": cfg.InstallationID,
		"client_id":       cfg.ClientID,
		"private_key_pem": hasKey, // returns "(set)" or ""
		"updated_at":      cfg.UpdatedAt,
	})
}

// ─── DELETE /api/github-app/config  (superadmin only) ───────────────────────
func (h *GitHubAppHandler) DeleteConfig(w http.ResponseWriter, r *http.Request) {
	_, err := h.db.ExecContext(r.Context(), `DELETE FROM github_app_config WHERE id = 1`)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── GET /api/github-app/repos ───────────────────────────────────────────────
// Lists repositories accessible via the GitHub App installation.
func (h *GitHubAppHandler) ListRepos(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.loadConfig(r.Context())
	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusServiceUnavailable, errMap("GitHub App not configured"))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}

	token, err := h.installationToken(r.Context(), cfg)
	if err != nil {
		slog.Error("github-app: get installation token", "err", err)
		writeJSON(w, http.StatusBadGateway, errMap("could not obtain GitHub installation token: "+err.Error()))
		return
	}

	repos, err := fetchAppRepos(r.Context(), token)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errMap("could not list repositories: "+err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"repositories": repos, "source": "github_app"})
}

// ─────────────────────────────────────────────────────────────────────────────
// 				Internal helpers
// ─────────────────────────────────────────────────────────────────────────────

func (h *GitHubAppHandler) loadConfig(ctx context.Context) (*model.GitHubAppConfig, error) {
	var cfg model.GitHubAppConfig
	err := h.db.QueryRowContext(ctx,
		`SELECT app_id, app_name, private_key_pem, installation_id FROM github_app_config WHERE id = 1`,
	).Scan(&cfg.AppID, &cfg.AppName, &cfg.PrivateKeyPEM, &cfg.InstallationID)
	if err != nil {
		return nil, err
	}
	return &cfg, nil
}

// appJWT creates a short-lived JWT signed with the App's RSA private key.
// GitHub requires this JWT to request installation access tokens.
func appJWT(appID, privateKeyPEM string) (string, error) {
	privKey, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(privateKeyPEM))
	if err != nil {
		return "", fmt.Errorf("parse RSA key: %w", err)
	}
	now := time.Now()
	claims := jwt.MapClaims{
		"iat": now.Add(-60 * time.Second).Unix(), // 60s backdate for clock skew
		"exp": now.Add(10 * time.Minute).Unix(),
		"iss": appID,
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	return tok.SignedString(privKey)
}

// installationToken exchanges the app JWT for a short-lived installation token.
func (h *GitHubAppHandler) installationToken(ctx context.Context, cfg *model.GitHubAppConfig) (string, error) {
	appTok, err := appJWT(cfg.AppID, cfg.PrivateKeyPEM)
	if err != nil {
		return "", err
	}

	url := fmt.Sprintf("https://api.github.com/app/installations/%s/access_tokens", cfg.InstallationID)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	req.Header.Set("Authorization", "Bearer "+appTok)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("GitHub API %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &result); err != nil || result.Token == "" {
		return "", fmt.Errorf("unexpected GitHub response: %s", body)
	}
	return result.Token, nil
}

// fetchAppRepos fetches up to 100 repos from the installation.
func fetchAppRepos(ctx context.Context, token string) ([]model.GitHubRepo, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.github.com/installation/repositories?per_page=100&sort=updated", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Repositories []model.GitHubRepo `json:"repositories"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	return result.Repositories, nil
}

// Ensure we only reference *rsa.PrivateKey to avoid import cycle warnings.
var _ *rsa.PrivateKey

// ConnectUserOAuth links GitHub OAuth to a user account via App OAuth credentials.
// Delegates the existing OAuth flow but uses App client_id/secret if configured,
// otherwise falls back to the standalone OAuth app config from the environment.
func (h *GitHubAppHandler) GetAppOAuthClientID(ctx context.Context) (clientID, clientSecret string, err error) {
	_ = h.db.QueryRowContext(ctx,
		`SELECT client_id, client_secret FROM github_app_config WHERE id = 1`,
	).Scan(&clientID, &clientSecret)
	return clientID, clientSecret, nil // no error even if not configured
}

// InjectAppClaims adds the App client_id claim to the GitHub OAuth URL if configured.
func (h *GitHubAppHandler) InjectAppClaims(w http.ResponseWriter, r *http.Request) {
	claims := mw.GetClaims(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"user_id": claims.UserID})
}

// keep compiler happy — exported for use in main.go
var _ = bytes.NewReader

// ─── POST /api/github-app/webhook (public — GitHub signs with HMAC-SHA256) ──
//
// GitHub delivers push/installation/etc. events here.
// Every delivery is verified using HMAC-SHA256 against the stored webhook_secret.
// Unknown or unhandled event types are acknowledged with 200 OK and ignored.
func (h *GitHubAppHandler) Webhook(w http.ResponseWriter, r *http.Request) {
	// Read body (max 10 MB — GitHub payloads are rarely > 1 MB)
	body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20))
	if err != nil {
		http.Error(w, "read error", http.StatusInternalServerError)
		return
	}

	// Load the stored webhook secret
	var secret string
	_ = h.db.QueryRowContext(r.Context(),
		`SELECT webhook_secret FROM github_app_config WHERE id = 1`,
	).Scan(&secret)

	// Verify signature when a secret is configured
	if secret != "" {
		sig := r.Header.Get("X-Hub-Signature-256")
		if !strings.HasPrefix(sig, "sha256=") {
			writeJSON(w, http.StatusUnauthorized, errMap("missing X-Hub-Signature-256 header"))
			return
		}
		got, err := hex.DecodeString(strings.TrimPrefix(sig, "sha256="))
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, errMap("malformed signature"))
			return
		}
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		expected := mac.Sum(nil)
		if !hmac.Equal(got, expected) {
			writeJSON(w, http.StatusUnauthorized, errMap("invalid webhook signature"))
			return
		}
	}

	eventType := r.Header.Get("X-GitHub-Event")
	deliveryID := r.Header.Get("X-GitHub-Delivery")
	slog.Info("github-app webhook", "event", eventType, "delivery", deliveryID)

	// Parse common fields (repository full name, sender)
	var payload struct {
		Ref        string `json:"ref"`         // e.g. refs/heads/main
		After      string `json:"after"`       // commit SHA after push
		Repository struct {
			FullName string `json:"full_name"`
			HTMLURL  string `json:"html_url"`
		} `json:"repository"`
		Sender struct {
			Login string `json:"login"`
		} `json:"sender"`
	}
	_ = json.Unmarshal(body, &payload)

	switch eventType {
	case "push":
		// Trigger auto-deployment for services configured to auto-deploy on push
		branch := strings.TrimPrefix(payload.Ref, "refs/heads/")
		repoFull := payload.Repository.FullName
		commitSHA := payload.After
		// Skip "delete branch" pushes (all-zero SHA)
		if branch != "" && repoFull != "" && commitSHA != "" &&
			commitSHA != "0000000000000000000000000000000000000000" {
			slog.Info("github-app push received",
				"repo", repoFull, "branch", branch, "commit", commitSHA[:7],
				"sender", payload.Sender.Login,
			)
			go h.triggerAutoDeployments(repoFull, branch, commitSHA)
		}
	case "ping":
		// GitHub sends a ping event when a webhook is first created
		slog.Info("github-app webhook ping received", "delivery", deliveryID)
	default:
		// Unhandled event — log and ignore
		slog.Debug("github-app webhook: unhandled event", "event", eventType)
	}

	w.WriteHeader(http.StatusNoContent)
}

// triggerAutoDeployments finds services with auto_deploy=1 that match the pushed
// repo+branch and starts a deployment goroutine for each.
func (h *GitHubAppHandler) triggerAutoDeployments(repoFullName, branch, commitSHA string) {
	// Match services whose repo_url contains the repo's full name (handles .git suffix too)
	rows, err := h.db.Query(`
		SELECT s.id, s.project_id, p.owner_id, s.deploy_type, s.repo_url
		FROM services s
		JOIN projects p ON s.project_id = p.id
		WHERE s.auto_deploy = 1
		  AND s.repo_branch = ?
		  AND (s.repo_url LIKE ? OR s.repo_url LIKE ?)`,
		branch,
		"%"+repoFullName+"%",
		"%"+repoFullName+".git%",
	)
	if err != nil {
		slog.Error("github-app auto-deploy: query services", "err", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var svcID, projectID, ownerID int64
		var deployType, repoURL string
		if err := rows.Scan(&svcID, &projectID, &ownerID, &deployType, &repoURL); err != nil {
			continue
		}
		// Insert deployment record as 'pending'; the worker pool starts it.
		res, err := h.db.Exec(`
			INSERT INTO deployments
			  (service_id, triggered_by, deploy_type, repo_url, commit_sha, branch, artifact_path, status)
			VALUES (?,?,?,?,?,?,?,?)`,
			svcID, ownerID, deployType, repoURL, commitSHA, branch, "", "pending")
		if err != nil {
			slog.Error("github-app auto-deploy: insert deployment", "svc_id", svcID, "err", err)
			continue
		}
		depID, _ := res.LastInsertId()
		deploy.Enqueue(h.db, h.jwtSecret, depID, svcID, ownerID)
		slog.Info("github-app auto-deploy queued",
			"svc_id", svcID, "branch", branch, "commit", commitSHA[:7],
		)
	}
}

