// Package classify assigns a market type and an actionability/importance level
// to a raw announcement using an explainable, keyword-driven rule set. Every
// decision records which rules matched so results can be audited and tested.
//
// Design goals:
//   - Deterministic and dependency-free (no ML, no network).
//   - Explainable: MatchedRules tells you exactly why something was flagged.
//   - Tunable: rules are data, not code branches, so they are easy to extend
//     and unit-test.
package classify

import (
	"regexp"
	"strings"

	"exchangebot/internal/model"
)

// rule is a single keyword/phrase test contributing to a classification.
type rule struct {
	// name is a short identifier surfaced in MatchedRules.
	name string
	// re matches against the lowercased "title. body" text.
	re *regexp.Regexp
}

func mk(name string, patterns ...string) rule {
	// Word-ish boundaries: match the phrase as a substring but prefer whole
	// tokens where it matters. We keep it permissive because exchange copy is
	// inconsistent ("API", "api-key", "APIs").
	quoted := make([]string, len(patterns))
	for i, p := range patterns {
		quoted[i] = regexp.QuoteMeta(p)
	}
	return rule{name: name, re: regexp.MustCompile("(?i)(" + strings.Join(quoted, "|") + ")")}
}

// criticalRules capture changes that REQUIRE an integrator to modify code:
// API deprecations, breaking changes, endpoint/websocket sunsets, forced
// migrations, auth/security changes affecting API access.
var criticalRules = []rule{
	mk("api-deprecation", "deprecat", "discontinu", "shut down", "shutdown", "shutting down", "will be removed", "end of life", "sunset", "no longer be supported", "no longer supported", "will be retired", "phased out", "phase out"),
	mk("api-breaking", "breaking change", "breaking changes", "incompatible change", "not backward compatible", "backward-incompatible"),
	mk("api-endpoint-change", "endpoint change", "endpoint update", "new endpoint", "endpoint will", "api change", "api update", "api upgrade", "interface adjustment", "interface change", "interface upgrade"),
	mk("api-migration", "migrate to", "migration to", "must migrate", "please migrate", "upgrade your", "mandatory upgrade", "action required", "action is required", "required action"),
	mk("websocket-change", "websocket", "web socket", "ws stream", "stream will", "push data adjustment"),
	mk("auth-change", "api key", "api-key", "signature", "authentication change", "ip whitelist", "domain change", "domain migration", "base url", "certificate"),
	mk("ratelimit-change", "rate limit", "rate-limit", "weight adjustment", "request limit"),
	mk("api-version", "v1 will", "v2 will", "v3 will", "version deprecat", "old version", "legacy api"),
}

// highRules capture operationally significant events that often need attention
// but are not necessarily API code changes.
var highRules = []rule{
	mk("delisting", "delist", "will be delisted", "removal of", "will remove the", "trading suspension", "suspend trading", "cease trading", "termination of"),
	mk("maintenance", "system upgrade", "system maintenance", "scheduled maintenance", "wallet maintenance", "platform upgrade", "service suspension", "temporarily suspend"),
	mk("network-upgrade", "network upgrade", "hard fork", "hardfork", "chain swap", "mainnet swap", "token swap", "contract swap", "contract migration", "token migration", "redenomination", "rebranding"),
	mk("incident", "outage", "degraded", "degradation", "downtime", "disruption", "service interruption", "experiencing issues", "elevated error", "connectivity issue", "partial outage", "major outage", "unavailable"),
	mk("settlement", "settlement", "auto-settle", "will be settled", "final settlement", "delivery contract"),
	mk("deposit-withdrawal", "suspend deposit", "suspend withdrawal", "resume deposit", "resume withdrawal", "deposits and withdrawals", "open deposit", "close withdrawal"),
}

// mediumRules capture informational product/market changes.
var mediumRules = []rule{
	mk("listing", "will list", "listing of", "new listing", "lists ", "to list ", "spot listing", "futures listing", "perpetual listing", "launch of", "will launch", "now available", "goes live", "new trading pair", "new pair"),
	mk("product", "new feature", "introduc", "release of", "now support", "adds support", "added support", "expansion", "new product", "upgrade of the"),
	mk("adjustment", "adjustment", "modif", "revise", "revision", "update to", "changes to the", "parameter", "leverage and margin", "tier adjustment", "funding rate"),
}

// lowRules capture marketing / promo noise.
var lowRules = []rule{
	mk("promo", "promotion", "campaign", "airdrop", "giveaway", "reward", "carnival", "festival", "celebrat", "win ", "prize pool", "trading competition", "bonus", "cashback", "vote", "candy"),
}

// marketRules map keywords to a market type.
var marketRules = []struct {
	market model.MarketType
	re     *regexp.Regexp
}{
	{model.MarketFutures, regexp.MustCompile(`(?i)(futures|perpetual|perp\b|swap\b|usd[ⓈsⓈ]?[- ]?m|coin[- ]?m|delivery contract|perpetual contract|contract trading|leverage)`)},
	{model.MarketOptions, regexp.MustCompile(`(?i)(options?\b)`)},
	{model.MarketMargin, regexp.MustCompile(`(?i)(margin)`)},
	{model.MarketEarn, regexp.MustCompile(`(?i)(earn|staking|savings|launchpool|launchpad|simple earn|yield|dual investment|shark fin|liquidity mining)`)},
	{model.MarketConvert, regexp.MustCompile(`(?i)(convert\b)`)},
	{model.MarketWallet, regexp.MustCompile(`(?i)(deposit|withdrawal|network|chain\b|wallet|on-chain|erc-?20|bep-?20|trc-?20)`)},
	{model.MarketAPI, regexp.MustCompile(`(?i)(\bapis?\b|websocket|web socket|\bws\b|endpoint|fix protocol|\bsdk\b)`)},
	{model.MarketSpot, regexp.MustCompile(`(?i)(spot)`)},
}

// Result is the classifier output.
type Result struct {
	Importance   model.Importance
	Markets      []model.MarketType
	MatchedRules []string
}

// Classify inspects an announcement's title + body and returns its importance,
// the market types it touches, and the rules that fired.
func Classify(a model.Announcement) Result {
	text := strings.ToLower(strings.TrimSpace(a.Title + ". " + a.Body))

	var matched []string
	imp := model.ImportanceLow
	hasSignal := false

	// Importance is the MAX severity among all matched tiers. We still collect
	// every matched rule for explainability.
	if names := fire(criticalRules, text); len(names) > 0 {
		imp = model.ImportanceCritical
		matched = append(matched, prefix("critical", names)...)
		hasSignal = true
	}
	if names := fire(highRules, text); len(names) > 0 {
		if imp < model.ImportanceHigh {
			imp = model.ImportanceHigh
		}
		matched = append(matched, prefix("high", names)...)
		hasSignal = true
	}
	if names := fire(mediumRules, text); len(names) > 0 {
		if imp < model.ImportanceMedium {
			imp = model.ImportanceMedium
		}
		matched = append(matched, prefix("medium", names)...)
		hasSignal = true
	}
	if names := fire(lowRules, text); len(names) > 0 {
		matched = append(matched, prefix("low", names)...)
		hasSignal = true
	}

	// If nothing matched at all, default to medium: an unclassified official
	// announcement is more likely informational than pure marketing, and we
	// would rather surface it than hide it.
	if !hasSignal {
		imp = model.ImportanceMedium
		matched = append(matched, "default:unclassified")
	}

	markets := detectMarkets(text)

	return Result{Importance: imp, Markets: markets, MatchedRules: matched}
}

func detectMarkets(text string) []model.MarketType {
	var out []model.MarketType
	seen := map[model.MarketType]bool{}
	for _, mr := range marketRules {
		if mr.re.MatchString(text) && !seen[mr.market] {
			out = append(out, mr.market)
			seen[mr.market] = true
		}
	}
	if len(out) == 0 {
		out = []model.MarketType{model.MarketGeneral}
	}
	return out
}

func fire(rules []rule, text string) []string {
	var names []string
	for _, r := range rules {
		if r.re.MatchString(text) {
			names = append(names, r.name)
		}
	}
	return names
}

func prefix(tier string, names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = tier + ":" + n
	}
	return out
}
