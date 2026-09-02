package googleplayscraper

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"
)

func adaptiveClient(t *testing.T, route routeFunc, p AdaptivePolicy) *Client {
	t.Helper()
	c := newMockClient(t, route)
	WithAdaptiveThrottle(p)(c)
	return c
}

// The controller starts at Max, not Min. A caller whose address is already
// unwelcome should find that out at one request per second, not at thirty.
func TestAdaptiveStartsSlow(t *testing.T) {
	c := adaptiveClient(t, routePath("/x", []byte("ok")), AdaptivePolicy{
		Min: 10 * time.Millisecond,
		Max: time.Second,
	})
	if got := c.CurrentThrottle(); got != time.Second {
		t.Errorf("initial throttle = %v, want Max (1s)", got)
	}
}

// Clean responses speed it up, but only after a full window: one lucky
// response is not evidence.
func TestAdaptiveSpeedsUpAfterACleanWindow(t *testing.T) {
	c := adaptiveClient(t, routePath("/x", []byte("ok")), AdaptivePolicy{
		Min: 10 * time.Millisecond, Max: time.Second, Window: 5, Speedup: 0.5,
	})

	for range 4 {
		c.observe(http.StatusOK, time.Millisecond, nil)
	}
	if got := c.CurrentThrottle(); got != time.Second {
		t.Errorf("throttle moved to %v before the window closed", got)
	}

	c.observe(http.StatusOK, time.Millisecond, nil)
	if got := c.CurrentThrottle(); got != 500*time.Millisecond {
		t.Errorf("throttle = %v after a clean window, want 500ms", got)
	}
}

// A 429 undoes much more than one clean window earned. The asymmetry is the
// point: being too fast costs a block, being too slow costs time.
func TestAdaptiveBacksOffHardOnRejection(t *testing.T) {
	// 429 and 503 only: an unexplained 500 gets the gentler treatment, which
	// TestAdaptiveGradesTheSignal covers.
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		c := adaptiveClient(t, routePath("/x", nil), AdaptivePolicy{
			Min: 10 * time.Millisecond, Max: time.Second, Window: 1, Speedup: 0.5, Slowdown: 4,
		})
		// Two clean windows: 1s -> 500ms -> 250ms.
		c.observe(http.StatusOK, time.Millisecond, nil)
		c.observe(http.StatusOK, time.Millisecond, nil)
		if got := c.CurrentThrottle(); got != 250*time.Millisecond {
			t.Fatalf("setup: throttle = %v, want 250ms", got)
		}

		c.observe(status, time.Millisecond, nil)
		if got := c.CurrentThrottle(); got != time.Second {
			t.Errorf("status %d: throttle = %v, want it back at Max", status, got)
		}
	}
}

// Latency is corroborating evidence, not a verdict. A client sees end-to-end
// time -- DNS, TLS, peering, CDN routing, cache misses -- and against a large
// CDN shared with thousands of others it cannot tell its own contribution from
// anything else. So one slow response changes nothing, and a streak does.
func TestAdaptiveNeedsAStreakBeforeActingOnLatency(t *testing.T) {
	c := adaptiveClient(t, routePath("/x", []byte("ok")), AdaptivePolicy{
		Min: time.Millisecond, Max: time.Second, Start: 100 * time.Millisecond,
		Window: 100, Speedup: 0.5, LatencyRatio: 1.5, SlowStreak: 5,
	})

	// Fill the window so the baseline means something. Until it is half full
	// the controller deliberately ignores latency entirely.
	for range 20 {
		c.observe(http.StatusOK, 100*time.Millisecond, nil)
	}
	before := c.CurrentThrottle()

	// Four slow responses in a row: suggestive, not yet evidence.
	for range 4 {
		c.observe(http.StatusOK, 300*time.Millisecond, nil)
	}
	if got := c.CurrentThrottle(); got != before {
		t.Errorf("throttle moved to %v after four slow responses; want a full streak first", got)
	}

	// The fifth completes the streak.
	c.observe(http.StatusOK, 300*time.Millisecond, nil)
	if got := c.CurrentThrottle(); got <= before {
		t.Errorf("throttle = %v after a streak of slow responses, want slower than %v", got, before)
	}

	// A single slow response after a healthy one starts the count over.
	c.observe(http.StatusOK, 100*time.Millisecond, nil)
	now := c.CurrentThrottle()
	c.observe(http.StatusOK, 300*time.Millisecond, nil)
	if got := c.CurrentThrottle(); got != now {
		t.Errorf("throttle = %v; one slow response after a healthy one acted immediately", got)
	}
}

// An explicit refusal and an unexplained failure are not the same evidence. A
// 429 is the server naming the problem; a 500 may be a bug on their side or a
// flaky link on ours, and should not cost as much.
func TestAdaptiveGradesTheSignal(t *testing.T) {
	newClient := func() *Client {
		c := adaptiveClient(t, routePath("/x", nil), AdaptivePolicy{
			Min: time.Millisecond, Max: time.Minute, Start: 100 * time.Millisecond,
			Slowdown: 4, SoftSlowdown: 2,
		})
		return c
	}

	hard := newClient()
	hard.observe(http.StatusTooManyRequests, time.Millisecond, nil)
	if got := hard.CurrentThrottle(); got != 400*time.Millisecond {
		t.Errorf("429: throttle = %v, want the full 4x decrease", got)
	}

	soft := newClient()
	soft.observe(500, time.Millisecond, nil)
	if got := soft.CurrentThrottle(); got != 200*time.Millisecond {
		t.Errorf("500: throttle = %v, want the gentler 2x", got)
	}

	// 503 is explicit overload, so it counts as a refusal rather than a fault.
	unavailable := newClient()
	unavailable.observe(http.StatusServiceUnavailable, time.Millisecond, nil)
	if got := unavailable.CurrentThrottle(); got != 400*time.Millisecond {
		t.Errorf("503: throttle = %v, want the full 4x decrease", got)
	}
}

// Bounds are the caller's and are never exceeded. An adaptive controller with
// no ceiling is one bug away from hammering someone.
func TestAdaptiveRespectsBounds(t *testing.T) {
	c := adaptiveClient(t, routePath("/x", []byte("ok")), AdaptivePolicy{
		Min: 100 * time.Millisecond, Max: 200 * time.Millisecond, Window: 1, Speedup: 0.1, Slowdown: 100,
	})

	for range 50 {
		c.observe(http.StatusOK, time.Millisecond, nil)
	}
	if got := c.CurrentThrottle(); got < 100*time.Millisecond {
		t.Errorf("throttle = %v, went below Min", got)
	}
	for range 50 {
		c.observe(http.StatusTooManyRequests, time.Millisecond, nil)
	}
	if got := c.CurrentThrottle(); got > 200*time.Millisecond {
		t.Errorf("throttle = %v, went above Max", got)
	}
}

// A server that names a delay longer than the controller's own estimate is
// believed. Retry-After is a minimum, not a target: a shorter one must never
// speed the client up, which is the direction that gets an address blocked.
func TestAdaptiveHonoursRetryAfter(t *testing.T) {
	c := adaptiveClient(t, routePath("/x", nil), AdaptivePolicy{
		Min: time.Millisecond, Max: 30 * time.Second, Window: 1, Speedup: 0.01,
	})
	// One clean window takes 30s down to 300ms.
	c.observe(http.StatusOK, time.Millisecond, nil)
	if got := c.CurrentThrottle(); got != 300*time.Millisecond {
		t.Fatalf("setup: throttle = %v, want 300ms", got)
	}

	c.retryAfterHint(&StatusError{Code: 429, RetryAfter: 5 * time.Second})
	if got := c.CurrentThrottle(); got != 5*time.Second {
		t.Errorf("throttle = %v, want the 5s the server asked for", got)
	}

	// A shorter request than the current interval must not speed anything up.
	c.retryAfterHint(&StatusError{Code: 429, RetryAfter: time.Second})
	if got := c.CurrentThrottle(); got != 5*time.Second {
		t.Errorf("throttle = %v; a shorter Retry-After sped the client up", got)
	}

	// And never past the caller's ceiling.
	c.retryAfterHint(&StatusError{Code: 429, RetryAfter: time.Hour})
	if got := c.CurrentThrottle(); got != 30*time.Second {
		t.Errorf("throttle = %v, want it clamped to Max", got)
	}
}

// The interval is written by observe() while waitThrottle reads it on every
// request. Both go through throttleLock; this is the test that would have
// caught it if one of them did not.
func TestAdaptiveIsRaceFree(t *testing.T) {
	var hits int
	var mu sync.Mutex
	c := adaptiveClient(t, func(req *http.Request) (mockResponse, bool) {
		mu.Lock()
		hits++
		n := hits
		mu.Unlock()
		// Alternate healthy and rejected so the controller keeps moving.
		if n%3 == 0 {
			return mockResponse{Status: http.StatusTooManyRequests}, true
		}
		return mockResponse{Body: []byte("ok")}, true
	}, AdaptivePolicy{Min: time.Microsecond, Max: time.Millisecond, Window: 2})

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 40 {
				_, _ = c.get(context.Background(), BaseURL+"/x")
				_ = c.CurrentThrottle()
			}
		}()
	}
	wg.Wait()
}

// Without the option the client behaves exactly as it always did: the fixed
// interval is never touched.
func TestAdaptiveIsOptIn(t *testing.T) {
	c := newMockClient(t, routePath("/x", []byte("ok")))
	WithThrottle(250 * time.Millisecond)(c)

	c.observe(http.StatusTooManyRequests, time.Second, nil)
	c.observe(http.StatusOK, time.Microsecond, nil)

	if got := c.CurrentThrottle(); got != 250*time.Millisecond {
		t.Errorf("throttle = %v, want the fixed 250ms untouched", got)
	}
}

// Starting at Max was the original design and it made the controller slower
// than the fixed interval it was meant to improve on: it spent the whole run
// climbing back to where the caller had already told it to be. It now begins
// at the caller's number, whichever order the options are applied in.
func TestAdaptiveStartsFromTheCallersThrottle(t *testing.T) {
	p := AdaptivePolicy{Min: 10 * time.Millisecond, Max: 5 * time.Second}

	throttleFirst := newMockClient(t, routePath("/x", nil))
	WithThrottle(200 * time.Millisecond)(throttleFirst)
	WithAdaptiveThrottle(p)(throttleFirst)
	if got := throttleFirst.CurrentThrottle(); got != 200*time.Millisecond {
		t.Errorf("throttle then adaptive: started at %v, want 200ms", got)
	}

	// Start wins over both when given explicitly.
	explicit := newMockClient(t, routePath("/x", nil))
	WithThrottle(200 * time.Millisecond)(explicit)
	WithAdaptiveThrottle(AdaptivePolicy{Min: p.Min, Max: p.Max, Start: time.Second})(explicit)
	if got := explicit.CurrentThrottle(); got != time.Second {
		t.Errorf("explicit Start: began at %v, want 1s", got)
	}

	// With nothing else set, Max remains the cautious default.
	alone := newMockClient(t, routePath("/x", nil))
	WithAdaptiveThrottle(p)(alone)
	if got := alone.CurrentThrottle(); got != 5*time.Second {
		t.Errorf("adaptive alone: began at %v, want Max", got)
	}
}
