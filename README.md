# exchange-api-update-bot

A single-binary Go service that continuously monitors **official announcements —
especially API changes — across 12 crypto exchanges**, classifies each item by
**market type** and **actionability**, and pushes new items to **Telegram** with a
link, the exchange, what changed, and how urgent it is.

> Exchanges: **binance · okx · bybit · bitget · kucoin · bingx · mexc · gate ·
> hyperliquid · aster · lighter · huobi (HTX)**
>
> Also monitors hosting-provider **status, scheduled maintenance and billing**:
> **render · digitalocean · vultr**

Every endpoint was **live-verified** (see [`docs/SOURCES.md`](docs/SOURCES.md)).

---

## What a message looks like

```
🔴 CRITICAL · OKX · api
OKX Order Book Channels Checksum Field Deprecation
🕒 2026-05-21 06:51 UTC
🔗 https://www.okx.com/help/okx-order-book-channels-checksum-field-deprecation

⚠️ Likely requires API code changes — review before it takes effect.
```

```
🟠 HIGH · BINANCE · margin
Notice of Removal of Margin Trading Pairs - 2026-07-17
🕒 2026-07-13 10:00 UTC
🔗 https://www.binance.com/en/support/announcement/148235767ff84acfb9096d60c6eedd01
```

## Importance levels

| Level | Meaning | Examples |
|---|---|---|
| 🔴 **critical** | API/endpoint change — **you likely need to change code** | deprecation, breaking change, websocket/auth/rate-limit change |
| 🟠 **high** | operationally significant, may need action | delisting, maintenance, network/contract upgrade |
| 🟡 **medium** | informational | new listing, new feature, parameter tweak |
| ⚪️ **low** | noise | promotions, campaigns, airdrops |

Each classification is **explainable** — the matched rules are logged, and the
dedicated API-update feeds are floored at `high` so bland notices never slip through.

---

## Quick start

### 1. Local dry-run (no Telegram needed)

Prints would-be messages to stdout — the fastest way to see it work:

```bash
make dry-run
# or:
DRY_RUN=true LOG_LEVEL=debug go run ./cmd/bot
```

### 2. One-shot manual check

Fetch and print the latest announcements for any exchange, no Telegram, no state:

```bash
go run ./cmd/bot check                      # all exchanges, everything
go run ./cmd/bot check --api                 # all exchanges, API changes ONLY
go run ./cmd/bot check binance okx           # specific ones
go run ./cmd/bot check --api --min high      # API changes, high+ severity
go run ./cmd/bot check --min high --limit 10 --json
```

`--api` keeps only announcements about the exchange API (deprecations, endpoint/
websocket/auth/rate-limit/SDK changes) and hides listings, delistings, and promos.

### 3. Run for real (Docker, recommended)

```bash
cp .env.example .env         # then fill in TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID
docker compose up -d --build
docker compose logs -f
```

- **Auto-restart:** `restart: unless-stopped` + a built-in `HEALTHCHECK` (the binary
  probes its own `/healthz`) restart the container on crash or hang.
- **No re-spam on restart:** delivered items are remembered in a persisted volume
  (`/data/state.json`).
- **First-run backfill:** on the very first run each feed sends its newest item as a
  labelled `🗄 backfill` so you can confirm the pipeline end-to-end; afterwards only
  genuinely new items are delivered. Disable with `SEND_HISTORY_ON_START=false`.

### Getting Telegram credentials
1. Create a bot with [@BotFather](https://t.me/BotFather) → copy the token into `TELEGRAM_BOT_TOKEN`.
2. Message [@userinfobot](https://t.me/userinfobot) for your numeric chat id (or use a channel id like `-1001234567890`, and add the bot to the channel as admin) → `TELEGRAM_CHAT_ID`.

---

## Configuration

All via environment (see [`.env.example`](.env.example)). Defaults in **bold**.

| Var | Default | Purpose |
|---|---|---|
| `TELEGRAM_BOT_TOKEN` | — | **required** (unless `DRY_RUN`) |
| `TELEGRAM_CHAT_ID` | — | **required** (unless `DRY_RUN`) |
| `POLL_INTERVAL` | **5m** | poll cadence per feed (min 10s) |
| `MIN_IMPORTANCE` | **low** | drop below `low/medium/high/critical` |
| `API_ONLY` | **false** | deliver only API changes + infra/billing (drop listings/delistings/promos) |
| `ENABLED_EXCHANGES` | **all** | CSV filter, e.g. `binance,okx,render,vultr` |
| `DIGITALOCEAN_API_TOKEN` | — | optional; enables DO daily billing summary (+ load if `LOAD_MONITORING`) |
| `VULTR_API_KEY` | — | optional; enables Vultr daily billing summary |
| `BILLING_INTERVAL` | **6h** | billing poll cadence (deduped to 1 msg/day) |
| `FEED_FAILURE_ALERT` | **12** | consecutive source failures before a "feed down" alert (0=off) |
| `UPTIME_TARGETS` | — | `name=url,…` endpoints to probe for up/down |
| `LOAD_MONITORING` | **false** | poll DO metrics for CPU/RAM threshold alerts |
| `SEND_HISTORY_ON_START` | **true** | backfill newest item per feed on first run |
| `HISTORY_COUNT` | **1** | how many to backfill per feed |
| `DRY_RUN` | **false** | log instead of send |
| `STATE_PATH` | **/data/state.json** | persisted "already sent" set |
| `HEALTH_ADDR` | **:8080** | `/healthz` + `/stats` (empty disables) |
| `HTTP_TIMEOUT` | **20s** | per-request timeout |
| `LOG_LEVEL` | **info** | `debug/info/warn/error` |

Only want API changes (no listings/delistings/promos)? `API_ONLY=true` — this still
keeps hosting status/maintenance/billing (they're operationally actionable).
Only want *breaking* API changes? `API_ONLY=true` + `MIN_IMPORTANCE=critical`.

### Hosting providers (Render · DigitalOcean · Vultr)

- **Status + scheduled maintenance** ("тех. работы") are polled from each provider's
  public status page — no credentials. Maintenance is flagged 🟠 HIGH so you can plan
  around downtime that affects your own deployment.
- **Billing / payments due** is *optional* and per-account: set `DIGITALOCEAN_API_TOKEN`
  and/or `VULTR_API_KEY` and the bot posts a **once-daily** balance summary, flagged
  🟠 HIGH when there's an outstanding amount to pay. Without a token, billing is simply
  off. *(Render exposes no public balance API — only its status page is monitored; it
  emails invoices.)*
- Note: Vultr's **status page** is behind a Cloudflare challenge that blocks Go's TLS
  fingerprint from many datacenter IPs, so it's best-effort (fails gracefully). Vultr
  **billing** uses a different host and works fine with a key.

---

## Monitoring & control

Beyond exchange/infra announcements, the bot watches itself and your servers, and
is controllable from Telegram:

- **Feed self-monitoring** — if a source fails `FEED_FAILURE_ALERT` polls in a row
  (default 12) you get a 🟠 alert *"Feed binance:api-updates is DOWN"*, and a notice
  when it recovers. This is your safety net: if an exchange silently changes its API,
  you find out instead of assuming "no news".
- **Incident lifecycle** — hosting incidents/maintenance are re-delivered on each
  status change (e.g. `[investigating]` → `[resolved]`), not just when opened.
- **Uptime of your own endpoints** — set `UPTIME_TARGETS=api=https://api.you.com/health,web=https://you.com`
  and the bot probes them every `UPTIME_INTERVAL` and alerts on down → recovered,
  with latency. No credentials needed.
- **Load monitoring (DigitalOcean)** — `LOAD_MONITORING=true` (+ `DIGITALOCEAN_API_TOKEN`)
  polls the DO metrics API and alerts when a droplet's CPU/RAM crosses
  `LOAD_CPU_PERCENT` / `LOAD_MEM_PERCENT`, and when it recovers. *Requires the DO
  monitoring agent on the droplets* (without it the metrics are empty and it stays
  silent). Vultr's API doesn't expose CPU, so load is DO-only.

### Telegram commands & buttons

Once running (non-dry-run), message the bot from the configured chat:

| Command | Does |
|---|---|
| `/status` | feed health, down feeds, muted list, current threshold |
| `/check <exchange> [api]` | fetch the latest items for an exchange right now |
| `/mute <slug> [dur]` | silence a source (e.g. `/mute bybit 12h`; no dur = forever) |
| `/unmute <slug>` · `/muted` | re-enable / list mutes |
| `/min low\|medium\|high\|critical\|reset` | change the delivery threshold live |
| `/sources` · `/help` | list feeds / usage |

Every alert also carries inline **🔕 Mute** and **✅ OK** buttons. Mute/threshold
changes are in-memory (reset on restart). The bot only obeys the configured chat.

---

## Architecture

```
cmd/bot            entrypoint · subcommands: (run) · check · healthcheck
internal/
  config           env parsing + validation
  model            Announcement, MarketType, Importance
  httpx            resilient HTTP client (retry, backoff, Retry-After)
  sources          Source interface + generic JSON & RSS/Atom adapters
    specs.go       ← every exchange feed declared as DATA (edit here to fix a feed)
    uptime.go      your-endpoint up/down probes (SystemSource)
    billing.go     optional DO/Vultr balance summaries
    digitalocean_load.go  optional DO CPU/RAM threshold alerts
  classify         explainable keyword importance + market-type detection
  store            atomic JSON "seen" set (dedup across restarts)
  control          runtime state: mutes + live min-importance (thread-safe)
  telegram         send + interactive Bot API (getUpdates, keyboards)
  bot              command handler (/status /check /mute …) + button callbacks
  poller           orchestration: schedule · dedup · feed-health · paced delivery
  health           /healthz + /stats for container liveness
```

**Design choices worth knowing:**
- **Zero third-party dependencies** (stdlib only) → hermetic, reproducible Docker
  builds with no module-download step and no supply-chain surface.
- **Feeds are data, not code** — add or repair a source by editing one struct in
  `internal/sources/specs.go`. Two generic adapters (JSON + RSS/Atom) cover all 12
  exchanges; `[]`-path flattening handles nested shapes (Binance catalogs, OKX pages).
- **At-least-once delivery** — an item is marked *seen* only after a successful
  send; an in-flight set prevents double-queueing; failed sends retry next poll.
- **Resilient by default** — every source failure is isolated and logged; one dead
  feed never stops the others (e.g. Gate behind Akamai just logs a warning).

---

## The `exchange-updates` skill

`.claude/skills/exchange-updates/` is a Claude Code skill for ad-hoc checks
("what's new on bybit?", "any API changes on kucoin?"). It runs `bot check` and,
if a feed is blocked, falls back to the documented per-exchange sources — never
fabricating results. See its [`references/sources.md`](.claude/skills/exchange-updates/references/sources.md).

---

## Development

```bash
make test        # unit + adapter-parsing + classification tests
make race        # race detector
make vet fmt     # static checks
make build       # static binary → bin/bot
make cover       # coverage summary
```

## Maintaining sources

Exchanges change endpoints. When a feed drifts, `bot check` shows a `fetch error`.
Fix it by editing the one affected struct in `internal/sources/specs.go` and
re-running `check`. Fallbacks are in the skill reference and `docs/SOURCES.md`.

## Limitations & notes

- Announcement endpoints are mostly **undocumented/unofficial** — they can change
  or start geo/anti-bot blocking. The service degrades gracefully and the source
  table is trivial to patch.
- **Gate** is Akamai-guarded; it works from residential/allowed egress and fails
  gracefully from many datacenter IPs.
- Classification is keyword-based (deterministic, explainable) — not perfect, but
  tunable in `internal/classify/classify.go` with tests to back changes.
- Run from a **non-restricted region** for exchanges that geo-block (e.g. Binance).
