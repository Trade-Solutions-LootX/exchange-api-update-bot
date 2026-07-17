# Source verification status

All feeds were **live-verified on 2026-07-13** by fetching them and confirming
current data. This table is the maintenance dashboard; the full per-exchange
endpoint reference, field mappings, and fallbacks live in
[`.claude/skills/exchange-updates/references/sources.md`](../.claude/skills/exchange-updates/references/sources.md).

| Exchange | Feeds configured | API-updates feed | Transport | Status | Watch-outs |
|---|---|---|---|---|---|
| binance | api-updates (51), delisting (161), new-listing (48) | ✅ catalogId 51 | JSON (bapi) | ✅ live | Unofficial bapi; browser UA; geo-blocked in some regions |
| okx | api-updates, announcements | ✅ `annType=announcements-api` | JSON (official) | ✅ live | Most reliable; no auth |
| bybit | announcements (all types) | via changelog (manual) | JSON (official) | ✅ live | No id → dedup on url; sorted by dateTimestamp |
| bitget | api-updates, announcements | ✅ `annType=api_trading` | JSON (official) | ✅ live | Endpoint spelled "annoucements" |
| kucoin | api-updates, announcements | ✅ `annType=api-campaigns` | JSON (official) | ✅ live | API type is `api-campaigns` (not `api-announcements`) |
| bingx | announcements, product-updates | via general (classified) | JSON (open-api) | ✅ live | No id → url-hash dedup; releaseTime +08:00 |
| mexc | api-updates, announcements | ✅ section 15425930840820 | JSON (support) | ✅ live | Use `.co` host; `.com` blocks non-browser TLS |
| gate | announcements | via general (classified) | JSON (POST) | ⚠️ best-effort | Akamai "Access Denied" from datacenter IPs (intermittent) |
| hyperliquid | status, sdk-commits | via SDK commits | JSON + Atom | ✅ live | DEX; no announcement API |
| aster | api-docs commits | ✅ api-docs repo commits | Atom (GitHub) | ✅ live | Commit stream (noisy but authoritative) |
| lighter | announcements, sdk-commits | via SDK commits | JSON + Atom | ✅ live | SDK branch is `main`; announcements have no per-item URL |
| huobi (HTX) | api-updates, status | ✅ twoLevelId 360000070201 | JSON (support) | ✅ live | Cloudflare — browser UA + Accept-Language |
| **render** | maintenance, incidents | n/a (infra) | JSON (Statuspage v2) | ✅ live | `status.render.com/api/v2/*` |
| **digitalocean** | maintenance, incidents, *billing* | n/a (infra) | JSON (Statuspage v2 + API) | ✅ live | status public; billing needs `DIGITALOCEAN_API_TOKEN` |
| **vultr** | alerts, *billing* | n/a (infra) | JSON | ⚠️ status best-effort | status.vultr.com CF-blocks Go TLS; billing (`api.vultr.com`) works with `VULTR_API_KEY` |

Infra feeds are forced to the `infra` market and bypass `API_ONLY`. Billing feeds
(`digitalocean:billing`, `vultr:billing`) are authenticated, off unless a token is set,
and post one deduped summary per calendar day.

## How to re-verify

```bash
go run ./cmd/bot check                 # everything, human-readable
go run ./cmd/bot check binance --json  # one exchange, machine-readable
```

A feed that returns a `fetch error` has drifted (endpoint moved, anti-bot block,
geo). Fix it by editing the single data table in
[`internal/sources/specs.go`](../internal/sources/specs.go) — no code changes —
then re-run `check`. Consult the fallback list in the skill reference.

## Importance model

`internal/classify` scores each item by keyword rules and records *why*
(`MatchedRules`):

- 🔴 **critical** — API deprecation / breaking change / websocket / auth / rate-limit → *code change likely required*.
- 🟠 **high** — delisting, maintenance, network/contract upgrade, settlement → *may need action*.
- 🟡 **medium** — new listing / feature / product / parameter tweak → *informational*.
- ⚪️ **low** — promotions / campaigns / airdrops → *noise*.

Feeds flagged `APIUpdatesFeed` (the dedicated API categories) are floored at
**high** so bland-worded API notices are never buried.
