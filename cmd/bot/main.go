// Command bot is the entrypoint for the exchange API-update monitor. It polls
// every configured exchange announcement feed, classifies each item by market
// type and actionability, and pushes new items to Telegram.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"exchangebot/internal/bot"
	"exchangebot/internal/config"
	"exchangebot/internal/control"
	"exchangebot/internal/health"
	"exchangebot/internal/httpx"
	"exchangebot/internal/poller"
	"exchangebot/internal/sources"
	"exchangebot/internal/store"
	"exchangebot/internal/telegram"
)

// version is injected at build time via -ldflags.
var version = "dev"

func main() {
	// Self-healthcheck subcommand, used by the container HEALTHCHECK. The
	// distroless runtime has no shell/curl, so the binary probes itself.
	if len(os.Args) > 1 && (os.Args[1] == "healthcheck" || os.Args[1] == "--healthcheck") {
		if err := healthcheck(); err != nil {
			fmt.Fprintln(os.Stderr, "healthcheck failed:", err)
			os.Exit(1)
		}
		return
	}
	// One-shot manual check: fetch and print latest announcements, no Telegram.
	if len(os.Args) > 1 && os.Args[1] == "check" {
		if err := runCheck(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "check failed:", err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

// healthcheck GETs the local /healthz endpoint and exits non-zero on failure.
func healthcheck() error {
	addr := os.Getenv("HEALTH_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		port = strings.TrimPrefix(addr, ":")
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := newLogger(cfg.LogLevel)
	slog.SetDefault(log)
	log.Info("starting exchange-api-update-bot",
		"version", version,
		"dry_run", cfg.DryRun,
		"poll_interval", cfg.PollInterval.String(),
		"min_importance", cfg.MinImportance.String(),
		"api_only", cfg.APIOnly,
		"state_path", cfg.StatePath,
	)

	st, err := store.Open(cfg.StatePath)
	if err != nil {
		// Open returns a usable (empty) store even on a corrupt file; log and go.
		log.Warn("state store notice", "err", err)
	}

	hc := httpx.New(cfg.HTTPTimeout, cfg.UserAgent, log)
	sender := telegram.New(cfg.TelegramToken, cfg.TelegramChatID, cfg.DryRun, hc, log)

	// Fail fast on a bad Telegram token.
	pingCtx, cancelPing := context.WithTimeout(context.Background(), 15*time.Second)
	if who, err := sender.Ping(pingCtx); err != nil {
		cancelPing()
		return err
	} else {
		log.Info("telegram connected", "bot", who)
	}
	cancelPing()

	deps := sources.Deps{HTTP: hc, Log: log}
	srcs := sources.Build(deps, cfg.ExchangeEnabled, cfg.PollInterval)

	// Optional authenticated billing sources (only when a token is configured).
	if cfg.DigitalOceanToken != "" && cfg.ExchangeEnabled("digitalocean") {
		srcs = append(srcs, sources.NewDigitalOceanBilling(cfg.DigitalOceanToken, cfg.BillingInterval, deps))
		log.Info("digitalocean billing monitoring enabled", "interval", cfg.BillingInterval.String())
	}
	if cfg.VultrAPIKey != "" && cfg.ExchangeEnabled("vultr") {
		srcs = append(srcs, sources.NewVultrBilling(cfg.VultrAPIKey, cfg.BillingInterval, deps))
		log.Info("vultr billing monitoring enabled", "interval", cfg.BillingInterval.String())
	}

	// Uptime monitoring of the operator's own endpoints.
	if targets := sources.ParseUptimeTargets(cfg.UptimeTargetsRaw); len(targets) > 0 {
		srcs = append(srcs, sources.NewUptimeSource(targets, cfg.UptimeInterval, cfg.UptimeTimeout))
		names := make([]string, len(targets))
		for i, t := range targets {
			names[i] = t.Name
		}
		log.Info("uptime monitoring enabled", "targets", names, "interval", cfg.UptimeInterval.String())
	}

	// Provider load monitoring (DigitalOcean metrics API).
	if cfg.LoadMonitoring && cfg.DigitalOceanToken != "" {
		srcs = append(srcs, sources.NewDigitalOceanLoad(cfg.DigitalOceanToken, cfg.LoadCPUPercent, cfg.LoadMemPercent, cfg.LoadInterval, deps))
		log.Info("digitalocean load monitoring enabled", "cpu_pct", cfg.LoadCPUPercent, "mem_pct", cfg.LoadMemPercent)
	}

	if len(srcs) == 0 {
		log.Warn("no sources enabled — check ENABLED_EXCHANGES")
	}
	names := make([]string, 0, len(srcs))
	for _, s := range srcs {
		names = append(names, s.Name())
	}
	log.Info("sources configured", "count", len(srcs), "feeds", names)

	ctrl := control.New(cfg.MinImportance)
	p := poller.New(cfg, srcs, st, sender, ctrl, log)

	hs := health.New(cfg.HealthAddr, p, log)
	hs.Start()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Interactive command handler (needs a real bot; off in dry-run).
	if !cfg.DryRun {
		b := bot.New(sender, ctrl, p, srcs, log)
		go b.Run(ctx)
	}

	p.Run(ctx)

	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	hs.Stop(shutCtx)
	log.Info("stopped cleanly")
	return nil
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})
	return slog.New(h)
}
