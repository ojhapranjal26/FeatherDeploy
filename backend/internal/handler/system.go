package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"time"
)

// AppVersion is the version of the currently running binary.
// Bump this whenever VERSION.json on the main branch is updated.
const AppVersion = "1.1.0"

const versionManifestURL = "https://raw.githubusercontent.com/ojhapranjal26/FeatherDeploy/main/VERSION.json"

// semverRe accepts "MAJOR.MINOR.PATCH" only — protects against arbitrary
// strings being interpolated into shell commands in the update script.
var semverRe = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// SystemHandler serves version-check and self-update endpoints.
type SystemHandler struct{}

// NewSystemHandler constructs a SystemHandler.
func NewSystemHandler() *SystemHandler { return &SystemHandler{} }

type remoteVersionManifest struct {
	Version   string `json:"version"`
	Branch    string `json:"branch"`
	Changelog string `json:"changelog"`
}

// VersionCheckResp is the JSON response for GET /api/system/version.
type VersionCheckResp struct {
	CurrentVersion  string `json:"current_version"`
	LatestVersion   string `json:"latest_version"`
	UpdateAvailable bool   `json:"update_available"`
	Changelog       string `json:"changelog"`
	Branch          string `json:"branch"`
}

// VersionCheck fetches VERSION.json from the GitHub main branch and compares
// it against the currently-running binary version.
func (h *SystemHandler) VersionCheck(w http.ResponseWriter, r *http.Request) {
	c := &http.Client{Timeout: 10 * time.Second}
	resp, err := c.Get(versionManifestURL)
	if err != nil {
		// GitHub unreachable — return current version with no update flag.
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(VersionCheckResp{ //nolint
			CurrentVersion: AppVersion,
			LatestVersion:  AppVersion,
		})
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))

	var remote remoteVersionManifest
	if err := json.Unmarshal(body, &remote); err != nil {
		http.Error(w, "malformed version manifest from GitHub", http.StatusBadGateway)
		return
	}

	// Only offer updates from the main branch; never expose dev / pre-release.
	updateAvail := remote.Branch == "main" &&
		semverRe.MatchString(remote.Version) &&
		remote.Version != AppVersion

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(VersionCheckResp{ //nolint
		CurrentVersion:  AppVersion,
		LatestVersion:   remote.Version,
		UpdateAvailable: updateAvail,
		Changelog:       remote.Changelog,
		Branch:          remote.Branch,
	})
}

// TriggerUpdate validates the remote manifest, responds with 202 Accepted,
// then runs /usr/local/bin/featherdeploy-update in a background goroutine.
// The update script does a source-based rebuild: git pull + npm build + go build,
// installs the new binary to /usr/local/bin/featherdeploy, then calls
// `featherdeploy update` to apply DB migrations and restart the service.
//
// Requirements (installed by build.sh):
//   - /opt/featherdeploy-src          (git clone of the repository)
//   - /usr/local/bin/featherdeploy-update  (the update shell script)
//   - sudoers entry allowing featherdeploy to run it as root without a password
func (h *SystemHandler) TriggerUpdate(w http.ResponseWriter, r *http.Request) {
	const updateScript = "/usr/local/bin/featherdeploy-update"
	if _, err := os.Stat(updateScript); os.IsNotExist(err) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotImplemented)
		fmt.Fprintf(w, `{"error":"update script not found — re-run build.sh on the server to enable one-click updates"}`)
		return
	}

	// Fetch the remote manifest to get the exact version tag.
	c := &http.Client{Timeout: 10 * time.Second}
	resp, err := c.Get(versionManifestURL)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, `{"error":"cannot reach GitHub to fetch version manifest"}`)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))

	var remote remoteVersionManifest
	if err := json.Unmarshal(body, &remote); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, `{"error":"malformed version manifest from GitHub"}`)
		return
	}

	if !semverRe.MatchString(remote.Version) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, `{"error":"version in remote manifest has unexpected format"}`)
		return
	}
	if remote.Branch != "main" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"updates are only available from the main branch"}`)
		return
	}
	if remote.Version == AppVersion {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		fmt.Fprintf(w, `{"error":"already running the latest version"}`)
		return
	}

	// Send the 202 before launching the goroutine; once the service restarts
	// the process is killed, so nothing can be written to w afterwards.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{ //nolint
		"message": "Update started. The panel will restart automatically in ~60 seconds — refresh the page to verify.",
		"version": remote.Version,
	})
	if fl, ok := w.(http.Flusher); ok {
		fl.Flush()
	}

	go func() {
		slog.Info("self-update triggered", "from", AppVersion, "to", remote.Version)
		cmd := exec.Command("sudo", "-n", updateScript)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			slog.Error("self-update failed", "err", err, "version", remote.Version)
		}
	}()
}
