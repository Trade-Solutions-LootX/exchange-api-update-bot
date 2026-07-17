package sources

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"exchangebot/internal/classify"
	"exchangebot/internal/model"
)

// JSONSpec declaratively describes a JSON REST announcement feed.
type JSONSpec struct {
	Exchange string // "binance"
	Name     string // "api-updates"
	URL      string // full endpoint incl. query string
	Method   string // default GET
	Body     string // optional request body for POST endpoints
	Headers  map[string]string

	// ItemsPath is a dot path to the array of item objects. Use "[]" on a
	// segment to flatten nested arrays, e.g. "data.catalogs[].articles".
	ItemsPath string

	// Field paths within each item object.
	IDField    string
	TitleField string
	// URLField is the item field holding the article URL or a slug/code. If the
	// value is not absolute (no scheme), URLPrefix is prepended.
	URLField  string
	URLPrefix string
	// DefaultURL is used when a feed has no per-item link (e.g. banner-style
	// announcements) so every message still carries a clickable destination.
	DefaultURL string
	BodyField  string

	TimeField  string
	TimeLayout string // see parseTime: "unix_ms","unix_s","rfc3339", or a Go layout

	// VersionField makes an item's dedup key include a lifecycle field (e.g. a
	// statuspage incident "status"), so a change (investigating → resolved)
	// re-delivers as a distinct update and is prefixed "[status] " in the title.
	VersionField string

	// APIUpdatesFeed marks a feed that is specifically the API-changes category,
	// so items get a minimum importance floor even if wording is bland.
	APIUpdatesFeed bool

	// DefaultMarkets, when set, REPLACES the classifier's market detection — used
	// for non-exchange feeds (e.g. hosting status → [infra]) where spot/futures
	// keyword detection is meaningless.
	DefaultMarkets []model.MarketType
	// ImportanceFloor raises every item to at least this importance (e.g. a
	// scheduled-maintenance feed floored to HIGH so you plan around it).
	ImportanceFloor model.Importance

	Interval time.Duration
}

type jsonSource struct {
	spec JSONSpec
	deps Deps
}

func newJSONSource(spec JSONSpec, deps Deps) *jsonSource {
	if spec.Method == "" {
		spec.Method = "GET"
	}
	return &jsonSource{spec: spec, deps: deps}
}

func (s *jsonSource) Exchange() string        { return s.spec.Exchange }
func (s *jsonSource) Name() string            { return s.spec.Exchange + ":" + s.spec.Name }
func (s *jsonSource) Interval() time.Duration { return s.spec.Interval }

func (s *jsonSource) Fetch(ctx context.Context) ([]model.Announcement, error) {
	var raw []byte
	var err error
	if strings.EqualFold(s.spec.Method, "POST") {
		raw, err = s.deps.HTTP.Post(ctx, s.spec.URL, []byte(s.spec.Body), s.spec.Headers)
	} else {
		raw, err = s.deps.HTTP.Get(ctx, s.spec.URL, s.spec.Headers)
	}
	if err != nil {
		return nil, fmt.Errorf("%s fetch: %w", s.Name(), err)
	}

	// UseNumber keeps JSON numbers as their exact source text (json.Number)
	// instead of float64, so large integer ids (>2^53) never lose precision and
	// collide into one DedupKey.
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return nil, fmt.Errorf("%s decode: %w", s.Name(), err)
	}

	items := extractArray(tree, s.spec.ItemsPath)
	if items == nil {
		return nil, fmt.Errorf("%s: no items at path %q (endpoint shape may have changed)", s.Name(), s.spec.ItemsPath)
	}

	out := make([]model.Announcement, 0, len(items))
	for _, it := range items {
		a := s.normalize(it)
		// A title-less row is useless (and, for feeds with a DefaultURL, would
		// otherwise slip past a URL-only guard as a blank message).
		if a.Title == "" {
			continue
		}
		out = append(out, a)
	}
	return out, nil
}

func (s *jsonSource) normalize(item any) model.Announcement {
	a := model.Announcement{
		Exchange:    s.spec.Exchange,
		NativeID:    getString(item, s.spec.IDField),
		Title:       strings.TrimSpace(getString(item, s.spec.TitleField)),
		Body:        strings.TrimSpace(getString(item, s.spec.BodyField)),
		Source:      s.Name(),
		PublishedAt: parseTime(fieldValue(item, s.spec.TimeField), s.spec.TimeLayout),
	}
	a.URL = s.buildURL(getString(item, s.spec.URLField))
	if a.URL == "" {
		a.URL = s.spec.DefaultURL
	}

	// Lifecycle: fold a status field into the dedup key + title so each state
	// transition (e.g. incident resolved) is delivered as a distinct update.
	if s.spec.VersionField != "" {
		if v := strings.TrimSpace(getString(item, s.spec.VersionField)); v != "" {
			if a.NativeID != "" {
				a.NativeID = a.NativeID + "#" + v
			}
			a.Title = "[" + v + "] " + a.Title
		}
	}

	res := classify.Classify(a)
	a.Markets = res.Markets
	a.Importance = res.Importance
	a.MatchedRules = res.MatchedRules

	// A dedicated API-updates feed is, by definition, about the API — always tag
	// it — and is inherently actionable, so floor importance at HIGH when bland
	// wording ("Notice on interface adjustment") would otherwise bury it.
	if s.spec.APIUpdatesFeed {
		a.Markets = appendUnique(a.Markets, model.MarketAPI)
		if a.Importance < model.ImportanceHigh {
			a.Importance = model.ImportanceHigh
			a.MatchedRules = append(a.MatchedRules, "floor:api-updates-feed")
		}
	}
	// Non-exchange feeds force their market (classifier keywords don't apply).
	if len(s.spec.DefaultMarkets) > 0 {
		a.Markets = s.spec.DefaultMarkets
	}
	if s.spec.ImportanceFloor > a.Importance {
		a.Importance = s.spec.ImportanceFloor
		a.MatchedRules = append(a.MatchedRules, "floor:"+s.spec.Name)
	}
	return a
}

func (s *jsonSource) buildURL(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://") {
		return v
	}
	if s.spec.URLPrefix == "" {
		return v
	}
	// Join prefix + slug without doubling slashes.
	prefix := strings.TrimRight(s.spec.URLPrefix, "/")
	slug := strings.TrimLeft(v, "/")
	joined := prefix + "/" + slug
	if _, err := url.Parse(joined); err != nil {
		return v
	}
	return joined
}

// fieldValue returns the raw (untyped) field value for time parsing.
func fieldValue(item any, path string) any {
	v, ok := getField(item, path)
	if !ok {
		return nil
	}
	return v
}

func appendUnique(list []model.MarketType, m model.MarketType) []model.MarketType {
	for _, x := range list {
		if x == m {
			return list
		}
	}
	// Drop the "general" placeholder once we have a real market type.
	if len(list) == 1 && list[0] == model.MarketGeneral {
		return []model.MarketType{m}
	}
	return append(list, m)
}
