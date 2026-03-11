package handler

import (
	"database/sql"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/ojhapranjal26/featherdeploy/backend/internal/detect"
)

// DetectHandler handles automatic app-stack detection by cloning a service's
// git repository into a temp directory and running static analysis.
type DetectHandler struct{ db *sql.DB }

func NewDetectHandler(db *sql.DB) *DetectHandler { return &DetectHandler{db: db} }

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

	// First attempt: clone with the configured branch
	cloneArgs := []string{"clone", "--depth", "1", "--branch", repoBranch, "--", repoURL, tmpDir}
	if out, err := exec.CommandContext(r.Context(), "git", cloneArgs...).CombinedOutput(); err != nil {
		// Second attempt: clone without --branch (uses the repo default)
		cloneArgs2 := []string{"clone", "--depth", "1", "--", repoURL, tmpDir}
		if out2, err2 := exec.CommandContext(r.Context(), "git", cloneArgs2...).CombinedOutput(); err2 != nil {
			slog.Error("git clone for detect", "url", repoURL, "out", strings.TrimSpace(string(out)+string(out2)))
			writeJSON(w, http.StatusBadGateway,
				errMap("failed to clone repository — make sure the URL is correct and accessible"))
			return
		}
	}

	result := detect.Detect(tmpDir)
	writeJSON(w, http.StatusOK, result)
}

