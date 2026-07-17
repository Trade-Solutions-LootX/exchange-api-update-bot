package classify

import (
	"testing"

	"exchangebot/internal/model"
)

func TestImportance(t *testing.T) {
	cases := []struct {
		name  string
		title string
		body  string
		want  model.Importance
	}{
		{
			name:  "api deprecation is critical",
			title: "Notice on the Deprecation of Certain Spot Trading API Endpoints",
			want:  model.ImportanceCritical,
		},
		{
			name:  "websocket change is critical",
			title: "Adjustment of WebSocket Push Data for USDⓈ-M Futures",
			want:  model.ImportanceCritical,
		},
		{
			name:  "present-tense discontinue is critical",
			title: "OKX to discontinue certain V3 API endpoints",
			want:  model.ImportanceCritical,
		},
		{
			name:  "api shutdown amid promo wording is still critical",
			title: "Legacy API will shut down — join our trading competition after you migrate",
			want:  model.ImportanceCritical,
		},
		{
			name:  "rate limit change is critical",
			title: "Update on API Request Rate Limit Rules",
			want:  model.ImportanceCritical,
		},
		{
			name:  "delisting is high",
			title: "Binance Will Delist XYZ, ABC on 2026-08-01",
			want:  model.ImportanceHigh,
		},
		{
			name:  "system maintenance is high",
			title: "Scheduled System Maintenance Notice",
			want:  model.ImportanceHigh,
		},
		{
			name:  "new listing is medium",
			title: "Binance Will List NewCoin (NEW) in the Innovation Zone",
			want:  model.ImportanceMedium,
		},
		{
			name:  "promotion is low",
			title: "Trading Competition: Win a Share of 1,000,000 USDT Prize Pool",
			want:  model.ImportanceLow,
		},
		{
			name:  "unclassified defaults to medium",
			title: "A perfectly neutral corporate statement about nothing specific",
			want:  model.ImportanceMedium,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(model.Announcement{Title: tc.title, Body: tc.body})
			if got.Importance != tc.want {
				t.Fatalf("Classify(%q) importance = %v (%s), want %v\nrules=%v",
					tc.title, got.Importance, got.Importance, tc.want, got.MatchedRules)
			}
			if len(got.MatchedRules) == 0 {
				t.Errorf("expected at least one matched rule for %q", tc.title)
			}
		})
	}
}

func TestMarketDetection(t *testing.T) {
	cases := []struct {
		title string
		want  model.MarketType
	}{
		{"USDⓈ-M Futures leverage adjustment", model.MarketFutures},
		{"New Perpetual Contract Listing", model.MarketFutures},
		{"Spot Trading pair added", model.MarketSpot},
		{"Options expiry schedule", model.MarketOptions},
		{"Cross Margin update", model.MarketMargin},
		{"Simple Earn new product", model.MarketEarn},
		{"Deposit and Withdrawal suspension for network upgrade", model.MarketWallet},
		{"REST API endpoint change", model.MarketAPI},
		{"Deprecation of legacy APIs", model.MarketAPI}, // plural must match
	}
	for _, tc := range cases {
		t.Run(tc.title, func(t *testing.T) {
			got := Classify(model.Announcement{Title: tc.title})
			if !containsMarket(got.Markets, tc.want) {
				t.Fatalf("Classify(%q) markets = %v, want to contain %v", tc.title, got.Markets, tc.want)
			}
		})
	}
}

func TestSmartContractNotTaggedFutures(t *testing.T) {
	got := Classify(model.Announcement{Title: "Token smart contract migration to a new address"})
	if containsMarket(got.Markets, model.MarketFutures) {
		t.Fatalf("smart-contract/token migration should not be tagged futures: %v", got.Markets)
	}
}

func TestGeneralFallback(t *testing.T) {
	got := Classify(model.Announcement{Title: "hello world"})
	if !containsMarket(got.Markets, model.MarketGeneral) {
		t.Fatalf("expected general market fallback, got %v", got.Markets)
	}
}

func containsMarket(list []model.MarketType, m model.MarketType) bool {
	for _, x := range list {
		if x == m {
			return true
		}
	}
	return false
}
