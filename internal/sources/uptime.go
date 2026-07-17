package sources

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"exchangebot/internal/model"
)

// SystemSource marks a source whose emitted items are live state-transition
// alerts (uptime up/down, load thresholds) rather than announcements. The poller
// delivers them immediately, bypassing first-run backlog suppression, the
// importance/API_ONLY filters, and the persistent seen-store.
type SystemSource interface {
	Source
	IsSystem() bool
}

// UptimeTarget is one endpoint to probe.
type UptimeTarget struct {
	Name string
	URL  string
}

// ParseUptimeTargets parses "name=url,name2=url2" (or bare "url"). A bare URL
// derives its name from the host.
func ParseUptimeTargets(csv string) []UptimeTarget {
	var out []UptimeTarget
	for _, part := range strings.Split(csv, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, url := "", part
		if i := strings.Index(part, "="); i > 0 {
			name = strings.TrimSpace(part[:i])
			url = strings.TrimSpace(part[i+1:])
		}
		if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
			url = "https://" + url
		}
		if name == "" {
			name = hostOf(url)
		}
		out = append(out, UptimeTarget{Name: name, URL: url})
	}
	return out
}

func hostOf(url string) string {
	s := strings.TrimPrefix(strings.TrimPrefix(url, "https://"), "http://")
	if i := strings.IndexAny(s, "/:"); i > 0 {
		s = s[:i]
	}
	return s
}

// uptimeSource probes a set of endpoints and emits an alert whenever one
// transitions up→down or down→up.
type uptimeSource struct {
	targets  []UptimeTarget
	interval time.Duration
	client   *http.Client

	mu       sync.Mutex
	up       map[string]bool      // last known state
	known    map[string]bool      // whether we've probed at least once
	since    map[string]time.Time // when the current state began
	sequence map[string]int       // transition counter → unique dedup keys
}

// NewUptimeSource builds an uptime monitor. It uses a dedicated no-retry HTTP
// client so a down endpoint is reported immediately, not after backoff.
func NewUptimeSource(targets []UptimeTarget, interval, timeout time.Duration) Source {
	return &uptimeSource{
		targets:  targets,
		interval: interval,
		client:   &http.Client{Timeout: timeout},
		up:       map[string]bool{},
		known:    map[string]bool{},
		since:    map[string]time.Time{},
		sequence: map[string]int{},
	}
}

func (u *uptimeSource) Exchange() string        { return "uptime" }
func (u *uptimeSource) Name() string            { return "uptime:probes" }
func (u *uptimeSource) Interval() time.Duration { return u.interval }
func (u *uptimeSource) IsSystem() bool          { return true }

func (u *uptimeSource) Fetch(ctx context.Context) ([]model.Announcement, error) {
	now := time.Now().UTC()
	var alerts []model.Announcement
	for _, t := range u.targets {
		up, latency, reason := u.probe(ctx, t.URL)
		if a, ok := u.transition(t, up, latency, reason, now); ok {
			alerts = append(alerts, a)
		}
	}
	return alerts, nil
}

// probe performs a single GET and classifies the result as up (2xx/3xx) or down.
func (u *uptimeSource) probe(ctx context.Context, url string) (up bool, latency time.Duration, reason string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, 0, "bad url: " + err.Error()
	}
	req.Header.Set("User-Agent", "ExchangeAPIUpdateBot-uptime/1.0")
	start := time.Now()
	resp, err := u.client.Do(req)
	latency = time.Since(start)
	if err != nil {
		return false, latency, err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		return true, latency, ""
	}
	return false, latency, fmt.Sprintf("HTTP %d", resp.StatusCode)
}

// transition compares the probe result to the last known state and returns an
// alert announcement only when the state changes (or on the first-ever down).
func (u *uptimeSource) transition(t UptimeTarget, up bool, latency time.Duration, reason string, now time.Time) (model.Announcement, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()

	prevUp, wasKnown := u.up[t.Name], u.known[t.Name]
	u.known[t.Name] = true

	// No change (and not a brand-new down): nothing to report.
	if wasKnown && prevUp == up {
		return model.Announcement{}, false
	}
	// First probe and it's UP: record silently, don't alert.
	if !wasKnown && up {
		u.up[t.Name] = true
		u.since[t.Name] = now
		return model.Announcement{}, false
	}

	prevSince := u.since[t.Name]
	u.up[t.Name] = up
	u.since[t.Name] = now
	u.sequence[t.Name]++
	seq := u.sequence[t.Name]

	if !up {
		return model.Announcement{
			Exchange:     "uptime",
			NativeID:     fmt.Sprintf("down:%s:%d", t.Name, seq),
			Title:        fmt.Sprintf("%s is DOWN (%s)", t.Name, reason),
			Body:         fmt.Sprintf("Probe of %s failed after %s.", t.URL, latency.Round(time.Millisecond)),
			URL:          t.URL,
			Source:       "uptime:probes",
			PublishedAt:  now,
			Markets:      []model.MarketType{model.MarketInfra},
			Importance:   model.ImportanceHigh,
			MatchedRules: []string{"uptime:down"},
		}, true
	}

	downFor := ""
	if wasKnown && !prevSince.IsZero() {
		downFor = " after " + now.Sub(prevSince).Round(time.Second).String() + " down"
	}
	return model.Announcement{
		Exchange:     "uptime",
		NativeID:     fmt.Sprintf("up:%s:%d", t.Name, seq),
		Title:        fmt.Sprintf("%s has RECOVERED%s", t.Name, downFor),
		Body:         fmt.Sprintf("%s responding in %s.", t.URL, latency.Round(time.Millisecond)),
		URL:          t.URL,
		Source:       "uptime:probes",
		PublishedAt:  now,
		Markets:      []model.MarketType{model.MarketInfra},
		Importance:   model.ImportanceMedium,
		MatchedRules: []string{"uptime:recovered"},
	}, true
}
