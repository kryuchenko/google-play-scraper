package googleplayscraper

import (
	"context"
	"fmt"
	"net/http"
	"runtime/trace"
	"strings"
	"sync"
	"time"
)

// StatusError reports an HTTP response whose status code was not 200 OK.
// It lets callers branch on the specific status (e.g. 404 vs 429) instead of
// matching on the error string:
//
//	app, err := client.App(ctx, id, opts)
//	var se *StatusError
//	if errors.As(err, &se) {
//		switch se.Code {
//		case http.StatusNotFound:
//			// app does not exist
//		case http.StatusTooManyRequests:
//			// back off and retry
//		}
//	}
type StatusError struct {
	// RetryAfter carries the server's Retry-After header when it sent one, so
	// a retry policy can honour it without holding on to the response.
	RetryAfter time.Duration
	Code       int
}

// Error returns the message historically produced by the request helpers, so
// existing callers that match on the string keep working.
func (e *StatusError) Error() string {
	return fmt.Sprintf("unexpected status: %d", e.Code)
}

// Client handles HTTP requests to Google Play
type Client struct {
	httpClient   *http.Client
	userAgent    string
	throttle     time.Duration
	timeout      time.Duration
	concurrency  int
	lastRequest  time.Time
	throttleLock sync.Mutex
	adaptive     *AdaptivePolicy
	rttWindow    [32]time.Duration
	rttNext      int
	cleanRun     int
	slowRun      int
	retry        RetryPolicy
	hooks        Hooks
	reqIDSeq     int64 // monotonic counter for batchexecute _reqid (see nextReqID)
}

// ClientOption configures the client
type ClientOption func(*Client)

// WithTimeout sets the HTTP client timeout.
//
// It is applied after the client is fully built, so it works regardless of
// ordering relative to WithHTTPClient: a custom client always receives the
// requested timeout.
func WithTimeout(d time.Duration) ClientOption {
	return func(c *Client) {
		c.timeout = d
	}
}

// WithUserAgent sets a custom user agent
func WithUserAgent(ua string) ClientOption {
	return func(c *Client) {
		c.userAgent = ua
	}
}

// WithThrottle sets minimum delay between requests (rate limiting)
func WithThrottle(d time.Duration) ClientOption {
	return func(c *Client) {
		c.throttle = d
	}
}

// WithConcurrency sets how many requests may be in flight at once for the
// operations that fan out -- today that is Availability's per-country sweep and
// the catalog shard sweep. FullDetail enrichment no longer uses it: that path
// packs its lookups into a batched RPC and is two requests, not one per result.
//
// Under a fixed throttle, concurrency past latency/throttle buys nothing --
// Little's Law, and measured: a 40-country availability sweep at a 200ms
// interval took 8.28s, 7.98s and 7.98s at 1, 4 and 16 workers. The throttle
// paces request starts; workers beyond that only wait. Raise the rate, or send
// fewer requests, before raising this.
func WithConcurrency(n int) ClientOption {
	return func(c *Client) {
		if n < 1 {
			n = 1
		}
		c.concurrency = n
	}
}

// WithHTTPClient replaces the underlying *http.Client used for all requests.
//
// The supplied client must follow redirects and persist cookies the way the
// default one does (the default uses a zero-value http.Client, which follows
// up to 10 redirects). Google Play relies on both: detail and listing pages
// redirect across locales, and several endpoints set cookies that later
// requests echo back. A client with redirects disabled or a nil Jar where one
// is expected will silently return empty or malformed results.
//
// Use it to inject a transport with proxy support, custom TLS, or request
// tracing. Combine with WithTimeout to override the client's timeout without
// constructing your own.
func WithHTTPClient(client *http.Client) ClientOption {
	return func(c *Client) {
		if client != nil {
			c.httpClient = client
		}
	}
}

// NewClient creates a new Google Play scraper client.
//
// The transport is deliberately left at its default. The obvious suspicion is
// that http.DefaultTransport's MaxIdleConnsPerHost of 2 forces connection churn
// once concurrency rises above it; measured against a throttled client, it does
// not. Google serves HTTP/2 and Go negotiates it by default, so 120 requests at
// concurrency 32 open one connection and reuse it 119 times, tuned or not.
// Forced down to HTTP/1.1 it opens 32 and is no faster.
//
// The qualifier is load-bearing and was missing from an earlier version of this
// comment. Go's HTTP/2 transport has no dial coordination: with truly
// simultaneous cold starts every goroutine opens its own connection, and a
// measurement at 120 requests, concurrency 32 and zero spacing gives 32
// connections, not one. One connection appears once starts are spaced -- 4 at
// 1ms apart, 2 at 5ms -- which is exactly what waitThrottle does. So the
// conclusion holds for the intended configuration and not for an unthrottled
// client, which is the zero value here. Set a throttle.
func NewClient(opts ...ClientOption) *Client {
	c := &Client{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		userAgent:   "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
		concurrency: 1,
	}

	for _, opt := range opts {
		opt(c)
	}

	// Apply the timeout last so it wins regardless of option ordering and
	// applies to a client supplied via WithHTTPClient too.
	if c.timeout > 0 {
		c.httpClient.Timeout = c.timeout
	}

	return c
}

// waitThrottle enforces a minimum interval between the *starts* of consecutive
// requests. It reserves the next start slot under a short lock, then sleeps
// without holding the lock so concurrent callers serialize on their slot
// reservations rather than on the sleep itself. It returns ctx.Err() if the
// context is cancelled before the reserved slot arrives.
//
// Slots are reserved on a monotonic schedule: each caller claims max(now,
// lastSlot+throttle), so N concurrent callers spread out to one start per
// throttle interval instead of all firing at once after a shared sleep.
func (c *Client) waitThrottle(ctx context.Context) error {
	c.throttleLock.Lock()
	// Read under the lock: with WithAdaptiveThrottle the interval is rewritten
	// by observe() as responses arrive, and an unsynchronised read of it is a
	// race whether or not the resulting number looks plausible.
	if c.throttle == 0 {
		c.throttleLock.Unlock()
		return nil
	}
	now := time.Now()
	slot := c.lastRequest.Add(c.throttle)
	if slot.Before(now) {
		slot = now
	}
	c.lastRequest = slot
	c.throttleLock.Unlock()

	wait := time.Until(slot)
	if wait <= 0 {
		return nil
	}

	// Opened only on the path that actually blocks: a reserved slot that has
	// already arrived is not a wait, and recording it as one would make the
	// throttle look responsible for time it did not consume.
	defer trace.StartRegion(ctx, traceRegionThrottle).End()

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// get performs a GET request through the pipeline.
func (c *Client) get(ctx context.Context, url string) ([]byte, error) {
	return c.do(ctx, http.MethodGet, url, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", c.userAgent)
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
		req.Header.Set("Accept-Language", "en-US,en;q=0.9")
		return req, nil
	})
}

// post performs a POST request through the pipeline.
func (c *Client) post(ctx context.Context, url string, contentType string, body string) ([]byte, error) {
	return c.do(ctx, http.MethodPost, url, func() (*http.Request, error) {
		// Rebuilt per attempt: a reader that has been drained cannot be
		// replayed, so sharing one across retries sends an empty body.
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", c.userAgent)
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("Accept", "*/*")
		return req, nil
	})
}

// buildURL constructs a Google Play URL
func buildURL(path string, params map[string]string) string {
	url := BaseURL + path
	if len(params) == 0 {
		return url
	}

	var parts []string
	for k, v := range params {
		parts = append(parts, fmt.Sprintf("%s=%s", k, v))
	}
	return url + "?" + strings.Join(parts, "&")
}
