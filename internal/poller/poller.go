// Package poller orchestrates the polling of every source, deduplication,
// importance filtering, and paced delivery to Telegram.
//
// Delivery guarantees:
//   - Exactly-once-ish: an item is marked "seen" only AFTER a successful send,
//     and an in-flight set prevents the same item being queued twice by
//     overlapping ticks. A send that fails is retried on the next poll rather
//     than lost.
//   - First run is quiet: on a source's first-ever poll we suppress the whole
//     backlog (marking it seen) except for the newest N items, which are sent
//     as clearly-labelled "backfill" so the operator can confirm the pipeline
//     works. Subsequent polls deliver only genuinely new items.
package poller

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"exchangebot/internal/config"
	"exchangebot/internal/control"
	"exchangebot/internal/model"
	"exchangebot/internal/sources"
	"exchangebot/internal/store"
	"exchangebot/internal/telegram"
)

// Poller wires sources, the seen-store, and the Telegram sender together.
type Poller struct {
	cfg     *config.Config
	sources []sources.Source
	store   *store.Store
	sender  *telegram.Sender
	control *control.Controller
	log     *slog.Logger

	// pacing between Telegram sends to respect per-chat rate limits.
	sendGap time.Duration

	deliveries chan delivery
	mu         sync.Mutex
	inflight   map[string]bool

	// feed-health self-monitoring: consecutive failures + whether we've alerted.
	healthMu     sync.Mutex
	failCount    map[string]int
	downNotified map[string]bool

	// stats, read by the health endpoint.
	statsMu   sync.Mutex
	sent      int
	errors    int
	lastCycle time.Time
}

type delivery struct {
	ann      model.Announcement
	backfill bool
	// system marks an internally-generated alert (feed-down, uptime, etc.). It
	// is sent but never recorded in the seen-store, so the same condition can
	// alert again after it recovers and re-occurs.
	system bool
	// onSuccess, if set, runs after a confirmed send — used to commit state only
	// once an alert has actually been delivered.
	onSuccess func()
}

// New builds a Poller.
func New(cfg *config.Config, srcs []sources.Source, st *store.Store, sender *telegram.Sender, ctrl *control.Controller, log *slog.Logger) *Poller {
	return &Poller{
		cfg:          cfg,
		sources:      srcs,
		store:        st,
		sender:       sender,
		control:      ctrl,
		log:          log,
		sendGap:      1200 * time.Millisecond,
		deliveries:   make(chan delivery, 256),
		inflight:     map[string]bool{},
		failCount:    map[string]int{},
		downNotified: map[string]bool{},
	}
}

// Run starts all goroutines and blocks until ctx is cancelled, then drains and
// flushes state before returning.
//
// Shutdown ordering matters: the source goroutines are the ONLY producers on the
// deliveries channel, so we must wait for every one of them to exit before
// closing it — otherwise a source mid-enqueue would panic with "send on closed
// channel". We therefore track producers separately from the consumer.
func (p *Poller) Run(ctx context.Context) {
	var producers sync.WaitGroup // source goroutines (the only channel writers)
	var others sync.WaitGroup    // dispatcher + maintenance

	// Single dispatcher paces all sends through one channel.
	others.Add(1)
	go func() {
		defer others.Done()
		p.dispatch(ctx)
	}()

	// One goroutine per source, each on its own jittered schedule.
	for i, src := range p.sources {
		producers.Add(1)
		go func(idx int, s sources.Source) {
			defer producers.Done()
			p.runSource(ctx, idx, s)
		}(i, src)
	}

	// Periodic state flush + prune.
	others.Add(1)
	go func() {
		defer others.Done()
		p.maintenance(ctx)
	}()

	<-ctx.Done()
	p.log.Info("shutdown signal received, draining")

	// 1) Wait for all producers to stop before closing the channel they write to.
	producers.Wait()
	// 2) Now it is safe to close; the dispatcher drains and exits on close.
	close(p.deliveries)
	// 3) Wait for the dispatcher and maintenance goroutines to finish.
	others.Wait()

	if err := p.store.Flush(); err != nil {
		p.log.Error("final state flush failed", "err", err)
	}
}

func (p *Poller) runSource(ctx context.Context, idx int, s sources.Source) {
	// Stagger startup so we do not hit 12 exchanges in the same instant.
	stagger := time.Duration(idx) * 800 * time.Millisecond
	select {
	case <-ctx.Done():
		return
	case <-time.After(stagger):
	}

	p.pollOnce(ctx, s)

	ticker := time.NewTicker(s.Interval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.pollOnce(ctx, s)
		}
	}
}

func (p *Poller) pollOnce(ctx context.Context, s sources.Source) {
	items, err := s.Fetch(ctx)
	if err != nil {
		p.log.Warn("source fetch failed", "source", s.Name(), "err", err)
		p.bumpErrors()
		p.recordFailure(s.Name(), err)
		return
	}
	p.recordSuccess(s.Name())
	p.setLastCycle(time.Now())
	p.log.Debug("polled source", "source", s.Name(), "items", len(items))

	// System sources (uptime, load) emit live transition alerts: deliver each
	// immediately, bypassing first-run suppression, filters and the seen-store.
	if sys, ok := s.(sources.SystemSource); ok && sys.IsSystem() {
		// System sources (uptime/load) are EDGE-triggered: Fetch has already
		// advanced their state machine, so we must deliver the transition now —
		// dropping it (e.g. for a mute) would lose the event forever, since the
		// source never re-announces current state. These critical operational
		// alerts therefore intentionally bypass /mute.
		for _, a := range items {
			p.enqueueSystem(a)
		}
		return
	}

	if !p.store.IsInitialized(s.Name()) {
		p.handleFirstRun(s, items)
		return
	}
	p.handleUpdates(items)
}

// handleFirstRun suppresses the historical backlog and optionally backfills the
// newest N items so the operator sees the pipeline is live.
func (p *Poller) handleFirstRun(s sources.Source, items []model.Announcement) {
	now := time.Now()
	backfillKeys := map[string]bool{}
	if p.cfg.SendHistoryOnStart && p.cfg.HistoryCount > 0 {
		// Under API_ONLY, backfill the newest API item (not just the newest
		// item, which might be an ignored listing/promo).
		candidates := items
		if p.cfg.APIOnly {
			candidates = filterDeliverableUnderAPIOnly(items)
		}
		for _, a := range newestN(candidates, p.cfg.HistoryCount) {
			backfillKeys[a.DedupKey()] = true
		}
	}
	for _, a := range items {
		key := a.DedupKey()
		if backfillKeys[key] && !p.control.IsMuted(a.Exchange) && p.control.IsSubscribed(a.Category()) {
			// Backfill bypasses the importance filter so every exchange emits at
			// least one confirming message. Skip if another feed of the same
			// exchange already delivered this exact article (shared DedupKey),
			// so it is not backfilled twice. Marked seen after successful send.
			if p.store.IsSeen(key) {
				continue
			}
			p.enqueue(a, true)
		} else {
			p.store.MarkSeen(key, now) // silently suppress the rest of the backlog
		}
	}
	p.store.MarkInitialized(s.Name())
	p.log.Info("source initialized", "source", s.Name(), "backlog", len(items), "backfilled", len(backfillKeys))
}

// handleUpdates delivers genuinely new items, oldest-first, above the min
// importance threshold.
func (p *Poller) handleUpdates(items []model.Announcement) {
	// Oldest-first so a burst of new items arrives in chronological order.
	sorted := append([]model.Announcement(nil), items...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].PublishedAt.Before(sorted[j].PublishedAt)
	})
	for _, a := range sorted {
		key := a.DedupKey()
		if p.store.IsSeen(key) {
			continue
		}
		if p.control.IsMuted(a.Exchange) {
			// Muted: drop (mark seen) so the backlog doesn't flood after unmute.
			p.store.MarkSeen(key, time.Now())
			continue
		}
		if !p.control.IsSubscribed(a.Category()) {
			// Unsubscribed category (e.g. delistings/promos): drop it.
			p.store.MarkSeen(key, time.Now())
			continue
		}
		if p.cfg.APIOnly && !a.IsAPIRelated() && !a.IsOperational() {
			// API_ONLY keeps API changes plus infra/billing (operationally
			// actionable) and drops listing/delisting/promo noise. Skip WITHOUT
			// marking seen, so this exact article can still be delivered if it
			// re-surfaces via a dedicated api-updates feed (tagged as api).
			continue
		}
		if a.Importance < p.control.MinImportance() {
			// Below threshold: skip WITHOUT marking seen. The same article may
			// arrive via this exchange's dedicated api-updates feed (same
			// DedupKey) where it is floored to a higher importance and SHOULD be
			// delivered — marking it seen here would silently poison that copy.
			// Re-classifying an unchanged item each poll is cheap.
			continue
		}
		p.enqueue(a, false)
	}
}

// enqueue schedules a delivery unless the same key is already queued/in flight.
func (p *Poller) enqueue(a model.Announcement, backfill bool) {
	p.enqueueDelivery(delivery{ann: a, backfill: backfill})
}

// enqueueSystem schedules an internally-generated alert (feed-down/uptime). It
// is never persisted to the seen-store so the condition can re-alert later.
func (p *Poller) enqueueSystem(a model.Announcement) {
	p.enqueueDelivery(delivery{ann: a, system: true})
}

// enqueueSystemCB is like enqueueSystem but runs onSuccess only after the alert
// is actually delivered — so state is committed only on confirmed send.
func (p *Poller) enqueueSystemCB(a model.Announcement, onSuccess func()) {
	p.enqueueDelivery(delivery{ann: a, system: true, onSuccess: onSuccess})
}

func (p *Poller) enqueueDelivery(d delivery) {
	key := d.ann.DedupKey()
	p.mu.Lock()
	if p.inflight[key] {
		p.mu.Unlock()
		return
	}
	p.inflight[key] = true
	p.mu.Unlock()

	select {
	case p.deliveries <- d:
	default:
		// Queue full: drop the in-flight reservation so a later poll retries.
		p.mu.Lock()
		delete(p.inflight, key)
		p.mu.Unlock()
		p.log.Warn("delivery queue full, will retry next poll", "source", d.ann.Source, "title", d.ann.Title)
	}
}

// recordFailure tracks a source's consecutive failures and fires a "feed is
// down" alert once the threshold is crossed. downNotified is committed only
// AFTER the alert is actually delivered (via onSuccess), so a send lost to a
// 429/network blip or a full queue is retried on the next failed poll instead
// of being silently swallowed.
func (p *Poller) recordFailure(name string, cause error) {
	if p.cfg.FeedFailureAlert <= 0 {
		return
	}
	p.healthMu.Lock()
	p.failCount[name]++
	c := p.failCount[name]
	alreadyDown := p.downNotified[name]
	p.healthMu.Unlock()

	if c >= p.cfg.FeedFailureAlert && !alreadyDown {
		p.enqueueSystemCB(feedDownAlert(name, c, cause), func() {
			p.healthMu.Lock()
			p.downNotified[name] = true
			p.healthMu.Unlock()
		})
	}
}

// recordSuccess resets a source's failure counter and, ONLY if a down alert was
// actually delivered, sends a recovery notice — so a recovery never fires
// without a preceding down.
func (p *Poller) recordSuccess(name string) {
	p.healthMu.Lock()
	wasDown := p.downNotified[name]
	p.failCount[name] = 0
	p.downNotified[name] = false
	p.healthMu.Unlock()
	if wasDown && p.cfg.FeedRecoveryNotice {
		p.enqueueSystem(feedRecoveredAlert(name))
	}
}

// downFeeds returns sources currently at/over the failure threshold (for
// /status) — independent of whether the down ALERT has been delivered yet.
func (p *Poller) downFeeds() []string {
	if p.cfg.FeedFailureAlert <= 0 {
		return nil
	}
	p.healthMu.Lock()
	defer p.healthMu.Unlock()
	var out []string
	for name, c := range p.failCount {
		if c >= p.cfg.FeedFailureAlert {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func feedDownAlert(name string, fails int, cause error) model.Announcement {
	msg := ""
	if cause != nil {
		msg = ": " + cause.Error()
	}
	return model.Announcement{
		Exchange:     "monitor",
		NativeID:     "feed-down:" + name,
		Title:        fmt.Sprintf("Feed %s is DOWN — %d consecutive failures%s", name, fails, msg),
		Body:         "The bot cannot fetch this source. The exchange may have changed its endpoint (patch internal/sources/specs.go) or it is temporarily unreachable.",
		Source:       "monitor:feed-health",
		PublishedAt:  time.Now().UTC(),
		Markets:      []model.MarketType{model.MarketInfra},
		Importance:   model.ImportanceHigh,
		MatchedRules: []string{"monitor:feed-down"},
	}
}

func feedRecoveredAlert(name string) model.Announcement {
	return model.Announcement{
		Exchange:     "monitor",
		NativeID:     "feed-recovered:" + name,
		Title:        fmt.Sprintf("Feed %s has RECOVERED", name),
		Source:       "monitor:feed-health",
		PublishedAt:  time.Now().UTC(),
		Markets:      []model.MarketType{model.MarketInfra},
		Importance:   model.ImportanceMedium,
		MatchedRules: []string{"monitor:feed-recovered"},
	}
}

// dispatch is the single sender: it paces sends and marks items seen only on
// success, guaranteeing failed sends are retried on the next poll.
func (p *Poller) dispatch(ctx context.Context) {
	for d := range p.deliveries {
		// A cancelled context stops sending but we keep draining the channel so
		// the producer close does not block; enqueue reservations are released.
		if ctx.Err() == nil {
			err := p.sender.Send(ctx, d.ann, d.backfill)
			if err != nil {
				p.log.Error("send failed", "source", d.ann.Source, "title", d.ann.Title, "err", err)
				p.bumpErrors()
			} else {
				if !d.system {
					// System alerts (feed-down/uptime) are never persisted, so the
					// same condition can alert again after it recovers.
					p.store.MarkSeen(d.ann.DedupKey(), time.Now())
				}
				if d.onSuccess != nil {
					d.onSuccess() // commit state only after a confirmed send
				}
				p.bumpSent()
				p.log.Info("delivered",
					"exchange", d.ann.Exchange,
					"importance", d.ann.Importance.String(),
					"backfill", d.backfill,
					"title", d.ann.Title,
				)
			}
		}
		p.mu.Lock()
		delete(p.inflight, d.ann.DedupKey())
		p.mu.Unlock()

		if ctx.Err() == nil {
			select {
			case <-ctx.Done():
			case <-time.After(p.sendGap):
			}
		}
	}
}

func (p *Poller) maintenance(ctx context.Context) {
	flush := time.NewTicker(30 * time.Second)
	prune := time.NewTicker(6 * time.Hour)
	defer flush.Stop()
	defer prune.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-flush.C:
			if err := p.store.Flush(); err != nil {
				p.log.Error("state flush failed", "err", err)
			}
		case <-prune.C:
			// Keep seen-keys for 60 days; older news will never resurface.
			if n := p.store.Prune(60*24*time.Hour, time.Now()); n > 0 {
				p.log.Info("pruned old seen entries", "removed", n)
			}
		}
	}
}

// filterDeliverableUnderAPIOnly keeps the items API_ONLY would actually deliver:
// exchange API changes plus infra/billing (operationally actionable).
func filterDeliverableUnderAPIOnly(items []model.Announcement) []model.Announcement {
	out := make([]model.Announcement, 0, len(items))
	for _, a := range items {
		if a.IsAPIRelated() || a.IsOperational() {
			out = append(out, a)
		}
	}
	return out
}

// newestN returns the newest n items by PublishedAt (stable for equal/zero
// times, which preserves the source's own newest-first ordering).
func newestN(items []model.Announcement, n int) []model.Announcement {
	if n <= 0 || len(items) == 0 {
		return nil
	}
	sorted := append([]model.Announcement(nil), items...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].PublishedAt.After(sorted[j].PublishedAt)
	})
	if n > len(sorted) {
		n = len(sorted)
	}
	return sorted[:n]
}

// ---- stats accessors (read by the health endpoint) ----

func (p *Poller) bumpSent()   { p.statsMu.Lock(); p.sent++; p.statsMu.Unlock() }
func (p *Poller) bumpErrors() { p.statsMu.Lock(); p.errors++; p.statsMu.Unlock() }
func (p *Poller) setLastCycle(t time.Time) {
	p.statsMu.Lock()
	p.lastCycle = t
	p.statsMu.Unlock()
}

// Stats is a snapshot for the health endpoint.
type Stats struct {
	Sent        int       `json:"sent"`
	Errors      int       `json:"errors"`
	LastCycle   time.Time `json:"last_cycle"`
	SeenEntries int       `json:"seen_entries"`
	Sources     int       `json:"sources"`
	DownFeeds   []string  `json:"down_feeds"`
}

// Snapshot returns current stats.
func (p *Poller) Snapshot() Stats {
	down := p.downFeeds()
	p.statsMu.Lock()
	defer p.statsMu.Unlock()
	return Stats{
		Sent:        p.sent,
		Errors:      p.errors,
		LastCycle:   p.lastCycle,
		SeenEntries: p.store.Count(),
		Sources:     len(p.sources),
		DownFeeds:   down,
	}
}
