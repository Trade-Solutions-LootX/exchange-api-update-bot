package sources

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func decode(t *testing.T, s string) any {
	t.Helper()
	// Match the production decoder: UseNumber keeps numbers as json.Number so
	// tests exercise the same precision behavior as jsonSource.Fetch.
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return v
}

func TestGetStringLargeIntIDNoPrecisionLoss(t *testing.T) {
	// Two 18-digit ids differing only in the last digit — both exceed 2^53, so a
	// float64 decode would collapse them to the same value.
	a := decode(t, `{"id": 100000000000000001}`)
	b := decode(t, `{"id": 100000000000000002}`)
	ga, gb := getString(a, "id"), getString(b, "id")
	if ga == gb {
		t.Fatalf("large ids collided: %q == %q (precision lost)", ga, gb)
	}
	if ga != "100000000000000001" || gb != "100000000000000002" {
		t.Fatalf("ids not preserved exactly: %q %q", ga, gb)
	}
}

func TestExtractArrayEmptyFlattenIsNonNil(t *testing.T) {
	// Binance-style path where the catalog exists but its articles array is
	// empty (a quiet period). Must be a non-nil empty slice, NOT nil (which the
	// caller treats as "endpoint shape changed").
	tree := decode(t, `{"data":{"catalogs":[{"articles":[]}]}}`)
	got := extractArray(tree, "data.catalogs[].articles")
	if got == nil {
		t.Fatalf("empty-but-present flatten array must be non-nil (got nil)")
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 items, got %d", len(got))
	}
}

func TestExtractArrayFlat(t *testing.T) {
	tree := decode(t, `{"data":{"items":[{"id":1},{"id":2}]}}`)
	got := extractArray(tree, "data.items")
	if len(got) != 2 {
		t.Fatalf("want 2 items, got %d", len(got))
	}
}

func TestExtractArrayNestedFlatten(t *testing.T) {
	// Binance-style: data.catalogs[].articles
	tree := decode(t, `{"data":{"catalogs":[
		{"articles":[{"id":1},{"id":2}]},
		{"articles":[{"id":3}]}
	]}}`)
	got := extractArray(tree, "data.catalogs[].articles")
	if len(got) != 3 {
		t.Fatalf("want 3 flattened items, got %d", len(got))
	}
}

func TestExtractArrayLeadingArray(t *testing.T) {
	// OKX-style: data[].details
	tree := decode(t, `{"data":[{"details":[{"title":"a"},{"title":"b"}]}]}`)
	got := extractArray(tree, "data[].details")
	if len(got) != 2 {
		t.Fatalf("want 2 items, got %d", len(got))
	}
}

func TestExtractArrayMissingPath(t *testing.T) {
	tree := decode(t, `{"data":{}}`)
	if got := extractArray(tree, "data.nope"); got != nil {
		t.Fatalf("want nil for missing path, got %v", got)
	}
}

func TestGetStringNumberPreservesIntID(t *testing.T) {
	item := decode(t, `{"id": 123456789012, "name":"x"}`)
	if got := getString(item, "id"); got != "123456789012" {
		t.Fatalf("want exact int id, got %q", got)
	}
}

func TestGetStringNested(t *testing.T) {
	item := decode(t, `{"a":{"b":{"c":"deep"}}}`)
	if got := getString(item, "a.b.c"); got != "deep" {
		t.Fatalf("want deep, got %q", got)
	}
}

func TestParseTimeUnixMs(t *testing.T) {
	got := parseTime(float64(1_700_000_000_000), "unix_ms")
	want := time.UnixMilli(1_700_000_000_000).UTC()
	if !got.Equal(want) {
		t.Fatalf("unix_ms: got %v want %v", got, want)
	}
}

func TestParseTimeRFC3339(t *testing.T) {
	got := parseTime("2026-07-12T14:03:00Z", "rfc3339")
	if got.Year() != 2026 || got.Month() != time.July || got.Day() != 12 {
		t.Fatalf("rfc3339 parse wrong: %v", got)
	}
}

func TestParseTimeHeuristicEpochString(t *testing.T) {
	// A millisecond epoch delivered as a string, unknown layout.
	got := parseTime("1700000000000", "")
	if got.IsZero() {
		t.Fatalf("expected heuristic epoch parse to succeed")
	}
	if got.Year() != 2023 {
		t.Fatalf("expected 2023, got %v", got)
	}
}

func TestParseTimeInvalid(t *testing.T) {
	if got := parseTime("not-a-date", "rfc3339"); !got.IsZero() {
		t.Fatalf("want zero time for junk, got %v", got)
	}
}
