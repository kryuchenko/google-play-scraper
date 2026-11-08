package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	googleplayscraper "github.com/kryuchenko/google-play-scraper"
)

// Fault injection against the sweep, on the fake store, with the CLI's own
// retry configuration rather than the bare client useFakeClient builds.
//
// useFakeClient deliberately leaves retries out, so the sweep's retry pass is
// what those tests see. These are about the layers together: the client's
// retry policy honouring Retry-After, the breaker under -adaptive, the sweep's
// retry pass, and what is left on disk when the run cannot finish. Each case
// is a few shards and runs in a second or two.

// useRealClient sends the CLI's real client through the fake store. The CLI
// builds its client with no transport of its own, so http.DefaultTransport is
// the seam: it is swapped for one that rewrites play.google.com onto the test
// server, and newClientHook stays nil so common.client builds exactly what a
// run would -- retry policy, Retry-After, -adaptive and all.
func useRealClient(t *testing.T, store *fakeStore) {
	t.Helper()
	orig := http.DefaultTransport
	host := strings.TrimPrefix(store.srv.URL, "http://")
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		r2 := req.Clone(req.Context())
		r2.URL.Scheme, r2.URL.Host, r2.Host = "http", host, host
		return orig.RoundTrip(r2)
	})
	prev := newClientHook
	newClientHook = nil
	t.Cleanup(func() {
		http.DefaultTransport = orig
		newClientHook = prev
	})
}

// useCLIClientWith is common.client's configuration with the knobs a test
// cannot otherwise turn, compressed so a case runs in about a second: the
// per-request timeout (30s in a run), the breaker's cooldown (30s under
// -adaptive) and the adaptive interval's ceiling (2s, which after a refusal
// spaces every later shard). Everything else mirrors client():
// three attempts honouring Retry-After, a 0.1 retry budget and the adaptive
// controller with the breaker reporting through onBreaker when c.adaptive.
func useCLIClientWith(t *testing.T, store *fakeStore, timeout, cooldown time.Duration) {
	t.Helper()
	const maxInterval = 100 * time.Millisecond // 2s in a run
	prev := newClientHook
	newClientHook = func(c *common) *googleplayscraper.Client {
		retry := googleplayscraper.RetryPolicy{MaxAttempts: 3, RespectRetryAfter: true}
		opts := []googleplayscraper.ClientOption{
			googleplayscraper.WithHTTPClient(&http.Client{Transport: store.transport()}),
			googleplayscraper.WithThrottle(c.throttle),
			googleplayscraper.WithConcurrency(c.concurrency),
			googleplayscraper.WithTimeout(timeout),
		}
		if c.adaptive {
			retry.Budget = 0.1
			opts = append(opts,
				googleplayscraper.WithAdaptiveThrottle(googleplayscraper.AdaptivePolicy{
					Min: c.throttle, Max: maxInterval, Cooldown: cooldown,
				}),
				googleplayscraper.WithHooks(googleplayscraper.Hooks{OnBreaker: c.onBreaker}),
			)
		}
		opts = append(opts, googleplayscraper.WithRetry(retry))
		return googleplayscraper.NewClient(opts...)
	}
	t.Cleanup(func() { newClientHook = prev })
}

// faultFixture is four shards of generation A on a fresh store, no client
// installed yet.
func faultFixture(t *testing.T) (*fakeStore, *sitemapFixture, string) {
	t.Helper()
	store := newFakeStore(t)
	return store, newSitemapFixture(t, store, fixtureGenA, fourShards()), t.TempDir()
}

func manifestExists(dir string, gen googleplayscraper.Generation) bool {
	_, err := os.Stat(filepath.Join(dir, "manifest-"+gen.ID()+".json"))
	return err == nil
}

// The resume state an incomplete run must leave for the next one.
func assertResumeStateKept(t *testing.T, dir string, f *sitemapFixture, wantDone int) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, "state.json")); err != nil {
		t.Errorf("state.json missing after an incomplete run: %v", err)
	}
	done, err := readDoneLog(doneLogPath(dir, f.gen))
	if err != nil {
		t.Fatalf("done log: %v", err)
	}
	if len(done) != wantDone {
		t.Errorf("done log holds %d shards, want %d", len(done), wantDone)
	}
}

// A 429 carrying Retry-After is waited out by the client and the shard then
// succeeds: no failed shard, a complete manifest, and the wait honoured.
func TestFault429WithRetryAfterIsWaitedOut(t *testing.T) {
	store, f, dir := faultFixture(t)
	useRealClient(t, store)
	store.failNextWith(f.shardPath(2), http.StatusTooManyRequests, 1, http.Header{"Retry-After": {"1"}})

	start := time.Now()
	_, stderr, err := runVerb(t, cmdSync, "-dir", dir, "-concurrency", "1", "-throttle", "0", "-adaptive=false")
	if err != nil {
		t.Fatalf("sweep: %v\n%s", err, stderr)
	}
	if took := time.Since(start); took < 900*time.Millisecond {
		t.Errorf("finished in %v: the 1s Retry-After was not honoured", took)
	}
	if got := store.hitCount(f.shardPath(2)); got != 2 {
		t.Errorf("the 429 shard was fetched %d times, want 2", got)
	}
	if strings.Contains(stderr, "retrying") {
		t.Errorf("the sweep's retry pass ran for a shard the client recovered:\n%s", stderr)
	}
	if !manifestExists(dir, fixtureGenA) {
		t.Error("no manifest after a recovered 429")
	}
}

// A shard that answers 503 every time is retried by the client, then by the
// sweep's retry pass, and the sweep refuses to publish: the failure line names
// the shard, no manifest is written, and the other shards' progress is kept.
// The fault tests that do not pass -adaptive pass -adaptive=false: they pin
// the fixed-interval behaviour, and catalog sweep now defaults to -adaptive,
// whose breaker pauses and then stops on a shard that refuses every time.
func TestFault503EveryTimeIsRefusedAndNamed(t *testing.T) {
	store, f, dir := faultFixture(t)
	useRealClient(t, store)
	store.set(f.shardPath(2), http.StatusServiceUnavailable, nil)

	_, stderr, err := runVerb(t, cmdSync, "-dir", dir, "-concurrency", "1", "-throttle", "0", "-adaptive=false")
	if err == nil || !strings.Contains(err.Error(), "after a retry pass") {
		t.Fatalf("err = %v, want the refusal", err)
	}
	if !strings.Contains(stderr, f.shardPath(2)) {
		t.Errorf("stderr does not name the failing shard %s:\n%s", f.shardPath(2), stderr)
	}
	// Three attempts in the main pass and three in the retry pass.
	if got := store.hitCount(f.shardPath(2)); got != 6 {
		t.Errorf("the 503 shard was fetched %d times, want 6 (3 attempts x 2 passes)", got)
	}
	if manifestExists(dir, fixtureGenA) {
		t.Error("a manifest was written for a sweep missing a shard")
	}
	assertResumeStateKept(t, dir, f, 3)
}

// A response slower than the per-request timeout fails that attempt, and the
// next attempt -- answered promptly -- completes the shard.
func TestFaultSlowResponseTimesOutAndIsRetried(t *testing.T) {
	store, f, dir := faultFixture(t)
	useCLIClientWith(t, store, 300*time.Millisecond, 0)
	var calls atomic.Int32
	body := gzipTestBytes(t, urlsetTestXML(
		googleplayscraper.BaseURL+"/store/apps/details?id="+f.ids[1][0],
		googleplayscraper.BaseURL+"/store/apps/details?id="+f.ids[1][1]))
	store.setFunc(f.shardPath(1), func(r *http.Request) (int, []byte) {
		if calls.Add(1) == 1 {
			select { // hang past the client's timeout
			case <-r.Context().Done():
			case <-time.After(3 * time.Second):
			}
		}
		return http.StatusOK, body
	})

	_, stderr, err := runVerb(t, cmdSync, "-dir", dir, "-concurrency", "1", "-throttle", "0", "-adaptive=false")
	if err != nil {
		t.Fatalf("sweep: %v\n%s", err, stderr)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("the slow shard was fetched %d times, want 2 (timed out, then answered)", got)
	}
	if !manifestExists(dir, fixtureGenA) {
		t.Error("no manifest after a timed-out attempt was retried")
	}
}

// A gzip stream cut short fails the shard on both passes; the sweep refuses,
// names it, writes no manifest and keeps the rest.
func TestFaultTruncatedGzipIsRefused(t *testing.T) {
	store, f, dir := faultFixture(t)
	useRealClient(t, store)
	whole := gzipTestBytes(t, urlsetTestXML(googleplayscraper.BaseURL+"/store/apps/details?id=com.d"))
	store.set(f.shardPath(3), http.StatusOK, whole[:len(whole)/2])

	_, stderr, err := runVerb(t, cmdSync, "-dir", dir, "-concurrency", "1", "-throttle", "0", "-adaptive=false")
	if err == nil || !strings.Contains(err.Error(), "after a retry pass") {
		t.Fatalf("err = %v, want the refusal", err)
	}
	if !strings.Contains(stderr, f.shardPath(3)) {
		t.Errorf("stderr does not name the truncated shard:\n%s", stderr)
	}
	if manifestExists(dir, fixtureGenA) {
		t.Error("a manifest was written over a truncated shard")
	}
	assertResumeStateKept(t, dir, f, 3)
}

// The generation rolls while the sweep is running: the shard that triggers it
// still answers, every later one is gone. The run stops with the rolled error
// and no manifest, and the next run sweeps the new generation cleanly.
func TestFaultGenerationRollsMidSweep(t *testing.T) {
	store, f, dir := faultFixture(t)
	useRealClient(t, store)
	body := gzipTestBytes(t, urlsetTestXML(googleplayscraper.BaseURL+"/store/apps/details?id=com.c"))
	var rolled atomic.Bool
	store.setFunc(f.shardPath(1), func(*http.Request) (int, []byte) {
		if rolled.CompareAndSwap(false, true) {
			f.roll(t, fixtureGenB, fourShards())
		}
		return http.StatusOK, body
	})

	_, _, err := runVerb(t, cmdSync, "-dir", dir, "-concurrency", "1", "-throttle", "0", "-adaptive=false")
	if err == nil || !strings.Contains(err.Error(), "rolled") {
		t.Fatalf("err = %v, want the rolled-generation error", err)
	}
	if manifestExists(dir, fixtureGenA) {
		t.Error("a manifest was written for a generation that rolled mid-sweep")
	}
	if _, _, err := runVerb(t, cmdSync, "-dir", dir, "-concurrency", "1", "-throttle", "0", "-adaptive=false"); err != nil {
		t.Fatalf("sweep of the new generation: %v", err)
	}
	if !manifestExists(dir, fixtureGenB) {
		t.Error("no manifest for the new generation")
	}
}

// Under -adaptive a 429 opens the breaker: the whole run pauses, one probe
// goes out, and when it is answered the run resumes and completes. Said on
// stderr both ways.
func TestFaultAdaptiveBreakerPausesAndResumes(t *testing.T) {
	store, f, dir := faultFixture(t)
	useCLIClientWith(t, store, 5*time.Second, 200*time.Millisecond)
	store.failNextWith(f.shardPath(2), http.StatusTooManyRequests, 1, nil)

	_, stderr, err := runVerb(t, cmdSync, "-dir", dir, "-concurrency", "1", "-throttle", "0", "-adaptive")
	if err != nil {
		t.Fatalf("sweep: %v\n%s", err, stderr)
	}
	if !strings.Contains(stderr, "pausing") || !strings.Contains(stderr, "resuming") {
		t.Errorf("stderr does not report the pause and the resume:\n%s", stderr)
	}
	if !manifestExists(dir, fixtureGenA) {
		t.Error("no manifest after the breaker recovered")
	}
}

// When every request from some point on is refused, the default run grinds
// through its attempts and both passes before refusing; under -adaptive the
// breaker gives up after its probes and stops the run, with far fewer
// requests sent. Both leave no manifest and keep the resume state.
func TestFaultPersistentRefusalDefaultVersusAdaptive(t *testing.T) {
	refuseFrom1 := func(store *fakeStore, f *sitemapFixture) {
		for i := 1; i < len(f.ids); i++ {
			store.set(f.shardPath(i), http.StatusTooManyRequests, nil)
		}
	}
	hits := func(store *fakeStore, f *sitemapFixture) int {
		n := 0
		for i := 1; i < len(f.ids); i++ {
			n += store.hitCount(f.shardPath(i))
		}
		return n
	}

	store, f, dir := faultFixture(t)
	useCLIClientWith(t, store, 5*time.Second, 0)
	refuseFrom1(store, f)
	// Concurrency 3 so the three shards' backoffs overlap; the request count
	// is the same either way.
	_, _, err := runVerb(t, cmdSync, "-dir", dir, "-concurrency", "3", "-throttle", "0", "-adaptive=false")
	if err == nil || !strings.Contains(err.Error(), "after a retry pass") {
		t.Fatalf("default: err = %v, want the refusal", err)
	}
	defaultHits := hits(store, f)
	if defaultHits != 3*3*2 {
		t.Errorf("default: %d requests to the refused shards, want 18 (3 shards x 3 attempts x 2 passes)", defaultHits)
	}
	if manifestExists(dir, fixtureGenA) {
		t.Error("default: a manifest was written")
	}
	assertResumeStateKept(t, dir, f, 1)

	store, f, dir = faultFixture(t)
	useCLIClientWith(t, store, 5*time.Second, 100*time.Millisecond)
	refuseFrom1(store, f)
	_, stderr, err := runVerb(t, cmdSync, "-dir", dir, "-concurrency", "1", "-throttle", "0", "-adaptive")
	if err == nil {
		t.Fatal("adaptive: a run refused throughout reported success")
	}
	if !strings.Contains(stderr, "stopping") {
		t.Errorf("adaptive: stderr does not say the breaker stopped the run:\n%s", stderr)
	}
	if adaptiveHits := hits(store, f); adaptiveHits >= defaultHits {
		t.Errorf("adaptive: %d requests to refused shards, not fewer than the default run's %d", adaptiveHits, defaultHits)
	}
	if manifestExists(dir, fixtureGenA) {
		t.Error("adaptive: a manifest was written")
	}
	assertResumeStateKept(t, dir, f, 1)
}
