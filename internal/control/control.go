// Package control holds the bot's mutable runtime state — muted exchanges and a
// live min-importance override — shared safely between the poller (which reads
// it on every delivery decision) and the Telegram command handler (which
// mutates it). State is in-memory: a restart resets mutes and the override to
// the configured defaults, which is the safe/expected behavior.
package control

import (
	"sort"
	"sync"
	"time"

	"exchangebot/internal/model"
)

// Controller is the concurrency-safe runtime control surface.
type Controller struct {
	mu         sync.Mutex
	muted      map[string]time.Time // slug → unmute time (zero = muted forever)
	defaultMin model.Importance
	minSet     bool
	min        model.Importance
}

// New builds a Controller with the configured default min importance.
func New(defaultMin model.Importance) *Controller {
	return &Controller{
		muted:      map[string]time.Time{},
		defaultMin: defaultMin,
	}
}

// Mute silences an exchange/provider slug for d (0 = until unmuted).
func (c *Controller) Mute(slug string, d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if d <= 0 {
		c.muted[slug] = time.Time{}
		return
	}
	c.muted[slug] = time.Now().Add(d)
}

// Unmute clears a mute.
func (c *Controller) Unmute(slug string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.muted, slug)
}

// IsMuted reports whether a slug is currently muted, expiring timed mutes.
func (c *Controller) IsMuted(slug string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	until, ok := c.muted[slug]
	if !ok {
		return false
	}
	if until.IsZero() {
		return true // forever
	}
	if time.Now().After(until) {
		delete(c.muted, slug)
		return false
	}
	return true
}

// Muted returns the active mutes (slug → unmute time; zero = forever).
func (c *Controller) Muted() map[string]time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]time.Time{}
	now := time.Now()
	for slug, until := range c.muted {
		if until.IsZero() || now.Before(until) {
			out[slug] = until
		}
	}
	return out
}

// MutedSlugs returns the sorted list of currently-muted slugs.
func (c *Controller) MutedSlugs() []string {
	m := c.Muted()
	out := make([]string, 0, len(m))
	for s := range m {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// MinImportance returns the effective threshold (runtime override or default).
func (c *Controller) MinImportance() model.Importance {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.minSet {
		return c.min
	}
	return c.defaultMin
}

// SetMinImportance overrides the threshold at runtime.
func (c *Controller) SetMinImportance(imp model.Importance) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.minSet = true
	c.min = imp
}

// ResetMinImportance reverts to the configured default.
func (c *Controller) ResetMinImportance() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.minSet = false
}
