package sources

import (
	"context"
	"encoding/xml"
	"fmt"
	"strings"
	"time"

	"exchangebot/internal/classify"
	"exchangebot/internal/model"
)

// RSSSpec declaratively describes an RSS 2.0 or Atom feed.
type RSSSpec struct {
	Exchange       string
	Name           string
	URL            string
	Headers        map[string]string
	APIUpdatesFeed bool
	Interval       time.Duration
}

type rssSource struct {
	spec RSSSpec
	deps Deps
}

func newRSSSource(spec RSSSpec, deps Deps) *rssSource {
	return &rssSource{spec: spec, deps: deps}
}

func (s *rssSource) Exchange() string        { return s.spec.Exchange }
func (s *rssSource) Name() string            { return s.spec.Exchange + ":" + s.spec.Name }
func (s *rssSource) Interval() time.Duration { return s.spec.Interval }

func (s *rssSource) Fetch(ctx context.Context) ([]model.Announcement, error) {
	raw, err := s.deps.HTTP.Get(ctx, s.spec.URL, s.spec.Headers)
	if err != nil {
		return nil, fmt.Errorf("%s fetch: %w", s.Name(), err)
	}
	items, err := ParseFeed(raw)
	if err != nil {
		return nil, fmt.Errorf("%s parse: %w", s.Name(), err)
	}
	out := make([]model.Announcement, 0, len(items))
	for _, it := range items {
		a := model.Announcement{
			Exchange:    s.spec.Exchange,
			NativeID:    firstNonEmpty(it.GUID, it.ID, it.Link),
			Title:       strings.TrimSpace(it.Title),
			URL:         strings.TrimSpace(it.Link),
			Body:        strings.TrimSpace(stripTags(it.Description)),
			Source:      s.Name(),
			PublishedAt: it.Published,
		}
		if a.Title == "" && a.URL == "" {
			continue
		}
		res := classify.Classify(a)
		a.Markets = res.Markets
		a.Importance = res.Importance
		a.MatchedRules = res.MatchedRules
		if s.spec.APIUpdatesFeed {
			a.Markets = appendUnique(a.Markets, model.MarketAPI)
			if a.Importance < model.ImportanceHigh {
				a.Importance = model.ImportanceHigh
				a.MatchedRules = append(a.MatchedRules, "floor:api-updates-feed")
			}
		}
		out = append(out, a)
	}
	return out, nil
}

// xmlLink captures both forms a <link> can take: RSS 2.0 chardata
// (<link>https://…</link>) and Atom-style attributes (<atom:link href=".."/>).
type xmlLink struct {
	Href  string `xml:"href,attr"`
	Rel   string `xml:"rel,attr"`
	Value string `xml:",chardata"`
}

// pickLink chooses the best article URL from a set of <link> elements: prefer
// the RSS chardata link, then an alternate/plain Atom href, then any href.
func pickLink(links []xmlLink) string {
	for _, l := range links {
		if v := strings.TrimSpace(l.Value); v != "" {
			return v
		}
	}
	for _, l := range links {
		if (l.Rel == "alternate" || l.Rel == "") && strings.TrimSpace(l.Href) != "" {
			return strings.TrimSpace(l.Href)
		}
	}
	for _, l := range links {
		if strings.TrimSpace(l.Href) != "" {
			return strings.TrimSpace(l.Href)
		}
	}
	return ""
}

// feedItem is a source-agnostic normalized feed entry.
type feedItem struct {
	Title       string
	Link        string
	GUID        string
	ID          string
	Description string
	Published   time.Time
}

// ParseFeed parses either RSS 2.0 or Atom, whichever the payload is. It is
// intentionally lenient: many exchange feeds are slightly non-conforming.
func ParseFeed(data []byte) ([]feedItem, error) {
	// Try RSS 2.0 first. Links are captured as a slice because encoding/xml
	// matches by local name only, so a stray <atom:link href=".." rel="self"/>
	// in the same <item> would otherwise clobber the real <link>text</link>.
	var rss struct {
		Channel struct {
			Items []struct {
				Title       string    `xml:"title"`
				Links       []xmlLink `xml:"link"`
				GUID        string    `xml:"guid"`
				Description string    `xml:"description"`
				PubDate     string    `xml:"pubDate"`
				Date        string    `xml:"date"` // dc:date
			} `xml:"item"`
		} `xml:"channel"`
	}
	if err := xml.Unmarshal(data, &rss); err == nil && len(rss.Channel.Items) > 0 {
		out := make([]feedItem, 0, len(rss.Channel.Items))
		for _, it := range rss.Channel.Items {
			out = append(out, feedItem{
				Title:       decodeText(it.Title),
				Link:        pickLink(it.Links),
				GUID:        strings.TrimSpace(it.GUID),
				Description: it.Description,
				Published:   parseFeedTime(firstNonEmpty(it.PubDate, it.Date)),
			})
		}
		return out, nil
	}

	// Fall back to Atom.
	var atom struct {
		Entries []struct {
			Title   string `xml:"title"`
			ID      string `xml:"id"`
			Summary string `xml:"summary"`
			Content string `xml:"content"`
			Updated string `xml:"updated"`
			Publish string `xml:"published"`
			Links   []struct {
				Href string `xml:"href,attr"`
				Rel  string `xml:"rel,attr"`
			} `xml:"link"`
		} `xml:"entry"`
	}
	if err := xml.Unmarshal(data, &atom); err == nil && len(atom.Entries) > 0 {
		out := make([]feedItem, 0, len(atom.Entries))
		for _, e := range atom.Entries {
			link := ""
			for _, l := range e.Links {
				if l.Rel == "alternate" || l.Rel == "" {
					link = l.Href
					break
				}
			}
			if link == "" && len(e.Links) > 0 {
				link = e.Links[0].Href
			}
			out = append(out, feedItem{
				Title:       decodeText(e.Title),
				Link:        strings.TrimSpace(link),
				ID:          strings.TrimSpace(e.ID),
				Description: firstNonEmpty(e.Summary, e.Content),
				Published:   parseFeedTime(firstNonEmpty(e.Publish, e.Updated)),
			})
		}
		return out, nil
	}

	return nil, fmt.Errorf("unrecognized feed format (neither RSS item nor Atom entry found)")
}

// parseFeedTime accepts the many date formats seen across real feeds.
func parseFeedTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	layouts := []string{
		time.RFC1123Z, time.RFC1123,
		time.RFC3339, time.RFC3339Nano,
		"2006-01-02T15:04:05Z07:00",
		"2006-01-02 15:04:05",
		"Mon, 2 Jan 2006 15:04:05 -0700",
		"Mon, 02 Jan 2006 15:04:05 GMT",
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// decodeText trims and collapses whitespace in feed titles.
func decodeText(s string) string {
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}

// stripTags removes crude HTML from RSS descriptions for a clean preview.
func stripTags(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(strings.Join(strings.Fields(b.String()), " "))
}
