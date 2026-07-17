// Package bot implements the interactive Telegram command handler: it
// long-polls getUpdates and turns commands (/status, /check, /mute, /min …) and
// inline-button presses (mute/ack) into control-state changes and replies.
//
// It only responds to the single configured chat, so the bot cannot be driven
// by strangers who happen to find it.
package bot

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"exchangebot/internal/control"
	"exchangebot/internal/model"
	"exchangebot/internal/poller"
	"exchangebot/internal/sources"
	"exchangebot/internal/telegram"

	"log/slog"
)

// Bot is the interactive command handler.
type Bot struct {
	sender  *telegram.Sender
	control *control.Controller
	poller  *poller.Poller
	sources []sources.Source
	chatID  string
	log     *slog.Logger
}

// New builds the command handler.
func New(sender *telegram.Sender, ctrl *control.Controller, p *poller.Poller, srcs []sources.Source, log *slog.Logger) *Bot {
	return &Bot{sender: sender, control: ctrl, poller: p, sources: srcs, chatID: sender.ChatID(), log: log}
}

// Run long-polls until ctx is cancelled.
func (b *Bot) Run(ctx context.Context) {
	b.log.Info("interactive command handler started")
	// Skip any commands queued while the bot was down, so a restart doesn't
	// replay stale /check or /mute messages.
	offset := b.skipBacklog(ctx)
	for {
		if ctx.Err() != nil {
			return
		}
		updates, err := b.sender.GetUpdates(ctx, offset, 30)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			b.log.Warn("getUpdates failed", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
			continue
		}
		for _, u := range updates {
			offset = u.UpdateID + 1
			b.handle(ctx, u)
		}
	}
}

// skipBacklog returns an offset past any already-queued updates (offset=-1
// fetches only the most recent one), so a restart ignores stale commands.
func (b *Bot) skipBacklog(ctx context.Context) int {
	updates, err := b.sender.GetUpdates(ctx, -1, 0)
	if err != nil || len(updates) == 0 {
		return 0
	}
	return updates[len(updates)-1].UpdateID + 1
}

func (b *Bot) handle(ctx context.Context, u telegram.Update) {
	switch {
	case u.CallbackQuery != nil:
		if strconv.FormatInt(u.CallbackQuery.Message.Chat.ID, 10) != b.chatID {
			return
		}
		b.handleCallback(ctx, u)
	case u.Message != nil && u.Message.Text != "":
		if strconv.FormatInt(u.Message.Chat.ID, 10) != b.chatID {
			b.log.Debug("ignoring command from unauthorized chat", "chat", u.Message.Chat.ID)
			return
		}
		b.handleCommand(ctx, u.Message.Text)
	}
}

func (b *Bot) handleCommand(ctx context.Context, text string) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 {
		return
	}
	// Strip a trailing @botname (groups append it).
	cmd := strings.ToLower(fields[0])
	if i := strings.Index(cmd, "@"); i > 0 {
		cmd = cmd[:i]
	}
	args := fields[1:]

	switch cmd {
	case "/start", "/help":
		b.reply(ctx, helpText)
	case "/status":
		b.reply(ctx, b.statusText())
	case "/sources":
		b.reply(ctx, b.sourcesText())
	case "/muted":
		b.reply(ctx, b.mutedText())
	case "/mute":
		b.cmdMute(ctx, args)
	case "/unmute":
		b.cmdUnmute(ctx, args)
	case "/min":
		b.cmdMin(ctx, args)
	case "/check":
		b.cmdCheck(ctx, args)
	default:
		b.reply(ctx, "Unknown command. Send /help.")
	}
}

func (b *Bot) handleCallback(ctx context.Context, u telegram.Update) {
	cq := u.CallbackQuery
	parts := strings.Split(cq.Data, "|")
	switch parts[0] {
	case "m": // m|<slug>|24h
		if len(parts) >= 3 {
			slug := parts[1]
			d, _ := time.ParseDuration(parts[2])
			if d <= 0 {
				d = 24 * time.Hour
			}
			b.control.Mute(slug, d)
			_ = b.sender.AnswerCallback(ctx, cq.ID, "Muted "+strings.ToUpper(slug)+" for "+parts[2])
		}
	case "ok":
		_ = b.sender.AnswerCallback(ctx, cq.ID, "OK")
	default:
		_ = b.sender.AnswerCallback(ctx, cq.ID, "")
	}
	// Clear the inline keyboard so the buttons can't be pressed twice.
	_ = b.sender.EditReplyMarkup(ctx, strconv.FormatInt(cq.Message.Chat.ID, 10), cq.Message.MessageID, nil)
}

func (b *Bot) cmdMute(ctx context.Context, args []string) {
	if len(args) == 0 {
		b.reply(ctx, "Usage: <code>/mute &lt;exchange&gt; [duration]</code>  e.g. <code>/mute bybit 12h</code> (omit duration = forever)")
		return
	}
	slug := strings.ToLower(args[0])
	var d time.Duration
	human := "forever"
	if len(args) >= 2 {
		if parsed, err := time.ParseDuration(args[1]); err == nil && parsed > 0 {
			d = parsed
			human = "for " + args[1]
		}
	}
	b.control.Mute(slug, d)
	b.reply(ctx, "🔕 Muted <b>"+esc(strings.ToUpper(slug))+"</b> "+human+".")
}

func (b *Bot) cmdUnmute(ctx context.Context, args []string) {
	if len(args) == 0 {
		b.reply(ctx, "Usage: <code>/unmute &lt;exchange&gt;</code>")
		return
	}
	slug := strings.ToLower(args[0])
	b.control.Unmute(slug)
	b.reply(ctx, "🔔 Unmuted <b>"+esc(strings.ToUpper(slug))+"</b>.")
}

func (b *Bot) cmdMin(ctx context.Context, args []string) {
	if len(args) == 0 {
		b.reply(ctx, "Current threshold: <b>"+b.control.MinImportance().String()+"</b>. Set with <code>/min low|medium|high|critical|reset</code>.")
		return
	}
	switch strings.ToLower(args[0]) {
	case "reset", "default":
		b.control.ResetMinImportance()
		b.reply(ctx, "Threshold reset to the configured default.")
	case "low":
		b.setMin(ctx, model.ImportanceLow)
	case "medium", "med":
		b.setMin(ctx, model.ImportanceMedium)
	case "high":
		b.setMin(ctx, model.ImportanceHigh)
	case "critical":
		b.setMin(ctx, model.ImportanceCritical)
	default:
		b.reply(ctx, "Unknown level. Use low|medium|high|critical|reset.")
	}
}

func (b *Bot) setMin(ctx context.Context, imp model.Importance) {
	b.control.SetMinImportance(imp)
	b.reply(ctx, "Threshold set to <b>"+imp.String()+"</b>. Only "+imp.String()+"+ items will be delivered.")
}

// cmdCheck fetches the newest items for the named exchange(s) live.
func (b *Bot) cmdCheck(ctx context.Context, args []string) {
	apiOnly := false
	var filter []string
	for _, a := range args {
		if strings.ToLower(a) == "api" || a == "--api" {
			apiOnly = true
			continue
		}
		filter = append(filter, strings.ToLower(a))
	}
	if len(filter) == 0 {
		b.reply(ctx, "Usage: <code>/check &lt;exchange&gt; [api]</code>  e.g. <code>/check binance</code> or <code>/check okx api</code>")
		return
	}

	cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	var lines []string
	matched := 0
	for _, s := range b.sources {
		if !containsSlug(filter, s.Exchange()) {
			continue
		}
		// Never Fetch a system source here: its Fetch has side effects (it
		// mutates transition state), which would race the poller's own polling.
		if sys, ok := s.(sources.SystemSource); ok && sys.IsSystem() {
			continue
		}
		matched++
		items, err := s.Fetch(cctx)
		if err != nil {
			lines = append(lines, "⚠️ "+esc(s.Name())+": "+esc(short(err.Error())))
			continue
		}
		sort.SliceStable(items, func(i, j int) bool { return items[i].PublishedAt.After(items[j].PublishedAt) })
		shown := 0
		for _, a := range items {
			if apiOnly && !a.IsAPIRelated() && !a.IsOperational() {
				continue
			}
			lines = append(lines, checkLine(a))
			if shown++; shown >= 2 {
				break
			}
		}
	}
	if matched == 0 {
		b.reply(ctx, "No sources match "+esc(strings.Join(filter, ", "))+". Try /sources.")
		return
	}
	if len(lines) == 0 {
		b.reply(ctx, "No matching items right now.")
		return
	}
	b.reply(ctx, strings.Join(lines, "\n\n"))
}

func (b *Bot) statusText() string {
	st := b.poller.Snapshot()
	var sb strings.Builder
	sb.WriteString("<b>📊 Status</b>\n")
	sb.WriteString(fmt.Sprintf("Feeds: %d · delivered: %d · errors: %d\n", st.Sources, st.Sent, st.Errors))
	sb.WriteString("Min importance: <b>" + b.control.MinImportance().String() + "</b>\n")
	if !st.LastCycle.IsZero() {
		sb.WriteString("Last poll: " + st.LastCycle.UTC().Format("2006-01-02 15:04 MST") + "\n")
	}
	if len(st.DownFeeds) > 0 {
		sb.WriteString("🔴 Down feeds: <b>" + esc(strings.Join(st.DownFeeds, ", ")) + "</b>\n")
	} else {
		sb.WriteString("🟢 All feeds healthy\n")
	}
	muted := b.control.MutedSlugs()
	if len(muted) > 0 {
		sb.WriteString("🔕 Muted: " + esc(strings.Join(muted, ", ")))
	} else {
		sb.WriteString("🔔 Nothing muted")
	}
	return sb.String()
}

func (b *Bot) sourcesText() string {
	names := make([]string, 0, len(b.sources))
	for _, s := range b.sources {
		names = append(names, s.Name())
	}
	sort.Strings(names)
	return "<b>Feeds (" + strconv.Itoa(len(names)) + ")</b>\n" + esc(strings.Join(names, "\n"))
}

func (b *Bot) mutedText() string {
	m := b.control.Muted()
	if len(m) == 0 {
		return "🔔 Nothing is muted."
	}
	slugs := make([]string, 0, len(m))
	for s := range m {
		slugs = append(slugs, s)
	}
	sort.Strings(slugs)
	var sb strings.Builder
	sb.WriteString("<b>🔕 Muted</b>\n")
	for _, s := range slugs {
		until := m[s]
		if until.IsZero() {
			sb.WriteString(esc(s) + " — forever\n")
		} else {
			sb.WriteString(esc(s) + " — until " + until.UTC().Format("15:04 MST 02 Jan") + "\n")
		}
	}
	return sb.String()
}

func (b *Bot) reply(ctx context.Context, text string) {
	if err := b.sender.SendMessage(ctx, b.chatID, text, nil); err != nil {
		b.log.Warn("reply failed", "err", err)
	}
}

const helpText = `<b>🤖 Exchange &amp; infra monitor</b>
/status — feed health, muted list, threshold
/check &lt;exchange&gt; [api] — latest items now (e.g. /check binance)
/mute &lt;slug&gt; [dur] — silence a source (e.g. /mute bybit 12h)
/unmute &lt;slug&gt; — re-enable
/muted — what's silenced
/min low|medium|high|critical|reset — delivery threshold
/sources — list all feeds
Tap 🔕 / ✅ under any alert to mute or dismiss it.`

func checkLine(a model.Announcement) string {
	when := ""
	if !a.PublishedAt.IsZero() {
		when = " · " + a.PublishedAt.UTC().Format("2006-01-02 15:04")
	}
	line := fmt.Sprintf("%s <b>%s</b> · %s%s\n%s",
		a.Importance.Emoji(), esc(strings.ToUpper(a.Exchange)), esc(a.MarketsString()), when, esc(a.Title))
	if a.URL != "" {
		line += "\n" + esc(a.URL)
	}
	return line
}

func containsSlug(list []string, slug string) bool {
	for _, s := range list {
		if s == slug {
			return true
		}
	}
	return false
}

func esc(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func short(s string) string {
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}
