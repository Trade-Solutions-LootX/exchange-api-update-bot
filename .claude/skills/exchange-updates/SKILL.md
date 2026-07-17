---
name: exchange-updates
description: Check the latest official announcements and API updates for crypto exchanges (binance, okx, bybit, bitget, kucoin, bingx, mexc, gate, hyperliquid, aster, lighter, huobi/htx). Use when the user asks "what's new on <exchange>", "any API changes on <exchange>", "check exchange announcements/updates", or wants to verify whether an exchange's API changed. Reports each item with a link, the exchange, what changed, the market type (spot/futures/margin/…), and an importance level (critical = API code change needed).
---

# Exchange Updates checker

This skill surfaces the newest official announcements — especially **API updates** —
for the supported exchanges, classified by **market type** and **importance**.

Supported exchanges: `binance okx bybit bitget kucoin bingx mexc gate hyperliquid aster lighter huobi`

Also monitors hosting providers (status + scheduled maintenance, and optional billing):
`render digitalocean vultr`. These are tagged `infra` / `billing` and always pass the
`--api` filter (they're operationally actionable, like API changes).

Importance levels:
- 🔴 **critical** — API/endpoint deprecation, breaking change, websocket/auth/rate-limit change → *you likely need to change code*.
- 🟠 **high** — delisting, maintenance, network/contract upgrade → *may need action*.
- 🟡 **medium** — new listing/feature/product → *informational*.
- ⚪️ **low** — promotions/campaigns → *noise*.

## How to run

The bot binary ships a Telegram-free `check` subcommand that fetches every source
live and prints the latest items with classification. Prefer it — it reuses the
exact same adapters and classifier the monitor uses.

From the repo root (`c:\WorkFlow\exchange-api-update-bot`):

```bash
# All exchanges, latest 5 each:
go run ./cmd/bot check

# One or more exchanges:
go run ./cmd/bot check binance okx

# Only actionable items, more history, machine-readable:
go run ./cmd/bot check --min high --limit 10 --json
```

Flags:
- `--api` — show ONLY API-change announcements (deprecations, endpoint/websocket/
  auth/rate-limit/SDK changes); hides listings, delistings, and promos. Use this
  when the user asks specifically about API changes.
- `--limit N` / `-n N` — items per feed (default 5).
- `--min low|medium|high|critical` — hide anything below this importance.
- `--json` — emit JSON (use this when you need to parse/filter results yourself).

If a compiled binary exists (`bin/bot` or `/bot` in the container) call it directly
instead of `go run`.

## What to do when invoked

1. Determine which exchange(s) the user cares about (default: all). Map loose names:
   `htx` → `huobi`, `gate.io` → `gate`, `hl` → `hyperliquid`.
2. Run `go run ./cmd/bot check <exchanges> --json`. If the user asked specifically
   about **API changes/updates**, add `--api` (and `--min high` for only the
   actionable ones).
3. If a feed returns a `fetch error` (Cloudflare / geo-block / endpoint moved),
   say so explicitly and fall back to `references/sources.md` for that exchange —
   fetch the listed HTML/RSS/social source with WebFetch and read the latest items
   manually. **Never invent announcements**; if you cannot verify, say the source
   is unreachable.
4. Summarize per the user's ask. For each item report, on one line:
   `<importance emoji> EXCHANGE · market — short "what they did" · <link>`
   Group by exchange, most important first. Call out any 🔴 critical items up top
   with a one-line "action needed" note.
5. Verify freshness: include each item's publish time and flag anything older than
   ~30 days if the user asked "what's new".

## Verifying the data is current

- Compare the newest item's timestamp against today. A feed whose newest item is
  months old is likely broken — flag it and check `references/sources.md`.
- Cross-check a suspicious item against a second source (the exchange's status page
  or X/Twitter listed in `references/sources.md`) before reporting it as fact.

See `references/sources.md` for every endpoint, fallback source, and known
gotcha per exchange.
