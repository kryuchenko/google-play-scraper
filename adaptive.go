package googleplayscraper

import (
	"errors"
	"net/http"
	"time"
)

// Adaptive throttling.
//
// WithThrottle asks the caller to pick a number, and the number is a guess.
// This package shipped 200ms as its example -- five requests a second -- which
// a live measurement says is between six and twenty times more conservative
// than Google actually tolerates:
//
//	sitemap shards   2880 requests at a sustained 32 rps for 90s, no failures,
//	                 p95 flat at ~100ms throughout
//	details pages    clean to 12 rps in bursts of a few dozen -- but see below
//
// Those two numbers are not equally trustworthy, and the difference is the
// most useful thing measuring produced. The shard figure is from a sustained
// run. The details figure came from 48 requests, and 48 was far too few: a
// later experiment that made about seven hundred requests to the same endpoint
// over a few minutes ran into a limit, lost seven to ten countries out of 177
// to fetch errors, and drove this controller all the way back to its ceiling,
// where a sweep that had taken 16 seconds took over three minutes. The limit
// released within minutes and a modest rate worked again immediately.
//
// So: an endpoint that tolerates a burst is not an endpoint that tolerates
// sustained load, and a short probe cannot tell the difference. Treat the
// details endpoint as the tighter of the two, keep Max generous enough to
// retreat into, and expect a figure measured on a fresh quota not to survive
// repetition.
//
// A guess that low turns a catalog sweep into four and a half hours of mostly
// waiting. A guess too high gets the caller blocked. The way out is not a
// better guess but to stop guessing: start slow, speed up while the server is
// answering cleanly, and back off hard the moment it is not.
//
// This is the congestion-control shape -- additive increase, multiplicative
// decrease -- driven primarily by what the server says, with latency as
// corroboration rather than as a verdict.
//
// That balance is deliberate and was got wrong first. Delay-based control
// assumes the queue it measures is one the sender created: LEDBAT is built on
// exactly that premise, and where it does not hold, delay-based schemes lose
// throughput to loss-based traffic they share a path with. A crawler is one of
// thousands of clients of a large CDN, so an inflated round-trip time is far
// more likely to be somebody else's cross traffic, a route change, a cache
// miss or a different point of presence than anything this client caused.
// Netflix's limiter draws the same line, recommending delay-based control on
// servers -- which can see their own bottleneck -- and loss-based on clients.
//
// A first version here treated a single slow response as congestion, with an
// all-time-minimum baseline. Against Google it throttled itself from 1.06s to
// 1.91s with no rejection anywhere in the run. Latency now needs a streak, is
// measured against a windowed minimum, and gets the gentle decrease; an
// explicit 429 or 503 gets the full one.
//
// Bounds are the caller's, and Max is never exceeded: an adaptive controller
// with no ceiling is one bug away from a denial of service.

// AdaptivePolicy configures WithAdaptiveThrottle. The zero value is not
// useful; use the constructor.
type AdaptivePolicy struct {
	// Min is the shortest interval between request starts -- the fastest this
	// will ever go. It is a ceiling on rate, and it is deliberately the
	// caller's decision rather than something discovered: "the server has not
	// complained yet" is not a reason to go faster without limit.
	Min time.Duration

	// Max is the longest interval: where the controller retreats to under
	// pressure. It is a floor on politeness, not a starting point.
	Max time.Duration

	// Start is where the controller begins. Zero means whatever WithThrottle
	// set, or Max if nothing did.
	//
	// Beginning at Max was the first design and it was worse than useless: on
	// a 300-shard run it finished 5x slower than a fixed 200ms interval,
	// because climbing from two seconds to thirty milliseconds takes more
	// clean responses than the run contained. The caller has already said what
	// they think is safe; the controller's job is to go faster than that when
	// the evidence allows and slower when it does not, not to rediscover the
	// starting point from scratch every time.
	Start time.Duration

	// Speedup is the factor applied to the interval after a clean window.
	// Below 1. Default 0.9, which reaches Min from Max in about 30 windows.
	Speedup float64

	// Slowdown is the factor applied when the server says no -- 429, or 503.
	// Above 1. Default 2, the classic multiplicative decrease: give back much
	// more than was gained, because the cost of being wrong is asymmetric.
	//
	// Only explicit overload gets this. A transport error or a plain 500 is
	// more often an application failure than capacity feedback, and latency is
	// weaker still; both get SoftSlowdown instead.
	Slowdown float64

	// SoftSlowdown is the factor applied to signals that suggest strain
	// without stating it: transport errors, 5xx other than 503, and sustained
	// latency. Above 1. Default 1.3.
	SoftSlowdown float64

	// SlowStreak is how many consecutive responses must exceed LatencyRatio
	// before latency alone is acted on. Default 5.
	//
	// A single slow response says almost nothing. A client sees end-to-end
	// latency -- DNS, TLS, peering, CDN point-of-presence selection, cache
	// misses, origin work -- and against a large CDN shared with thousands of
	// others, one sample cannot distinguish load this client caused from
	// anything else. Requiring a streak is what turns it from noise into
	// corroborating evidence.
	SlowStreak int

	// Window is how many consecutive clean responses justify speeding up.
	// Default 20. Larger is more cautious.
	Window int

	// LatencyRatio is how much slower than the recent baseline a response may
	// be before it counts as trouble. Default 2.
	//
	// The baseline is the minimum over a sliding window, not the best ever
	// seen. An all-time minimum is a trap: one unusually quick response sets a
	// floor the network cannot hold, and from then on ordinary jitter reads as
	// congestion. Measured against Google's sitemap CDN, p50 wanders between
	// 57ms and 85ms with p95 near 100ms while nothing at all is wrong, so a
	// ratio of 1.5 against an all-time best fires more or less constantly --
	// which is exactly what an early version of this did.
	LatencyRatio float64
}

// WithAdaptiveThrottle replaces the fixed interval with one that finds its own
// level between min and max.
//
//	c := googleplayscraper.NewClient(
//		googleplayscraper.WithAdaptiveThrottle(googleplayscraper.AdaptivePolicy{
//			Min: 30 * time.Millisecond,  // ~33 rps, the measured ceiling for shards
//			Max: 2 * time.Second,        // where it retreats to under pressure
//		}),
//	)
//
// It starts wherever WithThrottle left the interval -- or at Max if nothing
// did -- and moves from there. Combining the two is the intended use: the
// fixed value is the caller's judgement about what is safe, and this decides
// how far either side of it the evidence justifies going.
func WithAdaptiveThrottle(p AdaptivePolicy) ClientOption {
	if p.Min <= 0 {
		p.Min = 50 * time.Millisecond
	}
	if p.Max < p.Min {
		p.Max = p.Min
	}
	if p.Speedup <= 0 || p.Speedup >= 1 {
		p.Speedup = 0.9
	}
	if p.Slowdown <= 1 {
		p.Slowdown = 2
	}
	if p.SoftSlowdown <= 1 {
		p.SoftSlowdown = 1.3
	}
	if p.SlowStreak <= 0 {
		p.SlowStreak = 5
	}
	if p.Window <= 0 {
		p.Window = 20
	}
	if p.LatencyRatio <= 1 {
		p.LatencyRatio = 2
	}
	return func(c *Client) {
		c.adaptive = &p
		switch {
		case p.Start > 0:
			c.throttle = p.Start
		case c.throttle > 0:
			// WithThrottle already ran: take the caller's number as the
			// starting point regardless of option order.
			c.throttle = min(max(c.throttle, p.Min), p.Max)
		default:
			c.throttle = p.Max
		}
	}
}

// observe feeds one response back into the controller. It is called from the
// request pipeline for every attempt, successful or not.
//
// All of it runs under throttleLock, the same lock waitThrottle takes to
// reserve a slot: the interval is read on every request and written here, and
// a mutable field read without a lock is a race whether or not it looks benign.
func (c *Client) observe(status int, rtt time.Duration, err error) {
	if c.adaptive == nil {
		return
	}
	p := c.adaptive

	c.throttleLock.Lock()
	defer c.throttleLock.Unlock()

	// The baseline is the minimum over a sliding window of recent successes,
	// which is what a delay-based controller actually needs: it tracks the
	// network as it is now rather than as it was at its luckiest.
	if err == nil && status == http.StatusOK && rtt > 0 {
		c.rttWindow[c.rttNext%len(c.rttWindow)] = rtt
		c.rttNext++
	}
	baseline := c.baselineRTT()

	// Signals are graded by how much they actually say.
	//
	// An explicit refusal is the server telling us the rate is wrong, and it
	// gets the full multiplicative decrease. A transport error or a 500 might
	// be capacity, or might be a bug on their side or a flaky link on ours, so
	// it gets a gentler one. Latency gets the gentlest treatment of all and
	// only after a streak, because a client cannot tell its own contribution
	// to a shared CDN's queue from anything else happening on the path.
	switch {
	case status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable:
		c.cleanRun, c.slowRun = 0, 0
		c.slower(p.Slowdown, p.Max)
		return

	case err != nil || status >= 500:
		c.cleanRun, c.slowRun = 0, 0
		c.slower(p.SoftSlowdown, p.Max)
		return

	case baseline > 0 && rtt > time.Duration(float64(baseline)*p.LatencyRatio):
		c.cleanRun = 0
		c.slowRun++
		if c.slowRun >= p.SlowStreak {
			c.slowRun = 0
			c.slower(p.SoftSlowdown, p.Max)
		}
		return
	}

	c.slowRun = 0
	c.cleanRun++
	if c.cleanRun < p.Window {
		return
	}
	c.cleanRun = 0
	next := time.Duration(float64(c.throttle) * p.Speedup)
	if next < p.Min {
		next = p.Min
	}
	c.throttle = next
}

// slower multiplies the interval, clamped to max. Callers hold throttleLock.
func (c *Client) slower(factor float64, max time.Duration) {
	next := time.Duration(float64(c.throttle) * factor)
	if next > max || next <= 0 {
		next = max
	}
	c.throttle = next
}

// baselineRTT is the minimum over the sliding window, ignoring slots that have
// not been filled yet. It returns zero until the window has enough samples to
// mean anything -- reacting to latency before knowing what normal looks like
// is how a controller talks itself into backing off from nothing.
func (c *Client) baselineRTT() time.Duration {
	var best time.Duration
	var n int
	for _, d := range c.rttWindow {
		if d == 0 {
			continue
		}
		n++
		if best == 0 || d < best {
			best = d
		}
	}
	if n < len(c.rttWindow)/2 {
		return 0
	}
	return best
}

// CurrentThrottle reports the interval the client is presently using between
// request starts. It exists so a long run can be observed -- the whole point
// of an adaptive control loop is that its state is worth watching -- and it is
// safe to call while requests are in flight.
func (c *Client) CurrentThrottle() time.Duration {
	c.throttleLock.Lock()
	defer c.throttleLock.Unlock()
	return c.throttle
}

// retryAfterHint lets a server's explicit instruction override the
// controller's own estimate: if it asked for a delay longer than the interval
// we would have chosen, take its word.
func (c *Client) retryAfterHint(err error) {
	if c.adaptive == nil {
		return
	}
	var se *StatusError
	if !errors.As(err, &se) || se.RetryAfter <= 0 {
		return
	}
	c.throttleLock.Lock()
	defer c.throttleLock.Unlock()
	if se.RetryAfter > c.throttle {
		c.throttle = min(se.RetryAfter, c.adaptive.Max)
	}
}
