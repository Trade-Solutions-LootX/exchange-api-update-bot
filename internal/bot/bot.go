// Package bot implements the interactive Telegram command handler: it
// long-polls getUpdates and turns commands (/status, /subscribe, /balance,
// /servers, /check, /mute, /min …) and inline-button presses into control-state
// changes and replies. It only responds to the single configured chat.
package bot

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"exchangebot/internal/control"
	"exchangebot/internal/httpx"
	"exchangebot/internal/model"
	"exchangebot/internal/poller"
	"exchangebot/internal/sources"
	"exchangebot/internal/telegram"
)

// Deps are everything the command handler needs.
type Deps struct {
	Sender  *telegram.Sender
	Control *control.Controller
	Poller  *poller.Poller
	Sources []sources.Source
	HTTP    *httpx.Client
	// Provider credentials for /balance (optional).
	DOToken, VultrKey, RenderKey string
	// Servers for /servers (optional).
	Servers []sources.ServerTarget
	Log     *slog.Logger
}

// Bot is the interactive command handler.
type Bot struct {
	d          Deps
	chatID     string
	httpClient *http.Client // for /servers metric probes
}

// New builds the command handler.
func New(d Deps) *Bot {
	return &Bot{d: d, chatID: d.Sender.ChatID(), httpClient: &http.Client{Timeout: 10 * time.Second}}
}

// Run long-polls until ctx is cancelled.
func (b *Bot) Run(ctx context.Context) {
	b.d.Log.Info("interactive command handler started")
	offset := b.skipBacklog(ctx)
	for {
		if ctx.Err() != nil {
			return
		}
		updates, err := b.d.Sender.GetUpdates(ctx, offset, 30)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			b.d.Log.Warn("getUpdates failed", "err", err)
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

// skipBacklog returns an offset past any already-queued updates so a restart
// ignores stale commands.
func (b *Bot) skipBacklog(ctx context.Context) int {
	updates, err := b.d.Sender.GetUpdates(ctx, -1, 0)
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
			b.d.Log.Debug("ignoring command from unauthorized chat", "chat", u.Message.Chat.ID)
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
	case "/subscribe", "/topics", "/podpiska":
		kb := b.subscribeKeyboard()
		_ = b.d.Sender.SendMessage(ctx, b.chatID, subscribeIntro, &kb)
	case "/balance", "/balances":
		b.cmdBalance(ctx)
	case "/servers", "/serv":
		b.cmdServers(ctx)
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
		b.reply(ctx, "Не знаю такую команду. Нажмите /help.")
	}
}

func (b *Bot) handleCallback(ctx context.Context, u telegram.Update) {
	cq := u.CallbackQuery
	parts := strings.Split(cq.Data, "|")
	chat := strconv.FormatInt(cq.Message.Chat.ID, 10)
	switch parts[0] {
	case "t": // t|<category> — toggle subscription, refresh the menu in place
		if len(parts) >= 2 {
			cat := model.Category(parts[1])
			on := b.d.Control.ToggleSubscription(cat)
			state := "включено ✅"
			if !on {
				state = "выключено ❌"
			}
			_ = b.d.Sender.AnswerCallback(ctx, cq.ID, categoryLabel(cat)+": "+state)
			kb := b.subscribeKeyboard()
			_ = b.d.Sender.EditReplyMarkup(ctx, chat, cq.Message.MessageID, &kb)
			return
		}
	case "m": // m|<slug>|24h — mute
		if len(parts) >= 3 {
			slug := parts[1]
			d, _ := time.ParseDuration(parts[2])
			if d <= 0 {
				d = 24 * time.Hour
			}
			b.d.Control.Mute(slug, d)
			_ = b.d.Sender.AnswerCallback(ctx, cq.ID, "Замьютил "+strings.ToUpper(slug)+" на "+parts[2])
		}
	case "ok":
		_ = b.d.Sender.AnswerCallback(ctx, cq.ID, "Ок")
	default:
		_ = b.d.Sender.AnswerCallback(ctx, cq.ID, "")
	}
	// For non-toggle buttons, clear the keyboard so they can't be pressed twice.
	if parts[0] != "t" {
		_ = b.d.Sender.EditReplyMarkup(ctx, chat, cq.Message.MessageID, nil)
	}
}

// ── Subscribe menu ──────────────────────────────────────────────────────────

func (b *Bot) subscribeKeyboard() telegram.InlineKeyboard {
	subs := b.d.Control.Subscriptions()
	var rows [][]telegram.InlineButton
	row := []telegram.InlineButton{}
	for _, cat := range model.AllCategories {
		mark := "❌"
		if subs[cat] {
			mark = "✅"
		}
		row = append(row, telegram.InlineButton{
			Text: mark + " " + categoryLabel(cat),
			Data: "t|" + string(cat),
		})
		if len(row) == 2 {
			rows = append(rows, row)
			row = []telegram.InlineButton{}
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	return telegram.InlineKeyboard{Inline: rows}
}

func categoryLabel(cat model.Category) string {
	switch cat {
	case model.CatAPI:
		return "🔧 API-апдейты"
	case model.CatListing:
		return "🆕 Листинги"
	case model.CatDelisting:
		return "🗑 Делистинги"
	case model.CatMaintenance:
		return "🛠 Техработы/сбои"
	case model.CatInfra:
		return "☁️ Хостинг (статус)"
	case model.CatBilling:
		return "💳 Биллинг"
	case model.CatPromo:
		return "🎁 Промо/акции"
	default:
		return "📰 Прочее"
	}
}

// ── Balance & servers ───────────────────────────────────────────────────────

func (b *Bot) cmdBalance(ctx context.Context) {
	if b.d.DOToken == "" && b.d.VultrKey == "" && b.d.RenderKey == "" {
		b.reply(ctx, "Провайдеры не настроены (нет токенов DO/Vultr/Render).")
		return
	}
	b.reply(ctx, "💳 Запрашиваю балансы, секунду…")
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	b.reply(cctx, sources.BalancesText(cctx, b.d.HTTP, b.d.DOToken, b.d.VultrKey, b.d.RenderKey))
}

func (b *Bot) cmdServers(ctx context.Context) {
	if len(b.d.Servers) == 0 {
		b.reply(ctx, "Серверы не настроены. Добавьте SERVER_METRICS=имя|url|токен;…")
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var sb strings.Builder
	sb.WriteString("🖥 <b>Серверы</b>\n")
	for _, t := range b.d.Servers {
		m, err := sources.FetchServerMetrics(cctx, b.httpClient, t)
		if err != nil {
			sb.WriteString("\n<b>" + esc(t.Name) + "</b> — 🔴 недоступен (" + esc(short(err.Error())) + ")\n")
			continue
		}
		sb.WriteString(fmt.Sprintf("\n<b>%s</b> — 🟢 up %s\n  CPU %.0f%% · RAM %.0f%% · диск %.0f%%\n  load %.2f / %.2f / %.2f\n",
			esc(t.Name), humanUptime(m.UptimeSecs), m.CPU.UsagePct, m.Mem.UsedPct, m.Disk.UsedPct,
			m.CPU.Load1, m.CPU.Load5, m.CPU.Load15))
	}
	b.reply(cctx, sb.String())
}

func humanUptime(secs int64) string {
	d := time.Duration(secs) * time.Second
	if d >= 24*time.Hour {
		return fmt.Sprintf("%dд %dч", int(d.Hours())/24, int(d.Hours())%24)
	}
	if d >= time.Hour {
		return fmt.Sprintf("%dч %dм", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dм", int(d.Minutes()))
}

// ── Check / mute / min / status ─────────────────────────────────────────────

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
		b.reply(ctx, "Как пользоваться: <code>/check &lt;биржа&gt; [api]</code>\nНапр. <code>/check binance</code> или <code>/check okx api</code>")
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	var lines []string
	matched := 0
	for _, s := range b.d.Sources {
		if !containsSlug(filter, s.Exchange()) {
			continue
		}
		if sys, ok := s.(sources.SystemSource); ok && sys.IsSystem() {
			continue // system sources have side-effecting Fetch; skip
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
		b.reply(ctx, "Нет источников для "+esc(strings.Join(filter, ", "))+". Список: /sources.")
		return
	}
	if len(lines) == 0 {
		b.reply(ctx, "Сейчас ничего подходящего нет.")
		return
	}
	b.reply(ctx, strings.Join(lines, "\n\n"))
}

func (b *Bot) cmdMute(ctx context.Context, args []string) {
	if len(args) == 0 {
		b.reply(ctx, "Как пользоваться: <code>/mute &lt;биржа&gt; [время]</code>\nНапр. <code>/mute bybit 12h</code> (без времени = навсегда)")
		return
	}
	slug := strings.ToLower(args[0])
	var d time.Duration
	human := "навсегда"
	if len(args) >= 2 {
		if parsed, err := time.ParseDuration(args[1]); err == nil && parsed > 0 {
			d = parsed
			human = "на " + args[1]
		}
	}
	b.d.Control.Mute(slug, d)
	b.reply(ctx, "🔕 Замьютил <b>"+esc(strings.ToUpper(slug))+"</b> "+human+".")
}

func (b *Bot) cmdUnmute(ctx context.Context, args []string) {
	if len(args) == 0 {
		b.reply(ctx, "Как пользоваться: <code>/unmute &lt;биржа&gt;</code>")
		return
	}
	slug := strings.ToLower(args[0])
	b.d.Control.Unmute(slug)
	b.reply(ctx, "🔔 Включил обратно <b>"+esc(strings.ToUpper(slug))+"</b>.")
}

func (b *Bot) cmdMin(ctx context.Context, args []string) {
	if len(args) == 0 {
		b.reply(ctx, "Сейчас порог важности: <b>"+b.d.Control.MinImportance().String()+"</b>.\nМеняется: <code>/min low|medium|high|critical|reset</code>.")
		return
	}
	switch strings.ToLower(args[0]) {
	case "reset", "default":
		b.d.Control.ResetMinImportance()
		b.reply(ctx, "Порог сброшен на значение по умолчанию.")
	case "low":
		b.setMin(ctx, model.ImportanceLow)
	case "medium", "med":
		b.setMin(ctx, model.ImportanceMedium)
	case "high":
		b.setMin(ctx, model.ImportanceHigh)
	case "critical":
		b.setMin(ctx, model.ImportanceCritical)
	default:
		b.reply(ctx, "Не понял уровень. Варианты: low|medium|high|critical|reset.")
	}
}

func (b *Bot) setMin(ctx context.Context, imp model.Importance) {
	b.d.Control.SetMinImportance(imp)
	b.reply(ctx, "Порог важности: <b>"+imp.String()+"</b>. Приходить будет только "+imp.String()+" и выше.")
}

func (b *Bot) statusText() string {
	st := b.d.Poller.Snapshot()
	var sb strings.Builder
	sb.WriteString("📊 <b>Статус</b>\n")
	sb.WriteString(fmt.Sprintf("Источников: %d · отправлено: %d · ошибок: %d\n", st.Sources, st.Sent, st.Errors))
	sb.WriteString("Порог важности: <b>" + b.d.Control.MinImportance().String() + "</b>\n")
	if !st.LastCycle.IsZero() {
		sb.WriteString("Последний опрос: " + st.LastCycle.UTC().Format("2006-01-02 15:04 MST") + "\n")
	}
	if len(st.DownFeeds) > 0 {
		sb.WriteString("🔴 Не отвечают: <b>" + esc(strings.Join(st.DownFeeds, ", ")) + "</b>\n")
	} else {
		sb.WriteString("🟢 Все источники живы\n")
	}
	// Active subscriptions.
	subs := b.d.Control.Subscriptions()
	var on []string
	for _, cat := range model.AllCategories {
		if subs[cat] {
			on = append(on, string(cat))
		}
	}
	sb.WriteString("Подписки: " + esc(strings.Join(on, ", ")) + "\n(изменить — /subscribe)\n")
	muted := b.d.Control.MutedSlugs()
	if len(muted) > 0 {
		sb.WriteString("🔕 Замьючено: " + esc(strings.Join(muted, ", ")))
	} else {
		sb.WriteString("🔔 Ничего не замьючено")
	}
	return sb.String()
}

func (b *Bot) sourcesText() string {
	names := make([]string, 0, len(b.d.Sources))
	for _, s := range b.d.Sources {
		names = append(names, s.Name())
	}
	sort.Strings(names)
	return "<b>Источники (" + strconv.Itoa(len(names)) + ")</b>\n" + esc(strings.Join(names, "\n"))
}

func (b *Bot) mutedText() string {
	m := b.d.Control.Muted()
	if len(m) == 0 {
		return "🔔 Ничего не замьючено."
	}
	slugs := make([]string, 0, len(m))
	for s := range m {
		slugs = append(slugs, s)
	}
	sort.Strings(slugs)
	var sb strings.Builder
	sb.WriteString("<b>🔕 Замьючено</b>\n")
	for _, s := range slugs {
		until := m[s]
		if until.IsZero() {
			sb.WriteString(esc(s) + " — навсегда\n")
		} else {
			sb.WriteString(esc(s) + " — до " + until.UTC().Format("15:04 MST 02 Jan") + "\n")
		}
	}
	return sb.String()
}

func (b *Bot) reply(ctx context.Context, text string) {
	if err := b.d.Sender.SendMessage(ctx, b.chatID, text, nil); err != nil {
		b.d.Log.Warn("reply failed", "err", err)
	}
}

const subscribeIntro = "⚙️ <b>Что присылать?</b>\nНажимайте, чтобы включить (✅) или выключить (❌) тип новостей:"

const helpText = `<b>🤖 Монитор бирж и серверов</b>

<b>Новости</b>
/subscribe — выбрать типы новостей (кнопками ✅/❌)
/check &lt;биржа&gt; [api] — свежие новости прямо сейчас
/min low|medium|high|critical — порог важности
/mute &lt;биржа&gt; [время] · /unmute · /muted

<b>Серверы и деньги</b>
/balance — балансы DigitalOcean / Vultr / Render
/servers — нагрузка ваших серверов (CPU/RAM/диск)

<b>Прочее</b>
/status — что происходит
/sources — список источников

Под каждым сообщением есть кнопки 🔕 замьютить и ✅ ок.`

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
