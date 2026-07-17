package telegram

import (
	"strings"
	"testing"
	"time"

	"exchangebot/internal/model"
)

func TestFormatCriticalIncludesWarning(t *testing.T) {
	a := model.Announcement{
		Exchange:    "binance",
		Title:       "Deprecation of <legacy> API & endpoints",
		URL:         "https://ex.com/a?b=1&c=2",
		Importance:  model.ImportanceCritical,
		Markets:     []model.MarketType{model.MarketAPI, model.MarketFutures},
		PublishedAt: time.Date(2026, 7, 12, 14, 3, 0, 0, time.UTC),
	}
	out := Format(a, false)

	if !strings.Contains(out, "CRITICAL") {
		t.Errorf("missing severity label: %s", out)
	}
	if !strings.Contains(out, "requires API code changes") {
		t.Errorf("critical warning missing")
	}
	// HTML special characters must be escaped, not raw.
	if strings.Contains(out, "<legacy>") {
		t.Errorf("angle brackets not escaped: %s", out)
	}
	if !strings.Contains(out, "&lt;legacy&gt;") {
		t.Errorf("expected escaped title")
	}
	if !strings.Contains(out, "&amp;") {
		t.Errorf("expected escaped ampersand in url/title")
	}
	if !strings.Contains(out, "🔗") {
		t.Errorf("expected link marker")
	}
}

func TestFormatBackfillTagged(t *testing.T) {
	a := model.Announcement{Exchange: "okx", Title: "Old news", Importance: model.ImportanceMedium}
	out := Format(a, true)
	if !strings.Contains(out, "backfill") {
		t.Errorf("backfill message should be tagged: %s", out)
	}
}

func TestFormatTruncatesLongMessage(t *testing.T) {
	a := model.Announcement{
		Exchange:   "mexc",
		Title:      strings.Repeat("A", 6000),
		Importance: model.ImportanceLow,
	}
	out := Format(a, false)
	if len([]rune(out)) > 4100 {
		t.Errorf("message not truncated: %d runes", len([]rune(out)))
	}
}

func TestSafeTruncateNeverSplitsAnHTMLTag(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 800; i++ { // ~9600 runes, well over the cap
		sb.WriteString("<b>line</b>\n")
	}
	out := safeTruncate(sb.String(), 4000)

	if r := []rune(out); len(r) > 4002 {
		t.Fatalf("not truncated: %d runes", len(r))
	}
	// A split tag would leave an unbalanced count of <b> vs </b>.
	if strings.Count(out, "<b>") != strings.Count(out, "</b>") {
		t.Fatalf("truncation split an HTML tag: %q…", out[len(out)-40:])
	}
}
