package analyze

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"exchangebot/internal/httpx"
	"exchangebot/internal/model"
	"exchangebot/internal/store"
	"exchangebot/internal/telegram"
)

// Verdict is what the model returns for one announcement.
type Verdict struct {
	// APIRelated: the item concerns the exchange's public trading API or
	// connectivity (REST/WebSocket/auth/limits/symbols) rather than marketing.
	APIRelated bool `json:"api_related"`
	// Importance: none | low | medium | high | critical, from the terminal's view.
	Importance string `json:"importance"`
	// NeedsTerminalChange: LootX terminal code must change (or be verified) to
	// keep working — the trigger for a ClickUp task.
	NeedsTerminalChange bool     `json:"needs_terminal_change"`
	Summary             string   `json:"summary"`
	Changes             []string `json:"changes"`
	Actions             []string `json:"actions"`
	Affected            []string `json:"affected"`
	EffectiveAt         string   `json:"effective_at"`
	TaskTitle           string   `json:"task_title"`
}

// ImportanceRank orders the model's importance labels.
func ImportanceRank(s string) int {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "critical":
		return 4
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default:
		return 0
	}
}

// Options configures the Analyzer.
type Options struct {
	// Scope: "all" analyzes every new announcement; "api" only items the
	// keyword classifier already tagged as API-related (cheaper).
	Scope string
	// MinTaskImportance: a ClickUp task is filed only at/above this label.
	MinTaskImportance string
	// ClickUp target. Empty token disables task creation (analysis still runs
	// and is logged / announced in Telegram).
	ClickUpToken  string
	ClickUpListID string
	ClickUpTag    string
	// DryRun logs instead of creating tasks / sending Telegram messages.
	DryRun bool
	// FetchArticle downloads the announcement page for the model (the feed
	// preview is usually a sentence or two).
	FetchArticle bool
	MaxArticle   int
	// SkipTitle: announcements whose title matches are dropped before the
	// model sees them (listings, promos, earn…). nil = DefaultSkipTitle.
	SkipTitle *regexp.Regexp
}

// DefaultSkipTitle is the noise filter applied before the model: coin
// listings / delistings, promos, earn products, competitions. None of these
// change the API contract, and the owner explicitly does not want tasks for
// them. Anything whose title mentions the API surface passes regardless
// (keepTitle), so an API change hidden in a "delisting" post still gets read.
var DefaultSkipTitle = regexp.MustCompile(`(?i)(` +
	`will (list|delist|launch|add|support|open trading)|new listing|listing of|delist(ing|ed)? |` +
	`launchpool|launchpad|megadrop|\balpha\b|\bearn\b|savings|staking|` +
	`airdrop|giveaway|promotion|campaign|competition|contest|carnival|festival|bonus|reward|lucky draw|voucher|` +
	`trading pair|spot trading pair|perpetual contract|perpetual( futures)? (listing|launch)|pre-market|` +
	`copy trading|grid bot|trading bot|referral|vip |affiliate|` +
	`fiat|deposit|withdrawal|p2p|\bcard\b|\bconvert\b|\bloan|margin (interest|rate)|funding rate (adjust|update)|` +
	`token (swap|migration|rename)|network (upgrade|maintenance)|wallet maintenance|` +
	`leverage adjust|tick size|price precision|minimum order|position limit|` +
	`hot (coin|token|project)|\bmeme|web3|\bnft\b|square|community|live stream|\bama\b` +
	`)`)

// keepTitle overrides the skip list: if the title itself talks about the
// API surface, it is never treated as noise.
var keepTitle = regexp.MustCompile(`(?i)\b(api|websocket|ws|endpoint|rate limit|sdk|listenkey|signature|deprecat|sunset|v[0-9]\b|unified|migrat)`)

// Analyzer is the async worker: Submit enqueues, Run drains.
type Analyzer struct {
	llm     *LLM
	page    *httpx.Client
	clickup *ClickUp
	store   *store.Store
	sender  *telegram.Sender
	opts    Options
	log     *slog.Logger

	queue chan model.Announcement
	mu    sync.Mutex
	// inflight prevents the same item being queued twice by overlapping polls
	// before the store records it as analyzed.
	inflight map[string]bool

	statsMu  sync.Mutex
	analyzed int
	skipped  int
	tasks    int
	merged   int
	errors   int
}

// New wires an Analyzer. pageClient is used for article downloads (short
// timeout); the LLM client carries its own longer timeout.
func New(llm *LLM, pageClient *httpx.Client, st *store.Store, sender *telegram.Sender, opts Options, log *slog.Logger) *Analyzer {
	if opts.Scope == "" {
		opts.Scope = "all"
	}
	if opts.MinTaskImportance == "" {
		opts.MinTaskImportance = "high"
	}
	if opts.MaxArticle <= 0 {
		opts.MaxArticle = 12000
	}
	if opts.ClickUpTag == "" {
		opts.ClickUpTag = "exchange-api"
	}
	a := &Analyzer{
		llm:      llm,
		page:     pageClient,
		store:    st,
		sender:   sender,
		opts:     opts,
		log:      log.With("component", "analyze"),
		queue:    make(chan model.Announcement, 512),
		inflight: map[string]bool{},
	}
	if opts.ClickUpToken != "" && opts.ClickUpListID != "" {
		a.clickup = NewClickUp(opts.ClickUpToken, opts.ClickUpListID, opts.ClickUpTag, pageClient)
	}
	return a
}

// Stats is a snapshot for /stats.
type Stats struct {
	Analyzed int `json:"analyzed"`
	Skipped  int `json:"skipped"`
	Tasks    int `json:"tasks_created"`
	Merged   int `json:"merged_into_existing"`
	Errors   int `json:"errors"`
	Queued   int `json:"queued"`
}

// Snapshot returns counters.
func (a *Analyzer) Snapshot() Stats {
	a.statsMu.Lock()
	defer a.statsMu.Unlock()
	return Stats{Analyzed: a.analyzed, Skipped: a.skipped, Tasks: a.tasks, Merged: a.merged, Errors: a.errors, Queued: len(a.queue)}
}

func analyzedKey(a model.Announcement) string { return "ai:" + a.DedupKey() }
func taskKey(a model.Announcement) string     { return "task:" + a.DedupKey() }

// Submit queues an announcement unless it was analyzed before. Safe to call
// from every poll: the store makes it idempotent across restarts.
func (a *Analyzer) Submit(ann model.Announcement) {
	if a.opts.Scope == "api" && !ann.IsAPIRelated() {
		return
	}
	if ann.IsOperational() {
		return // hosting status / billing: not exchange news
	}
	key := analyzedKey(ann)
	if a.store.IsSeen(key) {
		return
	}
	if a.isNoise(ann) {
		a.store.MarkSeen(key, time.Now())
		a.bump(&a.skipped)
		a.log.Debug("analysis skipped as noise", "exchange", ann.Exchange, "title", ann.Title)
		return
	}
	a.mu.Lock()
	if a.inflight[key] {
		a.mu.Unlock()
		return
	}
	a.inflight[key] = true
	a.mu.Unlock()
	select {
	case a.queue <- ann:
	default:
		a.mu.Lock()
		delete(a.inflight, key)
		a.mu.Unlock()
		a.log.Warn("analyze queue full, will retry next poll", "title", ann.Title)
	}
}

// Run processes the queue until ctx is done. One item at a time: the model
// call dominates, and serial keeps vendor rate limits trivially satisfied.
func (a *Analyzer) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ann := <-a.queue:
			a.process(ctx, ann)
			a.mu.Lock()
			delete(a.inflight, analyzedKey(ann))
			a.mu.Unlock()
		}
	}
}

func (a *Analyzer) process(ctx context.Context, ann model.Announcement) {
	v, err := a.Analyze(ctx, ann)
	if err != nil {
		// Not marked analyzed: the next poll re-submits it. A permanent failure
		// (bad key) would loop, so log loudly and count it.
		a.log.Error("analysis failed", "exchange", ann.Exchange, "title", ann.Title, "err", err)
		a.bump(&a.errors)
		return
	}
	a.store.MarkSeen(analyzedKey(ann), time.Now())
	a.bump(&a.analyzed)
	a.log.Info("analyzed",
		"exchange", ann.Exchange, "title", ann.Title,
		"api", v.APIRelated, "importance", v.Importance, "needs_change", v.NeedsTerminalChange,
	)
	if !a.shouldFileTask(v) {
		return
	}
	url, err := a.fileTask(ctx, ann, v)
	if err != nil {
		a.log.Error("clickup task failed", "title", ann.Title, "err", err)
		a.bump(&a.errors)
		return
	}
	a.bump(&a.tasks)
	a.notify(ctx, ann, v, url)
}

// isNoise: feed category or title says listing/promo/earn and nothing in the
// title points at the API surface.
func (a *Analyzer) isNoise(ann model.Announcement) bool {
	if keepTitle.MatchString(ann.Title) {
		return false
	}
	src := strings.ToLower(ann.Source)
	if strings.Contains(src, "listing") || strings.Contains(src, "delisting") {
		return true
	}
	skip := a.opts.SkipTitle
	if skip == nil {
		skip = DefaultSkipTitle
	}
	return skip.MatchString(ann.Title)
}

func (a *Analyzer) shouldFileTask(v *Verdict) bool {
	return v.APIRelated && v.NeedsTerminalChange && ImportanceRank(v.Importance) >= ImportanceRank(a.opts.MinTaskImportance)
}

// Analyze runs the model on one announcement (article text + feed metadata).
func (a *Analyzer) Analyze(ctx context.Context, ann model.Announcement) (*Verdict, error) {
	article := ""
	if a.opts.FetchArticle && ann.URL != "" {
		article = a.fetchArticle(ctx, ann.URL)
	}
	user := buildUserPrompt(ann, article, a.opts.MaxArticle)
	cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	raw, err := a.llm.Complete(cctx, systemPrompt, user, 3000)
	if err != nil {
		return nil, err
	}
	v, err := parseVerdict(raw)
	if err != nil {
		return nil, fmt.Errorf("%w (answer: %s)", err, truncate(raw, 200))
	}
	return v, nil
}

func parseVerdict(raw string) (*Verdict, error) {
	js := extractJSON(raw)
	if js == "" {
		return nil, fmt.Errorf("model answer is not JSON")
	}
	var v Verdict
	if err := json.Unmarshal([]byte(js), &v); err != nil {
		return nil, fmt.Errorf("model JSON: %w", err)
	}
	v.Importance = strings.ToLower(strings.TrimSpace(v.Importance))
	if ImportanceRank(v.Importance) == 0 {
		v.Importance = "none"
	}
	return &v, nil
}

// fetchArticle downloads the page and returns its visible text (best effort:
// JS-rendered pages yield little, and the model is told to rely on the feed
// preview then).
func (a *Analyzer) fetchArticle(ctx context.Context, url string) string {
	fctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	body, err := a.page.Get(fctx, url, map[string]string{
		"Accept": "text/html,application/xhtml+xml,application/json;q=0.9,*/*;q=0.8",
	})
	if err != nil {
		a.log.Debug("article fetch failed", "url", url, "err", err)
		return ""
	}
	text := HTMLToText(string(body))
	if len(text) < 200 {
		return ""
	}
	return text
}

var (
	reScript = regexp.MustCompile(`(?is)<(script|style|noscript|svg|head)[^>]*>.*?</(script|style|noscript|svg|head)>`)
	reBlock  = regexp.MustCompile(`(?i)</(p|div|li|tr|h[1-6]|br|section|article|table)>|<br\s*/?>`)
	reTag    = regexp.MustCompile(`(?s)<[^>]+>`)
	reSpaces = regexp.MustCompile(`[ \t\r\f\v]+`)
	reLines  = regexp.MustCompile(`\n{3,}`)
)

// HTMLToText strips markup, keeping paragraph breaks so lists and tables in an
// announcement stay readable for the model.
func HTMLToText(s string) string {
	s = reScript.ReplaceAllString(s, " ")
	s = reBlock.ReplaceAllString(s, "\n")
	s = reTag.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	s = reSpaces.ReplaceAllString(s, " ")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	s = strings.Join(lines, "\n")
	s = reLines.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func (a *Analyzer) bump(c *int) {
	a.statsMu.Lock()
	*c++
	a.statsMu.Unlock()
}

// fileTask creates the ClickUp task (or returns the existing one's URL when a
// task for this announcement already exists).
func (a *Analyzer) fileTask(ctx context.Context, ann model.Announcement, v *Verdict) (string, error) {
	title := taskName(ann, v)
	md := taskMarkdown(ann, v)
	if a.opts.DryRun || a.clickup == nil {
		a.log.Info("DRY_RUN clickup task", "name", title, "importance", v.Importance)
		fmt.Println("------ (dry-run clickup task) ------\n" + title + "\n\n" + md + "\n------------------------------------")
		return "", nil
	}
	if a.store.IsSeen(taskKey(ann)) {
		return "", nil
	}
	// Second guard against a lost state file: an open task with the same name.
	if existing, err := a.clickup.FindOpenByName(ctx, title); err == nil && existing != nil {
		a.store.MarkSeen(taskKey(ann), time.Now())
		return existing.URL, nil
	}
	// Cross-source dedup: the same change seen via another feed / docs commit
	// goes into the existing task as a comment with the new link.
	if dup, err := a.findDuplicate(ctx, ann, v); err != nil {
		a.log.Warn("dedup check failed, creating task", "title", ann.Title, "err", err)
	} else if dup != nil {
		if err := a.clickup.AddComment(ctx, dup.ID, mergeComment(ann, v)); err != nil {
			return "", err
		}
		a.store.MarkSeen(taskKey(ann), time.Now())
		a.bump(&a.merged)
		return dup.URL, nil
	}
	prio := 2 // high
	if v.Importance == "critical" {
		prio = 1 // urgent
	}
	t, err := a.clickup.CreateTask(ctx, title, md, prio)
	if err != nil {
		return "", err
	}
	a.store.MarkSeen(taskKey(ann), time.Now())
	return t.URL, nil
}

func taskName(ann model.Announcement, v *Verdict) string {
	t := strings.TrimSpace(v.TaskTitle)
	if t == "" {
		t = ann.Title
	}
	return truncate(fmt.Sprintf("[%s] %s", strings.ToUpper(ann.Exchange), t), 240)
}

func bullets(items []string) string {
	if len(items) == 0 {
		return "—"
	}
	var sb strings.Builder
	for _, it := range items {
		it = strings.TrimSpace(it)
		if it == "" {
			continue
		}
		sb.WriteString("- ")
		sb.WriteString(it)
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

func taskMarkdown(ann model.Announcement, v *Verdict) string {
	when := "—"
	if !ann.PublishedAt.IsZero() {
		when = ann.PublishedAt.UTC().Format("2006-01-02 15:04 UTC")
	}
	eff := strings.TrimSpace(v.EffectiveAt)
	if eff == "" {
		eff = "не указан / уточнить в анонсе"
	}
	return strings.Join([]string{
		"## Что изменилось",
		strings.TrimSpace(v.Summary),
		"",
		bullets(v.Changes),
		"",
		"## Что сделать в терминале",
		bullets(v.Actions),
		"",
		"## Контекст",
		fmt.Sprintf("- Биржа: **%s** · затронуто: %s", strings.ToUpper(ann.Exchange), strings.Join(v.Affected, ", ")),
		fmt.Sprintf("- Вступает в силу: **%s**", eff),
		fmt.Sprintf("- Анонс: %s", ann.URL),
		fmt.Sprintf("- Опубликован: %s · источник `%s`", when, ann.Source),
		fmt.Sprintf("- Оценка модели: %s", v.Importance),
		"",
		"_Создано exchange-api-update-bot автоматически. Проверить первоисточник перед изменением кода._",
	}, "\n")
}

// notify posts a short Telegram note with the task link.
func (a *Analyzer) notify(ctx context.Context, ann model.Announcement, v *Verdict, taskURL string) {
	if a.sender == nil {
		return
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "🧠 <b>API-изменение · %s · %s</b>\n", html.EscapeString(strings.ToUpper(ann.Exchange)), html.EscapeString(v.Importance))
	fmt.Fprintf(&sb, "%s\n", html.EscapeString(ann.Title))
	fmt.Fprintf(&sb, "\n%s\n", html.EscapeString(truncate(v.Summary, 700)))
	if len(v.Actions) > 0 {
		sb.WriteString("\n<b>В терминале:</b>\n")
		for i, act := range v.Actions {
			if i >= 5 {
				break
			}
			fmt.Fprintf(&sb, "• %s\n", html.EscapeString(truncate(act, 200)))
		}
	}
	if eff := strings.TrimSpace(v.EffectiveAt); eff != "" {
		fmt.Fprintf(&sb, "\n⏰ Вступает в силу: %s\n", html.EscapeString(eff))
	}
	if taskURL != "" {
		fmt.Fprintf(&sb, "\n📌 <a href=\"%s\">Задача в ClickUp</a>", taskURL)
	}
	fmt.Fprintf(&sb, "\n🔗 %s", html.EscapeString(ann.URL))
	text := sb.String()
	if a.opts.DryRun {
		fmt.Println("------ (dry-run analysis note) ------\n" + text + "\n-------------------------------------")
		return
	}
	if err := a.sender.SendMessage(ctx, a.sender.ChatID(), text, nil); err != nil {
		a.log.Warn("analysis note failed", "err", err)
	}
}
