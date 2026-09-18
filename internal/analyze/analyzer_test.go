package analyze

import (
	"strings"
	"testing"
	"time"

	"exchangebot/internal/model"
)

func TestExtractJSON(t *testing.T) {
	cases := map[string]string{
		`{"a":1}`:                 `{"a":1}`,
		"```json\n{\"a\":1}\n```": `{"a":1}`,
		"Вот ответ:\n{\"a\": {\"b\": 2}}\nГотово.": `{"a": {"b": 2}}`,
		"no json here": "",
	}
	for in, want := range cases {
		if got := extractJSON(in); got != want {
			t.Errorf("extractJSON(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseVerdict(t *testing.T) {
	raw := "```json\n" + `{"api_related": true, "importance": "HIGH", "needs_terminal_change": true,
	  "summary": "s", "changes": ["c1"], "actions": ["a1"], "affected": ["rest"], "effective_at": "2026-10-01", "task_title": "t"}` + "\n```"
	v, err := parseVerdict(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !v.APIRelated || v.Importance != "high" || !v.NeedsTerminalChange || v.TaskTitle != "t" {
		t.Errorf("unexpected verdict: %+v", v)
	}
	if _, err := parseVerdict("nope"); err == nil {
		t.Error("expected error for non-JSON")
	}
	v2, _ := parseVerdict(`{"importance":"weird"}`)
	if v2.Importance != "none" {
		t.Errorf("unknown importance should normalise to none, got %q", v2.Importance)
	}
}

func TestShouldFileTask(t *testing.T) {
	a := &Analyzer{opts: Options{MinTaskImportance: "high"}}
	ok := &Verdict{APIRelated: true, NeedsTerminalChange: true, Importance: "critical"}
	if !a.shouldFileTask(ok) {
		t.Error("critical API change must file a task")
	}
	for _, v := range []*Verdict{
		{APIRelated: true, NeedsTerminalChange: true, Importance: "medium"},
		{APIRelated: false, NeedsTerminalChange: true, Importance: "critical"},
		{APIRelated: true, NeedsTerminalChange: false, Importance: "critical"},
	} {
		if a.shouldFileTask(v) {
			t.Errorf("should not file task for %+v", v)
		}
	}
}

func TestHTMLToText(t *testing.T) {
	in := `<html><head><title>x</title><style>.a{}</style></head><body>
	<script>var a = "<p>no</p>";</script>
	<h1>Deprecation &amp; Migration</h1><p>Endpoint <code>/api/v1/order</code> will be removed.</p>
	<ul><li>Item one</li><li>Item two</li></ul></body></html>`
	got := HTMLToText(in)
	if strings.Contains(got, "var a") || strings.Contains(got, ".a{}") || strings.Contains(got, "<") {
		t.Errorf("markup leaked: %q", got)
	}
	for _, want := range []string{"Deprecation & Migration", "/api/v1/order", "Item one\nItem two"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestTaskMarkdown(t *testing.T) {
	ann := model.Announcement{Exchange: "okx", Title: "Checksum field deprecation", URL: "https://okx.com/x", Source: "okx:api", PublishedAt: time.Date(2026, 5, 21, 6, 51, 0, 0, time.UTC)}
	v := &Verdict{Importance: "critical", Summary: "Поле checksum уходит.", Changes: []string{"books channel: checksum → удалено"}, Actions: []string{"Убрать проверку checksum в OkxOrderBook"}, Affected: []string{"websocket", "market-data"}, EffectiveAt: "2026-06-30", TaskTitle: "OKX: убрать checksum из стакана"}
	name := taskName(ann, v)
	if name != "[OKX] OKX: убрать checksum из стакана" {
		t.Errorf("name = %q", name)
	}
	md := taskMarkdown(ann, v)
	for _, want := range []string{"## Что изменилось", "## Что сделать в терминале", "- Убрать проверку checksum", "2026-06-30", "https://okx.com/x", "2026-05-21 06:51 UTC"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q:\n%s", want, md)
		}
	}
	// Empty task title falls back to the announcement title.
	v.TaskTitle = ""
	if got := taskName(ann, v); got != "[OKX] Checksum field deprecation" {
		t.Errorf("fallback name = %q", got)
	}
}

func TestBuildUserPrompt(t *testing.T) {
	ann := model.Announcement{Exchange: "binance", Title: "T", URL: "u", Source: "binance:api", Body: "preview"}
	p := buildUserPrompt(ann, "", 100)
	if !strings.Contains(p, "получить не удалось") || !strings.Contains(p, "preview") {
		t.Errorf("prompt without article: %q", p)
	}
	p = buildUserPrompt(ann, strings.Repeat("x", 500), 100)
	if strings.Contains(p, "получить не удалось") || !strings.Contains(p, "…") {
		t.Errorf("article should be present and truncated: %q", p)
	}
}

func TestIsNoise(t *testing.T) {
	a := &Analyzer{}
	noise := []model.Announcement{
		{Source: "binance:new-listings", Title: "Binance Will List Foo (FOO)"},
		{Source: "bybit:announcements", Title: "Bybit Launchpool: Stake USDT to earn BAR"},
		{Source: "okx:announcements", Title: "OKX to delist several spot trading pairs"},
		{Source: "bitget:announcements", Title: "Adjustment of leverage and tick size for XYZUSDT"},
		{Source: "kucoin:announcements", Title: "Trading competition: share 100,000 USDT prize pool"},
	}
	for _, n := range noise {
		if !a.isNoise(n) {
			t.Errorf("should be noise: %q", n.Title)
		}
	}
	keep := []model.Announcement{
		{Source: "binance:api-updates", Title: "Binance Spot API: depth stream now supports 5000 levels"},
		{Source: "binance:delisting", Title: "Delisting of the /sapi/v1/margin/loan endpoint"},
		{Source: "bybit:announcements", Title: "Unified Trading Account migration deadline"},
		{Source: "okx:announcements", Title: "WebSocket order book channel update: checksum removed"},
		{Source: "gate:announcements", Title: "Rate limit changes for futures order placement"},
		{Source: "aster:api-docs", Title: "add batch order endpoint"},
	}
	for _, k := range keep {
		if a.isNoise(k) {
			t.Errorf("should NOT be noise: %q", k.Title)
		}
	}
}

func TestMergeComment(t *testing.T) {
	ann := model.Announcement{Exchange: "bybit", Title: "docs: remove checksum from orderbook", URL: "https://github.com/bybit-exchange/docs/commit/abc", Source: "bybit:api-docs"}
	v := &Verdict{Importance: "high", Summary: "Убрали checksum.", Changes: []string{"orderbook.50: поле checksum удалено"}, Actions: []string{"Отключить проверку"}, EffectiveAt: "2026-10-01"}
	c := mergeComment(ann, v)
	for _, want := range []string{"https://github.com/bybit-exchange/docs/commit/abc", "bybit:api-docs", "checksum удалено", "Отключить проверку", "2026-10-01"} {
		if !strings.Contains(c, want) {
			t.Errorf("comment missing %q:\n%s", want, c)
		}
	}
}
