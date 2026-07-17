// Package config loads and validates runtime configuration from the
// environment. All knobs have sane defaults so the bot can run with only a
// Telegram token + chat id supplied.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"exchangebot/internal/model"
)

// Config is the fully-resolved, validated runtime configuration.
type Config struct {
	// Telegram delivery.
	TelegramToken  string
	TelegramChatID string

	// PollInterval is the default polling cadence for a source that does not
	// specify its own.
	PollInterval time.Duration

	// StatePath is the JSON file used to persist the "already sent" set.
	StatePath string

	// EnabledExchanges limits which exchanges are polled. Empty means "all".
	EnabledExchanges map[string]bool

	// SendHistoryOnStart, when true, sends the most recent HistoryCount items
	// per source on the very first run (test/backfill mode) instead of silently
	// marking existing items as seen.
	SendHistoryOnStart bool
	HistoryCount       int

	// MinImportance filters out anything below this severity.
	MinImportance model.Importance

	// APIOnly, when true, delivers ONLY announcements about the exchange API
	// (deprecations, endpoint/websocket/auth/rate-limit changes) plus infra /
	// billing alerts, and drops listings, delistings and promos.
	APIOnly bool

	// Optional hosting-provider billing tokens. When set, the bot polls the
	// provider's balance API and posts a once-daily "payment due" summary.
	DigitalOceanToken string
	VultrAPIKey       string
	BillingInterval   time.Duration

	// Uptime monitoring of your OWN endpoints. UPTIME_TARGETS is a comma list of
	// "name=url" (or bare "url"); the bot alerts on down and recovery.
	UptimeTargetsRaw string
	UptimeInterval   time.Duration
	UptimeTimeout    time.Duration

	// Provider load monitoring (CPU/RAM) via the DigitalOcean metrics API.
	// Reuses DigitalOceanToken; enabled when LOAD_MONITORING=true.
	LoadMonitoring bool
	LoadCPUPercent float64
	LoadMemPercent float64
	LoadInterval   time.Duration

	// DryRun logs would-be Telegram messages instead of sending them.
	DryRun bool

	// FeedFailureAlert is how many consecutive failed polls of a single source
	// trigger a "feed is down" Telegram alert (0 disables self-monitoring).
	FeedFailureAlert int
	// FeedRecoveryNotice sends a "feed recovered" message when a down feed works.
	FeedRecoveryNotice bool

	// HealthAddr is the listen address for the /healthz endpoint (empty = off).
	HealthAddr string

	// HTTPTimeout bounds each outbound request.
	HTTPTimeout time.Duration

	// UserAgent is sent on every outbound request.
	UserAgent string

	LogLevel string
}

// Load reads configuration from the environment and validates it.
func Load() (*Config, error) {
	c := &Config{
		PollInterval:       getDuration("POLL_INTERVAL", 5*time.Minute),
		StatePath:          getString("STATE_PATH", "/data/state.json"),
		SendHistoryOnStart: getBool("SEND_HISTORY_ON_START", true),
		HistoryCount:       getInt("HISTORY_COUNT", 1),
		DryRun:             getBool("DRY_RUN", false),
		FeedFailureAlert:   getInt("FEED_FAILURE_ALERT", 12),
		FeedRecoveryNotice: getBool("FEED_RECOVERY_NOTICE", true),
		HealthAddr:         getString("HEALTH_ADDR", ":8080"),
		HTTPTimeout:        getDuration("HTTP_TIMEOUT", 20*time.Second),
		UserAgent:          getString("USER_AGENT", "Mozilla/5.0 (compatible; ExchangeAPIUpdateBot/1.0; +https://github.com/)"),
		LogLevel:           getString("LOG_LEVEL", "info"),
		TelegramToken:      getString("TELEGRAM_BOT_TOKEN", ""),
		TelegramChatID:     getString("TELEGRAM_CHAT_ID", ""),
		DigitalOceanToken:  getString("DIGITALOCEAN_API_TOKEN", ""),
		VultrAPIKey:        getString("VULTR_API_KEY", ""),
		BillingInterval:    getDuration("BILLING_INTERVAL", 6*time.Hour),
		UptimeTargetsRaw:   getString("UPTIME_TARGETS", ""),
		UptimeInterval:     getDuration("UPTIME_INTERVAL", 60*time.Second),
		UptimeTimeout:      getDuration("UPTIME_TIMEOUT", 10*time.Second),
		LoadMonitoring:     getBool("LOAD_MONITORING", false),
		LoadCPUPercent:     float64(getInt("LOAD_CPU_PERCENT", 85)),
		LoadMemPercent:     float64(getInt("LOAD_MEM_PERCENT", 90)),
		LoadInterval:       getDuration("LOAD_INTERVAL", 5*time.Minute),
	}

	c.EnabledExchanges = parseSet(getString("ENABLED_EXCHANGES", ""))
	c.MinImportance = parseImportance(getString("MIN_IMPORTANCE", "low"))
	c.APIOnly = getBool("API_ONLY", false)

	if c.HistoryCount < 0 {
		c.HistoryCount = 0
	}
	if !c.DryRun {
		if c.TelegramToken == "" {
			return nil, fmt.Errorf("TELEGRAM_BOT_TOKEN is required (or set DRY_RUN=true)")
		}
		if c.TelegramChatID == "" {
			return nil, fmt.Errorf("TELEGRAM_CHAT_ID is required (or set DRY_RUN=true)")
		}
	}
	if c.PollInterval < 10*time.Second {
		return nil, fmt.Errorf("POLL_INTERVAL too small (%s); minimum 10s to stay polite", c.PollInterval)
	}
	if c.HTTPTimeout < time.Second {
		// A zero/negative http.Client.Timeout means "no timeout", which would
		// let a hung exchange endpoint stall a source goroutine forever.
		return nil, fmt.Errorf("HTTP_TIMEOUT too small (%s); minimum 1s", c.HTTPTimeout)
	}
	return c, nil
}

// ExchangeEnabled reports whether the given exchange slug should be polled.
func (c *Config) ExchangeEnabled(slug string) bool {
	if len(c.EnabledExchanges) == 0 {
		return true
	}
	return c.EnabledExchanges[strings.ToLower(slug)]
}

func parseSet(csv string) map[string]bool {
	out := map[string]bool{}
	for _, p := range strings.Split(csv, ",") {
		p = strings.ToLower(strings.TrimSpace(p))
		if p != "" {
			out[p] = true
		}
	}
	return out
}

func parseImportance(s string) model.Importance {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "critical":
		return model.ImportanceCritical
	case "high":
		return model.ImportanceHigh
	case "medium", "med":
		return model.ImportanceMedium
	default:
		return model.ImportanceLow
	}
}

func getString(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func getBool(key string, def bool) bool {
	if v, ok := os.LookupEnv(key); ok {
		b, err := strconv.ParseBool(strings.TrimSpace(v))
		if err == nil {
			return b
		}
	}
	return def
}

func getInt(key string, def int) int {
	if v, ok := os.LookupEnv(key); ok {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err == nil {
			return n
		}
	}
	return def
}

func getDuration(key string, def time.Duration) time.Duration {
	if v, ok := os.LookupEnv(key); ok {
		d, err := time.ParseDuration(strings.TrimSpace(v))
		if err == nil {
			return d
		}
	}
	return def
}
