package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"exchangebot/internal/model"
)

// Server monitoring that reuses the SAME handle our trading-terminal servers
// already expose (as in trading-terminal-cloud's server-probe): a bearer-authed
//   GET <base>/v1/system/metrics
// returning CPU / RAM / disk / load. No provider agent required — works for any
// box running the metrics endpoint (DigitalOcean, Vultr, Render, bare metal).

// SystemMetrics mirrors the /v1/system/metrics JSON verbatim.
type SystemMetrics struct {
	Hostname   string `json:"hostname"`
	UptimeSecs int64  `json:"uptime_secs"`
	CPU        struct {
		Cores    int     `json:"cores"`
		UsagePct float64 `json:"usage_pct"`
		PeakPct  float64 `json:"peak_pct"`
		Load1    float64 `json:"load1"`
		Load5    float64 `json:"load5"`
		Load15   float64 `json:"load15"`
	} `json:"cpu"`
	Mem struct {
		UsedPct float64 `json:"used_pct"`
	} `json:"mem"`
	Disk struct {
		Mount   string  `json:"mount"`
		UsedPct float64 `json:"used_pct"`
	} `json:"disk"`
}

// ServerTarget is one monitored box.
type ServerTarget struct {
	Name  string
	URL   string
	Token string
}

// ParseServerTargets parses "name|url|token" entries separated by ';'.
func ParseServerTargets(spec string) []ServerTarget {
	var out []ServerTarget
	for _, entry := range strings.Split(spec, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.SplitN(entry, "|", 3)
		if len(parts) < 2 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		url := strings.TrimSpace(parts[1])
		token := ""
		if len(parts) == 3 {
			token = strings.TrimSpace(parts[2])
		}
		if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
			url = "https://" + url
		}
		if name == "" {
			name = hostOf(url)
		}
		out = append(out, ServerTarget{Name: name, URL: url, Token: token})
	}
	return out
}

// FetchServerMetrics probes one server's /v1/system/metrics (used by the source
// and by the /servers command).
func FetchServerMetrics(ctx context.Context, client *http.Client, t ServerTarget) (*SystemMetrics, error) {
	url := strings.TrimRight(t.URL, "/") + "/v1/system/metrics"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if t.Token != "" {
		req.Header.Set("Authorization", "Bearer "+t.Token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var m SystemMetrics
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, fmt.Errorf("bad metrics json: %w", err)
	}
	return &m, nil
}

// serverMetricsSource alerts when a server's CPU/RAM/disk crosses a threshold
// (and when it recovers or becomes unreachable). SystemSource → transition
// alerts, no seen-store.
type serverMetricsSource struct {
	targets  []ServerTarget
	cpuPct   float64
	memPct   float64
	diskPct  float64
	interval time.Duration
	client   *http.Client

	mu    sync.Mutex
	over  map[string]bool // "<name>|<metric>" over threshold / unreachable
	known map[string]bool
	seq   map[string]int
}

// NewServerMetricsSource builds the server monitor.
func NewServerMetricsSource(targets []ServerTarget, cpuPct, memPct, diskPct float64, interval, timeout time.Duration) Source {
	return &serverMetricsSource{
		targets:  targets,
		cpuPct:   cpuPct,
		memPct:   memPct,
		diskPct:  diskPct,
		interval: interval,
		client:   &http.Client{Timeout: timeout},
		over:     map[string]bool{},
		known:    map[string]bool{},
		seq:      map[string]int{},
	}
}

func (s *serverMetricsSource) Exchange() string        { return "server" }
func (s *serverMetricsSource) Name() string            { return "server:metrics" }
func (s *serverMetricsSource) Interval() time.Duration { return s.interval }
func (s *serverMetricsSource) IsSystem() bool          { return true }

func (s *serverMetricsSource) Fetch(ctx context.Context) ([]model.Announcement, error) {
	now := time.Now().UTC()
	var alerts []model.Announcement
	for _, t := range s.targets {
		m, err := FetchServerMetrics(ctx, s.client, t)
		if err != nil {
			if a, ok := s.transition(t.Name, "reach", true, fmt.Sprintf("%s недоступен (%s)", t.Name, err.Error()), now); ok {
				alerts = append(alerts, a)
			}
			continue
		}
		// Reachable again?
		if a, ok := s.transition(t.Name, "reach", false, "", now); ok {
			alerts = append(alerts, a)
		}
		if a, ok := s.check(t.Name, "CPU", "cpu", m.CPU.UsagePct, s.cpuPct, now); ok {
			alerts = append(alerts, a)
		}
		if a, ok := s.check(t.Name, "RAM", "mem", m.Mem.UsedPct, s.memPct, now); ok {
			alerts = append(alerts, a)
		}
		if a, ok := s.check(t.Name, "диск", "disk", m.Disk.UsedPct, s.diskPct, now); ok {
			alerts = append(alerts, a)
		}
	}
	return alerts, nil
}

// check alerts when a resource crosses its threshold (and on recovery).
func (s *serverMetricsSource) check(server, label, metric string, pct, threshold float64, now time.Time) (model.Announcement, bool) {
	key := server + "|" + metric
	s.mu.Lock()
	defer s.mu.Unlock()
	nowOver := pct >= threshold
	prevOver, known := s.over[key], s.known[key]
	s.known[key] = true
	if known && prevOver == nowOver {
		return model.Announcement{}, false
	}
	if !known && !nowOver {
		s.over[key] = false
		return model.Announcement{}, false
	}
	s.over[key] = nowOver
	s.seq[key]++
	if nowOver {
		return sysAlert("server-load-over:"+key, s.seq[key],
			fmt.Sprintf("🖥 %s: %s высокая — %.0f%% (порог %.0f%%)", server, label, pct, threshold),
			model.ImportanceHigh, now), true
	}
	return sysAlert("server-load-ok:"+key, s.seq[key],
		fmt.Sprintf("🖥 %s: %s в норме — %.0f%%", server, label, pct),
		model.ImportanceMedium, now), true
}

// transition handles the reachable/unreachable edge for a server.
func (s *serverMetricsSource) transition(server, metric string, down bool, downMsg string, now time.Time) (model.Announcement, bool) {
	key := server + "|" + metric
	s.mu.Lock()
	defer s.mu.Unlock()
	prevDown, known := s.over[key], s.known[key]
	s.known[key] = true
	if known && prevDown == down {
		return model.Announcement{}, false
	}
	if !known && !down {
		s.over[key] = false
		return model.Announcement{}, false
	}
	s.over[key] = down
	s.seq[key]++
	if down {
		return sysAlert("server-unreach:"+key, s.seq[key], "🖥 "+downMsg, model.ImportanceHigh, now), true
	}
	return sysAlert("server-reach:"+key, s.seq[key], "🖥 "+server+" снова доступен", model.ImportanceMedium, now), true
}

func sysAlert(id string, seq int, title string, imp model.Importance, now time.Time) model.Announcement {
	return model.Announcement{
		Exchange:     "server",
		NativeID:     fmt.Sprintf("%s:%d", id, seq),
		Title:        title,
		Source:       "server:metrics",
		PublishedAt:  now,
		Markets:      []model.MarketType{model.MarketInfra},
		Importance:   imp,
		MatchedRules: []string{"server:metrics"},
	}
}
