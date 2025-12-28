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

// Client handles HTTP requests to Google Play
type Client struct {
	httpClient   *http.Client
	userAgent    string
	throttle     time.Duration
	lastRequest  time.Time
	throttleLock sync.Mutex
}

// ClientOption configures the client
type ClientOption func(*Client)

// WithTimeout sets the HTTP client timeout
func WithTimeout(d time.Duration) ClientOption {
	return func(c *Client) {
		c.httpClient.Timeout = d
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

	return c
}

// waitThrottle waits for throttle duration if needed
func (c *Client) waitThrottle() {
	if c.throttle == 0 {
		return
	}

	c.throttleLock.Lock()
	defer c.throttleLock.Unlock()

	elapsed := time.Since(c.lastRequest)
	if elapsed < c.throttle {
		time.Sleep(c.throttle - elapsed)
	}
	c.lastRequest = time.Now()
}

// get performs a GET request
func (c *Client) get(ctx context.Context, url string) ([]byte, error) {
	c.waitThrottle()

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
		return nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	return body, nil
}

// post performs a POST request
func (c *Client) post(ctx context.Context, url string, contentType string, body string) ([]byte, error) {
	c.waitThrottle()

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
		return nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
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
