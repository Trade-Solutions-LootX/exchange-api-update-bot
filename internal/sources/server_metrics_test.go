package sources

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"exchangebot/internal/model"
)

func TestParseServerTargets(t *testing.T) {
	got := ParseServerTargets("web|https://api.x.com|TOK1 ; oi|http://1.2.3.4:8081|TOK2 ; bad")
	if len(got) != 2 {
		t.Fatalf("want 2 targets, got %d: %+v", len(got), got)
	}
	if got[0].Name != "web" || got[0].URL != "https://api.x.com" || got[0].Token != "TOK1" {
		t.Errorf("target0 = %+v", got[0])
	}
	if got[1].URL != "http://1.2.3.4:8081" || got[1].Token != "TOK2" {
		t.Errorf("target1 = %+v", got[1])
	}
}

func TestFetchServerMetrics(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/system/metrics" {
			w.WriteHeader(404)
			return
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			w.WriteHeader(401)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"hostname": "box1", "uptime_secs": 3600,
			"cpu":  map[string]any{"cores": 4, "usage_pct": 91.5, "load1": 2.1, "load5": 1.8, "load15": 1.2},
			"mem":  map[string]any{"used_pct": 40.0},
			"disk": map[string]any{"mount": "/", "used_pct": 55.0},
		})
	}))
	defer srv.Close()

	m, err := FetchServerMetrics(context.Background(), srv.Client(), ServerTarget{Name: "b", URL: srv.URL, Token: "secret"})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if m.Hostname != "box1" || m.CPU.UsagePct != 91.5 || m.Mem.UsedPct != 40 {
		t.Fatalf("decoded wrong: %+v", m)
	}
}

func TestServerMetricsThresholdTransitions(t *testing.T) {
	s := NewServerMetricsSource(nil, 85, 90, 90, time.Minute, time.Second).(*serverMetricsSource)
	now := billingNow

	// First healthy sample → silent.
	if _, ok := s.check("web", "CPU", "cpu", 40, 85, now); ok {
		t.Fatal("healthy first sample must be silent")
	}
	// Crosses CPU threshold → HIGH alert (infra market → bypasses API_ONLY).
	a, ok := s.check("web", "CPU", "cpu", 92, 85, now)
	if !ok || a.Importance != model.ImportanceHigh || !a.IsOperational() {
		t.Fatalf("over-threshold should alert HIGH+operational, got ok=%v %+v", ok, a)
	}
	// Still over → no repeat.
	if _, ok := s.check("web", "CPU", "cpu", 95, 85, now); ok {
		t.Fatal("no repeat while still over")
	}
	// Recover → medium notice.
	a2, ok := s.check("web", "CPU", "cpu", 50, 85, now)
	if !ok || a2.Importance != model.ImportanceMedium {
		t.Fatalf("recovery should be medium, got %+v", a2)
	}
}
