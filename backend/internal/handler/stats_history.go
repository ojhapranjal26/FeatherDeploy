package handler

import (
	"database/sql"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
)

// StatsHistoryHandler serves persisted per-service stats snapshots.
type StatsHistoryHandler struct{ db *sql.DB }

func NewStatsHistoryHandler(db *sql.DB) *StatsHistoryHandler {
	return &StatsHistoryHandler{db: db}
}

type statPoint struct {
	Ts       int64   `json:"ts"`        // unix ms
	CPUPct   float64 `json:"cpu_pct"`
	MemPct   float64 `json:"mem_pct"`
	MemUsed  int64   `json:"mem_used"`
	MemTotal int64   `json:"mem_total"`
	NetIn    int64   `json:"net_in"`
	NetOut   int64   `json:"net_out"`
	BlkIn    int64   `json:"blk_in"`
	BlkOut   int64   `json:"blk_out"`
	PIDs     int64   `json:"pids"`
}

type peakValue struct {
	Value float64 `json:"value"`
	Ts    int64   `json:"ts"`
}

type peakSet struct {
	CPU    peakValue `json:"cpu"`
	Mem    peakValue `json:"mem"`
	NetIn  peakValue `json:"net_in"`
	NetOut peakValue `json:"net_out"`
}

type hourlyBucket struct {
	Hour    int     `json:"hour"`    // 0–23 UTC
	CPUAvg  float64 `json:"cpu_avg"` // average CPU % for samples in this hour slot
	MemAvg  float64 `json:"mem_avg"`
	Samples int     `json:"samples"`
}

type StatsHistoryResponse struct {
	Range     string         `json:"range"`
	Points    []statPoint    `json:"points"`     // downsampled chart data
	Peaks     peakSet        `json:"peaks"`
	HourlyAvg []hourlyBucket `json:"hourly_avg"` // which hours of the day see most load
}

// GET /api/projects/{projectID}/services/{serviceID}/stats/history?range=1h|6h|24h|7d
func (h *StatsHistoryHandler) History(w http.ResponseWriter, r *http.Request) {
	serviceID, err := strconv.ParseInt(chi.URLParam(r, "serviceID"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid serviceID"))
		return
	}

	rangeStr := r.URL.Query().Get("range")
	var since time.Duration
	switch rangeStr {
	case "1h":
		since = 1 * time.Hour
	case "6h":
		since = 6 * time.Hour
	case "7d":
		since = 7 * 24 * time.Hour
	default:
		rangeStr = "24h"
		since = 24 * time.Hour
	}

	cutoff := time.Now().UTC().Add(-since).Format("2006-01-02 15:04:05")

	rows, err := h.db.QueryContext(r.Context(),
		`SELECT recorded_at, cpu_pct, mem_pct, mem_used, mem_total,
		        net_in, net_out, blk_in, blk_out, pids
		 FROM service_stats
		 WHERE service_id=? AND recorded_at >= ?
		 ORDER BY recorded_at ASC`,
		serviceID, cutoff)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	defer rows.Close()

	var all []statPoint
	for rows.Next() {
		var p statPoint
		var recAt flexTime
		if err := rows.Scan(&recAt, &p.CPUPct, &p.MemPct, &p.MemUsed, &p.MemTotal,
			&p.NetIn, &p.NetOut, &p.BlkIn, &p.BlkOut, &p.PIDs); err != nil {
			continue
		}
		if !recAt.Valid {
			continue
		}
		p.Ts = recAt.Time.UnixMilli()
		all = append(all, p)
	}
	if all == nil {
		all = []statPoint{}
	}

	// ── Compute peaks from full dataset ───────────────────────────────────────
	var peaks peakSet
	for _, p := range all {
		if p.CPUPct > peaks.CPU.Value {
			peaks.CPU = peakValue{Value: p.CPUPct, Ts: p.Ts}
		}
		if p.MemPct > peaks.Mem.Value {
			peaks.Mem = peakValue{Value: p.MemPct, Ts: p.Ts}
		}
		if float64(p.NetIn) > peaks.NetIn.Value {
			peaks.NetIn = peakValue{Value: float64(p.NetIn), Ts: p.Ts}
		}
		if float64(p.NetOut) > peaks.NetOut.Value {
			peaks.NetOut = peakValue{Value: float64(p.NetOut), Ts: p.Ts}
		}
	}
	peaks.CPU.Value = round2(peaks.CPU.Value)
	peaks.Mem.Value = round2(peaks.Mem.Value)

	// ── Compute hourly averages (by hour-of-day, UTC) ─────────────────────────
	// Buckets are keyed by hour-of-day (0–23) to show which time of day is
	// typically busiest — useful regardless of the selected range.
	type bucket struct {
		cpuSum, memSum float64
		count          int
	}
	buckets := make(map[int]*bucket)
	for _, p := range all {
		h := time.UnixMilli(p.Ts).UTC().Hour()
		if buckets[h] == nil {
			buckets[h] = &bucket{}
		}
		buckets[h].cpuSum += p.CPUPct
		buckets[h].memSum += p.MemPct
		buckets[h].count++
	}
	var hourly []hourlyBucket
	for h := 0; h < 24; h++ {
		if b, ok := buckets[h]; ok {
			hourly = append(hourly, hourlyBucket{
				Hour:    h,
				CPUAvg:  round2(b.cpuSum / float64(b.count)),
				MemAvg:  round2(b.memSum / float64(b.count)),
				Samples: b.count,
			})
		}
	}
	if hourly == nil {
		hourly = []hourlyBucket{}
	}

	// ── Downsample chart points to max 300 ────────────────────────────────────
	const maxPts = 300
	var chartPts []statPoint
	if len(all) <= maxPts {
		chartPts = all
	} else {
		stride := (len(all) + maxPts - 1) / maxPts
		for i := 0; i < len(all); i += stride {
			chartPts = append(chartPts, all[i])
		}
	}
	if chartPts == nil {
		chartPts = []statPoint{}
	}

	writeJSON(w, http.StatusOK, StatsHistoryResponse{
		Range:     rangeStr,
		Points:    chartPts,
		Peaks:     peaks,
		HourlyAvg: hourly,
	})
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

// ─── Monthly history (aggregate table) ───────────────────────────────────────

type monthlyHourBucket struct {
	Hour    int     `json:"hour"`
	CPUAvg  float64 `json:"cpu_avg"`
	MemAvg  float64 `json:"mem_avg"`
	Samples int     `json:"samples"`
}

type monthlyEntry struct {
	Year   int                  `json:"year"`
	Month  int                  `json:"month"`
	Label  string               `json:"label"`
	Hourly []monthlyHourBucket  `json:"hourly"`
}

type MonthlyHistoryResponse struct {
	Months []monthlyEntry `json:"months"`
}

// GET /api/projects/{projectID}/services/{serviceID}/stats/monthly
func (h *StatsHistoryHandler) MonthlyHistory(w http.ResponseWriter, r *http.Request) {
	serviceID, err := strconv.ParseInt(chi.URLParam(r, "serviceID"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errMap("invalid serviceID"))
		return
	}

	rows, err := h.db.QueryContext(r.Context(),
		`SELECT year, month, hour, cpu_avg, mem_avg, samples
		 FROM service_stats_monthly
		 WHERE service_id=?
		 ORDER BY year DESC, month DESC, hour ASC`,
		serviceID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errMap("internal error"))
		return
	}
	defer rows.Close()

	type monthKey struct{ year, month int }
	entryMap := map[monthKey]*monthlyEntry{}
	var order []monthKey

	for rows.Next() {
		var year, month, hour, samples int
		var cpuAvg, memAvg float64
		if err := rows.Scan(&year, &month, &hour, &cpuAvg, &memAvg, &samples); err != nil {
			continue
		}
		k := monthKey{year, month}
		if _, ok := entryMap[k]; !ok {
			t := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
			entryMap[k] = &monthlyEntry{
				Year:  year,
				Month: month,
				Label: t.Format("Jan 2006"),
			}
			order = append(order, k)
		}
		entryMap[k].Hourly = append(entryMap[k].Hourly, monthlyHourBucket{
			Hour:    hour,
			CPUAvg:  round2(cpuAvg),
			MemAvg:  round2(memAvg),
			Samples: samples,
		})
	}

	var months []monthlyEntry
	for _, k := range order {
		months = append(months, *entryMap[k])
	}
	if months == nil {
		months = []monthlyEntry{}
	}
	writeJSON(w, http.StatusOK, MonthlyHistoryResponse{Months: months})
}
