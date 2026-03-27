package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// ContainerStatsHandler streams live podman container stats via SSE.
type ContainerStatsHandler struct{}

func NewContainerStatsHandler() *ContainerStatsHandler {
	return &ContainerStatsHandler{}
}

// podmanStatEntry handles both old podman (string "12.34%") and new podman
// (float64 12.34) CPU field formats via custom JSON unmarshaling.
type podmanStatEntry struct {
	Name      string  `json:"Name"`
	ID        string  `json:"ID"`
	CPUPerc   string  `json:"-"`        // populated by UnmarshalJSON
	MemUsage  uint64  `json:"MemUsage"` // bytes (old format)
	MemLimit  uint64  `json:"MemLimit"` // bytes (old format)
	MemPerc   string  `json:"-"`        // populated by UnmarshalJSON
	NetInput  uint64  `json:"NetInput"`
	NetOutput uint64  `json:"NetOutput"`
	BlockIn   uint64  `json:"BlockInput"`
	BlockOut  uint64  `json:"BlockOutput"`
	PIDs      uint64  `json:"PIDs"`
}

// UnmarshalJSON handles multiple podman JSON format variations:
//   - Old podman: {"CPU":"12.34%", "MemUsage":1234, "MemLimit":5678, "MemPerc":"1.23%"}
//   - Newer podman: {"cpu_percent":12.34, "mem_usage":{"value":1234,...}, "mem_percent":1.23}
func (e *podmanStatEntry) UnmarshalJSON(data []byte) error {
	// Use a raw map so we can handle any key casing and type variations
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	// Helper to read a string key as string
	str := func(key string) string {
		if v, ok := raw[key]; ok {
			var s string
			if json.Unmarshal(v, &s) == nil {
				return s
			}
		}
		return ""
	}
	// Helper to read a key as float64 (handles both string "12%" and float 12.0)
	numStr := func(key string) string {
		if v, ok := raw[key]; ok {
			var f float64
			if json.Unmarshal(v, &f) == nil {
				return strconv.FormatFloat(f, 'f', 2, 64) + "%"
			}
			var s string
			if json.Unmarshal(v, &s) == nil {
				return s
			}
		}
		return ""
	}
	// Helper to read a key as uint64
	u64 := func(key string) uint64 {
		if v, ok := raw[key]; ok {
			var n uint64
			if json.Unmarshal(v, &n) == nil {
				return n
			}
			// Some formats wrap it in {value: N, string: "..."}
			var obj struct{ Value uint64 `json:"value"` }
			if json.Unmarshal(v, &obj) == nil && obj.Value > 0 {
				return obj.Value
			}
		}
		return 0
	}

	e.Name = str("Name")
	if e.Name == "" {
		e.Name = str("name")
	}
	e.ID = str("ID")
	if e.ID == "" {
		e.ID = str("id")
	}

	// CPU: try multiple field names
	e.CPUPerc = numStr("CPU")
	if e.CPUPerc == "" || e.CPUPerc == "0.00%" {
		if v := numStr("cpu_percent"); v != "" {
			e.CPUPerc = v
		}
	}
	if e.CPUPerc == "" || e.CPUPerc == "0.00%" {
		if v := numStr("CPUPerc"); v != "" {
			e.CPUPerc = v
		}
	}

	// Memory
	e.MemPerc = numStr("MemPerc")
	if e.MemPerc == "" {
		e.MemPerc = numStr("mem_percent")
	}

	e.MemUsage = u64("MemUsage")
	if e.MemUsage == 0 {
		e.MemUsage = u64("mem_usage")
	}
	e.MemLimit = u64("MemLimit")
	if e.MemLimit == 0 {
		e.MemLimit = u64("mem_limit")
	}

	// Network
	e.NetInput = u64("NetInput")
	if e.NetInput == 0 {
		e.NetInput = u64("net_input")
	}
	e.NetOutput = u64("NetOutput")
	if e.NetOutput == 0 {
		e.NetOutput = u64("net_output")
	}

	// Block I/O
	e.BlockIn = u64("BlockInput")
	if e.BlockIn == 0 {
		e.BlockIn = u64("block_input")
	}
	e.BlockOut = u64("BlockOutput")
	if e.BlockOut == 0 {
		e.BlockOut = u64("block_output")
	}

	// PIDs
	e.PIDs = u64("PIDs")
	if e.PIDs == 0 {
		e.PIDs = u64("pids")
	}

	return nil
}

// ContainerStatsEvent is what we send to the frontend.
type ContainerStatsEvent struct {
	Name      string  `json:"name"`
	CPUPct    float64 `json:"cpu_pct"`    // 0–100
	MemUsed   uint64  `json:"mem_used"`   // bytes
	MemTotal  uint64  `json:"mem_total"`  // bytes
	MemPct    float64 `json:"mem_pct"`    // 0–100
	NetIn     uint64  `json:"net_in"`     // bytes total
	NetOut    uint64  `json:"net_out"`    // bytes total
	BlkIn     uint64  `json:"blk_in"`     // bytes total
	BlkOut    uint64  `json:"blk_out"`    // bytes total
	PIDs      uint64  `json:"pids"`
	Status    string  `json:"status"`     // "running"|"stopped"|"not_found"
	Timestamp int64   `json:"ts"`         // unix ms
}

// parsePercent strips the trailing "%", returns 0 on error.
func parsePercent(s string) float64 {
	s = strings.TrimSuffix(strings.TrimSpace(s), "%")
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

// Stream handles GET .../stats/stream — SSE endpoint for a single container.
// Polls every 2 seconds while the client is connected.
func (h *ContainerStatsHandler) Stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, errMap("streaming not supported"))
		return
	}

	serviceID, err := strconv.ParseInt(chi.URLParam(r, "serviceID"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid service ID"))
		return
	}
	cName := fmt.Sprintf("fd-svc-%d", serviceID)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	send := func() {
		ev := collectContainerStats(cName)
		ev.Timestamp = time.Now().UnixMilli()
		data, _ := json.Marshal(ev)
		fmt.Fprintf(w, "event: stats\ndata: %s\n\n", data)
		flusher.Flush()
	}

	send() // immediate first event

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			send()
		}
	}
}

// collectContainerStats runs podman stats --no-stream for cName and returns
// the parsed event. Returns a "not_found" event if the container is absent.
func collectContainerStats(cName string) ContainerStatsEvent {
	cmd := exec.Command("sudo", "-n", "podman", "stats", "--no-stream", "--format", "json", cName)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	// Capture the error but do NOT return yet — some podman versions exit with a
	// non-zero code even when the container is running (e.g. cgroup v1 systems).
	// We try to parse stdout first; only fall back to not_found if that fails.
	runErr := cmd.Run()

	// Strip any leading non-JSON content (warnings, TTY noise).
	raw := strings.TrimSpace(outBuf.String())
	if idx := strings.IndexAny(raw, "[{"); idx > 0 {
		raw = raw[idx:]
	}

	if raw != "" && raw != "null" && raw != "[]" {
		// podman stats --format json returns a JSON array
		var entries []podmanStatEntry
		if err := json.Unmarshal([]byte(raw), &entries); err != nil || len(entries) == 0 {
			// Fallback: try single-object format
			var single podmanStatEntry
			if err2 := json.Unmarshal([]byte(raw), &single); err2 == nil {
				entries = []podmanStatEntry{single}
			}
		}
		if len(entries) > 0 {
			e := entries[0]
			if e.Name == "" {
				e.Name = cName
			}
			return ContainerStatsEvent{
				Name:     e.Name,
				CPUPct:   parsePercent(e.CPUPerc),
				MemUsed:  e.MemUsage,
				MemTotal: e.MemLimit,
				MemPct:   parsePercent(e.MemPerc),
				NetIn:    e.NetInput,
				NetOut:   e.NetOutput,
				BlkIn:    e.BlockIn,
				BlkOut:   e.BlockOut,
				PIDs:     e.PIDs,
				Status:   "running",
			}
		}
	}

	// stdout was empty or unparseable.
	// Always verify via `podman inspect` before declaring the container gone.
	// On many VPS kernels (cgroup v1, restricted namespaces) `podman stats`
	// exits non-zero even for running containers. Never rely solely on
	// runErr here — inspect is the ground truth.
	_ = runErr // already attempted parse above; outcome doesn't matter now
	inspCmd := exec.Command("sudo", "-n", "podman", "inspect", "--format", "{{.State.Status}}", cName)
	out, _ := inspCmd.Output()
	state := strings.TrimSpace(string(out))
	if state == "running" {
		return ContainerStatsEvent{Name: cName, Status: "running"}
	}
	if state != "" {
		// Container exists but is stopped/paused/exited.
		return ContainerStatsEvent{Name: cName, Status: "stopped"}
	}
	return ContainerStatsEvent{Name: cName, Status: "not_found"}
}
