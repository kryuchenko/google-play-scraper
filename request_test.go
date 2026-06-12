package googleplayscraper

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewClient(t *testing.T) {
	c := NewClient()

	if c.httpClient == nil {
		t.Error("httpClient is nil")
	}
	if c.userAgent == "" {
		t.Error("userAgent is empty")
	}
}

func TestClientWithOptions(t *testing.T) {
	c := NewClient(
		WithTimeout(5*time.Second),
		WithUserAgent("TestAgent/1.0"),
	)

	if c.httpClient.Timeout != 5*time.Second {
		t.Errorf("Timeout: got %v, want %v", c.httpClient.Timeout, 5*time.Second)
	}
	if c.userAgent != "TestAgent/1.0" {
		t.Errorf("UserAgent: got %q, want %q", c.userAgent, "TestAgent/1.0")
	}
}

func TestClientGet(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("Method: got %q, want GET", r.Method)
		}
		if r.Header.Get("User-Agent") == "" {
			t.Error("User-Agent header is missing")
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status": "ok"}`))
	}))
	defer server.Close()

	c := NewClient()
	body, err := c.get(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}

	if string(body) != `{"status": "ok"}` {
		t.Errorf("Body: got %q, want %q", string(body), `{"status": "ok"}`)
	}
}

func TestClientGetError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := NewClient()
	_, err := c.get(context.Background(), server.URL)
	if err == nil {
		t.Fatal("expected error for 404 status")
	}

	var statusErr *StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("error is not *StatusError: %v", err)
	}
	if statusErr.Code != http.StatusNotFound {
		t.Errorf("Code: got %d, want %d", statusErr.Code, http.StatusNotFound)
	}
	if got, want := err.Error(), "unexpected status: 404"; got != want {
		t.Errorf("Error(): got %q, want %q", got, want)
	}
}

func TestWithHTTPClient(t *testing.T) {
	custom := &http.Client{Timeout: 7 * time.Second}

	// WithHTTPClient installs the supplied client.
	c := NewClient(WithHTTPClient(custom))
	if c.httpClient != custom {
		t.Error("WithHTTPClient did not install the custom client")
	}

	// A nil client is ignored, leaving the default in place.
	c = NewClient(WithHTTPClient(nil))
	if c.httpClient == nil {
		t.Error("nil client should be ignored, default preserved")
	}

	// WithTimeout overrides the timeout of a custom client regardless of
	// option ordering.
	for _, order := range []struct {
		name string
		opts []ClientOption
	}{
		{"client then timeout", []ClientOption{WithHTTPClient(&http.Client{Timeout: time.Minute}), WithTimeout(3 * time.Second)}},
		{"timeout then client", []ClientOption{WithTimeout(3 * time.Second), WithHTTPClient(&http.Client{Timeout: time.Minute})}},
	} {
		c := NewClient(order.opts...)
		if c.httpClient.Timeout != 3*time.Second {
			t.Errorf("%s: Timeout = %v, want %v", order.name, c.httpClient.Timeout, 3*time.Second)
		}
	}
}

func TestThrottleContextCancel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// A long throttle ensures the second request would block; cancelling the
	// context must return immediately with the context error.
	c := NewClient(WithThrottle(time.Hour))
	if _, err := c.get(context.Background(), server.URL); err != nil {
		t.Fatalf("first request failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	_, err := c.get(ctx, server.URL)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error: got %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("waited %v before honoring cancellation, want near-immediate", elapsed)
	}
}

func TestClientPost(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("Method: got %q, want POST", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Errorf("Content-Type: got %q", r.Header.Get("Content-Type"))
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`response`))
	}))
	defer server.Close()

	c := NewClient()
	body, err := c.post(context.Background(), server.URL, "application/x-www-form-urlencoded", "key=value")
	if err != nil {
		t.Fatalf("post failed: %v", err)
	}

	if string(body) != "response" {
		t.Errorf("Body: got %q, want %q", string(body), "response")
	}
}

func TestClientWithThrottle(t *testing.T) {
	c := NewClient(WithThrottle(100 * time.Millisecond))

	if c.throttle != 100*time.Millisecond {
		t.Errorf("Throttle: got %v, want %v", c.throttle, 100*time.Millisecond)
	}
}

func TestThrottleDelay(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`ok`))
	}))
	defer server.Close()

	throttleTime := 50 * time.Millisecond
	c := NewClient(WithThrottle(throttleTime))

	// First request
	start := time.Now()
	_, _ = c.get(context.Background(), server.URL)
	firstDuration := time.Since(start)

	// Second request should be throttled
	start = time.Now()
	_, _ = c.get(context.Background(), server.URL)
	secondDuration := time.Since(start)

	// Second request should take at least throttleTime minus some tolerance
	if secondDuration < throttleTime-10*time.Millisecond {
		t.Errorf("Second request too fast: %v, expected at least %v", secondDuration, throttleTime)
	}

	t.Logf("First: %v, Second: %v (throttle: %v)", firstDuration, secondDuration, throttleTime)
}

// TestThrottleConcurrentStarts verifies the throttle serializes request starts
// across concurrent goroutines: with a 10ms throttle and 8 workers firing 30
// requests, the whole run cannot finish faster than (requests-1) throttle
// intervals, and it must be race-free under -race.
//
// We assert total elapsed time rather than per-pair gaps: the throttle reserves
// monotonic start slots one interval apart under a lock, so the aggregate floor
// of (requests-1)*throttle is a hard lower bound. Scheduling and network jitter
// can only push individual handler timestamps later, which would falsely
// compress an individual gap but can never shorten the total below the floor —
// making this invariant robust on a loaded machine while still catching a
// throttle that fails to serialize (that run would finish far faster).
func TestThrottleConcurrentStarts(t *testing.T) {
	const (
		throttle = 10 * time.Millisecond
		requests = 30
		workers  = 8
	)

	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	c := NewClient(WithThrottle(throttle), WithConcurrency(workers))

	jobs := make(chan int)
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for range jobs {
				if _, err := c.get(context.Background(), server.URL); err != nil {
					t.Errorf("request failed: %v", err)
				}
			}
		}()
	}

	start := time.Now()
	for i := 0; i < requests; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	elapsed := time.Since(start)

	if got := atomic.LoadInt32(&hits); got != requests {
		t.Fatalf("server saw %d requests, want %d", got, requests)
	}

	// The first request takes its slot immediately; the remaining (requests-1)
	// each wait one throttle interval, so the run cannot complete sooner. A
	// small tolerance absorbs the measurement starting just before the first
	// reserved slot.
	floor := time.Duration(requests-1)*throttle - throttle
	if elapsed < floor {
		t.Errorf("run finished in %v, want >= %v (throttle not serializing starts)", elapsed, floor)
	}
}

func TestBuildURL(t *testing.T) {
	tests := []struct {
		path   string
		params map[string]string
		want   string
	}{
		{
			path:   "/store/apps/details",
			params: nil,
			want:   "https://play.google.com/store/apps/details",
		},
		{
			path:   "/store/apps/details",
			params: map[string]string{"id": "com.example"},
			want:   "https://play.google.com/store/apps/details?id=com.example",
		},
	}

	for _, tt := range tests {
		got := buildURL(tt.path, tt.params)
		// For single param, exact match; for multiple, just check contains
		if tt.params == nil || len(tt.params) <= 1 {
			if got != tt.want {
				t.Errorf("buildURL(%q, %v) = %q, want %q", tt.path, tt.params, got, tt.want)
			}
		}
	}
}
