package poller

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"exchangebot/internal/config"
	"exchangebot/internal/control"
	"exchangebot/internal/httpx"
	"exchangebot/internal/model"
	"exchangebot/internal/sources"
	"exchangebot/internal/store"
	"exchangebot/internal/telegram"
)

type fakeSource struct {
	name  string
	items []model.Announcement
}

func (f *fakeSource) Exchange() string        { return "fake" }
func (f *fakeSource) Name() string            { return f.name }
func (f *fakeSource) Interval() time.Duration { return time.Minute }
func (f *fakeSource) Fetch(context.Context) ([]model.Announcement, error) {
	return f.items, nil
}

func newTestPoller(t *testing.T, cfg *config.Config) (*Poller, *store.Store) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	hc := httpx.New(time.Second, "t", log)
	sender := telegram.New("", "", true, hc, log) // dry-run, never actually sends
	ctrl := control.New(cfg.MinImportance)
	return New(cfg, nil, st, sender, ctrl, log), st
}

func ann(id, title string, imp model.Importance, publishedMinAgo int) model.Announcement {
	return model.Announcement{
		Exchange:    "fake",
		NativeID:    id,
		Title:       title,
		URL:         "https://ex/" + id,
		Importance:  imp,
		PublishedAt: time.Now().Add(-time.Duration(publishedMinAgo) * time.Minute),
	}
}

func drain(p *Poller) []delivery {
	var out []delivery
	for {
		select {
		case d := <-p.deliveries:
			out = append(out, d)
		default:
			return out
		}
	}
}

func TestFirstRunBackfillsNewestAndSuppressesRest(t *testing.T) {
	cfg := &config.Config{SendHistoryOnStart: true, HistoryCount: 1, MinImportance: model.ImportanceLow}
	p, st := newTestPoller(t, cfg)

	src := &fakeSource{name: "fake:feed", items: []model.Announcement{
		ann("1", "oldest", model.ImportanceMedium, 300),
		ann("2", "newest", model.ImportanceLow, 5), // newest by publish time
		ann("3", "middle", model.ImportanceHigh, 60),
	}}

	p.handleFirstRun(src, src.items)

	got := drain(p)
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 backfill delivery, got %d", len(got))
	}
	if got[0].ann.NativeID != "2" || !got[0].backfill {
		t.Fatalf("expected newest item #2 as backfill, got %+v", got[0].ann)
	}
	// Every non-backfilled item must be suppressed (marked seen) so it is never
	// re-sent as "new" later.
	for _, id := range []string{"1", "3"} {
		if !st.IsSeen("fake:" + id) {
			t.Errorf("item %s should have been suppressed (seen) on first run", id)
		}
	}
	if !st.IsInitialized("fake:feed") {
		t.Errorf("source should be marked initialized")
	}
}

func TestUpdatesDeliverNewAndFilterLowImportance(t *testing.T) {
	cfg := &config.Config{MinImportance: model.ImportanceHigh}
	p, st := newTestPoller(t, cfg)

	// An already-seen item, a new high item, and a new low item.
	st.MarkSeen("fake:seen", time.Now())
	items := []model.Announcement{
		ann("seen", "already delivered", model.ImportanceCritical, 10),
		ann("hi", "delist something", model.ImportanceHigh, 5),
		ann("lo", "promo", model.ImportanceLow, 1),
	}

	p.handleUpdates(items)

	got := drain(p)
	if len(got) != 1 {
		t.Fatalf("expected 1 delivery (the new high item), got %d", len(got))
	}
	if got[0].ann.NativeID != "hi" || got[0].backfill {
		t.Fatalf("expected new high item, got %+v backfill=%v", got[0].ann, got[0].backfill)
	}
	// Below-threshold item must NOT be marked seen: the same article may arrive
	// via a dedicated api-updates feed (same DedupKey) floored to a higher
	// importance and should still be deliverable there.
	if st.IsSeen("fake:lo") {
		t.Errorf("low-importance item must NOT be marked seen (would poison a higher-importance copy from another feed)")
	}
}

// counterSource emits a fresh, unique announcement on every fetch so the poller
// keeps producing right up until shutdown — stressing the channel-close ordering.
type counterSource struct{ n atomic.Int64 }

func (c *counterSource) Exchange() string        { return "fake" }
func (c *counterSource) Name() string            { return "fake:counter" }
func (c *counterSource) Interval() time.Duration { return 5 * time.Millisecond }
func (c *counterSource) Fetch(context.Context) ([]model.Announcement, error) {
	id := strconv.FormatInt(c.n.Add(1), 10)
	return []model.Announcement{ann(id, "item "+id, model.ImportanceHigh, 0)}, nil
}

func TestRunShutsDownCleanlyUnderLoad(t *testing.T) {
	cfg := &config.Config{MinImportance: model.ImportanceLow, SendHistoryOnStart: false}
	p, _ := newTestPoller(t, cfg)
	p.sendGap = time.Millisecond // don't throttle the dispatcher in the test
	p.sources = []sources.Source{&counterSource{}}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()

	time.Sleep(80 * time.Millisecond) // let it poll/enqueue a while
	cancel()

	select {
	case <-done:
		// Run returned without a "send on closed channel" panic.
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not shut down within 3s")
	}
}

func TestAPIOnlyDeliversOnlyAPIItems(t *testing.T) {
	cfg := &config.Config{MinImportance: model.ImportanceLow, APIOnly: true}
	p, st := newTestPoller(t, cfg)

	apiItem := ann("api", "API endpoint deprecation", model.ImportanceCritical, 5)
	apiItem.Markets = []model.MarketType{model.MarketAPI}
	delisting := ann("del", "Delisting XYZ", model.ImportanceHigh, 4)
	delisting.Markets = []model.MarketType{model.MarketSpot}

	p.handleUpdates([]model.Announcement{apiItem, delisting})

	got := drain(p)
	if len(got) != 1 || got[0].ann.NativeID != "api" {
		t.Fatalf("API_ONLY should deliver only the api item, got %d: %+v", len(got), got)
	}
	// The non-API item must NOT be marked seen (a dedicated api feed could later
	// surface the same article correctly tagged).
	if st.IsSeen("fake:del") {
		t.Errorf("non-API item must not be marked seen under API_ONLY")
	}
}

func TestFeedHealthAlertsAndRecovers(t *testing.T) {
	cfg := &config.Config{MinImportance: model.ImportanceLow, FeedFailureAlert: 3, FeedRecoveryNotice: true}
	p, _ := newTestPoller(t, cfg)

	// Below threshold: no alert.
	p.recordFailure("binance:api-updates", errBoom)
	p.recordFailure("binance:api-updates", errBoom)
	if len(drain(p)) != 0 {
		t.Fatal("must not alert before threshold")
	}
	// Crossing the threshold fires exactly one down alert.
	p.recordFailure("binance:api-updates", errBoom)
	got := drain(p)
	if len(got) != 1 || !got[0].system {
		t.Fatalf("expected 1 system down-alert, got %d", len(got))
	}
	if got[0].ann.Importance != model.ImportanceHigh || !got[0].ann.IsOperational() {
		t.Errorf("down alert should be HIGH + operational: %+v", got[0].ann)
	}
	if got[0].onSuccess == nil {
		t.Fatal("down alert must carry an onSuccess callback (commit-after-send)")
	}
	if len(p.downFeeds()) != 1 {
		t.Errorf("feed should be listed as down (by failCount)")
	}
	// Simulate a CONFIRMED send: this commits downNotified.
	got[0].onSuccess()

	// Further failures do not re-alert now that the down was delivered.
	p.recordFailure("binance:api-updates", errBoom)
	if len(drain(p)) != 0 {
		t.Fatal("must not re-alert while still down")
	}
	// Recovery fires one notice and clears the down state.
	p.recordSuccess("binance:api-updates")
	rec := drain(p)
	if len(rec) != 1 || !rec[0].system {
		t.Fatalf("expected 1 recovery alert, got %d", len(rec))
	}
	if len(p.downFeeds()) != 0 {
		t.Errorf("feed should no longer be down")
	}
}

// TestFeedDownRetriesAfterFailedSend verifies that a down alert whose delivery
// fails (onSuccess never runs) is re-enqueued on the next failed poll, instead
// of being lost with downNotified stuck true.
func TestFeedDownRetriesAfterFailedSend(t *testing.T) {
	cfg := &config.Config{FeedFailureAlert: 2}
	p, _ := newTestPoller(t, cfg)

	p.recordFailure("okx:api", errBoom) // 1
	p.recordFailure("okx:api", errBoom) // 2 → enqueue down alert
	got := drain(p)
	if len(got) != 1 {
		t.Fatalf("expected down alert, got %d", len(got))
	}
	// Simulate the dispatcher processing a FAILED send: it clears the inflight
	// reservation but does NOT call onSuccess.
	p.mu.Lock()
	delete(p.inflight, got[0].ann.DedupKey())
	p.mu.Unlock()

	// Next failed poll must re-enqueue (the alert was never delivered).
	p.recordFailure("okx:api", errBoom) // 3
	retry := drain(p)
	if len(retry) != 1 {
		t.Fatalf("lost down-alert must be retried, got %d", len(retry))
	}
	// Now the send succeeds.
	retry[0].onSuccess()
	p.mu.Lock()
	delete(p.inflight, retry[0].ann.DedupKey())
	p.mu.Unlock()

	// No further re-alerts once delivered.
	p.recordFailure("okx:api", errBoom)
	if len(drain(p)) != 0 {
		t.Fatal("must not re-alert after confirmed delivery")
	}
}

var errBoom = errBoomType("boom")

type errBoomType string

func (e errBoomType) Error() string { return string(e) }

func TestEnqueueDedupesInFlight(t *testing.T) {
	cfg := &config.Config{MinImportance: model.ImportanceLow}
	p, _ := newTestPoller(t, cfg)

	a := ann("x", "same item", model.ImportanceHigh, 1)
	p.enqueue(a, false)
	p.enqueue(a, false) // second enqueue must be suppressed while in flight

	if got := drain(p); len(got) != 1 {
		t.Fatalf("expected 1 in-flight delivery, got %d", len(got))
	}
}
