package sources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"exchangebot/internal/model"
)

func TestParseUptimeTargets(t *testing.T) {
	got := ParseUptimeTargets("api=https://api.x.com/health, https://plain.com , web=web.example.com")
	if len(got) != 3 {
		t.Fatalf("want 3 targets, got %d: %+v", len(got), got)
	}
	if got[0].Name != "api" || got[0].URL != "https://api.x.com/health" {
		t.Errorf("target0 = %+v", got[0])
	}
	if got[1].Name != "plain.com" { // name derived from host
		t.Errorf("target1 name = %q", got[1].Name)
	}
	if got[2].URL != "https://web.example.com" { // scheme added
		t.Errorf("target2 url = %q", got[2].URL)
	}
}

func TestUptimeTransitions(t *testing.T) {
	var up bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if up {
			w.WriteHeader(200)
		} else {
			w.WriteHeader(503)
		}
	}))
	defer srv.Close()

	u := NewUptimeSource([]UptimeTarget{{Name: "svc", URL: srv.URL}}, time.Minute, 2*time.Second).(*uptimeSource)
	ctx := context.Background()

	// First probe healthy → recorded silently, no alert.
	up = true
	if a, _ := u.Fetch(ctx); len(a) != 0 {
		t.Fatalf("first healthy probe should not alert, got %d", len(a))
	}
	// Goes down → DOWN alert.
	up = false
	a, _ := u.Fetch(ctx)
	if len(a) != 1 || a[0].Importance != model.ImportanceHigh {
		t.Fatalf("expected 1 DOWN alert (high), got %+v", a)
	}
	// Still down → no repeat.
	if a2, _ := u.Fetch(ctx); len(a2) != 0 {
		t.Fatalf("no repeat while still down, got %d", len(a2))
	}
	// Recovers → RECOVERED alert.
	up = true
	a3, _ := u.Fetch(ctx)
	if len(a3) != 1 || a3[0].MatchedRules[0] != "uptime:recovered" {
		t.Fatalf("expected recovery alert, got %+v", a3)
	}
	if !a3[0].IsOperational() {
		t.Errorf("uptime alert must be operational (bypass API_ONLY)")
	}
}
