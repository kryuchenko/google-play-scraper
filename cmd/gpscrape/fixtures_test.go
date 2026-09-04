package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	googleplayscraper "github.com/kryuchenko/google-play-scraper"
)

// Offline fixtures for the catalog commands.
//
// Every verb builds its own client deep inside itself and BaseURL is a const,
// so before this file the only way to exercise cmdSync, catalogGenres or
// catalogNew was to run them against Google. That is why sweep, parallelShards,
// isGone, confirmGone and saveGenresDelta were all at 0%: the paths that decide
// whether a live row gets deleted were the ones nothing could reach.
//
// Two seams make them reachable. newClientHook replaces client construction,
// and a rewriting RoundTripper sends the URLs the library builds for
// play.google.com to an httptest server instead. Nothing here opens a socket
// outside the process, and an unrouted request is a test failure rather than a
// silent 404 -- a fixture that has stopped covering what the code fetches is
// worth hearing about immediately.

// useFakeClient routes every request a verb makes through rt. No throttle and
// no retry policy, deliberately: the sweep's own retry pass is under test and
// the library's would mask it, and -throttle's default would cost 200ms per
// request across the whole file.
func useFakeClient(t *testing.T, rt http.RoundTripper) {
	t.Helper()
	prev := newClientHook
	newClientHook = func(c *common) *googleplayscraper.Client {
		return googleplayscraper.NewClient(
			googleplayscraper.WithHTTPClient(&http.Client{Transport: rt}),
			googleplayscraper.WithConcurrency(c.concurrency))
	}
	t.Cleanup(func() { newClientHook = prev })
}

// fakeStore is play.google.com in a box: a path -> handler map behind an
// httptest server, with per-path hit counts and one-shot failures.
type fakeStore struct {
	t      *testing.T
	mu     sync.Mutex
	routes map[string]func(r *http.Request) (int, []byte)
	fails  map[string]*failure
	hits   map[string]int
	srv    *httptest.Server
}

type failure struct {
	status int
	left   int
}

func newFakeStore(t *testing.T) *fakeStore {
	t.Helper()
	s := &fakeStore{
		t:      t,
		routes: map[string]func(*http.Request) (int, []byte){},
		fails:  map[string]*failure{},
		hits:   map[string]int{},
	}
	s.srv = httptest.NewServer(s)
	t.Cleanup(s.srv.Close)
	return s
}

func (s *fakeStore) set(path string, status int, body []byte) {
	s.setFunc(path, func(*http.Request) (int, []byte) { return status, body })
}

func (s *fakeStore) setFunc(path string, fn func(*http.Request) (int, []byte)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes[path] = fn
}

// failNext makes the next `times` hits on path answer status before the normal
// route takes over again: a transient shard, which is what the retry pass
// exists for.
func (s *fakeStore) failNext(path string, status, times int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fails[path] = &failure{status: status, left: times}
}

func (s *fakeStore) hitCount(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[path]
}

func (s *fakeStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.hits[r.URL.Path]++
	fn, known := s.routes[r.URL.Path]
	if f := s.fails[r.URL.Path]; f != nil && f.left > 0 {
		f.left--
		status := f.status
		s.mu.Unlock()
		w.WriteHeader(status)
		return
	}
	s.mu.Unlock()

	if !known {
		// The same rule the library's routingTransport applies: a request the
		// fixture does not describe is a test that has stopped testing what it
		// says it does. Route a path to 404 explicitly for the cases where a
		// 404 is the answer under test.
		s.t.Errorf("unrouted %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
		return
	}
	status, body := fn(r)
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// transport rewrites scheme and host onto the test server. BaseURL is a const,
// so this is the only place a test can redirect the library's requests.
func (s *fakeStore) transport() http.RoundTripper {
	host := strings.TrimPrefix(s.srv.URL, "http://")
	return roundTripFunc(func(req *http.Request) (*http.Response, error) {
		r2 := req.Clone(req.Context())
		r2.URL.Scheme = "http"
		r2.URL.Host = host
		r2.Host = host
		return http.DefaultTransport.RoundTrip(r2)
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// ---- sitemaps ----

const (
	indexPath0 = "/sitemaps/sitemaps-index-0.xml"
	indexPath1 = "/sitemaps/sitemaps-index-1.xml"
)

// sitemapFixture publishes one generation: robots.txt advertising two indexes,
// each listing half the shards, and one gzipped urlset per shard.
type sitemapFixture struct {
	store *fakeStore
	gen   googleplayscraper.Generation
	urls  []string // absolute shard URLs, as the index advertises them
	ids   [][]string
}

func newSitemapFixture(t *testing.T, store *fakeStore, gen googleplayscraper.Generation,
	shardIDs [][]string,
) *sitemapFixture {
	t.Helper()
	gen.Shards = len(shardIDs)
	f := &sitemapFixture{store: store, gen: gen, ids: shardIDs}

	for i, ids := range shardIDs {
		locs := make([]string, len(ids))
		for j, id := range ids {
			locs[j] = googleplayscraper.BaseURL + "/store/apps/details?id=" + id
		}
		store.set(f.shardPath(i), http.StatusOK, gzipTestBytes(t, urlsetTestXML(locs...)))
		f.urls = append(f.urls, googleplayscraper.BaseURL+f.shardPath(i))
	}
	f.publish(t)
	return f
}

// publish (re)writes robots.txt and the two indexes for this fixture's shards.
func (f *sitemapFixture) publish(t *testing.T) {
	t.Helper()
	half := (len(f.urls) + 1) / 2
	f.store.set("/robots.txt", http.StatusOK, robotsTestBody(
		googleplayscraper.BaseURL+indexPath0, googleplayscraper.BaseURL+indexPath1))
	f.store.set(indexPath0, http.StatusOK, indexTestXML(f.urls[:half]...))
	f.store.set(indexPath1, http.StatusOK, indexTestXML(f.urls[half:]...))
}

// shardPath is the filename Google publishes, which generationRe has to match:
// play_sitemaps_<date>_<run>-NNNNN-of-NNNNN.xml.gz.
func (f *sitemapFixture) shardPath(i int) string {
	return fmt.Sprintf("/sitemaps/play_sitemaps_%s_%s-%05d-of-%05d.xml.gz",
		f.gen.Date, f.gen.Run, i, len(f.ids))
}

// roll republishes the directory as a new generation. Every shard of the old
// one answers 404 afterwards, which is what a mid-sweep roll looks like from
// inside a run: routed explicitly, so the 404 is the fixture's answer rather
// than an unrouted request.
func (f *sitemapFixture) roll(t *testing.T, newGen googleplayscraper.Generation,
	shardIDs [][]string,
) *sitemapFixture {
	t.Helper()
	for i := range f.ids {
		f.store.set(f.shardPath(i), http.StatusNotFound, nil)
	}
	return newSitemapFixture(t, f.store, newGen, shardIDs)
}

// preFinished marks shards done exactly as an interrupted sweep would have:
// their ids appended to partial-<gen>.txt, their URLs in done-<gen>.log, and a
// state.json for the generation and sampling. loadState insists on both, so a
// state written with the wrong sampling makes the run start over -- which is
// itself a case worth planting.
func preFinished(t *testing.T, dir string, f *sitemapFixture, samp sampling, shards ...int) {
	t.Helper()
	var ids, done strings.Builder
	var n int
	for _, i := range shards {
		for _, id := range f.ids[i] {
			ids.WriteString(id + "\n")
			n++
		}
		done.WriteString(googleplayscraper.BaseURL + f.shardPath(i) + "\n")
	}
	writeFixtureFile(t, filepath.Join(dir, "partial-"+f.gen.ID()+".txt"), ids.String())
	writeFixtureFile(t, doneLogPath(dir, f.gen), done.String())
	if err := saveState(filepath.Join(dir, "state.json"), syncState{
		Generation: f.gen, IDs: n, SamplePct: samp.Pct, SampleSeed: samp.Seed,
	}); err != nil {
		t.Fatal(err)
	}
}

func writeFixtureFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ---- sitemap bodies ----
//
// Copied rather than imported: these live in the root package's _test.go files,
// which no other package can reach.

func gzipTestBytes(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func robotsTestBody(indexURLs ...string) []byte {
	var sb bytes.Buffer
	sb.WriteString("User-agent: *\nDisallow: /search\n")
	for _, u := range indexURLs {
		fmt.Fprintf(&sb, "Sitemap: %s\n", u)
	}
	return sb.Bytes()
}

func indexTestXML(shardURLs ...string) []byte {
	var sb bytes.Buffer
	sb.WriteString(`<?xml version='1.0' encoding='UTF-8'?><sitemapindex xmlns='http://www.sitemaps.org/schemas/sitemap/0.9'>`)
	for _, u := range shardURLs {
		fmt.Fprintf(&sb, "<sitemap><loc>%s</loc></sitemap>", u)
	}
	sb.WriteString(`</sitemapindex>`)
	return sb.Bytes()
}

func urlsetTestXML(locs ...string) []byte {
	var sb bytes.Buffer
	sb.WriteString(`<?xml version='1.0' encoding='UTF-8'?><urlset xmlns='http://www.sitemaps.org/schemas/sitemap/0.9'>`)
	for _, l := range locs {
		// Real sitemaps XML-escape & in query strings as &amp;.
		fmt.Fprintf(&sb, "<url><loc>%s</loc></url>", strings.ReplaceAll(l, "&", "&amp;"))
	}
	sb.WriteString(`</urlset>`)
	return sb.Bytes()
}

// ---- batchexecute ----

const pathBatch = "/_/PlayStoreUi/data/batchexecute"

// appRe pulls the app ids, in call order, out of a url-unescaped f.req body.
// ws7gdcRPC embeds [["<id>",7]] for digests too, so it reads a digest request
// as well as an app one.
var appRe = regexp.MustCompile(`\[\\"([^"\\]+)\\",7\]`)

// framesEnvelope renders one wrb.fr frame per index, served in `order`. Tests
// serve them reversed on purpose: Google answers in whatever order it
// finishes, and pairing by position rather than by the echoed index returns
// another app's data.
func framesEnvelope(rpcID string, byIndex map[string]string, order []string) []byte {
	var frames []string
	for _, idx := range order {
		payload, _ := json.Marshal(byIndex[idx])
		frames = append(frames,
			fmt.Sprintf(`["wrb.fr",%q,%s,null,null,null,%q]`, rpcID, payload, idx))
	}
	return []byte(")]}'\n\n[" + strings.Join(frames, ",") + "]")
}

// requestedIDs reads the app ids a batchexecute request asked for, in order.
func requestedIDs(t *testing.T, r *http.Request) []string {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	decoded, err := url.QueryUnescape(string(body))
	if err != nil {
		t.Fatalf("unescape f.req: %v", err)
	}
	var ids []string
	for _, m := range appRe.FindAllStringSubmatch(decoded, -1) {
		ids = append(ids, m[1])
	}
	return ids
}

// genrePayload is the sparse shape a digest request gets back: one field, so
// the app node is [{"80": ...}] rather than a dense 80-element array. That
// asymmetry is exactly what digestField exists to handle, so the fixture has
// to reproduce it rather than the dense form.
func genrePayload(genreID, display string) string {
	return fmt.Sprintf(`[null,[null,null,[{"80":[[[%q,null,%q]]]}]]]`, display, genreID)
}

// digestStub answers Ws7gDc digest requests per storefront and per app id.
type digestStub struct {
	t *testing.T
	// answer returns the payload for one id in one storefront and whether a
	// frame is served at all. A present frame with an empty payload is "the
	// store answered and has no listing"; no frame is "the answer never
	// arrived", which is not evidence about the app.
	answer func(gl, id string) (payload string, present bool)
	// fail500 names storefronts whose every request answers 500.
	fail500 map[string]bool

	mu    sync.Mutex
	asked map[string][][]string // gl -> one entry per request -> the ids it carried
}

func newDigestStub(t *testing.T, answer func(gl, id string) (string, bool)) *digestStub {
	return &digestStub{
		t:       t,
		answer:  answer,
		fail500: map[string]bool{},
		asked:   map[string][][]string{},
	}
}

func (s *digestStub) install(store *fakeStore) {
	store.setFunc(pathBatch, func(r *http.Request) (int, []byte) {
		gl := r.URL.Query().Get("gl")
		ids := requestedIDs(s.t, r)

		s.mu.Lock()
		s.asked[gl] = append(s.asked[gl], ids)
		fail := s.fail500[gl]
		s.mu.Unlock()

		if fail {
			return http.StatusInternalServerError, nil
		}

		byIndex := map[string]string{}
		var order []string
		for i, id := range ids {
			payload, present := s.answer(gl, id)
			if !present {
				continue // a dropped frame: the index is simply missing
			}
			idx := fmt.Sprint(i)
			byIndex[idx] = payload
			order = append(order, idx)
		}
		// Reversed, as the root tests serve them.
		for i, j := 0, len(order)-1; i < j; i, j = i+1, j-1 {
			order[i], order[j] = order[j], order[i]
		}
		return http.StatusOK, framesEnvelope("Ws7gDc", byIndex, order)
	})
}

// askedFor reports whether any request to gl carried id.
func (s *digestStub) askedFor(gl, id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, req := range s.asked[gl] {
		for _, got := range req {
			if got == id {
				return true
			}
		}
	}
	return false
}

func (s *digestStub) requests(gl string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.asked[gl])
}

// ---- capture ----

// captureStderr is captureStdout for the other stream. The sweep says what it
// is doing there -- "resuming", "retrying", why a delta was skipped -- and
// those sentences are the only report a cron job gets.
func captureStderr(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stderr
	os.Stderr = w

	done := make(chan string, 1)
	go func() {
		var b strings.Builder
		_, _ = io.Copy(&b, r)
		done <- b.String()
	}()

	f()

	os.Stderr = saved
	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out
}

// runVerb runs one command function with both streams captured. Both matter
// together: stdout carries the NDJSON contract and stderr carries the only
// explanation a cron job ever reads.
func runVerb(t *testing.T, run func([]string) error, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	stderr = captureStderr(t, func() {
		stdout = captureStdout(t, func() { err = run(args) })
	})
	return stdout, stderr, err
}

// assertNoTempFiles fails if a write left a .tmp file behind. Every writer in
// this package goes through a temporary file, and one that survives is a
// half-written snapshot or table sitting beside the good one.
func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	leftover, err := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range leftover {
		info, serr := os.Stat(path)
		if serr == nil && info.IsDir() {
			continue // a directory a test planted to make a write fail
		}
		t.Errorf("%s survived", filepath.Base(path))
	}
}

// jsonLines parses NDJSON output into generic records, failing on anything
// that is not one JSON object per line: that contract is what most of these
// tests are ultimately about.
func jsonLines(t *testing.T, out string) []map[string]any {
	t.Helper()
	var recs []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("stdout line is not a JSON object: %q (%v)", line, err)
		}
		recs = append(recs, rec)
	}
	return recs
}
