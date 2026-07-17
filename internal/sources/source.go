// Package sources defines the pluggable adapter model for pulling
// announcements from each exchange. An adapter knows how to fetch and normalize
// items for one feed; the poller drives them on a schedule.
//
// Two generic adapters cover almost every exchange declaratively:
//   - JSONSource: a configurable JSON REST endpoint (most CEXes expose one).
//   - RSSSource:  an RSS/Atom feed.
//
// Exchange-specific quirks are expressed as data in specs.go, not as bespoke
// code, which keeps adapters testable and easy to extend.
package sources

import (
	"context"
	"log/slog"
	"time"

	"exchangebot/internal/httpx"
	"exchangebot/internal/model"
)

// Source is one pollable announcement feed.
type Source interface {
	// Exchange is the lowercase exchange slug.
	Exchange() string
	// Name is a unique, stable identifier, e.g. "binance:api-updates".
	Name() string
	// Interval is how often this feed should be polled.
	Interval() time.Duration
	// Fetch returns the current items (newest-first not required; the poller
	// sorts and dedups). Errors are logged and retried on the next tick.
	Fetch(ctx context.Context) ([]model.Announcement, error)
}

// Deps are shared dependencies handed to every adapter.
type Deps struct {
	HTTP *httpx.Client
	Log  *slog.Logger
}

// Build constructs every configured source, filtered by the enabled predicate.
// enabled(slug) reports whether an exchange should be included.
func Build(deps Deps, enabled func(slug string) bool, defaultInterval time.Duration) []Source {
	var out []Source
	for _, spec := range JSONSpecs() {
		if !enabled(spec.Exchange) {
			continue
		}
		s := spec
		if s.Interval == 0 {
			s.Interval = defaultInterval
		}
		out = append(out, newJSONSource(s, deps))
	}
	for _, spec := range RSSSpecs() {
		if !enabled(spec.Exchange) {
			continue
		}
		s := spec
		if s.Interval == 0 {
			s.Interval = defaultInterval
		}
		out = append(out, newRSSSource(s, deps))
	}
	return out
}
