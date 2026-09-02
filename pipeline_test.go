package googleplayscraper

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// statusSeq serves the given statuses in order, then 200 forever, recording
// every request body so a retry that replays an emptied reader is visible.
type statusSeq struct {
	statuses   []int
	calls      atomic.Int64
	bodies     []string
	retryAfter string
}

func (s *statusSeq) route() routeFunc {
	return func(req *http.Request) (mockResponse, bool) {
		n := int(s.calls.Add(1))
		if req.Body != nil {
			b, _ := io.ReadAll(req.Body)
			s.bodies = append(s.bodies, string(b))
		}
		if n <= len(s.statuses) {
			resp := mockResponse{Status: s.statuses[n-1]}
			if s.retryAfter != "" {
				resp.Header = http.Header{"Retry-After": []string{s.retryAfter}}
			}
			return resp, true
		}
		return mockResponse{Body: []byte("ok")}, true
	}
}

func TestRetryRepeatsTransientFailures(t *testing.T) {
	srv := &statusSeq{statuses: []int{503, 429}}
	c := newMockClient(t, srv.route())
	WithRetry(RetryPolicy{MaxAttempts: 4, BaseDelay: time.Microsecond, MaxDelay: time.Millisecond})(c)

	body, err := c.get(context.Background(), BaseURL+"/x")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(body) != "ok" {
		t.Errorf("body = %q, want \"ok\"", body)
	}
	if got := srv.calls.Load(); got != 3 {
		t.Errorf("made %d attempts, want 3 (two failures then success)", got)
	}
}

// A 404 is an answer. Repeating it burns the caller's rate budget against a
// question that already has a final reply, and on a 177-country availability
// sweep most of the answers are 404.
func TestRetryDoesNotRepeatAFinalAnswer(t *testing.T) {
	srv := &statusSeq{statuses: []int{404, 404, 404, 404}}
	c := newMockClient(t, srv.route())
	WithRetry(RetryPolicy{MaxAttempts: 4, BaseDelay: time.Microsecond})(c)

	_, err := c.get(context.Background(), BaseURL+"/x")
	var se *StatusError
	if !errors.As(err, &se) || se.Code != 404 {
		t.Fatalf("err = %v, want a StatusError carrying 404", err)
	}
	if got := srv.calls.Load(); got != 1 {
		t.Errorf("made %d attempts at a 404, want 1", got)
	}
}

func TestRetryGivesUpAfterMaxAttempts(t *testing.T) {
	srv := &statusSeq{statuses: []int{500, 500, 500, 500, 500}}
	c := newMockClient(t, srv.route())
	WithRetry(RetryPolicy{MaxAttempts: 3, BaseDelay: time.Microsecond})(c)

	if _, err := c.get(context.Background(), BaseURL+"/x"); err == nil {
		t.Fatal("a run of 500s eventually returned success")
	}
	if got := srv.calls.Load(); got != 3 {
		t.Errorf("made %d attempts, want exactly MaxAttempts=3", got)
	}
}

// The POST body is rebuilt per attempt. Sharing one reader across attempts
// sends an empty body on every retry -- the request succeeds, the response is
// wrong, and nothing reports an error. batchexecute is POST-only, so this
// would silently corrupt reviews, permissions and suggestions.
func TestRetryRebuildsThePostBody(t *testing.T) {
	srv := &statusSeq{statuses: []int{503}}
	c := newMockClient(t, srv.route())
	WithRetry(RetryPolicy{MaxAttempts: 3, BaseDelay: time.Microsecond})(c)

	if _, err := c.post(context.Background(), BaseURL+"/x", "application/json", "payload=1"); err != nil {
		t.Fatalf("post: %v", err)
	}
	if len(srv.bodies) != 2 {
		t.Fatalf("recorded %d bodies, want 2", len(srv.bodies))
	}
	for i, b := range srv.bodies {
		if b != "payload=1" {
			t.Errorf("attempt %d sent body %q, want \"payload=1\"", i+1, b)
		}
	}
}

// Retrying inside the throttle would let a burst of failures punch straight
// through the rate limit exactly when the server is asking for less traffic.
// Every attempt has to reserve its own slot.
//
// Under synctest the clock is virtual, so this asserts the real interval
// rather than sleeping for it: three attempts at a 100ms throttle cannot
// finish before 200ms of throttle waiting have elapsed.
func TestRetryReservesAThrottleSlotPerAttempt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv := &statusSeq{statuses: []int{503, 503}}
		c := newMockClient(t, srv.route())
		WithThrottle(100 * time.Millisecond)(c)
		// Backoff kept at zero so the elapsed time is the throttle alone.
		WithRetry(RetryPolicy{MaxAttempts: 3, BaseDelay: time.Nanosecond, MaxDelay: time.Nanosecond})(c)

		start := time.Now()
		if _, err := c.get(context.Background(), BaseURL+"/x"); err != nil {
			t.Fatalf("get: %v", err)
		}
		elapsed := time.Since(start)

		if want := 200 * time.Millisecond; elapsed < want {
			t.Errorf("three attempts took %v, want at least %v: "+
				"retries are bypassing the throttle", elapsed, want)
		}
	})
}

// Google sends Retry-After on 429. Ignoring it in favour of a self-chosen
// backoff is how a client turns a temporary limit into a longer one.
func TestRetryHonoursRetryAfter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv := &statusSeq{statuses: []int{429}, retryAfter: "2"}
		c := newMockClient(t, srv.route())
		WithRetry(RetryPolicy{
			MaxAttempts:       2,
			BaseDelay:         time.Millisecond, // far shorter than the header
			MaxDelay:          time.Minute,
			RespectRetryAfter: true,
		})(c)

		start := time.Now()
		if _, err := c.get(context.Background(), BaseURL+"/x"); err != nil {
			t.Fatalf("get: %v", err)
		}
		if elapsed := time.Since(start); elapsed < 2*time.Second {
			t.Errorf("waited %v, want at least the 2s the server asked for", elapsed)
		}
	})
}

func TestRetryStopsOnCancellation(t *testing.T) {
	srv := &statusSeq{statuses: []int{500, 500, 500, 500, 500}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Cancellation is triggered from inside the handler, on the first
	// response, rather than from a goroutine that sleeps for a fixed time.
	//
	// The two events were not ordered against each other: the backoff is full
	// jitter, uniform in [0, BaseDelay), so four short draws finish the whole
	// retry loop in under the sleep and the run ends with "unexpected status:
	// 500" instead of a cancellation. That is rare on an idle machine and not
	// rare at all when the cores are busy -- it surfaced once during a
	// parallel fuzz run and then refused to reproduce five times over, which
	// is exactly how a timing race presents.
	route := srv.route()
	c := newMockClient(t, func(req *http.Request) (mockResponse, bool) {
		resp, ok := route(req)
		if ok {
			cancel()
		}
		return resp, ok
	})
	WithRetry(RetryPolicy{MaxAttempts: 5, BaseDelay: 50 * time.Millisecond})(c)

	_, err := c.get(ctx, BaseURL+"/x")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want it to wrap context.Canceled", err)
	}
	// The cancellation is the headline, but the status the last attempt saw is
	// still in the chain: a caller diagnosing a stuck run wants both.
	var se *StatusError
	if !errors.As(err, &se) || se.Code != 500 {
		t.Errorf("err = %v, want the last attempt's 500 still reachable", err)
	}
	if got := srv.calls.Load(); got > 2 {
		t.Errorf("kept retrying after cancellation: %d attempts", got)
	}
}

// Without retry configured the client behaves as it always did: one attempt,
// and the status surfaces unchanged.
func TestNoRetryByDefault(t *testing.T) {
	srv := &statusSeq{statuses: []int{503, 503}}
	c := newMockClient(t, srv.route())

	if _, err := c.get(context.Background(), BaseURL+"/x"); err == nil {
		t.Fatal("a 503 was swallowed")
	}
	if got := srv.calls.Load(); got != 1 {
		t.Errorf("made %d attempts with no RetryPolicy, want 1", got)
	}
}

func TestHooksObserveTheWholePipeline(t *testing.T) {
	srv := &statusSeq{statuses: []int{500}}
	c := newMockClient(t, srv.route())

	var requests, responses, retries atomic.Int64
	var sawStatus atomic.Int64
	var sawDelay atomic.Int64
	WithHooks(Hooks{
		OnRequest: func(_, _ string, _ int) { requests.Add(1) },
		OnResponse: func(_, _ string, _, status int, _ time.Duration, _ error) {
			responses.Add(1)
			sawStatus.Store(int64(status))
		},
		OnRetry: func(_, _ string, _, _ int, delay time.Duration, _ error) {
			retries.Add(1)
			sawDelay.Store(int64(delay))
		},
	})(c)
	WithRetry(RetryPolicy{MaxAttempts: 2, BaseDelay: time.Microsecond, MaxDelay: time.Millisecond})(c)

	if _, err := c.get(context.Background(), BaseURL+"/x"); err != nil {
		t.Fatalf("get: %v", err)
	}

	if requests.Load() != 2 || responses.Load() != 2 {
		t.Errorf("OnRequest fired %d times and OnResponse %d, want 2 each",
			requests.Load(), responses.Load())
	}
	if retries.Load() != 1 {
		t.Errorf("OnRetry fired %d times, want 1", retries.Load())
	}
	if sawDelay.Load() <= 0 {
		t.Error("OnRetry reported a non-positive delay; a run that is slow because of backoff would be inexplicable")
	}
}

func TestRetryAfterHeaderForms(t *testing.T) {
	tests := []struct {
		header string
		want   bool // whether a positive duration is expected
	}{
		{"5", true},
		{"0", false},
		{"", false},
		{"not-a-number", false},
		{time.Now().Add(3 * time.Second).UTC().Format(http.TimeFormat), true},
		// A date in the past means "now", not a negative wait.
		{time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat), false},
	}

	for _, tt := range tests {
		resp := &http.Response{Header: http.Header{}}
		if tt.header != "" {
			resp.Header.Set("Retry-After", tt.header)
		}
		got := retryAfter(resp)
		if (got > 0) != tt.want {
			t.Errorf("retryAfter(%q) = %v, want positive=%v", tt.header, got, tt.want)
		}
	}
}

func TestDefaultRetryable(t *testing.T) {
	if !defaultRetryable(0, errors.New("connection reset")) {
		t.Error("a transport error should be retried")
	}
	for _, status := range []int{429, 500, 502, 503} {
		if !defaultRetryable(status, nil) {
			t.Errorf("status %d should be retried", status)
		}
	}
	for _, status := range []int{200, 400, 401, 403, 404} {
		if defaultRetryable(status, nil) {
			t.Errorf("status %d should not be retried", status)
		}
	}
}

// A Retry-After longer than MaxDelay is a refusal, not an invitation to come
// back sooner. Clamping it would have the client return before the server said
// it may, which is precisely what RespectRetryAfter exists to prevent.
func TestRetryAfterBeyondMaxDelayStopsRatherThanShortening(t *testing.T) {
	srv := &statusSeq{statuses: []int{429, 429}, retryAfter: "600"}
	c := newMockClient(t, srv.route())
	WithRetry(RetryPolicy{
		MaxAttempts:       3,
		BaseDelay:         time.Millisecond,
		MaxDelay:          10 * time.Second,
		RespectRetryAfter: true,
	})(c)

	_, err := c.get(context.Background(), BaseURL+"/x")
	var se *StatusError
	if !errors.As(err, &se) || se.Code != 429 {
		t.Fatalf("err = %v, want a StatusError carrying 429", err)
	}
	// The caller has to be able to act on it, so the header survives in the error.
	if se.RetryAfter != 600*time.Second {
		t.Errorf("RetryAfter = %v, want 10m preserved in the error", se.RetryAfter)
	}
	if got := srv.calls.Load(); got != 1 {
		t.Errorf("made %d attempts, want 1: a 10m Retry-After must not be shortened to 10s", got)
	}
}

// Backoff must stay inside MaxDelay. The previous implementation added half
// the window to a jittered value and so could return 1.5x the stated ceiling.
func TestBackoffStaysWithinMaxDelay(t *testing.T) {
	c := NewClient()
	WithRetry(RetryPolicy{MaxAttempts: 10, BaseDelay: time.Second, MaxDelay: 2 * time.Second})(c)

	for attempt := 1; attempt <= 10; attempt++ {
		for range 200 {
			if d := c.backoff(attempt); d < 0 || d >= 2*time.Second {
				t.Fatalf("backoff(attempt=%d) = %v, outside [0, MaxDelay)", attempt, d)
			}
		}
	}
}

// Full jitter means the whole window is reachable, not a narrow band around
// the exponent. Workers that failed together have to spread out.
func TestBackoffSpreadsAcrossTheWindow(t *testing.T) {
	c := NewClient()
	WithRetry(RetryPolicy{MaxAttempts: 5, BaseDelay: time.Second, MaxDelay: time.Second})(c)

	var low, high int
	for range 500 {
		if c.backoff(1) < 250*time.Millisecond {
			low++
		}
		if c.backoff(1) > 750*time.Millisecond {
			high++
		}
	}
	// With full jitter each quarter is reachable; with the old [d/2, 1.5d)
	// shape the bottom quarter never was.
	if low == 0 || high == 0 {
		t.Errorf("backoff never reached the bottom (%d) or top (%d) quarter of the window", low, high)
	}
}
