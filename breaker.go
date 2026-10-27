package googleplayscraper

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"time"
)

// The breaker.
//
// AIMD slows the client down when Google refuses; it does not stop it. After a
// 429 the interval doubles and requests keep going, each one more evidence for
// whatever decided to refuse. Crawlers and the circuit-breaker pattern both do
// the other thing: stop the host for a while, then send one request and see.
// That is this file. It is off unless AdaptivePolicy.Cooldown is set.
//
// The states are the usual three plus one:
//
//	closed     requests go out at the throttle's pace
//	open       nothing goes out until the pause ends
//	half-open  one probe goes out; everyone else waits for its answer
//	stopped    probes kept failing: every request fails fast with ErrBlocked
//	           until MaxCooldown has passed, then the next one probes
//
// The part that is easy to get wrong is the requests already on their way when
// a refusal lands. Each admission hands out a ticket stamped with the breaker
// generation it was admitted under, and only a ticket from the current
// generation can move the breaker. Without that, four requests in flight when
// the block hits would come back as four refusals and count as three failed
// probes before a single pause had happened.

// ErrBlocked is returned, wrapped with the last refusal, while the breaker is
// stopped: probes kept failing and the client is waiting out MaxCooldown.
var ErrBlocked = errors.New("blocked by Google Play")

// BreakerState is the state of the client's breaker.
type BreakerState int

const (
	BreakerClosed BreakerState = iota
	BreakerOpen
	BreakerHalfOpen
	BreakerStopped
)

func (s BreakerState) String() string {
	switch s {
	case BreakerClosed:
		return "closed"
	case BreakerOpen:
		return "open"
	case BreakerHalfOpen:
		return "half-open"
	case BreakerStopped:
		return "stopped"
	}
	return fmt.Sprintf("breaker(%d)", int(s))
}

// BreakerEvent describes one state change, for Hooks.OnBreaker.
type BreakerEvent struct {
	From, To BreakerState
	// Until is when an open or stopped breaker lets the next probe out.
	Until time.Time
	// Reason names the signal behind the change: "refused", "retry-after",
	// "probes", "probe-ok", "pause-over".
	Reason string
	// FailedProbes counts probes refused in a row so far.
	FailedProbes int
	// Err is the refusal that caused the change, when there was one.
	Err error
}

type breaker struct {
	state    BreakerState
	gen      uint64
	until    time.Time
	k        int // probes refused in a row; also the escalation exponent
	probeOut bool
	clean    int // clean responses since the last close; resets k at Window
	wake     chan struct{}
	last     error
}

// ticket is one admission. gen is the breaker generation it was admitted
// under; probe marks the half-open probe. done is set once its outcome has
// been applied, so release after a resolved attempt is a no-op.
type ticket struct {
	gen   uint64
	probe bool
	done  bool
}

// outcome is everything one attempt tells the controller.
type outcome struct {
	status     int
	rtt        time.Duration
	err        error
	verdict    Verdict
	retryAfter time.Duration
	// cause is the error the attempt returns, a StatusError included. err is
	// only set for failures that are not a status, so a 404 stays an answer.
	cause error
}

type signal int

const (
	signalClean   signal = iota // an answer: 2xx with data, an empty result, a 404
	signalRefusal               // 429, 503, a soft block
	signalSoft                  // transport error, other 5xx, malformed body, truncated body
	signalNeutral               // consent or sign-in page: says nothing about rate
)

func (o outcome) signal() signal {
	// The verdict comes first: a consent page served with a 429 is still a
	// consent page, and waiting will not change it.
	switch {
	case o.verdict == VerdictInterstitial:
		return signalNeutral
	case o.status == http.StatusTooManyRequests || o.status == http.StatusServiceUnavailable ||
		o.verdict == VerdictBlocked:
		return signalRefusal
	case o.err != nil || o.status >= 500 || o.verdict == VerdictMalformed:
		return signalSoft
	}
	return signalClean
}

// isRefusal reports whether a failed attempt was the server saying no.
func isRefusal(status int, err error) bool {
	if status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable {
		return true
	}
	var sb *SoftBlockError
	return errors.As(err, &sb) && sb.Verdict == VerdictBlocked
}

// isBlockErr reports whether err means Google is refusing this client, as
// opposed to failing one request. Operations that otherwise keep partial
// results return these instead.
func isBlockErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrBlocked) {
		return true
	}
	var se *StatusError
	if errors.As(err, &se) && isRefusal(se.Code, nil) {
		return true
	}
	var sb *SoftBlockError
	return errors.As(err, &sb) && (sb.Verdict == VerdictBlocked || sb.Verdict == VerdictInterstitial)
}

func (c *Client) breakerOn() bool {
	return c.adaptive != nil && c.adaptive.Cooldown > 0
}

// Breaker reports the breaker's state and, when open or stopped, when the next
// probe may go out. Without a Cooldown it is always closed.
func (c *Client) Breaker() (BreakerState, time.Time) {
	c.throttleLock.Lock()
	defer c.throttleLock.Unlock()
	return c.brk.state, c.brk.until
}

// setState moves the breaker and wakes every waiter. Callers hold
// throttleLock and emit the returned event after releasing it.
func (c *Client) setState(to BreakerState, reason string, err error) BreakerEvent {
	b := &c.brk
	ev := BreakerEvent{From: b.state, To: to, Until: b.until, Reason: reason, FailedProbes: b.k, Err: err}
	b.state = to
	if to != BreakerClosed {
		b.gen++
	}
	if to == BreakerOpen || to == BreakerStopped {
		// A probe out under the old generation can no longer resolve the
		// breaker; leaving it marked would keep the next half-open waiting
		// on an answer that will be ignored.
		b.probeOut = false
	}
	b.broadcast()
	return ev
}

// broadcast wakes every goroutine waiting on the breaker. Callers hold
// throttleLock.
func (b *breaker) broadcast() {
	if b.wake != nil {
		close(b.wake)
	}
	b.wake = make(chan struct{})
}

func (c *Client) emitBreaker(ctx context.Context, ev BreakerEvent) {
	c.log().LogAttrs(ctx, slog.LevelDebug, "breaker",
		slog.String("from", ev.From.String()), slog.String("to", ev.To.String()),
		slog.Time("until", ev.Until), slog.String("reason", ev.Reason),
		slog.Int("failed_probes", ev.FailedProbes))
	if c.hooks.OnBreaker != nil {
		c.hooks.OnBreaker(ev)
	}
}

// admit is the throttle with the breaker in front of it. Without a Cooldown it
// is waitThrottle and nothing else.
func (c *Client) admit(ctx context.Context) (*ticket, time.Duration, error) {
	if !c.breakerOn() {
		waited, err := c.waitThrottle(ctx)
		return &ticket{}, waited, err
	}
	start := time.Now()
	for {
		c.throttleLock.Lock()
		b := &c.brk
		if b.wake == nil {
			b.wake = make(chan struct{})
		}
		now := time.Now()

		var events []BreakerEvent
		if (b.state == BreakerOpen || b.state == BreakerStopped) && !now.Before(b.until) {
			events = append(events, c.setState(BreakerHalfOpen, "pause-over", nil))
		}

		switch b.state {
		case BreakerStopped:
			err := fmt.Errorf("%w until %s", ErrBlocked, b.until.Format(time.RFC3339))
			if b.last != nil {
				err = fmt.Errorf("%w: %w", err, b.last)
			}
			c.throttleLock.Unlock()
			c.emitAll(ctx, events)
			return nil, 0, err

		case BreakerOpen:
			wait, wake := b.until.Sub(now), b.wake
			c.throttleLock.Unlock()
			c.emitAll(ctx, events)
			if err := waitFor(ctx, wait, wake); err != nil {
				return nil, 0, err
			}

		case BreakerHalfOpen:
			if b.probeOut {
				wake := b.wake
				c.throttleLock.Unlock()
				c.emitAll(ctx, events)
				if err := waitFor(ctx, -1, wake); err != nil {
					return nil, 0, err
				}
				continue
			}
			b.probeOut = true
			c.lastRequest = now
			t := &ticket{gen: b.gen, probe: true}
			c.throttleLock.Unlock()
			c.emitAll(ctx, events)
			return t, time.Since(start), nil

		default: // closed
			gen, wake := b.gen, b.wake
			slot := now
			if c.throttle > 0 {
				if next := c.lastRequest.Add(c.throttle); next.After(now) {
					slot = next
				}
				c.lastRequest = slot
			}
			c.throttleLock.Unlock()
			c.emitAll(ctx, events)
			if d := time.Until(slot); d > 0 {
				if err := waitFor(ctx, d, wake); err != nil {
					return nil, 0, err
				}
			}
			// A pause that began while this goroutine slept invalidates its
			// slot: firing now would be a request the server was just told
			// it would not get.
			c.throttleLock.Lock()
			current := c.brk.gen == gen && c.brk.state == BreakerClosed
			c.throttleLock.Unlock()
			if current {
				return &ticket{gen: gen}, time.Since(start), nil
			}
		}
	}
}

func (c *Client) emitAll(ctx context.Context, events []BreakerEvent) {
	for _, ev := range events {
		c.emitBreaker(ctx, ev)
	}
}

// waitFor sleeps for d (forever when d < 0) or until wake is closed, whichever
// is first. It returns ctx.Err() if the context ends the wait.
func waitFor(ctx context.Context, d time.Duration, wake <-chan struct{}) error {
	var timeout <-chan time.Time
	if d >= 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		timeout = t.C
	}
	select {
	case <-timeout:
		return nil
	case <-wake:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// complete applies an attempt's outcome to the breaker. Callers hold
// throttleLock. It reports the state change, if there was one.
func (c *Client) complete(t *ticket, o outcome) (BreakerEvent, bool) {
	if t == nil || t.done || !c.breakerOn() {
		return BreakerEvent{}, false
	}
	t.done = true
	b := &c.brk
	p := c.adaptive
	current := t.gen == b.gen

	switch o.signal() {
	case signalRefusal:
		if !current {
			// Sent before the pause began. It says nothing new about the
			// rate and never counts as a failed probe, but a Retry-After is
			// still an instruction, and a long one stops the client exactly
			// as it would have from a current request.
			if o.retryAfter <= 0 {
				return BreakerEvent{}, false
			}
			u := time.Now().Add(o.retryAfter)
			switch {
			case o.retryAfter > p.MaxCooldown && b.state != BreakerStopped:
				b.last = o.cause
				b.until = u
				return c.setState(BreakerStopped, "retry-after", o.cause), true
			case b.state == BreakerHalfOpen || b.state == BreakerClosed:
				b.last = o.cause
				b.until = u
				return c.setState(BreakerOpen, "retry-after", o.cause), true
			case u.After(b.until):
				b.until = u
			}
			return BreakerEvent{}, false
		}
		b.last = o.cause
		b.clean = 0
		if t.probe {
			b.k++
			b.probeOut = false
		}
		now := time.Now()
		switch {
		case o.retryAfter > p.MaxCooldown:
			// "Come back in an hour" is not answered by holding every
			// request for an hour.
			b.until = now.Add(o.retryAfter)
			return c.setState(BreakerStopped, "retry-after", o.cause), true
		case t.probe && p.MaxProbes > 0 && b.k >= p.MaxProbes:
			b.until = now.Add(p.MaxCooldown)
			return c.setState(BreakerStopped, "probes", o.cause), true
		}
		b.until = now.Add(max(o.retryAfter, c.pause(b.k)))
		return c.setState(BreakerOpen, "refused", o.cause), true

	case signalClean:
		if !current {
			return BreakerEvent{}, false
		}
		if t.probe {
			b.probeOut = false
			b.clean = 0
			c.lastRequest = time.Now()
			return c.setState(BreakerClosed, "probe-ok", nil), true
		}
		b.clean++
		if b.clean >= p.Window {
			b.k = 0
		}
		return BreakerEvent{}, false
	}

	// Anything else -- a transport error, a malformed page, a consent page --
	// says nothing about the rate. It does interrupt a clean window, and a
	// probe that got one hands the probe on.
	if o.signal() == signalSoft {
		b.clean = 0
	}
	if t.probe && current {
		b.probeOut = false
		b.broadcast()
	}
	return BreakerEvent{}, false
}

// release applies "no outcome" to a ticket whose attempt never reached the
// server, or whose outcome was not applied: a cancelled context, a request
// that could not be built. A probe released this way is handed on.
func (c *Client) release(t *ticket) {
	if t == nil || t.done || !c.breakerOn() {
		return
	}
	c.throttleLock.Lock()
	t.done = true
	if t.probe && t.gen == c.brk.gen {
		c.brk.probeOut = false
		c.brk.broadcast()
	}
	c.throttleLock.Unlock()
}

// pause is the k-th escalating pause, jittered over its upper half so that it
// is never close to zero. Callers hold throttleLock.
func (c *Client) pause(k int) time.Duration {
	p := c.adaptive
	d := p.Cooldown
	for i := 0; i < k && d < p.MaxCooldown; i++ {
		d *= 2
	}
	d = min(d, p.MaxCooldown)
	half := d / 2
	if half <= 0 {
		return d
	}
	return half + time.Duration(rand.Int63n(int64(half)))
}

// Retry budget: a token bucket shared by the client. It starts with Burst
// tokens; every admitted first attempt adds Budget, capped at Burst; a retry
// takes one. Without a Budget every retry is allowed, as before.

func (c *Client) noteFirstAttempt() {
	if c.retry.Budget <= 0 {
		return
	}
	c.budgetLock.Lock()
	c.budgetTokens = min(float64(c.retry.Burst), c.budgetTokens+c.retry.Budget)
	c.budgetLock.Unlock()
}

// refundRetry returns a token taken for a retry that was never sent.
func (c *Client) refundRetry() {
	if c.retry.Budget <= 0 {
		return
	}
	c.budgetLock.Lock()
	c.budgetTokens = min(float64(c.retry.Burst), c.budgetTokens+1)
	c.budgetLock.Unlock()
}

func (c *Client) takeRetry() bool {
	if c.retry.Budget <= 0 {
		return true
	}
	c.budgetLock.Lock()
	defer c.budgetLock.Unlock()
	if c.budgetTokens < 1 {
		return false
	}
	c.budgetTokens--
	return true
}
