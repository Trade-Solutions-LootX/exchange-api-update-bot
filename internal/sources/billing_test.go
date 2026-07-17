package sources

import (
	"testing"
	"time"

	"exchangebot/internal/model"
)

var billingNow = time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)

func TestBuildDOBillingOwed(t *testing.T) {
	raw := []byte(`{"month_to_date_balance":"23.44","account_balance":"12.23","month_to_date_usage":"11.21"}`)
	a, err := buildDOBilling(raw, "2026-07-14", billingNow)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if a.Importance != model.ImportanceHigh {
		t.Errorf("owed balance should be HIGH, got %s", a.Importance)
	}
	if !a.IsOperational() {
		t.Errorf("billing item must be operational")
	}
	if a.DedupKey() != "digitalocean:billing:2026-07-14" {
		t.Errorf("dedup key = %q (must be one-per-day)", a.DedupKey())
	}
}

func TestBuildDOBillingNothingOwed(t *testing.T) {
	raw := []byte(`{"month_to_date_balance":"5.00","account_balance":"0","month_to_date_usage":"5.00"}`)
	a, err := buildDOBilling(raw, "2026-07-14", billingNow)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if a.Importance != model.ImportanceMedium {
		t.Errorf("no outstanding balance should be MEDIUM, got %s", a.Importance)
	}
}

func TestBuildVultrBillingNegativeBalanceOwed(t *testing.T) {
	raw := []byte(`{"account":{"balance":-4.50,"pending_charges":6.20,"last_payment_date":"2026-07-01T00:00:00Z","last_payment_amount":10.00}}`)
	a, err := buildVultrBilling(raw, "2026-07-14", billingNow)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if a.Importance != model.ImportanceHigh {
		t.Errorf("negative balance (owed) should be HIGH, got %s", a.Importance)
	}
	if a.DedupKey() != "vultr:billing:2026-07-14" {
		t.Errorf("dedup key = %q", a.DedupKey())
	}
}

func TestBuildVultrBillingPositiveBalance(t *testing.T) {
	raw := []byte(`{"account":{"balance":50.00,"pending_charges":6.20}}`)
	a, err := buildVultrBilling(raw, "2026-07-14", billingNow)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if a.Importance != model.ImportanceMedium {
		t.Errorf("credit balance should be MEDIUM, got %s", a.Importance)
	}
}
