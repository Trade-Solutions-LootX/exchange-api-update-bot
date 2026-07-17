package sources

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"exchangebot/internal/httpx"
	"exchangebot/internal/model"
)

func testDeps() Deps {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return Deps{HTTP: httpx.New(5*time.Second, "test-agent", log), Log: log}
}

func TestJSONSourceBinanceShape(t *testing.T) {
	// Mimic Binance article/list/query with catalogId.
	body := `{"code":"000000","data":{"articles":[
		{"id":123,"code":"abc123","title":"Notice on Deprecation of Legacy API Endpoints","releaseDate":1720792980000},
		{"id":124,"code":"def456","title":"Binance Will List FooCoin (FOO)","releaseDate":1720706580000}
	]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	spec := JSONSpec{
		Exchange:       "binance",
		Name:           "api-updates",
		URL:            srv.URL,
		ItemsPath:      "data.articles",
		IDField:        "id",
		TitleField:     "title",
		URLField:       "code",
		URLPrefix:      "https://www.binance.com/en/support/announcement/",
		TimeField:      "releaseDate",
		TimeLayout:     "unix_ms",
		APIUpdatesFeed: true,
	}
	s := newJSONSource(spec, testDeps())

	items, err := s.Fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d", len(items))
	}

	a := items[0]
	if a.NativeID != "123" {
		t.Errorf("NativeID = %q", a.NativeID)
	}
	if a.URL != "https://www.binance.com/en/support/announcement/abc123" {
		t.Errorf("URL = %q", a.URL)
	}
	if a.Importance != model.ImportanceCritical {
		t.Errorf("deprecation should be critical, got %s (%v)", a.Importance, a.MatchedRules)
	}
	if a.PublishedAt.IsZero() {
		t.Errorf("expected parsed releaseDate")
	}

	// Second item is a listing (medium) but the API-updates floor lifts it to high.
	b := items[1]
	if b.Importance < model.ImportanceHigh {
		t.Errorf("api-updates feed floor not applied: %s", b.Importance)
	}

	// DedupKey must be stable and exchange-prefixed.
	if a.DedupKey() != "binance:123" {
		t.Errorf("DedupKey = %q", a.DedupKey())
	}
}

func TestJSONSourceDefaultMarketsAndFloor(t *testing.T) {
	// A hosting status feed: force the "infra" market and floor to HIGH.
	body := `{"scheduled_maintenances":[{"id":"m1","name":"NYC2 Network Maintenance","shortlink":"https://stspg.io/abc","scheduled_for":"2026-07-20T10:30:00Z"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	spec := JSONSpec{
		Exchange:        "digitalocean",
		Name:            "maintenance",
		URL:             srv.URL,
		ItemsPath:       "scheduled_maintenances",
		IDField:         "id",
		TitleField:      "name",
		URLField:        "shortlink",
		TimeField:       "scheduled_for",
		TimeLayout:      "rfc3339",
		DefaultMarkets:  []model.MarketType{model.MarketInfra},
		ImportanceFloor: model.ImportanceHigh,
	}
	s := newJSONSource(spec, testDeps())
	items, err := s.Fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d", len(items))
	}
	a := items[0]
	if !a.IsOperational() || a.MarketsString() != "infra" {
		t.Errorf("expected forced infra market, got %v", a.Markets)
	}
	if a.Importance < model.ImportanceHigh {
		t.Errorf("importance floor not applied: %s", a.Importance)
	}
}

func TestJSONSourceMissingPathErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":{}}`)
	}))
	defer srv.Close()

	spec := JSONSpec{Exchange: "x", Name: "y", URL: srv.URL, ItemsPath: "data.items", TitleField: "t"}
	s := newJSONSource(spec, testDeps())
	if _, err := s.Fetch(context.Background()); err == nil {
		t.Fatalf("expected error when items path is absent")
	}
}

func TestJSONSourceOKXShape(t *testing.T) {
	body := `{"code":"0","data":[{"totalPage":"1","details":[
		{"title":"Adjustment of WebSocket Rate Limits","url":"https://okx.com/help/1","pTime":"1720792980000","annType":"announcements-api"}
	]}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	spec := JSONSpec{
		Exchange:   "okx",
		Name:       "announcements",
		URL:        srv.URL,
		ItemsPath:  "data[].details",
		IDField:    "url",
		TitleField: "title",
		URLField:   "url",
		TimeField:  "pTime",
		TimeLayout: "unix_ms",
	}
	s := newJSONSource(spec, testDeps())
	items, err := s.Fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d", len(items))
	}
	if items[0].Importance != model.ImportanceCritical {
		t.Errorf("websocket+rate limit should be critical, got %s", items[0].Importance)
	}
	if items[0].PublishedAt.IsZero() {
		t.Errorf("expected string-epoch pTime to parse")
	}
}
