package googleplayscraper

import (
	"context"
	"fmt"
	"io"
	"net/http"
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
	Code int
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
	lastRequest  time.Time
	throttleLock sync.Mutex
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

// NewClient creates a new Google Play scraper client
func NewClient(opts ...ClientOption) *Client {
	c := &Client{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		userAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
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

// waitThrottle waits for the throttle interval to elapse since the previous
// request, returning early with ctx.Err() if the context is cancelled while
// waiting.
func (c *Client) waitThrottle(ctx context.Context) error {
	if c.throttle == 0 {
		return nil
	}

	c.throttleLock.Lock()
	defer c.throttleLock.Unlock()

	elapsed := time.Since(c.lastRequest)
	if wait := c.throttle - elapsed; wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	c.lastRequest = time.Now()
	return nil
}

// get performs a GET request
func (c *Client) get(ctx context.Context, url string) ([]byte, error) {
	if err := c.waitThrottle(ctx); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &StatusError{Code: resp.StatusCode}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	return body, nil
}

// post performs a POST request
func (c *Client) post(ctx context.Context, url string, contentType string, body string) ([]byte, error) {
	if err := c.waitThrottle(ctx); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "*/*")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &StatusError{Code: resp.StatusCode}
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	return respBody, nil
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
