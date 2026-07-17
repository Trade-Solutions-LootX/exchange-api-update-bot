// Package httpx provides a small, resilient HTTP client used by all source
// adapters: sane timeouts, a realistic User-Agent, bounded retries with
// exponential backoff + jitter, and context cancellation.
package httpx

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Client wraps *http.Client with retry semantics.
type Client struct {
	hc         *http.Client
	userAgent  string
	maxRetries int
	log        *slog.Logger
}

// Option configures a Client.
type Option func(*Client)

// WithMaxRetries sets the retry count (default 3).
func WithMaxRetries(n int) Option { return func(c *Client) { c.maxRetries = n } }

// New builds a Client. timeout bounds each individual attempt.
func New(timeout time.Duration, userAgent string, log *slog.Logger, opts ...Option) *Client {
	c := &Client{
		hc: &http.Client{
			Timeout: timeout,
			// Do not auto-follow more than a sane number of redirects.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return fmt.Errorf("stopped after 10 redirects")
				}
				return nil
			},
		},
		userAgent:  userAgent,
		maxRetries: 3,
		log:        log,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// GetJSON fetches url and returns the raw body bytes, retrying on transient
// failures (5xx, 429, network errors). headers are optional per-request headers.
func (c *Client) Get(ctx context.Context, url string, headers map[string]string) ([]byte, error) {
	return c.do(ctx, http.MethodGet, url, nil, headers)
}

// PostJSON issues a POST with a body (may be nil).
func (c *Client) Post(ctx context.Context, url string, body []byte, headers map[string]string) ([]byte, error) {
	return c.do(ctx, http.MethodPost, url, body, headers)
}

func (c *Client) do(ctx context.Context, method, url string, body []byte, headers map[string]string) ([]byte, error) {
	var lastErr error
	var nextWait time.Duration // set from Retry-After to override backoff exactly once
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			wait := nextWait
			if wait == 0 {
				wait = backoff(attempt)
			}
			c.log.Debug("retrying request", "url", url, "attempt", attempt, "wait", wait.String())
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
		}
		nextWait = 0

		var rdr io.Reader
		if body != nil {
			rdr = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, url, rdr)
		if err != nil {
			// A malformed request will never succeed; do not retry.
			return nil, fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("User-Agent", c.userAgent)
		req.Header.Set("Accept", "application/json, text/xml, application/xml, text/html;q=0.9, */*;q=0.8")
		req.Header.Set("Accept-Language", "en-US,en;q=0.9")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		resp, err := c.hc.Do(req)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue // network error — retry
		}

		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 16<<20)) // 16 MiB cap
		resp.Body.Close()

		switch {
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			// Honor Retry-After when present; it overrides (not adds to) the
			// exponential backoff for the next attempt.
			if ra := retryAfter(resp.Header.Get("Retry-After")); ra > 0 {
				nextWait = ra
			}
			lastErr = fmt.Errorf("http %d from %s", resp.StatusCode, url)
			continue
		case resp.StatusCode >= 400:
			// Client error — retrying will not help.
			return nil, fmt.Errorf("http %d from %s: %s", resp.StatusCode, url, snippet(data))
		}
		if readErr != nil {
			lastErr = readErr
			continue
		}
		return data, nil
	}
	return nil, fmt.Errorf("request failed after %d attempts: %w", c.maxRetries+1, lastErr)
}

// backoff returns an exponential delay with a fixed jitter derived from the
// attempt number (deterministic — no global rand, keeps tests stable).
func backoff(attempt int) time.Duration {
	base := time.Duration(math.Min(math.Pow(2, float64(attempt)), 30)) * time.Second
	jitter := time.Duration(attempt*137) * time.Millisecond
	return base + jitter
}

// retryAfter parses an RFC 7231 Retry-After header in either the delta-seconds
// form ("120") or the HTTP-date form ("Wed, 21 Oct 2026 07:28:00 GMT"), and
// clamps the result to a sane [0, 120s] window.
func retryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		return clampWait(time.Duration(secs) * time.Second)
	}
	if t, err := http.ParseTime(v); err == nil {
		return clampWait(time.Until(t))
	}
	return 0
}

func clampWait(d time.Duration) time.Duration {
	if d < 0 {
		return 0
	}
	if d > 120*time.Second {
		return 120 * time.Second
	}
	return d
}

func snippet(b []byte) string {
	const max = 200
	if len(b) > max {
		return string(b[:max]) + "…"
	}
	return string(b)
}
