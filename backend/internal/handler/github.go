package handler

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	mw "github.com/ojhapranjal26/featherdeploy/backend/internal/middleware"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/model"
)

// GitHubHandler manages GitHub OAuth integration.
// Users connect their GitHub account once; all subsequent repo API calls
// use the stored access token.
type GitHubHandler struct {
	db     *sql.DB
	config *ConfigStore
	origin string // frontend base URL for redirect after OAuth
}

func NewGitHubHandler(db *sql.DB, config *ConfigStore, origin string) *GitHubHandler {
	return &GitHubHandler{db: db, config: config, origin: origin}
}

// ─── GET /api/github/auth ────────────────────────────────────────────────────
// Returns the GitHub OAuth URL the frontend should redirect to.
func (h *GitHubHandler) AuthURL(w http.ResponseWriter, r *http.Request) {
	clientID, _ := h.config.GitHubOAuth(r.Context())
	if clientID == "" {
		writeJSON(w, http.StatusServiceUnavailable,
			map[string]string{"error": "GitHub integration not configured — set credentials in Admin Settings"})
		return
	}

	// Generate state to prevent CSRF
	state, err := randomState()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "state generation failed"})
		return
	}

	// Store state + userID in short-lived HTTP-only cookies.
	// The callback route is public (no Authorization header on browser redirects),
	// so we carry identity via a companion cookie set here while auth IS present.
	claims := mw.GetClaims(r.Context())
	http.SetCookie(w, &http.Cookie{
		Name:     "gh_oauth_state",
		Value:    state,
		Path:     "/api/github/callback",
		MaxAge:   300,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     "gh_oauth_uid",
		Value:    fmt.Sprintf("%d", claims.UserID),
		Path:     "/api/github/callback",
		MaxAge:   300,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	redirectURI := fmt.Sprintf("%s/api/github/callback", strings.TrimRight(h.origin, "/"))
	params := url.Values{
		"client_id":    {clientID},
		"redirect_uri": {redirectURI},
		"scope":        {"repo read:user"},
		"state":        {state},
	}
	authURL := "https://github.com/login/oauth/authorize?" + params.Encode()
	writeJSON(w, http.StatusOK, map[string]string{"url": authURL})
}

// ─── GET /api/github/callback ────────────────────────────────────────────────
// GitHub redirects here after user authorizes (public route — no Authorization header).
// User identity is carried via the gh_oauth_uid cookie set during /api/github/auth.
func (h *GitHubHandler) Callback(w http.ResponseWriter, r *http.Request) {
	// Validate CSRF state
	stateCookie, err := r.Cookie("gh_oauth_state")
	if err != nil || stateCookie.Value == "" {
		http.Redirect(w, r, h.origin+"/settings/github?error=state_missing", http.StatusFound)
		return
	}
	if r.URL.Query().Get("state") != stateCookie.Value {
		http.Redirect(w, r, h.origin+"/settings/github?error=state_mismatch", http.StatusFound)
		return
	}

	// Clear state + uid cookies
	for _, name := range []string{"gh_oauth_state", "gh_oauth_uid"} {
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", MaxAge: -1, Path: "/api/github/callback"})
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		http.Redirect(w, r, h.origin+"/settings/github?error=no_code", http.StatusFound)
		return
	}

	// Recover user identity from companion cookie (set during /api/github/auth while user was authenticated)
	uidCookie, err := r.Cookie("gh_oauth_uid")
	if err != nil || uidCookie.Value == "" {
		http.Redirect(w, r, h.origin+"/settings/github?error=not_logged_in", http.StatusFound)
		return
	}
	var userID int64
	if _, scanErr := fmt.Sscanf(uidCookie.Value, "%d", &userID); scanErr != nil || userID == 0 {
		http.Redirect(w, r, h.origin+"/settings/github?error=not_logged_in", http.StatusFound)
		return
	}

	// Exchange code for access token
	accessToken, ghLogin, err := h.exchangeCode(r, code)
	if err != nil {
		slog.Error("github oauth exchange", "err", err)
		http.Redirect(w, r, h.origin+"/settings/github?error=exchange_failed", http.StatusFound)
		return
	}

	// Store access token and github_login
	_, err = h.db.ExecContext(r.Context(),
		`UPDATE users SET github_access_token = ?, github_login = ?, updated_at = datetime('now') WHERE id = ?`,
		accessToken, ghLogin, userID,
	)
	if err != nil {
		slog.Error("store github token", "err", err)
		http.Redirect(w, r, h.origin+"/settings/github?error=store_failed", http.StatusFound)
		return
	}

	http.Redirect(w, r, h.origin+"/settings/github?connected=1", http.StatusFound)
}

// ─── DELETE /api/github/disconnect ───────────────────────────────────────────
// Remove stored GitHub access token for the current user
func (h *GitHubHandler) Disconnect(w http.ResponseWriter, r *http.Request) {
	claims := mw.GetClaims(r.Context())
	_, err := h.db.ExecContext(r.Context(),
		`UPDATE users SET github_access_token = '', github_login = '', updated_at = datetime('now') WHERE id = ?`,
		claims.UserID,
	)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to disconnect"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "disconnected"})
}

// errGitHubTokenExpired is returned by GitHub API helpers when the stored token
// is no longer valid (expired, revoked, or permissions removed).
var errGitHubTokenExpired = fmt.Errorf("github_token_expired")

// clearUserGitHubToken wipes the stored OAuth token for the given user.
func (h *GitHubHandler) clearUserGitHubToken(ctx context.Context, userID int64) {
	h.db.ExecContext(ctx, //nolint
		`UPDATE users SET github_access_token='', github_login='', updated_at=datetime('now') WHERE id=?`,
		userID)
}

// ─── GET /api/github/repos ───────────────────────────────────────────────────
// List the authenticated user's GitHub repositories (requires connected GitHub)
func (h *GitHubHandler) ListRepos(w http.ResponseWriter, r *http.Request) {
	claims := mw.GetClaims(r.Context())

	var accessToken string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT github_access_token FROM users WHERE id = ?`, claims.UserID,
	).Scan(&accessToken)
	if err != nil || accessToken == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "GitHub account not connected"})
		return
	}

	// Fetch repos from GitHub API
	repos, err := h.fetchRepos(accessToken)
	if err != nil {
		if err == errGitHubTokenExpired {
			// Token is stale — auto-clear it so the frontend reflects "not connected"
			h.clearUserGitHubToken(r.Context(), claims.UserID)
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "github_token_expired"})
			return
		}
		slog.Error("github list repos", "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to fetch repositories from GitHub"})
		return
	}

	writeJSON(w, http.StatusOK, repos)
}

// ─── GET /api/github/status ──────────────────────────────────────────────────
// Returns whether the current user has a GitHub account connected.
// Also validates the stored token against the GitHub API so stale tokens are
// detected and cleared automatically (instead of the user noticing failures
// and having to manually disconnect+reconnect).
func (h *GitHubHandler) Status(w http.ResponseWriter, r *http.Request) {
	claims := mw.GetClaims(r.Context())

	var accessToken, ghLogin string
	_ = h.db.QueryRowContext(r.Context(),
		`SELECT github_access_token, github_login FROM users WHERE id = ?`, claims.UserID,
	).Scan(&accessToken, &ghLogin)

	if accessToken != "" {
		// Validate the token is still live. If GitHub returns 401/403 the token
		// was revoked or expired; auto-clear it so the frontend shows "not connected".
		if !h.isTokenValid(r.Context(), accessToken) {
			slog.Warn("github token expired or revoked — auto-clearing", "user_id", claims.UserID)
			h.clearUserGitHubToken(r.Context(), claims.UserID)
			accessToken = ""
			ghLogin = ""
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"connected":    accessToken != "",
		"github_login": ghLogin,
		"configured":   func() bool { id, _ := h.config.GitHubOAuth(r.Context()); return id != "" }(),
	})
}

// ─── Internal helpers ─────────────────────────────────────────────────────────

func (h *GitHubHandler) exchangeCode(r *http.Request, code string) (accessToken, login string, err error) {
	clientID, clientSecret := h.config.GitHubOAuth(r.Context())
	redirectURI := fmt.Sprintf("%s/api/github/callback", strings.TrimRight(h.origin, "/"))
	body := url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"code":          {code},
		"redirect_uri":  {redirectURI},
	}

	req, _ := http.NewRequestWithContext(r.Context(), http.MethodPost,
		"https://github.com/login/oauth/access_token",
		strings.NewReader(body.Encode()))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("token exchange request: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", "", fmt.Errorf("decode token response: %w", err)
	}
	if result.Error != "" {
		return "", "", fmt.Errorf("github oauth error: %s", result.Error)
	}
	if result.AccessToken == "" {
		return "", "", fmt.Errorf("empty access token returned")
	}

	// Fetch github login
	login, err = h.fetchGitHubLogin(r.Context(), result.AccessToken)
	if err != nil {
		return "", "", err
	}

	return result.AccessToken, login, nil
}

func (h *GitHubHandler) fetchGitHubLogin(ctx context.Context, token string) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/user", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch github user: %w", err)
	}
	defer resp.Body.Close()

	var u struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return "", fmt.Errorf("decode github user: %w", err)
	}
	return u.Login, nil
}

func (h *GitHubHandler) fetchRepos(token string) ([]model.GitHubRepo, error) {
	req, _ := http.NewRequest(http.MethodGet,
		"https://api.github.com/user/repos?sort=updated&per_page=100&type=all", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github repos request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, errGitHubTokenExpired
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20)) // 4 MB max
	if err != nil {
		return nil, fmt.Errorf("read repos body: %w", err)
	}

	var repos []model.GitHubRepo
	if err := json.Unmarshal(body, &repos); err != nil {
		return nil, fmt.Errorf("decode repos: %w", err)
	}
	return repos, nil
}

// isTokenValid does a cheap GET /user call to confirm the token is still live.
func (h *GitHubHandler) isTokenValid(ctx context.Context, token string) bool {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/user", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		// Network error — don't clear the token, assume it's still valid
		return true
	}
	defer resp.Body.Close()
	// Consume body to allow connection reuse
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096)) //nolint
	return resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden
}

func randomState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(b), nil
}

// ─── GET /api/github/repos/{owner}/{repo}/branches ───────────────────────────
// Lists branches for owner/repo using the current user's stored access token.
func (h *GitHubHandler) ListBranches(w http.ResponseWriter, r *http.Request) {
	claims := mw.GetClaims(r.Context())
	owner := r.PathValue("owner")
	repo := r.PathValue("repo")
	if owner == "" || repo == "" {
		writeJSON(w, http.StatusBadRequest, errMap("owner and repo are required"))
		return
	}

	token, err := h.userToken(r, claims.UserID)
	if err != nil || token == "" {
		writeJSON(w, http.StatusBadRequest, errMap("GitHub account not connected"))
		return
	}

	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/branches?per_page=100", owner, repo)
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, apiURL, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errMap("failed to fetch branches"))
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		h.clearUserGitHubToken(r.Context(), claims.UserID)
		writeJSON(w, http.StatusUnauthorized, errMap("github_token_expired"))
		return
	}
	if resp.StatusCode == http.StatusNotFound {
		writeJSON(w, http.StatusNotFound, errMap("repository not found"))
		return
	}
	if resp.StatusCode != http.StatusOK {
		writeJSON(w, http.StatusBadGateway, errMap("GitHub API error"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(body) //nolint
}

// ─── GET /api/github/repos/{owner}/{repo}/tree ───────────────────────────────
// Lists directory entries (folders only) at the given path & ref.
// Query params: ref (branch/sha), path (directory path, default "")
func (h *GitHubHandler) GetTree(w http.ResponseWriter, r *http.Request) {
	claims := mw.GetClaims(r.Context())
	owner := r.PathValue("owner")
	repo := r.PathValue("repo")
	ref := r.URL.Query().Get("ref")
	path := r.URL.Query().Get("path")
	if ref == "" {
		ref = "HEAD"
	}

	token, err := h.userToken(r, claims.UserID)
	if err != nil || token == "" {
		writeJSON(w, http.StatusBadRequest, errMap("GitHub account not connected"))
		return
	}

	// Use GitHub contents API — simpler than trees API, works with branch names
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/contents/%s?ref=%s",
		owner, repo, url.PathEscape(path), url.QueryEscape(ref))
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, apiURL, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errMap("failed to fetch tree"))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		h.clearUserGitHubToken(r.Context(), claims.UserID)
		writeJSON(w, http.StatusUnauthorized, errMap("github_token_expired"))
		return
	}
	if resp.StatusCode == http.StatusNotFound {
		// Empty or not a directory — return empty list
		writeJSON(w, http.StatusOK, map[string]any{"entries": []any{}})
		return
	}

	var items []struct {
		Name string `json:"name"`
		Path string `json:"path"`
		Type string `json:"type"` // "file" | "dir" | "symlink" | "submodule"
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err := json.Unmarshal(body, &items); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"entries": []any{}})
		return
	}

	// Return only directories
	type entry struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	dirs := make([]entry, 0)
	for _, it := range items {
		if it.Type == "dir" {
			dirs = append(dirs, entry{Name: it.Name, Path: it.Path})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": dirs})
}

// userToken fetches the GitHub access token for a user ID
func (h *GitHubHandler) userToken(r *http.Request, userID int64) (string, error) {
	var token string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT github_access_token FROM users WHERE id = ?`, userID,
	).Scan(&token)
	return token, err
}

