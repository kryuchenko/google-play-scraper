package googleplayscraper

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"runtime/trace"
	"strconv"
	"time"
)

// The request pipeline.
//
// Every request goes through the same stages, in this order:
//
//	method -> [cache] -> retry loop -> throttle -> HTTP
//
// The order is the design. Retry sits outside the throttle so that a repeated
// attempt reserves its own slot -- retrying inside would let a burst of
// failures punch straight through the rate limit, which is the fastest way to
// turn a temporary 429 into a lasting block. A cache, when there is one, sits
// outside retry so that a hit costs neither a slot nor an attempt.
//
// The stages are laid out here together rather than grown one at a time
// because they all cut the same twenty lines: retry, observability hooks and
// caching each want to wrap the same call, and adding them separately means
// rewriting this file three times and getting the ordering wrong at least
// once.
//
// Caching is the one stage not yet implemented, and there is deliberately no
// placeholder field for it: a stage that does nothing is speculative until
// there is a cache to put there. What the layout buys is that adding one is a
// lookup and a store around the retry loop in do(), not a rearrangement --
// which is the part that would otherwise have to happen twice.

// RetryPolicy configures how a failed request is repeated. The zero value
// retries nothing, which is what this package did before the policy existed.
type RetryPolicy struct {
	// MaxAttempts is the total number of attempts, not the number of retries.
	// Zero or one means no retrying.
	MaxAttempts int

	// BaseDelay is the first backoff interval; it doubles per attempt up to
	// MaxDelay. Zero defaults to 500ms.
	BaseDelay time.Duration

	// MaxDelay caps the backoff. Zero defaults to 30s.
	MaxDelay time.Duration

	// RespectRetryAfter honours a Retry-After header when the server sends
	// one, in place of the computed backoff. Google does send it on 429, and
	// ignoring it is how a client earns a longer block.
	RespectRetryAfter bool

	// Retryable decides whether a failed attempt is worth repeating. nil uses
	// defaultRetryable: transport errors, 429, and 5xx. A 404 is an answer,
	// not a failure, and repeating it wastes the caller's rate budget.
	Retryable func(status int, err error) bool
}

// WithRetry enables retrying with backoff and jitter.
//
// Nothing retried before this option existed: a caller who wanted to survive a
// transient 503 had to write the loop themselves, and the loop they wrote
// almost certainly did not re-enter the throttle, so it made the next failure
// more likely rather than less.
func WithRetry(p RetryPolicy) ClientOption {
	return func(c *Client) {
		if p.BaseDelay <= 0 {
			p.BaseDelay = 500 * time.Millisecond
		}
		if p.MaxDelay <= 0 {
			p.MaxDelay = 30 * time.Second
		}
		if p.Retryable == nil {
			p.Retryable = defaultRetryable
		}
		c.retry = p
	}
}

// defaultRetryable repeats what is plausibly transient and nothing else.
func defaultRetryable(status int, err error) bool {
	if err != nil {
		// Transport failures: connection reset, TLS handshake, DNS. Context
		// cancellation is excluded by the caller, which checks it first.
		return true
	}
	return status == http.StatusTooManyRequests || status >= 500
}

// Hooks observe the pipeline. Every field is optional, and every callback is
// invoked from the goroutine making the request -- which may be a worker in
// the pool, so an implementation that shares state needs its own locking.
//
// These exist so that observability can be added without this package growing
// a dependency on any particular framework: runtime/trace is built in (see
// trace.go), and anything else -- OpenTelemetry, a metrics registry, a
// structured logger -- hangs off here.
type Hooks struct {
	// OnRequest fires before each attempt, after the throttle slot is taken.
	OnRequest func(method, url string, attempt int)

	// OnResponse fires after each attempt that produced a status, successful
	// or not. err is set for transport failures, where status is 0.
	OnResponse func(method, url string, attempt, status int, took time.Duration, err error)

	// OnRetry fires before sleeping between attempts, with the delay that was
	// chosen. It is the hook that makes a slow run explicable.
	OnRetry func(method, url string, attempt, status int, delay time.Duration, err error)
}

// WithHooks installs observability callbacks.
func WithHooks(h Hooks) ClientOption {
	return func(c *Client) { c.hooks = h }
}

// do runs one request through the pipeline and returns its body.
//
// newReq builds a fresh *http.Request per attempt rather than taking one:
// a request whose body has been read cannot be replayed, and reusing one
// across attempts is the classic way a retry silently sends an empty body.
func (c *Client) do(ctx context.Context, method, url string, newReq func() (*http.Request, error)) ([]byte, error) {
	attempts := c.retry.MaxAttempts
	if attempts < 1 {
		attempts = 1
	}

	for attempt := 1; ; attempt++ {
		// The throttle is inside the retry loop on purpose: every attempt
		// reserves its own slot, so a run of failures spreads out instead of
		// bypassing the rate limit exactly when the server is asking for less.
		if err := c.waitThrottle(ctx); err != nil {
			return nil, err
		}

		body, status, err := c.attempt(ctx, method, url, newReq, attempt)
		if err == nil {
			return body, nil
		}

		// A cancelled context is the caller leaving, not a failure to retry --
		// and the error should say so whichever moment the cancellation landed
		// in. It used to return err untouched here, so a caller who cancelled
		// got context.Canceled when the cancel arrived during the backoff sleep
		// (below) and "unexpected status: 500" when it arrived during the
		// request, for the same action. Both causes are wrapped now: errors.Is
		// finds the cancellation, and the status the last attempt saw is still
		// reachable for anyone who wants it.
		if ctxErr := ctx.Err(); ctxErr != nil {
			if errors.Is(err, ctxErr) {
				return nil, err
			}
			return nil, fmt.Errorf("%w: %w", ctxErr, err)
		}
		c.retryAfterHint(err)
		if attempt >= attempts || !c.retry.Retryable(status, transportErr(err)) {
			return nil, err
		}

		delay, ok := c.retryDelay(attempt, err)
		if !ok {
			return nil, err // the server asked for longer than the policy allows
		}
		if c.hooks.OnRetry != nil {
			c.hooks.OnRetry(method, url, attempt, status, delay, err)
		}
		if werr := sleepCtx(ctx, delay); werr != nil {
			return nil, werr
		}
	}
}

// attempt performs one HTTP round trip. It returns the status alongside the
// error so the retry decision can see it without unwrapping.
func (c *Client) attempt(
	ctx context.Context,
	method, url string,
	newReq func() (*http.Request, error),
	attempt int,
) (body []byte, status int, err error) {
	req, err := newReq()
	if err != nil {
		return nil, 0, fmt.Errorf("create request: %w", err)
	}

	if c.hooks.OnRequest != nil {
		c.hooks.OnRequest(method, url, attempt)
	}
	region := trace.StartRegion(ctx, traceRegionHTTP)
	logTrace(ctx, "http.url", url)
	started := time.Now()

	resp, err := c.httpClient.Do(req)
	took := time.Since(started)
	if err != nil {
		region.End()
		c.observe(0, took, err)
		if c.hooks.OnResponse != nil {
			c.hooks.OnResponse(method, url, attempt, 0, took, err)
		}
		return nil, 0, fmt.Errorf("do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	logTrace(ctx, "http.status", resp.Status)
	c.observe(resp.StatusCode, took, nil)
	if c.hooks.OnResponse != nil {
		c.hooks.OnResponse(method, url, attempt, resp.StatusCode, took, nil)
	}

	if resp.StatusCode != http.StatusOK {
		region.End()
		return nil, resp.StatusCode, &StatusError{Code: resp.StatusCode, RetryAfter: retryAfter(resp)}
	}

	body, err = io.ReadAll(resp.Body)
	region.End()
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read body: %w", err)
	}
	return body, resp.StatusCode, nil
}

// backoff computes how long to wait before the next attempt.
//
// Full jitter -- uniform over [0, d) rather than d plus a wobble. Several pool
// workers that failed on the same upstream hiccup would otherwise retry in a
// tight cluster, reproducing the burst the backoff exists to prevent. Spreading
// them over the whole window is what actually decorrelates them.
//
// MaxDelay is a ceiling on what this returns, not on the exponent before
// jitter: a caller that says "never wait more than 30s" means the wait.
func (c *Client) backoff(attempt int) time.Duration {
	d := c.retry.BaseDelay << (attempt - 1)
	// The shift overflows into negative territory well before it reaches any
	// sane MaxDelay, so check both.
	if d <= 0 || d > c.retry.MaxDelay {
		d = c.retry.MaxDelay
	}
	if d <= 0 {
		return 0
	}
	// Not crypto, and does not need to be: this decorrelates workers, it does
	// not resist anyone.
	return time.Duration(rand.Int63n(int64(d)))
}

// retryDelay decides the wait before the next attempt, or reports that there
// should not be one.
//
// A Retry-After longer than MaxDelay is a refusal, not a suggestion to come
// back sooner. Clamping it would have the client return before the server said
// it may -- exactly the behaviour the option exists to avoid, and the fastest
// way to turn a temporary limit into a longer one. So the request fails, and
// the caller gets a StatusError that still carries RetryAfter and can decide
// for itself.
func (c *Client) retryDelay(attempt int, err error) (time.Duration, bool) {
	if c.retry.RespectRetryAfter {
		var se *StatusError
		if errors.As(err, &se) && se.RetryAfter > 0 {
			if se.RetryAfter > c.retry.MaxDelay {
				return 0, false
			}
			return se.RetryAfter, true
		}
	}
	return c.backoff(attempt), true
}

// retryAfter reads the header in both forms the RFC allows: delay-seconds,
// which is what Google sends, and an HTTP date.
func retryAfter(resp *http.Response) time.Duration {
	v := resp.Header.Get("Retry-After")
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	defer trace.StartRegion(ctx, traceRegionBackoff).End()

	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// transportErr unwraps to the underlying error for a transport failure, or
// returns nil when the failure was a status. defaultRetryable distinguishes
// the two by whether this is nil.
func transportErr(err error) error {
	var se *StatusError
	if errors.As(err, &se) {
		return nil
	}
	return err
}
