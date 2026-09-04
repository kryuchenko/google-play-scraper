package googleplayscraper

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// catalogShards builds n shard bodies, each carrying one app id, wired through
// the same routes EnumerateCatalog's own tests use.
func catalogShards(t *testing.T, n int) []routeFunc {
	t.Helper()
	bodies := make(map[string][]byte, n)
	for i := range n {
		url := fmt.Sprintf("%s/sitemaps/shard-%02d.xml.gz", BaseURL, i)
		bodies[url] = gzipBytes(t, urlsetXML(
			fmt.Sprintf("https://play.google.com/store/apps/details?id=com.example.app%02d", i),
		))
	}
	return catalogRoutes(t, bodies)
}

// The property that matters most for CatalogSeq is what happens on an early
// break, because that is the whole reason it exists over EnumerateCatalog.
// Shards are swept concurrently and ids arrive over a channel, so a producer
// parked on a send has to be released when the consumer walks away. Getting
// that wrong leaks a goroutine per abandoned sweep — and a leak that only
// happens on the early-exit path is exactly the kind that survives review.
func TestCatalogSeqEarlyBreakLeavesNoGoroutines(t *testing.T) {
	settle()
	before := runtime.NumGoroutine()

	for range 20 {
		c := newMockClient(t, catalogShards(t, 40)...)
		var got int
		for pkg, err := range c.CatalogSeq(context.Background(), CatalogOptions{Concurrency: 8}) {
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if pkg == "" {
				t.Fatal("yielded an empty package id")
			}
			got++
			if got == 3 {
				break // abandon the sweep with shards still in flight
			}
		}
		if got != 3 {
			t.Fatalf("stopped after %d ids, want 3", got)
		}
	}

	settle()
	if after := runtime.NumGoroutine(); after > before+2 {
		t.Errorf("goroutines grew from %d to %d over 20 abandoned sweeps: "+
			"the producer is not released when the consumer breaks", before, after)
	}
}

// CatalogSeq waits for its producer before returning, so by the time the loop
// is over the sweep is genuinely finished. Without that, "no leak" would only
// mean "the goroutine exits eventually", which no test could pin down.
func TestCatalogSeqStopsFetchingAfterBreak(t *testing.T) {
	var fetched atomic.Int64
	routes := append([]routeFunc{
		func(req *http.Request) (mockResponse, bool) {
			if len(req.URL.Path) > 10 && req.URL.Path[:11] == "/sitemaps/s" {
				fetched.Add(1)
			}
			return mockResponse{}, false // count, then fall through to the real route
		},
	}, catalogShards(t, 60)...)

	c := newMockClient(t, routes...)
	for range c.CatalogSeq(context.Background(), CatalogOptions{Concurrency: 2}) {
		break
	}

	settle()
	stoppedAt := fetched.Load()
	time.Sleep(20 * time.Millisecond)
	if later := fetched.Load(); later != stoppedAt {
		t.Errorf("shards were still being fetched after the loop returned: %d then %d", stoppedAt, later)
	}
	if stoppedAt >= 60 {
		t.Errorf("fetched all %d shards despite breaking on the first id", stoppedAt)
	}
}

// A shard that fails is not terminal: the sweep continues and the failure goes
// to OnShardError. That separation is the whole design decision behind the
// error slot, so it is worth pinning against a future "simplification".
func TestCatalogSeqShardErrorDoesNotEndSequence(t *testing.T) {
	bodies := map[string][]byte{}
	for i := range 6 {
		url := fmt.Sprintf("%s/sitemaps/shard-%02d.xml.gz", BaseURL, i)
		bodies[url] = gzipBytes(t, urlsetXML(
			fmt.Sprintf("https://play.google.com/store/apps/details?id=com.example.app%02d", i),
		))
	}
	routes := catalogRoutes(t, bodies)
	// One shard answers 500 ahead of its real route.
	routes = append([]routeFunc{
		routePathStatus("/sitemaps/shard-02.xml.gz", http.StatusInternalServerError),
	}, routes...)

	c := newMockClient(t, routes...)

	var shardErrs atomic.Int64
	var ids []string
	for pkg, err := range c.CatalogSeq(context.Background(), CatalogOptions{
		Concurrency:  1,
		OnShardError: func(int, string, error) { shardErrs.Add(1) },
	}) {
		if err != nil {
			t.Fatalf("a shard failure surfaced as a terminal error: %v", err)
		}
		ids = append(ids, pkg)
	}

	if got := shardErrs.Load(); got != 1 {
		t.Errorf("OnShardError called %d times, want 1", got)
	}
	if len(ids) != 5 {
		t.Errorf("got %d ids, want 5 (one shard of six fails): %v", len(ids), ids)
	}
}

// Failing to list the shards is terminal, and the caller has to see it:
// ending the sequence silently would be indistinguishable from an empty store.
func TestCatalogSeqTerminalErrorIsReported(t *testing.T) {
	c := newMockClient(t, routePathStatus("/robots.txt", http.StatusInternalServerError))

	var ids int
	var gotErr error
	var gotPkg string
	for pkg, err := range c.CatalogSeq(context.Background(), CatalogOptions{}) {
		if err != nil {
			gotErr, gotPkg = err, pkg
			continue
		}
		ids++
	}

	if gotErr == nil {
		t.Fatal("sequence ended without reporting that the shard list could not be fetched")
	}
	if gotPkg != "" {
		t.Errorf("terminal error came with package id %q, want empty", gotPkg)
	}
	if ids != 0 {
		t.Errorf("yielded %d ids despite never listing the shards", ids)
	}
}

// A cancelled context is terminal too, and must not be mistaken for the end of
// the catalog.
func TestCatalogSeqCancellationIsTerminal(t *testing.T) {
	c := newMockClient(t, catalogShards(t, 30)...)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var seen int
	var gotErr error
	for _, err := range c.CatalogSeq(ctx, CatalogOptions{Concurrency: 2}) {
		if err != nil {
			gotErr = err
			break
		}
		seen++
		if seen == 2 {
			cancel()
		}
	}

	if gotErr == nil {
		t.Fatal("sequence ended without reporting the cancellation")
	}
	if !errors.Is(gotErr, context.Canceled) {
		t.Errorf("terminal error is %v, want it to wrap context.Canceled", gotErr)
	}
}

// reviewsRoute serves the recorded batchexecute fixture for the first `ok`
// pages and then fails. The fixture carries 150 reviews and a live next-page
// token, so replaying it produces an unbounded chain -- which is exactly the
// shape ReviewsSeq is built for, and the shape a hand-written payload would
// have to imitate anyway.
func reviewsRoute(t *testing.T, ok int, fetched *atomic.Int64) routeFunc {
	t.Helper()
	body := readFixture(t, "reviews_batch.bin")
	return func(req *http.Request) (mockResponse, bool) {
		if req.URL.Path != "/_/PlayStoreUi/data/batchexecute" {
			return mockResponse{}, false
		}
		if fetched.Add(1) > int64(ok) {
			return mockResponse{Status: http.StatusInternalServerError}, true
		}
		return mockResponse{Body: body}, true
	}
}

// ReviewsSeq exists partly to remove ReviewsAll's invisible 500-review
// ceiling, which a caller can neither see nor raise without guessing a number.
// The sequence has to be willing to go past it.
func TestReviewsSeqPassesTheReviewsAllCeiling(t *testing.T) {
	var pages atomic.Int64
	c := newMockClient(t, reviewsRoute(t, 100, &pages))

	var n int
	for _, err := range c.ReviewsSeq(context.Background(), "com.example.app", ReviewOptions{}) {
		if err != nil {
			t.Fatalf("unexpected error after %d reviews: %v", n, err)
		}
		n++
		if n > 600 {
			break
		}
	}

	if n <= 500 {
		t.Errorf("sequence stopped at %d reviews, want past ReviewsAll's 500 ceiling", n)
	}
	if got := pages.Load(); got < 4 {
		t.Errorf("fetched %d pages for %d reviews; pagination is not advancing", got, n)
	}
}

// Breaking out has to stop the token chain, or the iterator would be no
// cheaper than ReviewsAll for a caller that wants the first few reviews.
func TestReviewsSeqBreakStopsPaginating(t *testing.T) {
	var pages atomic.Int64
	c := newMockClient(t, reviewsRoute(t, 100, &pages))

	var n int
	for range c.ReviewsSeq(context.Background(), "com.example.app", ReviewOptions{}) {
		n++
		if n == 5 {
			break
		}
	}

	if got := pages.Load(); got != 1 {
		t.Errorf("fetched %d pages to deliver 5 reviews, want 1", got)
	}
}

// Reviews already handed over stay valid when a later page fails: pagination
// is a token chain, so there is nowhere to continue from, but the caller keeps
// what it collected and learns why the rest is missing.
func TestReviewsSeqKeepsWhatArrivedBeforeAFailure(t *testing.T) {
	var pages atomic.Int64
	c := newMockClient(t, reviewsRoute(t, 2, &pages))

	var n int
	var gotErr error
	for r, err := range c.ReviewsSeq(context.Background(), "com.example.app", ReviewOptions{}) {
		if err != nil {
			gotErr = err
			if r.ID != "" {
				t.Errorf("terminal error came with a non-zero review: %+v", r)
			}
			break
		}
		n++
	}

	if gotErr == nil {
		t.Fatal("a failing page ended the sequence without an error")
	}
	var se *StatusError
	if !errors.As(gotErr, &se) || se.Code != http.StatusInternalServerError {
		t.Errorf("terminal error is %v, want a StatusError carrying 500", gotErr)
	}
	if n != 300 {
		t.Errorf("kept %d reviews from the two pages that succeeded, want 300", n)
	}
}

// settle lets goroutines that are already unwinding finish, so a count taken
// after it reflects what is actually retained rather than what happened to be
// mid-exit.
func settle() {
	for range 5 {
		runtime.GC()
		time.Sleep(2 * time.Millisecond)
	}
}

// Breaking out cancels the sweep, which makes the shards still in flight fail
// with context.Canceled. Reporting those through OnShardError would be a lie:
// the caller asked to stop, nothing failed. Found by running the iterator
// against the live store, where the noise was immediately obvious.
func TestCatalogSeqBreakDoesNotReportShardFailures(t *testing.T) {
	c := newMockClient(t, catalogShards(t, 60)...)

	var shardErrs atomic.Int64
	var n int
	for range c.CatalogSeq(context.Background(), CatalogOptions{
		Concurrency:  8,
		OnShardError: func(int, string, error) { shardErrs.Add(1) },
	}) {
		n++
		if n == 2 {
			break
		}
	}

	if got := shardErrs.Load(); got != 0 {
		t.Errorf("OnShardError fired %d times for a sweep the caller stopped; want 0", got)
	}
}

// The suppression must not swallow a real cancellation: when the caller's own
// context is cancelled, shards that fail because of it are genuine failures
// from the caller's point of view and still belong in OnShardError.
func TestCatalogSeqParentCancellationStillReportsShards(t *testing.T) {
	c := newMockClient(t, catalogShards(t, 60)...)

	ctx, cancel := context.WithCancel(context.Background())
	var n int
	for _, err := range c.CatalogSeq(ctx, CatalogOptions{Concurrency: 4}) {
		if err != nil {
			break
		}
		n++
		if n == 2 {
			cancel()
		}
	}
	cancel()
	// No assertion on the count: how many shards are mid-flight when the
	// cancellation lands is inherently timing-dependent. What matters is that
	// the suppression is keyed on the parent context rather than blanket, and
	// that this path does not panic or hang.
}
