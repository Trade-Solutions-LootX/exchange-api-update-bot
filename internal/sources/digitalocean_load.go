package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"

	"exchangebot/internal/model"
)

// doLoadSource polls the DigitalOcean monitoring API for per-droplet CPU and
// memory usage and emits a threshold-crossing alert (and a recovery when usage
// drops back). It is a SystemSource (transition alerts, no seen-store).
//
// Requires the DigitalOcean monitoring agent on the droplets; without it the
// metrics endpoints return empty series and the source stays silent (no error).
type doLoadSource struct {
	token    string
	cpuPct   float64
	memPct   float64
	interval time.Duration
	deps     Deps

	mu    sync.Mutex
	over  map[string]bool // "<host>|cpu" / "<host>|mem" → currently over threshold
	known map[string]bool
	seq   map[string]int
}

// NewDigitalOceanLoad builds the DO load monitor.
func NewDigitalOceanLoad(token string, cpuPct, memPct float64, interval time.Duration, deps Deps) Source {
	return &doLoadSource{
		token:    token,
		cpuPct:   cpuPct,
		memPct:   memPct,
		interval: interval,
		deps:     deps,
		over:     map[string]bool{},
		known:    map[string]bool{},
		seq:      map[string]int{},
	}
}

func (d *doLoadSource) Exchange() string        { return "digitalocean" }
func (d *doLoadSource) Name() string            { return "digitalocean:load" }
func (d *doLoadSource) Interval() time.Duration { return d.interval }
func (d *doLoadSource) IsSystem() bool          { return true }

func (d *doLoadSource) auth() map[string]string {
	return map[string]string{"Authorization": "Bearer " + d.token}
}

func (d *doLoadSource) Fetch(ctx context.Context) ([]model.Announcement, error) {
	droplets, err := d.listDroplets(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	start := now.Add(-10 * time.Minute)
	var alerts []model.Announcement
	for _, dr := range droplets {
		host := strconv.Itoa(dr.ID)
		if pct, ok := d.cpuPercent(ctx, host, start, now); ok {
			if a, ok := d.transition(host, dr.Name, "cpu", pct, d.cpuPct, now); ok {
				alerts = append(alerts, a)
			}
		}
		if pct, ok := d.memPercent(ctx, host, start, now); ok {
			if a, ok := d.transition(host, dr.Name, "mem", pct, d.memPct, now); ok {
				alerts = append(alerts, a)
			}
		}
	}
	return alerts, nil
}

type doDroplet struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func (d *doLoadSource) listDroplets(ctx context.Context) ([]doDroplet, error) {
	raw, err := d.deps.HTTP.Get(ctx, "https://api.digitalocean.com/v2/droplets?per_page=200", d.auth())
	if err != nil {
		return nil, fmt.Errorf("do list droplets: %w", err)
	}
	var r struct {
		Droplets []doDroplet `json:"droplets"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("do droplets decode: %w", err)
	}
	return r.Droplets, nil
}

// metricsMatrix is the Prometheus-style shape DO returns for monitoring metrics.
type metricsMatrix struct {
	Data struct {
		Result []struct {
			Metric map[string]string `json:"metric"`
			Values [][2]any          `json:"values"` // [ [unixSeconds, "value"], ... ]
		} `json:"result"`
	} `json:"data"`
}

func (d *doLoadSource) fetchMetric(ctx context.Context, metric, host string, start, end time.Time) (*metricsMatrix, error) {
	url := fmt.Sprintf("https://api.digitalocean.com/v2/monitoring/metrics/droplet/%s?host_id=%s&start=%d&end=%d",
		metric, host, start.Unix(), end.Unix())
	raw, err := d.deps.HTTP.Get(ctx, url, d.auth())
	if err != nil {
		return nil, err
	}
	var m metricsMatrix
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// cpuPercent computes CPU-used % from the cumulative per-mode counters as
// 100 * (1 - Δidle/Δtotal) over the window.
func (d *doLoadSource) cpuPercent(ctx context.Context, host string, start, end time.Time) (float64, bool) {
	m, err := d.fetchMetric(ctx, "cpu", host, start, end)
	if err != nil {
		return 0, false
	}
	return cpuUsedPercent(m)
}

func cpuUsedPercent(m *metricsMatrix) (float64, bool) {
	var totalFirst, totalLast, idleFirst, idleLast float64
	have := false
	for _, s := range m.Data.Result {
		f, okF := firstValue(s.Values)
		l, okL := lastValue(s.Values)
		if !okF || !okL {
			continue
		}
		totalFirst += f
		totalLast += l
		if s.Metric["mode"] == "idle" {
			idleFirst += f
			idleLast += l
		}
		have = true
	}
	dTotal := totalLast - totalFirst
	if !have || dTotal <= 0 {
		return 0, false
	}
	pct := 100 * (1 - (idleLast-idleFirst)/dTotal)
	if pct < 0 {
		pct = 0
	}
	return pct, true
}

// memPercent computes memory-used % = 100*(total-available)/total from the
// latest gauge samples.
func (d *doLoadSource) memPercent(ctx context.Context, host string, start, end time.Time) (float64, bool) {
	total, err := d.fetchMetric(ctx, "memory_total", host, start, end)
	if err != nil {
		return 0, false
	}
	avail, err := d.fetchMetric(ctx, "memory_available", host, start, end)
	if err != nil {
		return 0, false
	}
	return memUsedPercent(total, avail)
}

func memUsedPercent(total, avail *metricsMatrix) (float64, bool) {
	t, okT := latestScalar(total)
	a, okA := latestScalar(avail)
	if !okT || !okA || t <= 0 {
		return 0, false
	}
	pct := 100 * (t - a) / t
	if pct < 0 {
		pct = 0
	}
	return pct, true
}

// latestScalar returns the last value of the first series (for single-series
// gauge metrics like memory_total).
func latestScalar(m *metricsMatrix) (float64, bool) {
	if len(m.Data.Result) == 0 {
		return 0, false
	}
	return lastValue(m.Data.Result[0].Values)
}

func firstValue(vals [][2]any) (float64, bool) {
	if len(vals) == 0 {
		return 0, false
	}
	return numAt(vals[0])
}

func lastValue(vals [][2]any) (float64, bool) {
	if len(vals) == 0 {
		return 0, false
	}
	return numAt(vals[len(vals)-1])
}

// numAt parses the "value" element (index 1) of a [ts, "value"] pair.
func numAt(pair [2]any) (float64, bool) {
	switch v := pair[1].(type) {
	case string:
		f, err := strconv.ParseFloat(v, 64)
		return f, err == nil
	case float64:
		return v, true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	}
	return 0, false
}

func (d *doLoadSource) transition(hostID, name, metric string, pct, threshold float64, now time.Time) (model.Announcement, bool) {
	// Key state by the unique droplet ID (names can collide); display the name.
	key := hostID + "|" + metric
	d.mu.Lock()
	defer d.mu.Unlock()

	nowOver := pct >= threshold
	prevOver, known := d.over[key], d.known[key]
	d.known[key] = true
	if known && prevOver == nowOver {
		return model.Announcement{}, false
	}
	if !known && !nowOver {
		d.over[key] = false
		return model.Announcement{}, false // first sample healthy: record silently
	}
	d.over[key] = nowOver
	d.seq[key]++
	label := "CPU"
	if metric == "mem" {
		label = "memory"
	}

	if nowOver {
		return model.Announcement{
			Exchange:     "digitalocean",
			NativeID:     fmt.Sprintf("load-over:%s:%d", key, d.seq[key]),
			Title:        fmt.Sprintf("DigitalOcean %s %s HIGH: %.0f%% (threshold %.0f%%)", name, label, pct, threshold),
			URL:          "https://cloud.digitalocean.com/droplets",
			Source:       "digitalocean:load",
			PublishedAt:  now,
			Markets:      []model.MarketType{model.MarketInfra},
			Importance:   model.ImportanceHigh,
			MatchedRules: []string{"load:over-threshold"},
		}, true
	}
	return model.Announcement{
		Exchange:     "digitalocean",
		NativeID:     fmt.Sprintf("load-ok:%s:%d", key, d.seq[key]),
		Title:        fmt.Sprintf("DigitalOcean %s %s back to normal: %.0f%%", name, label, pct),
		URL:          "https://cloud.digitalocean.com/droplets",
		Source:       "digitalocean:load",
		PublishedAt:  now,
		Markets:      []model.MarketType{model.MarketInfra},
		Importance:   model.ImportanceMedium,
		MatchedRules: []string{"load:recovered"},
	}, true
}
