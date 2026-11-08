package googleplayscraper

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"testing"
	"time"
)

// Evidence for live tests that fail.
//
// A live test that flakes leaves a line like "got 20 results; more than one
// page should come back" and nothing else: the response that caused it is gone
// by the time anyone reads the log, and rerunning it asks Google a different
// question. A client built with Evidence keeps the last few responses it saw
// -- URL, status, headers, the first 4 KB of the body, when -- and the hook
// events around them (attempt, latency, retries, a soft block the pipeline
// recognised), and a watched test that fails writes them to
// $GPSCRAPE_EVIDENCE_DIR, or to a fresh temporary directory whose path the
// test logs. The canary workflow uploads that directory when it goes red.
//
// Exported from a _test.go file so the external test package (the canary and
// integration_test.go) can use it; it is not part of the library.

const (
	evidenceKeep = 20      // responses kept, and twice as many hook events
	evidenceBody = 4 << 10 // body bytes kept per response
)

// Evidence records what one client sent and got back.
type Evidence struct {
	mu        sync.Mutex
	seq       int
	exchanges []EvidenceExchange
	events    []EvidenceEvent
}

// EvidenceExchange is one HTTP round trip as the transport saw it.
type EvidenceExchange struct {
	Seq       int         `json:"seq"`
	Time      time.Time   `json:"time"`
	Method    string      `json:"method"`
	URL       string      `json:"url"`
	Status    int         `json:"status"`
	Header    http.Header `json:"header,omitempty"`
	Body      string      `json:"body,omitempty"`
	BodyBytes int         `json:"body_bytes"`
	Took      string      `json:"took"`
	Err       string      `json:"error,omitempty"`
}

// EvidenceEvent is one client hook call.
type EvidenceEvent struct {
	Seq     int       `json:"seq"`
	Time    time.Time `json:"time"`
	Hook    string    `json:"hook"`
	Method  string    `json:"method"`
	URL     string    `json:"url"`
	Attempt int       `json:"attempt"`
	Status  int       `json:"status,omitempty"`
	Took    string    `json:"took,omitempty"`
	Delay   string    `json:"delay,omitempty"`
	Err     string    `json:"error,omitempty"`
}

// NewEvidence returns an empty recorder.
func NewEvidence() *Evidence { return &Evidence{} }

// Options are the client options that record into e. They replace the HTTP
// client and the hooks, so pass them before any option that sets either.
func (e *Evidence) Options() []ClientOption {
	return []ClientOption{
		WithHTTPClient(&http.Client{Timeout: 30 * time.Second, Transport: evidenceTransport{e, http.DefaultTransport}}),
		WithHooks(Hooks{
			OnRequest: func(method, url string, attempt int) {
				e.event(EvidenceEvent{Hook: "request", Method: method, URL: url, Attempt: attempt})
			},
			OnResponse: func(method, url string, attempt, status int, took time.Duration, err error) {
				e.event(EvidenceEvent{Hook: "response", Method: method, URL: url, Attempt: attempt,
					Status: status, Took: took.String(), Err: errString(err)})
			},
			OnRetry: func(method, url string, attempt, status int, delay time.Duration, err error) {
				e.event(EvidenceEvent{Hook: "retry", Method: method, URL: url, Attempt: attempt,
					Status: status, Delay: delay.String(), Err: errString(err)})
			},
		}),
	}
}

// LiveClient is NewClient for a live test: a failure leaves its evidence.
func LiveClient(t testing.TB, opts ...ClientOption) *Client {
	e := NewEvidence()
	e.Watch(t)
	return NewClient(append(e.Options(), opts...)...)
}

// ParseSearchPage is parseSearchPage for the external test package, which
// checks the canary's history oracle on the frozen search fixture.
func ParseSearchPage(page []byte) ([]SearchResult, error) {
	rs, _, err := parseSearchPage(page)
	return rs, err
}

// liveClient is LiveClient for this package's own live tests.
func liveClient(t testing.TB, opts ...ClientOption) *Client { return LiveClient(t, opts...) }

// Watch makes t write what the client saw from now on if t fails.
func (e *Evidence) Watch(t testing.TB) {
	t.Helper()
	since := e.Seq()
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		path, err := e.Write(evidenceDir(t), t.Name(), since)
		if err != nil {
			t.Logf("evidence: %v", err)
			return
		}
		t.Logf("evidence: the last responses are in %s", path)
	})
}

// Write saves the exchanges and events after seq since to dir/<name>.json.
func (e *Evidence) Write(dir, name string, since int) (string, error) {
	e.mu.Lock()
	doc := struct {
		Test      string             `json:"test"`
		Written   time.Time          `json:"written"`
		Exchanges []EvidenceExchange `json:"exchanges"`
		Events    []EvidenceEvent    `json:"events"`
	}{Test: name, Written: time.Now().UTC()}
	for _, x := range e.exchanges {
		if x.Seq > since {
			doc.Exchanges = append(doc.Exchanges, x)
		}
	}
	for _, ev := range e.events {
		if ev.Seq > since {
			doc.Events = append(doc.Events, ev)
		}
	}
	e.mu.Unlock()

	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, unsafeName.ReplaceAllString(name, "_")+".json")
	return path, os.WriteFile(path, b, 0o644)
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

var (
	evidenceTmp     string
	evidenceTmpOnce sync.Once
)

// evidenceDir is $GPSCRAPE_EVIDENCE_DIR, or one temporary directory per test
// process. Not t.TempDir: that is deleted when the test ends, which is exactly
// when the evidence starts being needed.
func evidenceDir(t testing.TB) string {
	if d := os.Getenv("GPSCRAPE_EVIDENCE_DIR"); d != "" {
		return d
	}
	evidenceTmpOnce.Do(func() {
		d, err := os.MkdirTemp("", "gpscrape-evidence-")
		if err != nil {
			t.Logf("evidence: %v", err)
			d = os.TempDir()
		}
		evidenceTmp = d
	})
	return evidenceTmp
}

func (e *Evidence) event(ev EvidenceEvent) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.seq++
	ev.Seq, ev.Time = e.seq, time.Now().UTC()
	e.events = append(e.events, ev)
	if n := len(e.events) - 2*evidenceKeep; n > 0 {
		e.events = e.events[n:]
	}
}

func (e *Evidence) exchange(x EvidenceExchange) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.seq++
	x.Seq = e.seq
	e.exchanges = append(e.exchanges, x)
	if n := len(e.exchanges) - evidenceKeep; n > 0 {
		e.exchanges = e.exchanges[n:]
	}
}

// Seq is the number of the last thing recorded; pass it to EventsSince or
// Write to take only what comes after.
func (e *Evidence) Seq() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.seq
}

// EventsSince returns the hook events still held that came after seq, oldest
// first.
func (e *Evidence) EventsSince(seq int) []EvidenceEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []EvidenceEvent
	for _, ev := range e.events {
		if ev.Seq > seq {
			out = append(out, ev)
		}
	}
	return out
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// evidenceTransport records each round trip. The body is read through once
// and handed on unchanged; only its first evidenceBody bytes are kept.
type evidenceTransport struct {
	e    *Evidence
	next http.RoundTripper
}

func (rt evidenceTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	x := EvidenceExchange{Time: start.UTC(), Method: req.Method, URL: req.URL.String()}
	resp, err := rt.next.RoundTrip(req)
	if err != nil {
		x.Took, x.Err = time.Since(start).String(), err.Error()
		rt.e.exchange(x)
		return nil, err
	}
	body, rerr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	x.Status, x.Header, x.BodyBytes = resp.StatusCode, resp.Header.Clone(), len(body)
	x.Body = string(body[:min(len(body), evidenceBody)])
	x.Took = time.Since(start).String()
	var rest io.Reader = bytes.NewReader(nil)
	if rerr != nil {
		// The client reads the same failure it would have read unwatched.
		x.Err, rest = rerr.Error(), errReader{rerr}
	}
	rt.e.exchange(x)
	resp.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), rest))
	return resp, nil
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

// TestEvidenceKeepsTheLastResponses checks the recorder offline: the body
// reaches the caller whole, the record keeps its head, and Write keeps only
// what came after the watch began.
func TestEvidenceKeepsTheLastResponses(t *testing.T) {
	big := bytes.Repeat([]byte("x"), evidenceBody+100)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write(big)
	}))
	defer srv.Close()

	e := NewEvidence()
	hc := &http.Client{Transport: evidenceTransport{e, http.DefaultTransport}}
	get := func() {
		resp, err := hc.Get(srv.URL + "/page?q=1")
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if !bytes.Equal(b, big) {
			t.Errorf("the caller got %d bytes, want %d", len(b), len(big))
		}
	}
	get()
	since := e.Seq()
	for range evidenceKeep + 3 {
		get()
	}

	path, err := e.Write(t.TempDir(), "TestX/sub test", since)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "TestX_sub_test.json" {
		t.Errorf("file %s", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Exchanges []EvidenceExchange `json:"exchanges"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Exchanges) != evidenceKeep {
		t.Fatalf("%d exchanges written, want the last %d", len(doc.Exchanges), evidenceKeep)
	}
	x := doc.Exchanges[0]
	if x.Seq <= since || x.Status != 429 || x.Header.Get("Retry-After") != "7" ||
		len(x.Body) != evidenceBody || x.BodyBytes != len(big) || x.URL != srv.URL+"/page?q=1" || x.Time.IsZero() {
		t.Errorf("exchange %+v", x)
	}
}
