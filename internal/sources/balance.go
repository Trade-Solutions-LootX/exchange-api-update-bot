package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"exchangebot/internal/httpx"
)

// This file powers the on-demand /balance command: friendly, human-readable
// balance summaries for the hosting providers. DigitalOcean and Vultr expose a
// real account balance; Render does not (its API only manages services), so we
// list what's running there and point the user to the dashboard.

// BalancesText fetches and formats balances for whichever providers have a
// token configured. Returns a Telegram-HTML string.
func BalancesText(ctx context.Context, hc *httpx.Client, doToken, vultrKey, renderKey string) string {
	var b strings.Builder
	b.WriteString("💳 <b>Балансы провайдеров</b>\n")
	any := false
	if doToken != "" {
		any = true
		b.WriteString("\n<b>DigitalOcean</b>\n" + doBalance(ctx, hc, doToken))
	}
	if vultrKey != "" {
		any = true
		b.WriteString("\n<b>Vultr</b>\n" + vultrBalance(ctx, hc, vultrKey))
	}
	if renderKey != "" {
		any = true
		b.WriteString("\n<b>Render</b>\n" + renderSummary(ctx, hc, renderKey))
	}
	if !any {
		return "Не настроен ни один провайдер. Добавьте DIGITALOCEAN_API_TOKEN / VULTR_API_KEY / RENDER_API_KEY."
	}
	return b.String()
}

func doBalance(ctx context.Context, hc *httpx.Client, token string) string {
	raw, err := hc.Get(ctx, "https://api.digitalocean.com/v2/customers/my/balance",
		map[string]string{"Authorization": "Bearer " + token})
	if err != nil {
		return "  ⚠️ не удалось получить (" + esc(shortErr(err)) + ")\n"
	}
	var r struct {
		MonthToDateBalance string `json:"month_to_date_balance"`
		AccountBalance     string `json:"account_balance"`
		MonthToDateUsage   string `json:"month_to_date_usage"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return "  ⚠️ неожиданный ответ API\n"
	}
	owed := parseMoney(r.AccountBalance)
	line := "  К оплате: <b>$" + dash(r.AccountBalance) + "</b>"
	if owed > 0 {
		line += " ⚠️"
	}
	line += "\n  За месяц: $" + dash(r.MonthToDateBalance) + " (расход $" + dash(r.MonthToDateUsage) + ")\n"
	return line
}

func vultrBalance(ctx context.Context, hc *httpx.Client, key string) string {
	raw, err := hc.Get(ctx, "https://api.vultr.com/v2/account",
		map[string]string{"Authorization": "Bearer " + key})
	if err != nil {
		return "  ⚠️ не удалось получить (" + esc(shortErr(err)) + ")\n"
	}
	var r struct {
		Account struct {
			Balance           float64 `json:"balance"`
			PendingCharges    float64 `json:"pending_charges"`
			LastPaymentDate   string  `json:"last_payment_date"`
			LastPaymentAmount float64 `json:"last_payment_amount"`
		} `json:"account"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return "  ⚠️ неожиданный ответ API\n"
	}
	line := fmt.Sprintf("  Баланс: <b>$%.2f</b>", r.Account.Balance)
	if r.Account.Balance < 0 {
		line += " ⚠️ (задолженность)"
	}
	line += fmt.Sprintf("\n  Начислено к оплате: $%.2f\n", r.Account.PendingCharges)
	return line
}

// renderSummary — Render has NO balance endpoint in its API, so we report the
// running services (useful) and point to the dashboard for the balance.
func renderSummary(ctx context.Context, hc *httpx.Client, key string) string {
	raw, err := hc.Get(ctx, "https://api.render.com/v1/services?limit=100",
		map[string]string{"Authorization": "Bearer " + key, "Accept": "application/json"})
	if err != nil {
		return "  ⚠️ не удалось получить (" + esc(shortErr(err)) + ")\n"
	}
	// Response is an array of { service: {...} } wrappers.
	var list []struct {
		Service struct {
			Name      string `json:"name"`
			Type      string `json:"type"`
			Suspended string `json:"suspended"`
		} `json:"service"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return "  ⚠️ неожиданный ответ API\n"
	}
	active := 0
	var names []string
	for _, w := range list {
		s := w.Service
		if s.Name == "" {
			continue
		}
		if s.Suspended == "not_suspended" || s.Suspended == "" {
			active++
		}
		names = append(names, esc(s.Name))
	}
	line := fmt.Sprintf("  Сервисов: <b>%d</b> (активных %d)\n", len(names), active)
	if len(names) > 0 {
		shown := names
		if len(shown) > 8 {
			shown = shown[:8]
		}
		line += "  " + strings.Join(shown, ", ") + "\n"
	}
	line += "  ℹ️ Баланс Render в API недоступен — смотрите в dashboard.render.com/billing\n"
	return line
}

func shortErr(err error) string {
	s := err.Error()
	if len(s) > 120 {
		return s[:120] + "…"
	}
	return s
}

func esc(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
