# Exchange announcement sources (verified 2026-07-13)

Every source below was live-verified. The bot's `check` subcommand already uses
the **primary** for each. This file is the fallback map: if a primary feed starts
returning a `fetch error` (endpoint moved, Cloudflare/Akamai block, geo-block),
use the listed fallback and fetch it with WebFetch, then read the latest items.

Legend: ✅ machine-readable & auth-free · ⚠️ anti-bot guarded / best-effort · 🧑 human channel (manual).

---

## binance ✅
- **Primary (API Updates):** `https://www.binance.com/bapi/apex/v1/public/apex/cms/article/list/query?type=1&catalogId=51&pageNo=1&pageSize=20` → `data.catalogs[0].articles`. Fields: `id`, `title`, `code` (build URL `https://www.binance.com/en/support/announcement/{code}`), `releaseDate` (epoch ms).
- catalogId map: **51=API Updates**, 161=Delisting, 48=New Crypto Listing, 157=Wallet Maintenance, 49=Latest News, 50=New Fiat, 93=Activities.
- Notes: unofficial "bapi" endpoint (can change/403 from datacenter IPs — send a browser UA). Geo-blocked in some regions (redirects to binance.us).
- Fallbacks: composite host `.../bapi/composite/v1/public/cms/article/catalog/list/query?catalogId=51` (⚠️ `publishDate` is null there — parse date from title). Self-hosted RSSHub `/binance/announcement/51`. Announcements WebSocket topic `com_announcement_en` (needs an API key).

## okx ✅
- **Primary (API):** `https://www.okx.com/api/v5/support/announcements?annType=announcements-api` → `data[0].details`. Fields: `title`, `url` (absolute), `pTime` (epoch ms).
- All types: drop `annType`. Type list: `https://www.okx.com/api/v5/support/announcement-types`.
- Notes: official public REST, no auth, no headers needed. Very reliable.
- Fallbacks: API changelog HTML `https://www.okx.com/docs-v5/log_en/`.

## bybit ✅ (announcements) / 🧑 (API changelog)
- **Primary:** `https://api.bybit.com/v5/announcements/index?locale=en-US&limit=20` → `result.list`. Fields: `title`, `url`, `publishTime`/`dateTimestamp` (epoch ms). No numeric id → dedup on `url`. Sorted by `dateTimestamp` DESC (scan 1–2 pages).
- Mirror if blocked: `api.bytick.com`.
- **API-specific (manual):** changelog `https://bybit-exchange.github.io/docs/changelog/v5` (dated `<h2>` sections, `[NEW]`/`[UPDATE]` bullets). Telegram `https://t.me/s/Bybit_API_Announcements`.

## bitget ✅
- **Primary (API):** `https://api.bitget.com/api/v2/public/annoucements?language=en_US&annType=api_trading` → `data`. Fields: `annId`, `annTitle`, `annUrl`, `cTime` (epoch ms). *(Vendor misspells "annoucements".)*
- All types: drop `annType`.

## kucoin ✅
- **Primary (API):** `https://api.kucoin.com/api/v3/announcements?annType=api-campaigns&lang=en_US&currentPage=1&pageSize=20` → `data.items`. Fields: `annId`, `annTitle`, `annUrl`, `cTime` (epoch ms). *(API category is `api-campaigns`, NOT `api-announcements`.)*
- All types: drop `annType`. Also `product-updates`.
- API changelog HTML: `https://www.kucoin.com/docs-new/change-log`.

## bingx ✅
- **Primary:** `https://open-api.bingx.com/openApi/content/v1/announcement?contentType=LatestAnnouncements&language=en-us&page=1` → `data`. Fields: `title`, `url` (absolute), `releaseTime` (ISO-8601 +08:00). No id → dedup on url. Also `contentType=ProductUpdates`.
- Fallback: Zendesk `https://support.bingx.com/api/v2/help_center/en-001/articles.json?per_page=30&sort_by=created_at&sort_order=desc` → `articles` (`id`,`title`,`html_url`,`created_at`).

## mexc ✅ (via .co host)
- **Primary (API):** `https://www.mexc.co/help/announce/api/en-US/section/15425930840820/articles?page=1&perPage=20` → `data.results`. Fields: `id`, `title`, `routerUrl` (prepend `https://www.mexc.com`), `createdAt` (ISO-8601 Z).
- ⚠️ Use the **.co** host — `www.mexc.com` blocks non-browser TLS fingerprints (403 Cloudflare even with a browser UA).

## gate ⚠️ best-effort
- **Primary:** `POST https://www.gate.com/api/web/v1/portal/announcement/list_article` body `{"page":1,"limit":20}` → `data.list`. Fields: `id`, `title`, `url` (relative, prepend `https://www.gate.com`), `release_timestamp` (Unix seconds string). Headers: `Referer`/`Origin` = gate.com.
- ⚠️ Akamai Bot Manager guards this host and returns "Access Denied" from many datacenter IPs (intermittent). The bot fails gracefully. Manual fallback: fetch `https://www.gate.com/announcements/apiupdates` with WebFetch (browser-context render passes Akamai) and read `__NEXT_DATA__` → `props.pageProps.listData.list`.

## hyperliquid (DEX) ✅ signals / 🧑 human
- **Status:** `https://hyperliquid.statuspage.io/api/v2/incidents.json` → `incidents` (`id`,`name`,`shortlink`,`updated_at`).
- **SDK commits (Atom):** `https://github.com/hyperliquid-dex/hyperliquid-python-sdk/commits/master.atom`.
- Releases: `https://api.github.com/repos/hyperliquid-dex/hyperliquid-python-sdk/releases` (needs a UA).
- **Human:** Telegram `https://t.me/s/hyperliquid_announcements`, API channel `https://t.me/hyperliquid_api`, X `https://x.com/HyperliquidX`.

## aster (DEX, asterdex.com) ✅
- **Primary (API docs, Atom):** `https://github.com/asterdex/api-docs/commits/master.atom` — commits to the official API-docs repo = API changes. Most robust no-auth signal.
- Announcements JSON: `POST https://www.asterdex.com/bapi/composite/v1/public/composite/ae/announcement/search` (Content-Type: application/json) → `data.rows` (`id`,`title`,`publishTime`; URL `https://www.asterdex.com/en/announcement/{id}`).
- Product releases (Markdown): `https://docs.asterdex.com/trading/product-releases.md`. X: `https://x.com/Aster_DEX`.

## lighter (DEX, lighter.xyz / zkLighter) ✅
- **Primary (announcements):** `https://mainnet.zklighter.elliot.ai/api/v1/announcement` → `announcements` (`title`, `created_at` Unix seconds; no per-item URL → link to `https://app.lighter.xyz/announcements`).
- **SDK commits (Atom):** `https://github.com/elliottech/lighter-python/commits/main.atom` (branch is `main`).
- **Human:** Telegram API channel `https://t.me/s/lighter_api_updates`. Docs `https://apidocs.lighter.xyz`.

## huobi / HTX (htx.com) ✅
- **Primary (API Announcements):** `https://www.htx.com/-/x/support/public/getList/v2?page=1&limit=20&oneLevelId=360000031902&twoLevelId=360000070201&language=en-us` → `data.list` (`id`,`title`,`showTime`; URL `https://www.htx.com/support/{id}`). twoLevelId `360000070201` = API Announcements.
- **Status:** `https://huo.statuspage.io/api/v2/incidents.json` → `incidents`.
- ⚠️ htx.com sits behind Cloudflare — send a browser UA + `Accept-Language: en-us`.

---

## Hosting providers (infra status + billing)

### render ✅
- **Maintenance:** `https://status.render.com/api/v2/scheduled-maintenances.json` → `scheduled_maintenances` (`id`,`name`,`shortlink`,`scheduled_for`).
- **Incidents:** `https://status.render.com/api/v2/incidents.json` → `incidents`.
- Billing: no public balance API — Render emails invoices.

### digitalocean ✅
- **Maintenance:** `https://status.digitalocean.com/api/v2/scheduled-maintenances.json` → `scheduled_maintenances`.
- **Incidents:** `https://status.digitalocean.com/api/v2/incidents.json` → `incidents`.
- **Billing (optional):** `GET https://api.digitalocean.com/v2/customers/my/balance` with `Authorization: Bearer $DIGITALOCEAN_API_TOKEN` → `account_balance` (owed), `month_to_date_balance`, `month_to_date_usage`.

### vultr ⚠️ (status best-effort) / ✅ billing
- **Status:** `https://status.vultr.com/alerts.json` → `service_alerts` (`id`,`subject`,`start_date`). ⚠️ Cloudflare JS-challenge blocks Go's TLS from datacenter IPs — fetch with WebFetch (browser context) as the manual fallback.
- **Billing (optional):** `GET https://api.vultr.com/v2/account` with `Authorization: Bearer $VULTR_API_KEY` → `account.balance` (negative = owed), `account.pending_charges`, `account.last_payment_*`.

---

### Freshness sanity checks
- Compare the newest item's timestamp to today; a feed whose newest item is months old is likely broken — flag it.
- The GitHub Atom / status-page sources surface historical entries; only treat items newer than your last check as "new".
- Never fabricate an announcement. If a source is unreachable, say so.
