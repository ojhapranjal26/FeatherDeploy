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

// podmanStatEntry is the JSON object podman emits per container when run with
//
//	sudo podman stats --no-stream --format json <name>
type podmanStatEntry struct {
	Name      string  `json:"Name"`
	ID        string  `json:"ID"`
	CPUPerc   string  `json:"CPU"`      // "12.34%"
	MemUsage  uint64  `json:"MemUsage"` // bytes
	MemLimit  uint64  `json:"MemLimit"` // bytes
	MemPerc   string  `json:"MemPerc"`  // "1.23%"
	NetInput  uint64  `json:"NetInput"` // bytes
	NetOutput uint64  `json:"NetOutput"`
	BlockIn   uint64  `json:"BlockInput"`
	BlockOut  uint64  `json:"BlockOutput"`
	PIDs      uint64  `json:"PIDs"`
	Status    string  `json:"Status"`
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
			// Keep-alive ping if container gone
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
			send()
		}
	}
}

// collectContainerStats runs podman stats --no-stream for cName and returns
// the parsed event. Returns a "not_found" event if the container is absent.
func collectContainerStats(cName string) ContainerStatsEvent {
	cmd := exec.Command("sudo", "-n", "podman", "stats", "--no-stream", "--format", "json", cName)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	if err := cmd.Run(); err != nil {
		return ContainerStatsEvent{Name: cName, Status: "not_found"}
	}

	raw := strings.TrimSpace(buf.String())
	if raw == "" || raw == "null" || raw == "[]" {
		return ContainerStatsEvent{Name: cName, Status: "not_found"}
	}

	// podman stats --format json returns a JSON array
	var entries []podmanStatEntry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil || len(entries) == 0 {
		// Fallback: try single-object format
		var single podmanStatEntry
		if err2 := json.Unmarshal([]byte(raw), &single); err2 == nil {
			entries = []podmanStatEntry{single}
		} else {
			return ContainerStatsEvent{Name: cName, Status: "not_found"}
		}
	}

	e := entries[0]
	return ContainerStatsEvent{
		Name:     cName,
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
