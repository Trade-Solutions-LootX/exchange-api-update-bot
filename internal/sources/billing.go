package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"exchangebot/internal/model"
)

// billingSource polls a hosting provider's AUTHENTICATED billing API and emits a
// once-per-day balance summary so an outstanding payment is never missed. It is
// credential-gated — only constructed when an API token is configured — and
// deduplicates by calendar day (NativeID = "billing:YYYY-MM-DD") so a 6-hourly
// poll produces at most one message per day.
//
// Render is intentionally absent: Render exposes no public account-balance API,
// so its billing is covered only by email invoices + its status page.
type billingSource struct {
	exchange string
	name     string
	url      string
	headers  map[string]string
	interval time.Duration
	deps     Deps
	// build turns the raw API response into an announcement for the given UTC
	// day. now is passed in for a deterministic PublishedAt.
	build func(raw []byte, day string, now time.Time) (model.Announcement, error)
}

func (b *billingSource) Exchange() string        { return b.exchange }
func (b *billingSource) Name() string            { return b.exchange + ":" + b.name }
func (b *billingSource) Interval() time.Duration { return b.interval }

func (b *billingSource) Fetch(ctx context.Context) ([]model.Announcement, error) {
	raw, err := b.deps.HTTP.Get(ctx, b.url, b.headers)
	if err != nil {
		return nil, fmt.Errorf("%s fetch: %w", b.Name(), err)
	}
	now := time.Now().UTC()
	a, err := b.build(raw, now.Format("2006-01-02"), now)
	if err != nil {
		return nil, fmt.Errorf("%s parse: %w", b.Name(), err)
	}
	return []model.Announcement{a}, nil
}

// NewDigitalOceanBilling builds the DO billing source (token required).
func NewDigitalOceanBilling(token string, interval time.Duration, deps Deps) Source {
	return &billingSource{
		exchange: "digitalocean",
		name:     "billing",
		url:      "https://api.digitalocean.com/v2/customers/my/balance",
		headers:  map[string]string{"Authorization": "Bearer " + token},
		interval: interval,
		deps:     deps,
		build:    buildDOBilling,
	}
}

// NewVultrBilling builds the Vultr billing source (API key required).
func NewVultrBilling(key string, interval time.Duration, deps Deps) Source {
	return &billingSource{
		exchange: "vultr",
		name:     "billing",
		url:      "https://api.vultr.com/v2/account",
		headers:  map[string]string{"Authorization": "Bearer " + key},
		interval: interval,
		deps:     deps,
		build:    buildVultrBilling,
	}
}

// buildDOBilling parses GET /v2/customers/my/balance.
// account_balance is a positive number when the account owes money.
func buildDOBilling(raw []byte, day string, now time.Time) (model.Announcement, error) {
	var r struct {
		MonthToDateBalance string `json:"month_to_date_balance"`
		AccountBalance     string `json:"account_balance"`
		MonthToDateUsage   string `json:"month_to_date_usage"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return model.Announcement{}, err
	}
	owed := parseMoney(r.AccountBalance)
	imp := model.ImportanceMedium
	lead := "balance"
	if owed > 0 {
		imp = model.ImportanceHigh
		lead = "OUTSTANDING balance to pay"
	}
	title := fmt.Sprintf("DigitalOcean %s $%s (month-to-date $%s, usage $%s)",
		lead, dash(r.AccountBalance), dash(r.MonthToDateBalance), dash(r.MonthToDateUsage))
	return billingAnnouncement("digitalocean", day, title,
		"https://cloud.digitalocean.com/account/billing", imp, now), nil
}

// buildVultrBilling parses GET /v2/account.
// A negative balance means the account owes money; pending_charges is accrued
// usage not yet billed.
func buildVultrBilling(raw []byte, day string, now time.Time) (model.Announcement, error) {
	var r struct {
		Account struct {
			Balance           float64 `json:"balance"`
			PendingCharges    float64 `json:"pending_charges"`
			LastPaymentDate   string  `json:"last_payment_date"`
			LastPaymentAmount float64 `json:"last_payment_amount"`
		} `json:"account"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return model.Announcement{}, err
	}
	imp := model.ImportanceMedium
	lead := "balance"
	if r.Account.Balance < 0 {
		imp = model.ImportanceHigh
		lead = "OWED (negative balance)"
	}
	title := fmt.Sprintf("Vultr %s $%.2f, pending charges $%.2f (last payment $%.2f)",
		lead, r.Account.Balance, r.Account.PendingCharges, r.Account.LastPaymentAmount)
	return billingAnnouncement("vultr", day, title,
		"https://my.vultr.com/billing/", imp, now), nil
}

func billingAnnouncement(provider, day, title, url string, imp model.Importance, now time.Time) model.Announcement {
	return model.Announcement{
		Exchange:     provider,
		NativeID:     "billing:" + day, // one message per calendar day
		Title:        title,
		URL:          url,
		Source:       provider + ":billing",
		PublishedAt:  now,
		Markets:      []model.MarketType{model.MarketBilling},
		Importance:   imp,
		MatchedRules: []string{"billing:daily-summary"},
	}
}

// parseMoney parses a money string like "12.34" or "-5" to a float (0 on error).
func parseMoney(s string) float64 {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return f
}

// dash renders an empty money field as "0.00" for readable messages.
func dash(s string) string {
	if s == "" {
		return "0.00"
	}
	return s
}
