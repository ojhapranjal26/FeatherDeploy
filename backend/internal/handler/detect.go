package handler

import (
	"database/sql"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/ojhapranjal26/featherdeploy/backend/internal/deploy"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/detect"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/middleware"
)

// DetectHandler handles automatic app-stack detection by cloning a service's
// git repository into a temp directory and running static analysis.
type DetectHandler struct {
	db        *sql.DB
	jwtSecret string
}

func NewDetectHandler(db *sql.DB, jwtSecret string) *DetectHandler {
	return &DetectHandler{db: db, jwtSecret: jwtSecret}
}

// POST /api/projects/{projectID}/services/{serviceID}/detect
//
// Clones the service's repo at the configured branch (--depth 1) and runs
// stack detection. Returns language, framework, version, build command,
// start command, default app port, and suggested base image.
// The caller is expected to confirm / override the result and then PATCH the
// service with the chosen values.
func (h *DetectHandler) Detect(w http.ResponseWriter, r *http.Request) {
	svcID, err := strconv.ParseInt(r.PathValue("serviceID"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid serviceID"))
		return
	}

	var repoURL, repoBranch string
	err = h.db.QueryRowContext(r.Context(),
		`SELECT repo_url, repo_branch FROM services WHERE id=?`, svcID,
	).Scan(&repoURL, &repoBranch)
	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusNotFound, errMap("service not found"))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	if repoURL == "" {
		writeJSON(w, http.StatusBadRequest, errMap("service has no repo_url; set a repository URL first"))
		return
	}

	claims := middleware.GetClaims(r.Context())

	// Clone to a throw-away temp directory
	tmpDir, err := os.MkdirTemp("", "fd-detect-*")
	if err != nil {
		slog.Error("mktemp for detect", "err", err)
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	defer os.RemoveAll(tmpDir)

	if repoBranch == "" {
		repoBranch = "main"
	}

	// Inject GitHub App installation token for HTTPS GitHub repos
	// This must happen before SSH URL detection since the two are mutually exclusive.
	if !deploy.IsSSHURL(repoURL) {
		repoURL = deploy.InjectGitHubAppToken(r.Context(), h.db, repoURL)
	}

	// Optional SSH key setup for private repos
	gitEnv := os.Environ()
	if deploy.IsSSHURL(repoURL) {
		if keyFile, cleanup, keyErr := deploy.FetchSSHKey(h.db, h.jwtSecret, claims.UserID); keyErr == nil {
			defer cleanup()
			gitEnv = append(gitEnv, deploy.SSHGitEnv(keyFile))
		} else {
			slog.Warn("detect: no SSH key for user", "user", claims.UserID, "err", keyErr)
		}
	}

	newGitCmd := func(args ...string) *exec.Cmd {
		cmd := exec.CommandContext(r.Context(), "git", args...)
		cmd.Env = gitEnv
		return cmd
	}

	// First attempt: clone with the configured branch
	cloneArgs := []string{"clone", "--depth", "1", "--branch", repoBranch, "--", repoURL, tmpDir}
	out1, err1 := newGitCmd(cloneArgs...).CombinedOutput()
	var cloneOut string
	if err1 != nil {
		// Second attempt: clone without --branch (uses the repo default)
		if err2Out, err2 := newGitCmd("clone", "--depth", "1", "--", repoURL, tmpDir).CombinedOutput(); err2 != nil {
			cloneOut = strings.TrimSpace(string(out1) + "\n" + string(err2Out))
			slog.Error("git clone for detect", "url", repoURL, "out", cloneOut)
			writeJSON(w, http.StatusUnprocessableEntity, errMap(cloneErrMsg(cloneOut, repoURL)))
			return
		}
	}

	result := detect.Detect(tmpDir)
	writeJSON(w, http.StatusOK, result)
}

// cloneErrMsg returns a human-readable error message based on the git output.
func cloneErrMsg(out, repoURL string) string {
	lower := strings.ToLower(out)
	switch {
	case strings.Contains(lower, "repository not found") || strings.Contains(lower, "not found"):
		if strings.Contains(repoURL, "x-access-token") {
			return "GitHub App does not have access to this repository. " +
				"Open your GitHub App installation settings and grant access to this repository " +
				"(Settings → GitHub App → Configure → Repository access → Add repository)."
		}
		return "Repository not found — check the URL and make sure the repo exists."
	case strings.Contains(lower, "authentication failed") || strings.Contains(lower, "could not read username"):
		return "Authentication failed — connect a GitHub App or SSH key for private repositories."
	case strings.Contains(lower, "permission denied"):
		return "Permission denied — the configured credentials do not have read access to this repository."
	case strings.Contains(lower, "timeout") || strings.Contains(lower, "timed out"):
		return "Connection timed out while cloning the repository — check network connectivity."
	default:
		return "Failed to clone repository: " + firstLine(out)
	}
}

// firstLine returns the first non-empty line of s.
func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			return t
		}
	}
	return s
}
