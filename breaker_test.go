package googleplayscraper

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// scripted answers each request with the status its handler picks, and can
// hold a request until released, to model requests in flight.
type scripted struct {
	mu     sync.Mutex
	calls  int
	starts []time.Time
	answer func(n int) (status int, retryAfter int)
}

func (s *scripted) RoundTrip(req *http.Request) (*http.Response, error) {
	s.mu.Lock()
	s.calls++
	n := s.calls
	s.starts = append(s.starts, time.Now())
	s.mu.Unlock()
	status, ra := s.answer(n)
	h := make(http.Header)
	if ra > 0 {
		h.Set("Retry-After", strconv.Itoa(ra))
	}
	body := ")]}'\n[[\"wrb.fr\",\"x\",null]]"
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: h, Request: req}, nil
}

func (s *scripted) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func breakerClient(tr http.RoundTripper, p AdaptivePolicy, opts ...ClientOption) (*Client, *[]BreakerEvent) {
	var mu sync.Mutex
	events := &[]BreakerEvent{}
	if p.Min == 0 {
		p.Min = time.Millisecond
	}
	if p.Max == 0 {
		p.Max = time.Millisecond
	}
	all := append([]ClientOption{
		WithHTTPClient(&http.Client{Transport: tr}),
		WithAdaptiveThrottle(p),
		WithHooks(Hooks{OnBreaker: func(ev BreakerEvent) {
			mu.Lock()
			*events = append(*events, ev)
			mu.Unlock()
		}}),
	}, opts...)
	return NewClient(all...), events
}

func get(c *Client) error {
	_, err := c.get(context.Background(), BaseURL+"/x")
	return err
}

// One refusal pauses the client; after the pause one probe goes out, and its
// success closes the breaker.
func TestBreakerPausesThenProbes(t *testing.T) {
	tr := &scripted{answer: func(n int) (int, int) {
		if n == 1 {
			return http.StatusTooManyRequests, 0
		}
		return http.StatusOK, 0
	}}
	c, events := breakerClient(tr, AdaptivePolicy{Cooldown: 60 * time.Millisecond})

	refusedAt := time.Now()
	if err := get(c); err == nil {
		t.Fatal("first request should fail with 429")
	}
	if s, until := c.Breaker(); s != BreakerOpen || until.Sub(refusedAt) < 30*time.Millisecond {
		t.Fatalf("breaker = %s until +%v, want open for at least Cooldown/2", s, until.Sub(refusedAt))
	}
	if err := get(c); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if waited := tr.starts[1].Sub(refusedAt); waited < 30*time.Millisecond {
		t.Errorf("probe went out after %v, before the pause ended", waited)
	}
	if s, _ := c.Breaker(); s != BreakerClosed {
		t.Errorf("breaker = %s after a good probe, want closed", s)
	}
	var path []string
	for _, ev := range *events {
		path = append(path, ev.To.String())
	}
	if got := strings.Join(path, ","); got != "open,half-open,closed" {
		t.Errorf("transitions = %s", got)
	}
}

// Requests already in flight when the block lands come back refused too. They
// were sent before the pause began, so they must not count as failed probes:
// four of them would otherwise stop the client before a single pause.
func TestBreakerIgnoresStaleRefusals(t *testing.T) {
	release := make(chan struct{})
	var inFlight sync.WaitGroup
	inFlight.Add(4)
	tr := &scripted{answer: func(n int) (int, int) {
		if n <= 4 {
			inFlight.Done()
			<-release
			return http.StatusTooManyRequests, 0
		}
		return http.StatusOK, 0
	}}
	c, _ := breakerClient(tr, AdaptivePolicy{Cooldown: 40 * time.Millisecond, MaxProbes: 2})
	c.throttle = 0

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() { defer wg.Done(); _ = get(c) }()
	}
	inFlight.Wait()
	close(release)
	wg.Wait()

	if s, _ := c.Breaker(); s != BreakerOpen {
		t.Fatalf("breaker = %s, want open (not stopped) after one burst of refusals", s)
	}
	c.throttleLock.Lock()
	k := c.brk.k
	c.throttleLock.Unlock()
	if k != 0 {
		t.Errorf("failed probes = %d, want 0: no probe has been sent yet", k)
	}
	if err := get(c); err != nil {
		t.Errorf("probe after the pause: %v", err)
	}
}

// While half-open, exactly one request goes out; the rest wait for its answer.
func TestBreakerHalfOpenSendsOneProbe(t *testing.T) {
	probeHeld := make(chan struct{})
	releaseProbe := make(chan struct{})
	tr := &scripted{answer: func(n int) (int, int) {
		switch n {
		case 1:
			return http.StatusTooManyRequests, 0
		case 2:
			close(probeHeld)
			<-releaseProbe
		}
		return http.StatusOK, 0
	}}
	c, _ := breakerClient(tr, AdaptivePolicy{Cooldown: 20 * time.Millisecond})
	c.throttle = 0
	_ = get(c)

	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() { defer wg.Done(); _ = get(c) }()
	}
	<-probeHeld
	time.Sleep(30 * time.Millisecond)
	if n := tr.count(); n != 2 {
		t.Errorf("%d requests sent while the probe was out, want 2 (the refusal and the probe)", n)
	}
	close(releaseProbe)
	wg.Wait()
	if n := tr.count(); n != 6 {
		t.Errorf("%d requests in total, want 6", n)
	}
}

// A probe whose caller gives up must hand the probe on, or every request
// waiting on the half-open breaker waits forever.
func TestBreakerCancelledProbeHandsOn(t *testing.T) {
	tr := &scripted{answer: func(n int) (int, int) {
		if n == 1 {
			return http.StatusTooManyRequests, 0
		}
		return http.StatusOK, 0
	}}
	c, _ := breakerClient(tr, AdaptivePolicy{Cooldown: 20 * time.Millisecond})
	_ = get(c)
	time.Sleep(25 * time.Millisecond)

	// Take the probe and abandon it without a response.
	tk, _, err := c.admit(context.Background())
	if err != nil || !tk.probe {
		t.Fatalf("admit = %+v, %v; want the probe", tk, err)
	}
	done := make(chan error, 1)
	go func() { done <- get(c) }()
	time.Sleep(10 * time.Millisecond)
	c.release(tk)

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("waiter after release: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("waiter never got the probe after it was released")
	}
}

// Refused probes escalate to stopped: requests then fail fast with
// ErrBlocked, and after MaxCooldown the client probes again by itself.
func TestBreakerStopsAndRecovers(t *testing.T) {
	var healthy atomic.Bool
	tr := &scripted{answer: func(int) (int, int) {
		if healthy.Load() {
			return http.StatusOK, 0
		}
		return http.StatusTooManyRequests, 0
	}}
	c, _ := breakerClient(tr, AdaptivePolicy{Cooldown: 10 * time.Millisecond, MaxCooldown: 80 * time.Millisecond, MaxProbes: 2})

	_ = get(c) // opens
	_ = get(c) // probe 1 refused
	_ = get(c) // probe 2 refused: stopped
	if s, _ := c.Breaker(); s != BreakerStopped {
		t.Fatalf("breaker = %s, want stopped", s)
	}
	sent := tr.count()
	start := time.Now()
	err := get(c)
	if !errors.Is(err, ErrBlocked) || time.Since(start) > 10*time.Millisecond {
		t.Fatalf("while stopped: err %v after %v, want ErrBlocked at once", err, time.Since(start))
	}
	var se *StatusError
	if !errors.As(err, &se) || se.Code != http.StatusTooManyRequests {
		t.Errorf("ErrBlocked should wrap the last refusal, got %v", err)
	}
	if tr.count() != sent {
		t.Error("a stopped client sent a request")
	}

	healthy.Store(true)
	time.Sleep(90 * time.Millisecond)
	if err := get(c); err != nil {
		t.Fatalf("after MaxCooldown: %v", err)
	}
	if s, _ := c.Breaker(); s != BreakerClosed {
		t.Errorf("breaker = %s, want closed again", s)
	}
}

// "Come back in an hour" goes straight to stopped instead of holding every
// request for an hour.
func TestBreakerLongRetryAfterStops(t *testing.T) {
	tr := &scripted{answer: func(int) (int, int) { return http.StatusTooManyRequests, 3600 }}
	c, _ := breakerClient(tr, AdaptivePolicy{Cooldown: 10 * time.Millisecond})
	_ = get(c)
	s, until := c.Breaker()
	if s != BreakerStopped || time.Until(until) < 59*time.Minute {
		t.Fatalf("breaker = %s until +%v, want stopped for the server's hour", s, time.Until(until))
	}
	if err := get(c); !errors.Is(err, ErrBlocked) {
		t.Errorf("err = %v, want ErrBlocked", err)
	}
}

// With the breaker on, a retry after a refusal waits out the pause instead of
// a backoff, and a Retry-After above MaxDelay no longer ends the request.
func TestBreakerRetryWaitsOutPause(t *testing.T) {
	tr := &scripted{answer: func(n int) (int, int) {
		if n == 1 {
			return http.StatusTooManyRequests, 1
		}
		return http.StatusOK, 0
	}}
	c, _ := breakerClient(tr, AdaptivePolicy{Cooldown: 10 * time.Millisecond, MaxCooldown: 5 * time.Second},
		WithRetry(RetryPolicy{MaxAttempts: 2, MaxDelay: 10 * time.Millisecond, RespectRetryAfter: true}))
	start := time.Now()
	if err := get(c); err != nil {
		t.Fatalf("retried request: %v", err)
	}
	if took := time.Since(start); took < 900*time.Millisecond {
		t.Errorf("retry after %v, want the server's one second", took)
	}
}

// The retry budget caps retries across the client, not per request.
func TestRetryBudget(t *testing.T) {
	tr := &scripted{answer: func(int) (int, int) { return http.StatusInternalServerError, 0 }}
	c := NewClient(
		WithHTTPClient(&http.Client{Transport: tr}),
		WithRetry(RetryPolicy{MaxAttempts: 3, BaseDelay: time.Microsecond, Budget: 0.1, Burst: 1}),
	)
	_ = get(c) // 1 attempt + 1 retry from the burst; the third is refused by the budget
	_ = get(c) // budget earned 0.2 since: no retry
	if n := tr.count(); n != 3 {
		t.Errorf("%d requests, want 3: two first attempts and one budgeted retry", n)
	}
}

// Review finding: a consent page served with a 429 is still a consent page.
// The verdict decides before the status, so it neither slows the client nor
// opens the breaker.
func TestConsentWith429IsNeutral(t *testing.T) {
	var calls atomic.Int32
	c := NewClient(
		WithHTTPClient(redirectTo("https://consent.google.com/m?continue=x", http.StatusTooManyRequests, "", &calls)),
		WithAdaptiveThrottle(AdaptivePolicy{Min: time.Millisecond, Max: time.Second, Start: 10 * time.Millisecond,
			Cooldown: time.Hour}),
	)
	_, err := c.App(context.Background(), "x", AppOptions{})
	var sb *SoftBlockError
	if !errors.As(err, &sb) || sb.Verdict != VerdictInterstitial {
		t.Fatalf("err = %v, want interstitial", err)
	}
	if s, _ := c.Breaker(); s != BreakerClosed {
		t.Errorf("breaker = %s, want closed", s)
	}
	if got := c.CurrentThrottle(); got != 10*time.Millisecond {
		t.Errorf("interval = %v, want unchanged", got)
	}
}

// Review finding: a long Retry-After on a request that was already in flight
// must stop the client like it would from a current one, not leave every
// waiter hanging on an hour-long pause.
func TestBreakerStaleLongRetryAfterStops(t *testing.T) {
	release := make(chan struct{})
	var inFlight sync.WaitGroup
	inFlight.Add(2)
	var order atomic.Int32
	tr := &scripted{answer: func(int) (int, int) {
		inFlight.Done()
		<-release
		if order.Add(1) == 1 {
			return http.StatusTooManyRequests, 0
		}
		return http.StatusTooManyRequests, 3600
	}}
	c, events := breakerClient(tr, AdaptivePolicy{Cooldown: 20 * time.Millisecond})
	c.throttle = 0

	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); _ = get(c) }()
	}
	inFlight.Wait()
	close(release)
	wg.Wait()

	if s, until := c.Breaker(); s != BreakerStopped || time.Until(until) < 59*time.Minute {
		t.Fatalf("breaker = %s until +%v, want stopped for the hour", s, time.Until(until))
	}
	if err := get(c); !errors.Is(err, ErrBlocked) {
		t.Errorf("err = %v, want ErrBlocked at once", err)
	}
	last := (*events)[len(*events)-1]
	if last.To != BreakerStopped || last.Reason != "retry-after" {
		t.Errorf("last event = %s/%s, want stopped/retry-after", last.To, last.Reason)
	}
}

// Review finding: a retry that is never sent must not spend the budget. Here
// the server asks for longer than MaxDelay, so the first request gives up
// without retrying; the second request must still have its retry.
func TestRetryBudgetNotSpentOnUnsentRetry(t *testing.T) {
	tr := &scripted{answer: func(n int) (int, int) {
		if n == 1 {
			return http.StatusTooManyRequests, 60
		}
		return http.StatusInternalServerError, 0
	}}
	c := NewClient(
		WithHTTPClient(&http.Client{Transport: tr}),
		WithRetry(RetryPolicy{MaxAttempts: 2, BaseDelay: time.Microsecond, MaxDelay: time.Second,
			RespectRetryAfter: true, Budget: 0.01, Burst: 1}),
	)
	_ = get(c) // 429 with Retry-After 60s > MaxDelay: no retry, no token
	_ = get(c) // 500: retried with the token that is still there
	if n := tr.count(); n != 3 {
		t.Errorf("%d requests, want 3", n)
	}
}

// Review finding: the escalation exponent resets only after a full window of
// clean responses, not after Window clean responses with errors in between.
func TestBreakerEscalationNeedsAFullCleanWindow(t *testing.T) {
	c, _ := breakerClient(&scripted{answer: func(int) (int, int) { return 200, 0 }},
		AdaptivePolicy{Cooldown: time.Hour, Window: 5})
	c.throttleLock.Lock()
	c.brk.k = 2
	c.throttleLock.Unlock()
	ctx := context.Background()
	feed := func(o outcome) { c.record(ctx, &ticket{gen: c.brk.gen}, o) }
	for range 4 {
		feed(outcome{status: http.StatusOK})
	}
	feed(outcome{status: http.StatusInternalServerError})
	for range 4 {
		feed(outcome{status: http.StatusOK})
	}
	c.throttleLock.Lock()
	k := c.brk.k
	c.throttleLock.Unlock()
	if k != 2 {
		t.Errorf("escalation = %d after an interrupted window, want 2", k)
	}
	feed(outcome{status: http.StatusOK})
	c.throttleLock.Lock()
	k = c.brk.k
	c.throttleLock.Unlock()
	if k != 0 {
		t.Errorf("escalation = %d after a full clean window, want 0", k)
	}
}

// Re-review finding: a retry cancelled before it reaches the server gives its
// token back.
func TestRetryBudgetRefundsCancelledRetry(t *testing.T) {
	tr := &scripted{answer: func(int) (int, int) { return http.StatusInternalServerError, 0 }}
	ctx, cancel := context.WithCancel(context.Background())
	c := NewClient(
		WithHTTPClient(&http.Client{Transport: tr}),
		WithRetry(RetryPolicy{MaxAttempts: 2, BaseDelay: time.Microsecond, Budget: 0.01, Burst: 1}),
		WithHooks(Hooks{OnRequest: func(_, _ string, attempt int) {
			if attempt == 2 {
				cancel()
			}
		}}),
	)
	_, _ = c.get(ctx, BaseURL+"/x")
	c.budgetLock.Lock()
	tokens := c.budgetTokens
	c.budgetLock.Unlock()
	if tokens < 1 {
		t.Errorf("budget = %.2f after a cancelled retry, want the token back", tokens)
	}
}

// Re-review finding: a consent page interrupts a clean window too.
func TestBreakerCleanWindowInterruptedByInterstitial(t *testing.T) {
	c, _ := breakerClient(&scripted{answer: func(int) (int, int) { return 200, 0 }},
		AdaptivePolicy{Cooldown: time.Hour, Window: 5})
	c.throttleLock.Lock()
	c.brk.k = 2
	c.throttleLock.Unlock()
	ctx := context.Background()
	feed := func(o outcome) { c.record(ctx, &ticket{gen: c.brk.gen}, o) }
	for range 4 {
		feed(outcome{status: http.StatusOK})
	}
	feed(outcome{status: http.StatusOK, verdict: VerdictInterstitial})
	feed(outcome{status: http.StatusOK})
	c.throttleLock.Lock()
	k := c.brk.k
	c.throttleLock.Unlock()
	if k != 2 {
		t.Errorf("escalation = %d, want 2: the window was interrupted", k)
	}
}
