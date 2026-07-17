package sources

import "testing"

const rss20 = `<?xml version="1.0"?>
<rss version="2.0"><channel>
  <title>Exchange Blog</title>
  <item>
    <title>API v1 Will Be Deprecated</title>
    <link>https://ex.com/blog/api-v1-deprecated</link>
    <guid>https://ex.com/blog/api-v1-deprecated</guid>
    <description>&lt;p&gt;We will &lt;b&gt;deprecate&lt;/b&gt; v1.&lt;/p&gt;</description>
    <pubDate>Sat, 12 Jul 2026 14:03:00 +0000</pubDate>
  </item>
  <item>
    <title>New Coin Listed</title>
    <link>https://ex.com/blog/new-coin</link>
    <pubDate>Fri, 11 Jul 2026 09:00:00 +0000</pubDate>
  </item>
</channel></rss>`

const atomFeed = `<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>Docs Changelog</title>
  <entry>
    <title>Websocket endpoint migration</title>
    <id>tag:ex.com,2026:1</id>
    <link rel="alternate" href="https://ex.com/changelog/1"/>
    <updated>2026-07-10T00:00:00Z</updated>
    <summary>Move to the new ws host.</summary>
  </entry>
</feed>`

func TestParseFeedRSS(t *testing.T) {
	items, err := ParseFeed([]byte(rss20))
	if err != nil {
		t.Fatalf("parse rss: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d", len(items))
	}
	if items[0].Title != "API v1 Will Be Deprecated" {
		t.Errorf("title: %q", items[0].Title)
	}
	if items[0].Link != "https://ex.com/blog/api-v1-deprecated" {
		t.Errorf("link: %q", items[0].Link)
	}
	if items[0].Published.IsZero() {
		t.Errorf("expected parsed pubDate")
	}
}

func TestParseFeedAtom(t *testing.T) {
	items, err := ParseFeed([]byte(atomFeed))
	if err != nil {
		t.Fatalf("parse atom: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 entry, got %d", len(items))
	}
	if items[0].Link != "https://ex.com/changelog/1" {
		t.Errorf("atom link: %q", items[0].Link)
	}
	if items[0].Published.IsZero() {
		t.Errorf("expected parsed atom updated time")
	}
}

func TestParseFeedGarbage(t *testing.T) {
	if _, err := ParseFeed([]byte(`{"not":"xml"}`)); err == nil {
		t.Fatalf("expected error on non-feed input")
	}
}

const rssWithAtomSelfLink = `<?xml version="1.0"?>
<rss version="2.0" xmlns:atom="http://www.w3.org/2005/Atom"><channel>
  <item>
    <title>Real Title</title>
    <link>https://ex.com/real-post</link>
    <atom:link href="https://ex.com/feed" rel="self"/>
    <guid>abc-123</guid>
  </item>
</channel></rss>`

func TestParseFeedAtomSelfLinkDoesNotClobberRSSLink(t *testing.T) {
	items, err := ParseFeed([]byte(rssWithAtomSelfLink))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d", len(items))
	}
	if items[0].Link != "https://ex.com/real-post" {
		t.Fatalf("real <link> was clobbered by atom:link; got %q", items[0].Link)
	}
}

func TestStripTags(t *testing.T) {
	got := stripTags("<p>Hello <b>world</b>   spaced</p>")
	if got != "Hello world spaced" {
		t.Fatalf("stripTags = %q", got)
	}
}
