// Package telegram formats announcements and delivers them to a Telegram chat
// via the Bot API. It handles HTML escaping, the 4096-char message limit,
// 429 rate-limit backoff, and a dry-run mode for local testing.
package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"exchangebot/internal/httpx"
	"exchangebot/internal/model"
)

const apiBase = "https://api.telegram.org"

// Sender delivers announcements to a chat and (for the command handler) drives
// the interactive Bot API.
type Sender struct {
	token  string
	chatID string
	dryRun bool
	http   *httpx.Client
	// poll is a dedicated client for long-polling getUpdates, whose timeout
	// must exceed the server-side long-poll window (no retry semantics).
	poll *http.Client
	log  *slog.Logger
}

// ChatID returns the configured destination chat id.
func (s *Sender) ChatID() string { return s.chatID }

// New builds a Sender. When dryRun is true, messages are logged, not sent.
func New(token, chatID string, dryRun bool, hc *httpx.Client, log *slog.Logger) *Sender {
	return &Sender{
		token:  token,
		chatID: chatID,
		dryRun: dryRun,
		http:   hc,
		poll:   &http.Client{Timeout: 65 * time.Second},
		log:    log,
	}
}

// Send formats and delivers a single announcement. backfill=true tags the
// message as historical (test/seed) so the user knows it is not breaking news.
func (s *Sender) Send(ctx context.Context, a model.Announcement, backfill bool) error {
	text := Format(a, backfill)

	if s.dryRun {
		s.log.Info("DRY_RUN telegram message",
			"exchange", a.Exchange,
			"importance", a.Importance.String(),
			"title", a.Title,
			"url", a.URL,
		)
		fmt.Println("------ (dry-run) ------\n" + stripHTML(text) + "\n-----------------------")
		return nil
	}

	payload := map[string]any{
		"chat_id":                  s.chatID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": false,
		// Inline controls: mute this source for 24h, or acknowledge (clear).
		"reply_markup": alertKeyboard(a.Exchange),
	}
	body, _ := json.Marshal(payload)

	url := fmt.Sprintf("%s/bot%s/sendMessage", apiBase, s.token)
	// httpx already retries 429/5xx with Retry-After; one extra guard here for
	// the Telegram-specific "ok:false" envelope.
	resp, err := s.http.Post(ctx, url, body, nil)
	if err != nil {
		return fmt.Errorf("telegram send: %w", err)
	}
	var env struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		return fmt.Errorf("telegram decode: %w", err)
	}
	if !env.OK {
		if env.Parameters.RetryAfter > 0 {
			wait := time.Duration(env.Parameters.RetryAfter) * time.Second
			s.log.Warn("telegram rate limited, backing off", "retry_after", wait.String())
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
			// One retry after honoring the server-provided delay.
			resp2, err := s.http.Post(ctx, url, body, nil)
			if err != nil {
				return fmt.Errorf("telegram send (retry): %w", err)
			}
			if err := json.Unmarshal(resp2, &env); err == nil && env.OK {
				return nil
			}
		}
		return fmt.Errorf("telegram API error: %s", env.Description)
	}
	return nil
}

// Ping verifies the bot token by calling getMe. Used at startup so misconfig
// fails fast and loudly instead of silently dropping messages.
func (s *Sender) Ping(ctx context.Context) (string, error) {
	if s.dryRun {
		return "dry-run", nil
	}
	url := fmt.Sprintf("%s/bot%s/getMe", apiBase, s.token)
	resp, err := s.http.Get(ctx, url, nil)
	if err != nil {
		return "", err
	}
	var env struct {
		OK     bool `json:"ok"`
		Result struct {
			Username string `json:"username"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		return "", err
	}
	if !env.OK {
		return "", fmt.Errorf("telegram getMe failed (check TELEGRAM_BOT_TOKEN)")
	}
	return env.Result.Username, nil
}

// ─── Interactive API (used by the command handler) ──────────────────────────

// InlineButton is one inline-keyboard button.
type InlineButton struct {
	Text string `json:"text"`
	Data string `json:"callback_data"`
}

// InlineKeyboard is a Telegram inline keyboard markup.
type InlineKeyboard struct {
	Inline [][]InlineButton `json:"inline_keyboard"`
}

// alertKeyboard builds the mute/ack controls shown under every alert.
func alertKeyboard(slug string) InlineKeyboard {
	return InlineKeyboard{Inline: [][]InlineButton{{
		{Text: "🔕 Mute " + strings.ToUpper(slug) + " 24h", Data: "m|" + slug + "|24h"},
		{Text: "✅ OK", Data: "ok"},
	}}}
}

// Update is a minimal Telegram getUpdates result item.
type Update struct {
	UpdateID int `json:"update_id"`
	Message  *struct {
		MessageID int    `json:"message_id"`
		Text      string `json:"text"`
		Chat      struct {
			ID int64 `json:"id"`
		} `json:"chat"`
	} `json:"message"`
	CallbackQuery *struct {
		ID      string `json:"id"`
		Data    string `json:"data"`
		Message struct {
			MessageID int `json:"message_id"`
			Chat      struct {
				ID int64 `json:"id"`
			} `json:"chat"`
		} `json:"message"`
	} `json:"callback_query"`
}

// GetUpdates long-polls for new updates. It uses a dedicated client whose
// timeout exceeds the server-side long-poll window.
func (s *Sender) GetUpdates(ctx context.Context, offset, timeoutSec int) ([]Update, error) {
	url := fmt.Sprintf("%s/bot%s/getUpdates?offset=%d&timeout=%d&allowed_updates=[\"message\",\"callback_query\"]",
		apiBase, s.token, offset, timeoutSec)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.poll.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var env struct {
		OK     bool     `json:"ok"`
		Result []Update `json:"result"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, err
	}
	if !env.OK {
		return nil, fmt.Errorf("getUpdates not ok")
	}
	return env.Result, nil
}

// SendMessage sends arbitrary HTML text to a chat with an optional inline
// keyboard (pass nil for none). Returns nothing but errors on API failure.
func (s *Sender) SendMessage(ctx context.Context, chatID, text string, markup *InlineKeyboard) error {
	payload := map[string]any{
		"chat_id":                  chatID,
		"text":                     safeTruncate(text, 4000),
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	}
	if markup != nil {
		payload["reply_markup"] = markup
	}
	return s.simpleCall(ctx, "sendMessage", payload)
}

// AnswerCallback acknowledges a button press (removes the "loading" spinner).
func (s *Sender) AnswerCallback(ctx context.Context, id, text string) error {
	return s.simpleCall(ctx, "answerCallbackQuery", map[string]any{
		"callback_query_id": id,
		"text":              text,
	})
}

// EditReplyMarkup replaces (or clears, with nil) a message's inline keyboard.
func (s *Sender) EditReplyMarkup(ctx context.Context, chatID string, messageID int, markup *InlineKeyboard) error {
	payload := map[string]any{"chat_id": chatID, "message_id": messageID}
	if markup != nil {
		payload["reply_markup"] = markup
	} else {
		payload["reply_markup"] = InlineKeyboard{Inline: [][]InlineButton{}}
	}
	return s.simpleCall(ctx, "editMessageReplyMarkup", payload)
}

// simpleCall POSTs a payload to a Bot API method and checks the ok flag.
func (s *Sender) simpleCall(ctx context.Context, method string, payload map[string]any) error {
	body, _ := json.Marshal(payload)
	url := fmt.Sprintf("%s/bot%s/%s", apiBase, s.token, method)
	resp, err := s.http.Post(ctx, url, body, nil)
	if err != nil {
		return fmt.Errorf("telegram %s: %w", method, err)
	}
	var env struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		return fmt.Errorf("telegram %s decode: %w", method, err)
	}
	if !env.OK {
		return fmt.Errorf("telegram %s error: %s", method, env.Description)
	}
	return nil
}

// Format renders an announcement as a Telegram HTML message.
//
// Layout:
//
//	🔴 CRITICAL · Binance · api, futures
//	<b>Title</b>
//	short body preview…
//	🕒 2026-07-12 14:03 UTC
//	🔗 https://…
func Format(a model.Announcement, backfill bool) string {
	var b strings.Builder

	head := fmt.Sprintf("%s <b>%s</b> · %s · %s",
		a.Importance.Emoji(),
		strings.ToUpper(a.Importance.String()),
		esc(strings.ToUpper(a.Exchange)),
		esc(a.MarketsString()),
	)
	if backfill {
		head = "🗄 <i>backfill</i> " + head
	}
	b.WriteString(head)
	b.WriteString("\n\n")

	b.WriteString("<b>")
	b.WriteString(esc(truncate(a.Title, 300)))
	b.WriteString("</b>")

	if body := strings.TrimSpace(a.Body); body != "" {
		b.WriteString("\n")
		b.WriteString(esc(truncate(collapseSpace(body), 400)))
	}

	if !a.PublishedAt.IsZero() {
		b.WriteString("\n\n🕒 ")
		b.WriteString(a.PublishedAt.UTC().Format("2006-01-02 15:04 MST"))
	}

	if a.URL != "" {
		b.WriteString("\n🔗 ")
		// Bound the URL before escaping so the final message stays well under the
		// limit and truncation never has to cut inside this line.
		b.WriteString(esc(truncate(a.URL, 500)))
	}

	if a.Importance == model.ImportanceCritical {
		b.WriteString("\n\n⚠️ <b>Likely requires API code changes — review before it takes effect.</b>")
	}

	// Telegram's hard limit is 4096 UTF-16 units. Every field above is already
	// individually bounded (title 300, body 400, url 500), so a normal message
	// is well under the limit. This is a defensive net: cut at the last line
	// break before the cap so we never split an HTML tag or entity (which would
	// make Telegram reject the message) — tags/entities never span newlines.
	return safeTruncate(b.String(), 4000)
}

// safeTruncate caps s at n runes without splitting a line (and therefore never
// an HTML tag or &entity;, since those never contain a newline).
func safeTruncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	cut := string(r[:n])
	if i := strings.LastIndexByte(cut, '\n'); i > 0 {
		cut = cut[:i]
	}
	return cut + "\n…"
}

// esc escapes the characters Telegram's HTML parse mode treats specially.
func esc(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

func collapseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func stripHTML(s string) string {
	r := strings.NewReplacer("<b>", "", "</b>", "", "<i>", "", "</i>", "")
	out := r.Replace(s)
	out = strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">").Replace(out)
	return out
}
