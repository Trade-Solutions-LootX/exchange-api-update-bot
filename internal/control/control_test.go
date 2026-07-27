package control

import (
	"testing"
	"time"

	"exchangebot/internal/model"
)

func TestMuteForever(t *testing.T) {
	c := New(model.ImportanceLow)
	c.Mute("bybit", 0)
	if !c.IsMuted("bybit") {
		t.Fatal("bybit should be muted forever")
	}
	if c.IsMuted("okx") {
		t.Fatal("okx should not be muted")
	}
	c.Unmute("bybit")
	if c.IsMuted("bybit") {
		t.Fatal("bybit should be unmuted")
	}
}

func TestMuteExpires(t *testing.T) {
	c := New(model.ImportanceLow)
	c.Mute("gate", 20*time.Millisecond)
	if !c.IsMuted("gate") {
		t.Fatal("gate should be muted immediately")
	}
	time.Sleep(35 * time.Millisecond)
	if c.IsMuted("gate") {
		t.Fatal("gate mute should have expired")
	}
	if len(c.MutedSlugs()) != 0 {
		t.Fatalf("expired mute should be gone: %v", c.MutedSlugs())
	}
}

func TestSubscriptionDefaultsAndToggle(t *testing.T) {
	c := New(model.ImportanceLow)
	// Default: ONLY api + infra + billing are on.
	on := map[model.Category]bool{model.CatAPI: true, model.CatInfra: true, model.CatBilling: true}
	for _, cat := range model.AllCategories {
		if got := c.IsSubscribed(cat); got != on[cat] {
			t.Errorf("default IsSubscribed(%s) = %v, want %v", cat, got, on[cat])
		}
	}
	// Listings/delistings/promos are banned by default.
	for _, cat := range []model.Category{model.CatListing, model.CatDelisting, model.CatPromo, model.CatMaintenance, model.CatOther} {
		if c.IsSubscribed(cat) {
			t.Errorf("%s must be off by default", cat)
		}
	}
	// Toggle listings on then off.
	if !c.ToggleSubscription(model.CatListing) {
		t.Error("toggle should turn listings on")
	}
	if c.ToggleSubscription(model.CatListing) {
		t.Error("second toggle should turn listings off")
	}
}

func TestMinImportanceOverride(t *testing.T) {
	c := New(model.ImportanceLow)
	if c.MinImportance() != model.ImportanceLow {
		t.Fatal("should start at default low")
	}
	c.SetMinImportance(model.ImportanceHigh)
	if c.MinImportance() != model.ImportanceHigh {
		t.Fatal("override not applied")
	}
	c.ResetMinImportance()
	if c.MinImportance() != model.ImportanceLow {
		t.Fatal("reset should revert to default")
	}
}
