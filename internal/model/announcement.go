// Package model defines the core domain types shared across the bot: an
// exchange announcement, the market types it can touch, and how important
// (actionable) it is for an API consumer.
package model

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"time"
)

// MarketType is the kind of market an announcement is about. A single
// announcement may relate to several (e.g. "spot & margin delisting").
type MarketType string

const (
	MarketSpot    MarketType = "spot"
	MarketFutures MarketType = "futures" // USDⓈ / coin-margined perpetuals & delivery
	MarketMargin  MarketType = "margin"
	MarketOptions MarketType = "options"
	MarketEarn    MarketType = "earn" // staking, savings, launchpool, yield
	MarketConvert MarketType = "convert"
	MarketWallet  MarketType = "wallet" // deposits/withdrawals, networks, chains
	MarketAPI     MarketType = "api"    // API / websocket / connectivity itself
	MarketGeneral MarketType = "general"

	// Infrastructure / hosting providers (Render, DigitalOcean, Vultr …).
	MarketInfra   MarketType = "infra"   // status incidents & scheduled maintenance
	MarketBilling MarketType = "billing" // account balance / payments due
)

// Importance encodes how urgently a human integrator must react. It is
// ordered: higher values are more urgent.
type Importance int

const (
	// ImportanceLow: marketing, promotions, campaigns — no action needed.
	ImportanceLow Importance = iota
	// ImportanceMedium: new products/features/listings — informational.
	ImportanceMedium
	// ImportanceHigh: delistings, network upgrades, maintenance — may need action.
	ImportanceHigh
	// ImportanceCritical: API/endpoint changes that REQUIRE code changes
	// (deprecations, breaking changes, sunsets, mandatory migrations).
	ImportanceCritical
)

// String renders an Importance for logs and messages.
func (i Importance) String() string {
	switch i {
	case ImportanceCritical:
		return "critical"
	case ImportanceHigh:
		return "high"
	case ImportanceMedium:
		return "medium"
	default:
		return "low"
	}
}

// Emoji is a compact visual severity marker for Telegram.
func (i Importance) Emoji() string {
	switch i {
	case ImportanceCritical:
		return "🔴"
	case ImportanceHigh:
		return "🟠"
	case ImportanceMedium:
		return "🟡"
	default:
		return "⚪️"
	}
}

// Announcement is a single normalized item from any exchange source.
type Announcement struct {
	// Exchange is the lowercase exchange slug, e.g. "binance".
	Exchange string
	// NativeID is the exchange's own identifier when available; used for
	// stable deduplication. Empty is allowed (we fall back to a URL hash).
	NativeID string
	Title    string
	URL      string
	// Body is an optional short preview/summary text (may be empty).
	Body string
	// PublishedAt is the item's publish time; zero if the source omits it.
	PublishedAt time.Time

	// Source identifies which adapter produced the item, e.g. "binance:api-updates".
	Source string

	// The following are filled by the classifier.
	Markets    []MarketType
	Importance Importance
	// MatchedRules explains WHY the importance was assigned (for auditability).
	MatchedRules []string
}

// DedupKey returns a stable key used to decide whether we've already seen and
// sent this announcement. It prefers the exchange-native id and falls back to a
// hash of the URL (and title, in case the URL is not stable).
func (a Announcement) DedupKey() string {
	if a.NativeID != "" {
		return a.Exchange + ":" + a.NativeID
	}
	h := sha256.Sum256([]byte(strings.TrimSpace(a.URL) + "\n" + strings.TrimSpace(a.Title)))
	return a.Exchange + ":h:" + hex.EncodeToString(h[:8])
}

// IsAPIRelated reports whether this announcement concerns the exchange API.
// It is true when the item came from a dedicated API-updates feed (those always
// carry the "api" market tag) OR the classifier detected API / websocket /
// endpoint / SDK content. Used by the API_ONLY delivery filter and `check --api`.
func (a Announcement) IsAPIRelated() bool {
	return a.hasMarket(MarketAPI)
}

// IsOperational reports whether this item is infrastructure status or billing —
// hosting-provider maintenance/incidents or a payment-due alert. These bypass
// the API_ONLY filter because they are operationally actionable, not exchange
// listing/promo noise.
func (a Announcement) IsOperational() bool {
	return a.hasMarket(MarketInfra) || a.hasMarket(MarketBilling)
}

func (a Announcement) hasMarket(want MarketType) bool {
	for _, m := range a.Markets {
		if m == want {
			return true
		}
	}
	return false
}

// MarketsString renders the market types as a compact, human-friendly label.
func (a Announcement) MarketsString() string {
	if len(a.Markets) == 0 {
		return string(MarketGeneral)
	}
	parts := make([]string, 0, len(a.Markets))
	seen := map[MarketType]bool{}
	for _, m := range a.Markets {
		if seen[m] {
			continue
		}
		seen[m] = true
		parts = append(parts, string(m))
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}
