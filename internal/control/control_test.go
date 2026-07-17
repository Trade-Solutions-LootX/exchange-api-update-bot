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
	// Defaults: delistings + promos OFF, everything else ON.
	if c.IsSubscribed(model.CatDelisting) {
		t.Error("delistings should be off by default")
	}
	if c.IsSubscribed(model.CatPromo) {
		t.Error("promos should be off by default")
	}
	if !c.IsSubscribed(model.CatAPI) || !c.IsSubscribed(model.CatInfra) {
		t.Error("api/infra should be on by default")
	}
	// Toggle delistings on.
	if on := c.ToggleSubscription(model.CatDelisting); !on {
		t.Error("toggle should turn delistings on")
	}
	if !c.IsSubscribed(model.CatDelisting) {
		t.Error("delistings should now be on")
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
