package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"exchangebot/internal/httpx"
	"exchangebot/internal/model"
	"exchangebot/internal/sources"
)

// runCheck implements the `bot check` subcommand: a one-shot, Telegram-free
// fetch that prints the latest announcements per source. It is the engine
// behind the `exchange-updates` skill and a handy manual verification tool.
//
// Usage:
//
//	bot check                      # all exchanges, latest 5 each
//	bot check binance okx          # only these exchanges
//	bot check --limit 10 --min high --json
func runCheck(args []string) error {
	limit := 5
	minImp := model.ImportanceLow
	asJSON := false
	apiOnly := false
	var filter []string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--limit", "-n":
			if i+1 < len(args) {
				if n, err := strconv.Atoi(args[i+1]); err == nil {
					limit = n
				}
				i++
			}
		case "--min":
			if i+1 < len(args) {
				minImp = parseImp(args[i+1])
				i++
			}
		case "--json":
			asJSON = true
		case "--api":
			apiOnly = true
		default:
			filter = append(filter, strings.ToLower(args[i]))
		}
	}

	enabled := func(slug string) bool {
		if len(filter) == 0 {
			return true
		}
		for _, f := range filter {
			if f == slug {
				return true
			}
		}
		return false
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	hc := httpx.New(20*time.Second, "Mozilla/5.0 (compatible; ExchangeAPIUpdateBot/1.0)", log)
	srcs := sources.Build(sources.Deps{HTTP: hc, Log: log}, enabled, 5*time.Minute)
	if len(srcs) == 0 {
		return fmt.Errorf("no sources match filter %v", filter)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	type feedResult struct {
		Source string               `json:"source"`
		Error  string               `json:"error,omitempty"`
		Items  []model.Announcement `json:"items"`
	}
	var results []feedResult

	for _, s := range srcs {
		items, err := s.Fetch(ctx)
		fr := feedResult{Source: s.Name()}
		if err != nil {
			fr.Error = err.Error()
			results = append(results, fr)
			continue
		}
		sort.SliceStable(items, func(i, j int) bool {
			return items[i].PublishedAt.After(items[j].PublishedAt)
		})
		for _, a := range items {
			if a.Importance < minImp {
				continue
			}
			if apiOnly && !a.IsAPIRelated() && !a.IsOperational() {
				continue
			}
			fr.Items = append(fr.Items, a)
			if len(fr.Items) >= limit {
				break
			}
		}
		results = append(results, fr)
	}

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(results)
	}

	// Human-readable output.
	for _, fr := range results {
		fmt.Printf("\n═══ %s ═══\n", strings.ToUpper(fr.Source))
		if fr.Error != "" {
			fmt.Printf("  ⚠️  fetch error: %s\n", fr.Error)
			continue
		}
		if len(fr.Items) == 0 {
			kind := "items"
			if apiOnly {
				kind = "API items"
			}
			fmt.Printf("  (no %s at or above %s)\n", kind, minImp)
			continue
		}
		for _, a := range fr.Items {
			when := "unknown time"
			if !a.PublishedAt.IsZero() {
				when = a.PublishedAt.UTC().Format("2006-01-02 15:04 MST")
			}
			fmt.Printf("  %s [%s] %s · %s\n", a.Importance.Emoji(), strings.ToUpper(a.Importance.String()), strings.ToUpper(a.Exchange), a.MarketsString())
			fmt.Printf("     %s\n", a.Title)
			fmt.Printf("     🕒 %s\n", when)
			if a.URL != "" {
				fmt.Printf("     🔗 %s\n", a.URL)
			}
		}
	}
	fmt.Println()
	return nil
}

func parseImp(s string) model.Importance {
	switch strings.ToLower(s) {
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
