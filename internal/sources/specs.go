package sources

import "exchangebot/internal/model"

// This file is the single place where exchange feeds are declared. Adding or
// fixing a feed means editing data here — no new code.
//
// Every endpoint below was live-verified on 2026-07-13 (see docs/SOURCES.md for
// the full audit trail, per-exchange gotchas, and fallback sources). Notes call
// out the ones that are unofficial / anti-bot-guarded so future maintainers know
// what to watch.
//
// TimeLayout guidance:
//   - Use an explicit hint ("unix_ms","unix_s","rfc3339") when the format is
//     known and stable.
//   - Use "" (empty) when the format is uncertain: parseTime then tries RFC3339
//     layouts and falls back to an epoch-magnitude heuristic, which transparently
//     handles seconds/millis/ISO strings.

// JSONSpecs returns every JSON REST feed to poll.
func JSONSpecs() []JSONSpec {
	return []JSONSpec{
		// ── Binance ──────────────────────────────────────────────────────
		// Unofficial "bapi" CMS API. catalogId 51 == "API Updates" (verified).
		// item_path uses [] to flatten the single returned catalog's articles.
		{
			Exchange:       "binance",
			Name:           "api-updates",
			URL:            "https://www.binance.com/bapi/apex/v1/public/apex/cms/article/list/query?type=1&catalogId=51&pageNo=1&pageSize=20",
			ItemsPath:      "data.catalogs[].articles",
			IDField:        "id",
			TitleField:     "title",
			URLField:       "code",
			URLPrefix:      "https://www.binance.com/en/support/announcement/",
			TimeField:      "releaseDate",
			TimeLayout:     "unix_ms",
			APIUpdatesFeed: true,
			Headers:        map[string]string{"clienttype": "web", "lang": "en"},
		},
		{
			Exchange:   "binance",
			Name:       "delisting",
			URL:        "https://www.binance.com/bapi/apex/v1/public/apex/cms/article/list/query?type=1&catalogId=161&pageNo=1&pageSize=20",
			ItemsPath:  "data.catalogs[].articles",
			IDField:    "id",
			TitleField: "title",
			URLField:   "code",
			URLPrefix:  "https://www.binance.com/en/support/announcement/",
			TimeField:  "releaseDate",
			TimeLayout: "unix_ms",
			Headers:    map[string]string{"clienttype": "web", "lang": "en"},
		},
		{
			Exchange:   "binance",
			Name:       "new-listing",
			URL:        "https://www.binance.com/bapi/apex/v1/public/apex/cms/article/list/query?type=1&catalogId=48&pageNo=1&pageSize=20",
			ItemsPath:  "data.catalogs[].articles",
			IDField:    "id",
			TitleField: "title",
			URLField:   "code",
			URLPrefix:  "https://www.binance.com/en/support/announcement/",
			TimeField:  "releaseDate",
			TimeLayout: "unix_ms",
			Headers:    map[string]string{"clienttype": "web", "lang": "en"},
		},

		// ── OKX ──────────────────────────────────────────────────────────
		// Official public REST API. annType=announcements-api is the
		// API-changes category (verified). data is data[0].details.
		{
			Exchange:       "okx",
			Name:           "api-updates",
			URL:            "https://www.okx.com/api/v5/support/announcements?annType=announcements-api",
			ItemsPath:      "data[].details",
			IDField:        "url",
			TitleField:     "title",
			URLField:       "url",
			TimeField:      "pTime",
			TimeLayout:     "unix_ms",
			APIUpdatesFeed: true,
		},
		{
			Exchange:   "okx",
			Name:       "announcements",
			URL:        "https://www.okx.com/api/v5/support/announcements",
			ItemsPath:  "data[].details",
			IDField:    "url",
			TitleField: "title",
			URLField:   "url",
			TimeField:  "pTime",
			TimeLayout: "unix_ms",
		},

		// ── Bybit ────────────────────────────────────────────────────────
		// Official V5 announcements (all types; classifier assigns importance).
		// No numeric id — dedup on the article url. Sorted by dateTimestamp DESC.
		{
			Exchange:   "bybit",
			Name:       "announcements",
			URL:        "https://api.bybit.com/v5/announcements/index?locale=en-US&limit=20",
			ItemsPath:  "result.list",
			IDField:    "url",
			TitleField: "title",
			URLField:   "url",
			BodyField:  "description",
			TimeField:  "publishTime",
			TimeLayout: "unix_ms",
		},

		// ── Bitget ───────────────────────────────────────────────────────
		// V2 public announcements (note the vendor's spelling "annoucements").
		{
			Exchange:       "bitget",
			Name:           "api-updates",
			URL:            "https://api.bitget.com/api/v2/public/annoucements?language=en_US&annType=api_trading",
			ItemsPath:      "data",
			IDField:        "annId",
			TitleField:     "annTitle",
			URLField:       "annUrl",
			TimeField:      "cTime",
			TimeLayout:     "unix_ms",
			APIUpdatesFeed: true,
		},
		{
			Exchange:   "bitget",
			Name:       "announcements",
			URL:        "https://api.bitget.com/api/v2/public/annoucements?language=en_US",
			ItemsPath:  "data",
			IDField:    "annId",
			TitleField: "annTitle",
			URLField:   "annUrl",
			TimeField:  "cTime",
			TimeLayout: "unix_ms",
		},

		// ── KuCoin ───────────────────────────────────────────────────────
		// Official V3 announcements. The API category is annType=api-campaigns
		// (verified — NOT "api-announcements").
		{
			Exchange:       "kucoin",
			Name:           "api-updates",
			URL:            "https://api.kucoin.com/api/v3/announcements?annType=api-campaigns&lang=en_US&currentPage=1&pageSize=20",
			ItemsPath:      "data.items",
			IDField:        "annId",
			TitleField:     "annTitle",
			URLField:       "annUrl",
			TimeField:      "cTime",
			TimeLayout:     "unix_ms",
			APIUpdatesFeed: true,
		},
		{
			Exchange:   "kucoin",
			Name:       "announcements",
			URL:        "https://api.kucoin.com/api/v3/announcements?lang=en_US&currentPage=1&pageSize=20",
			ItemsPath:  "data.items",
			IDField:    "annId",
			TitleField: "annTitle",
			URLField:   "annUrl",
			TimeField:  "cTime",
			TimeLayout: "unix_ms",
		},

		// ── BingX ────────────────────────────────────────────────────────
		// Official open-api content endpoint. No id field — dedup on url hash.
		// releaseTime is ISO-8601 with a +08:00 offset.
		{
			Exchange:   "bingx",
			Name:       "announcements",
			URL:        "https://open-api.bingx.com/openApi/content/v1/announcement?contentType=LatestAnnouncements&language=en-us&page=1",
			ItemsPath:  "data",
			TitleField: "title",
			URLField:   "url",
			TimeField:  "releaseTime",
			TimeLayout: "rfc3339",
		},
		{
			Exchange:   "bingx",
			Name:       "product-updates",
			URL:        "https://open-api.bingx.com/openApi/content/v1/announcement?contentType=ProductUpdates&language=en-us&page=1",
			ItemsPath:  "data",
			TitleField: "title",
			URLField:   "url",
			TimeField:  "releaseTime",
			TimeLayout: "rfc3339",
		},

		// ── MEXC ─────────────────────────────────────────────────────────
		// Support-center API. Uses the .co host to sidestep the Cloudflare
		// TLS-fingerprint block on www.mexc.com. Section 15425930840820 == API.
		{
			Exchange:       "mexc",
			Name:           "api-updates",
			URL:            "https://www.mexc.co/help/announce/api/en-US/section/15425930840820/articles?page=1&perPage=20",
			ItemsPath:      "data.results",
			IDField:        "id",
			TitleField:     "title",
			URLField:       "routerUrl",
			URLPrefix:      "https://www.mexc.com",
			TimeField:      "createdAt",
			TimeLayout:     "rfc3339",
			APIUpdatesFeed: true,
			Headers:        map[string]string{"Accept": "application/json"},
		},
		{
			Exchange:   "mexc",
			Name:       "announcements",
			URL:        "https://www.mexc.co/help/announce/api/en-US/section/360000254192/articles?page=1&perPage=50",
			ItemsPath:  "data.results",
			IDField:    "id",
			TitleField: "title",
			URLField:   "routerUrl",
			URLPrefix:  "https://www.mexc.com",
			TimeField:  "createdAt",
			TimeLayout: "rfc3339",
			Headers:    map[string]string{"Accept": "application/json"},
		},

		// ── Gate ─────────────────────────────────────────────────────────
		// Web portal announcement list. POST-only (a GET returns 405). Guarded by
		// Akamai Bot Manager: confirmed "Access Denied" from datacenter IPs even
		// with browser headers, so this is BEST-EFFORT — it succeeds from a
		// residential/allowed egress and fails gracefully (logged, non-fatal)
		// otherwise. See docs/SOURCES.md for the manual browser-fetch fallback.
		// release_timestamp is Unix seconds sent as a string.
		{
			Exchange:   "gate",
			Name:       "announcements",
			URL:        "https://www.gate.com/api/web/v1/portal/announcement/list_article",
			Method:     "POST",
			Body:       `{"page":1,"limit":20}`,
			ItemsPath:  "data.list",
			IDField:    "id",
			TitleField: "title",
			URLField:   "url",
			URLPrefix:  "https://www.gate.com",
			TimeField:  "release_timestamp",
			TimeLayout: "unix_s",
			Headers: map[string]string{
				"Referer": "https://www.gate.com/announcements",
				"Origin":  "https://www.gate.com",
			},
		},

		// ── Hyperliquid (DEX) ────────────────────────────────────────────
		// No announcement API. GitHub releases of the reference SDK + the public
		// status page are the machine-readable signals; the Telegram channel is
		// the human channel (see docs/SOURCES.md).
		{
			Exchange:   "hyperliquid",
			Name:       "status",
			URL:        "https://hyperliquid.statuspage.io/api/v2/incidents.json",
			ItemsPath:  "incidents",
			IDField:    "id",
			TitleField: "name",
			URLField:   "shortlink",
			TimeField:  "updated_at",
			TimeLayout: "rfc3339",
		},

		// ── Lighter (DEX) ────────────────────────────────────────────────
		// Public banner-announcement endpoint on the mainnet backend. No per-item
		// url/id — synthesize a key from title+time, link to the app.
		{
			Exchange:   "lighter",
			Name:       "announcements",
			URL:        "https://mainnet.zklighter.elliot.ai/api/v1/announcement",
			ItemsPath:  "announcements",
			TitleField: "title",
			DefaultURL: "https://app.lighter.xyz/announcements",
			TimeField:  "created_at",
			TimeLayout: "unix_s",
		},

		// ── HTX (formerly Huobi) ─────────────────────────────────────────
		// Support-center list API. twoLevelId 360000070201 == API Announcements.
		{
			Exchange:       "huobi",
			Name:           "api-updates",
			URL:            "https://www.htx.com/-/x/support/public/getList/v2?page=1&limit=20&oneLevelId=360000031902&twoLevelId=360000070201&language=en-us",
			ItemsPath:      "data.list",
			IDField:        "id",
			TitleField:     "title",
			URLField:       "id",
			URLPrefix:      "https://www.htx.com/support/",
			TimeField:      "showTime",
			TimeLayout:     "", // uncertain format → robust heuristic parsing
			APIUpdatesFeed: true,
			Headers:        map[string]string{"Accept-Language": "en-us"},
		},
		{
			Exchange:   "huobi",
			Name:       "status",
			URL:        "https://huo.statuspage.io/api/v2/incidents.json",
			ItemsPath:  "incidents",
			IDField:    "id",
			TitleField: "name",
			URLField:   "shortlink",
			TimeField:  "updated_at",
			TimeLayout: "rfc3339",
		},

		// ═══════════════════════════════════════════════════════════════════
		// Hosting / infrastructure providers — scheduled maintenance ("тех.
		// работы") and incidents, so you know about downtime that affects the
		// bot's own deployment. These are forced to the "infra" market and
		// bypass API_ONLY (see model.IsOperational).
		// ═══════════════════════════════════════════════════════════════════

		// ── Render (Atlassian Statuspage) ────────────────────────────────
		{
			Exchange:        "render",
			Name:            "maintenance",
			URL:             "https://status.render.com/api/v2/scheduled-maintenances.json",
			ItemsPath:       "scheduled_maintenances",
			IDField:         "id",
			TitleField:      "name",
			URLField:        "shortlink",
			TimeField:       "scheduled_for",
			TimeLayout:      "rfc3339",
			DefaultMarkets:  []model.MarketType{model.MarketInfra},
			VersionField:    "status",
			ImportanceFloor: model.ImportanceHigh, // plan around maintenance windows
		},
		{
			Exchange:        "render",
			Name:            "incidents",
			URL:             "https://status.render.com/api/v2/incidents.json",
			ItemsPath:       "incidents",
			IDField:         "id",
			TitleField:      "name",
			URLField:        "shortlink",
			TimeField:       "updated_at",
			TimeLayout:      "rfc3339",
			DefaultMarkets:  []model.MarketType{model.MarketInfra},
			VersionField:    "status",
			ImportanceFloor: model.ImportanceMedium,
		},

		// ── DigitalOcean (Atlassian Statuspage) ──────────────────────────
		{
			Exchange:        "digitalocean",
			Name:            "maintenance",
			URL:             "https://status.digitalocean.com/api/v2/scheduled-maintenances.json",
			ItemsPath:       "scheduled_maintenances",
			IDField:         "id",
			TitleField:      "name",
			URLField:        "shortlink",
			TimeField:       "scheduled_for",
			TimeLayout:      "rfc3339",
			DefaultMarkets:  []model.MarketType{model.MarketInfra},
			VersionField:    "status",
			ImportanceFloor: model.ImportanceHigh,
		},
		{
			Exchange:        "digitalocean",
			Name:            "incidents",
			URL:             "https://status.digitalocean.com/api/v2/incidents.json",
			ItemsPath:       "incidents",
			IDField:         "id",
			TitleField:      "name",
			URLField:        "shortlink",
			TimeField:       "updated_at",
			TimeLayout:      "rfc3339",
			DefaultMarkets:  []model.MarketType{model.MarketInfra},
			VersionField:    "status",
			ImportanceFloor: model.ImportanceMedium,
		},

		// ── Vultr (custom status JSON — NOT Atlassian) ───────────────────
		// alerts.json is a combined stream of scheduled maintenance + outages.
		// BEST-EFFORT: status.vultr.com is behind a Cloudflare JS challenge that
		// blocks Go's TLS fingerprint (a browser/curl passes, the stdlib client
		// gets 403). It fails gracefully; from a residential/allowed egress it
		// works. Vultr BILLING (api.vultr.com, below) is a different host and is
		// NOT blocked. Manual fallback: WebFetch status.vultr.com in the skill.
		{
			Exchange:        "vultr",
			Name:            "alerts",
			URL:             "https://status.vultr.com/alerts.json",
			ItemsPath:       "service_alerts",
			IDField:         "id",
			TitleField:      "subject",
			TimeField:       "start_date",
			TimeLayout:      "rfc3339",
			DefaultURL:      "https://status.vultr.com/",
			DefaultMarkets:  []model.MarketType{model.MarketInfra},
			VersionField:    "status",
			ImportanceFloor: model.ImportanceHigh, // Vultr alerts are maintenance/outages
			Headers: map[string]string{
				"User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36",
			},
		},
	}
}

// RSSSpecs returns every RSS/Atom feed to poll. For the DEXes without a clean
// announcement API, the GitHub *.atom commit feed of the API-docs / SDK repo is
// the most reliable no-auth signal of an actual API change (github.com is not
// subject to the api.github.com 60/hr limit).
func RSSSpecs() []RSSSpec {
	return []RSSSpec{
		// Aster: commits to the official api-docs repo == API changes.
		{
			Exchange:       "aster",
			Name:           "api-docs",
			URL:            "https://github.com/asterdex/api-docs/commits/master.atom",
			APIUpdatesFeed: false, // commit stream is noisy; let the classifier judge
		},
		// Hyperliquid: SDK commit stream (product/API activity).
		{
			Exchange: "hyperliquid",
			Name:     "sdk-commits",
			URL:      "https://github.com/hyperliquid-dex/hyperliquid-python-sdk/commits/master.atom",
		},
		// Lighter: Python SDK commit stream (default branch is "main").
		{
			Exchange: "lighter",
			Name:     "sdk-commits",
			URL:      "https://github.com/elliottech/lighter-python/commits/main.atom",
		},
	}
}
