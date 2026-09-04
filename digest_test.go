package googleplayscraper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Payload shapes a digest can meet, all of them observed rather than invented.
// A lean request comes back sparse -- the field number is a map key inside a
// one-element array -- while a full one is dense and positional, and the same
// datum lives at [79] there. digestField exists for exactly that split.
const (
	sparseGenre = `[null,[null,null,[{"80":[[["Casual",null,"GAME_CASUAL"]]]}]]]`
	// echo mismatch: the response says it answered for another id
	sparseEchoOther = `[null,[null,null,[{"80":[[["Casual",null,"GAME_CASUAL"]]]}],{"12":[["com.other"]]}]]`
	sparseEchoSelf  = `[null,[null,null,[{"80":[[["Casual",null,"GAME_CASUAL"]]]}],{"12":[["com.x"]]}]]`
	mapNode         = `[null,[null,null,{"80":[[["Casual",null,"GAME_CASUAL"]]]}]]`
	sparseWrongKey  = `[null,[null,null,[{"79":[[["Casual",null,"GAME_CASUAL"]]]}]]]`
	noAppNode       = `[null,[null]]`
)

// denseGenre builds the positional form: an 80-element app node whose last
// element is the genre. Written in Go rather than as a literal because the
// literal is 79 nulls.
func denseGenre(t *testing.T) string {
	t.Helper()
	node := make([]any, 80)
	node[79] = []any{[]any{[]any{"Casual", nil, "GAME_CASUAL"}}}
	raw, err := json.Marshal([]any{nil, []any{nil, nil, node}})
	if err != nil {
		t.Fatalf("marshal dense node: %v", err)
	}
	return string(raw)
}

// digestIDRe pulls the app ids out of a url-unescaped f.req body, in call
// order. ws7gdcRPC embeds `[\"<id>\",7]` for a digest exactly as it does for a
// full lookup, so the same expression reads both.
var digestIDRe = regexp.MustCompile(`\[\\"([^"\\]+)\\",7\]`)

// captureBody reads a request body and puts it back, so the route that reads
// it for an assertion does not consume it for the route that answers.
func captureBody(req *http.Request) string {
	body, _ := io.ReadAll(req.Body)
	req.Body = io.NopCloser(bytes.NewReader(body))
	return string(body)
}

// digestRequest is one intercepted batch: the ids it asked for, in order.
func digestRequest(req *http.Request) []string {
	decoded, _ := url.QueryUnescape(strings.TrimPrefix(captureBody(req), "f.req="))
	var ids []string
	for _, m := range digestIDRe.FindAllStringSubmatch(decoded, -1) {
		ids = append(ids, m[1])
	}
	return ids
}

// digestRoute answers a DigestsSeq request id by id. answer returns the frame
// payload for an id and whether a frame is served at all; a nil answer serves
// the sparse genre payload for everything. Frames go back in reverse order, so
// pairing-by-index is exercised on every request.
func digestRoute(requests *atomic.Int64, answer func(id string) (string, bool)) routeFunc {
	return func(req *http.Request) (mockResponse, bool) {
		if req.URL.Path != pathBatch {
			return mockResponse{}, false
		}
		if requests != nil {
			requests.Add(1)
		}
		byIndex := map[string]string{}
		var order []string
		for i, id := range digestRequest(req) {
			payload, present := sparseGenre, true
			if answer != nil {
				payload, present = answer(id)
			}
			if !present {
				continue
			}
			byIndex[fmt.Sprint(i)] = payload
			order = append([]string{fmt.Sprint(i)}, order...)
		}
		return mockResponse{Body: framesEnvelope("Ws7gDc", byIndex, order)}, true
	}
}

// digestIDs builds n distinct app ids, in a form a test can rebuild by index.
func digestIDs(n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("com.app%03d", i)
	}
	return ids
}

// runDigests drains a sequence in a goroutine so a hang is a test failure
// rather than a hung suite. stop, when non-nil, is consulted after each digest
// and ends the loop when it returns true -- which is the case this whole file
// exists for.
func runDigests(t *testing.T, seq func(yield func(AppDigest, error) bool), stop func(int) bool) ([]AppDigest, []error) {
	t.Helper()
	var got []AppDigest
	var errs []error
	done := make(chan struct{})
	go func() {
		defer close(done)
		for d, err := range seq {
			if err != nil {
				errs = append(errs, err)
				continue
			}
			got = append(got, d)
			if stop != nil && stop(len(got)) {
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("DigestsSeq did not return: the consumer is parked on wg.Wait() " +
			"while the producer is parked on a send")
	}
	return got, errs
}

// The property the fix is about. On a break the consumer waits for the
// workers, the workers wait for the producer to close jobs, and the producer
// waits for room in ordered -- a cycle that only a cancellable context can
// break. Twenty abandoned passes are enough for a per-pass leak to show.
func TestDigestsSeqEarlyBreakLeavesNoGoroutines(t *testing.T) {
	settle()
	before := runtime.NumGoroutine()

	ids := digestIDs(5000)
	for range 20 {
		c := newMockClient(t, digestRoute(nil, nil))
		got, errs := runDigests(t, c.DigestsSeq(context.Background(), slices.Values(ids),
			DigestOptions{PackSize: 10, Concurrency: 4}),
			func(n int) bool { return n == 1 })
		if len(got) != 1 {
			t.Fatalf("stopped after %d digests, want 1", len(got))
		}
		if len(errs) != 0 {
			t.Fatalf("abandoning the pass reported %v", errs)
		}
	}

	settle()
	if after := runtime.NumGoroutine(); after > before+2 {
		t.Errorf("goroutines grew from %d to %d over 20 abandoned passes: "+
			"the producer and the workers are not released when the consumer breaks",
			before, after)
	}
}

// The sequence waits for its workers before returning, so "no leak" is a
// stronger statement than "the goroutines exit eventually": once the loop is
// over, no further request can be issued.
func TestDigestsSeqStopsRequestingAfterBreak(t *testing.T) {
	var requests atomic.Int64
	c := newMockClient(t, digestRoute(&requests, nil))

	got, _ := runDigests(t, c.DigestsSeq(context.Background(), slices.Values(digestIDs(500)),
		DigestOptions{PackSize: 10, Concurrency: 4}),
		func(n int) bool { return n == 1 })
	if len(got) != 1 {
		t.Fatalf("stopped after %d digests, want 1", len(got))
	}

	atReturn := requests.Load()
	settle()
	if after := requests.Load(); after != atReturn {
		t.Errorf("%d more requests were issued after the sequence returned (%d -> %d)",
			after-atReturn, atReturn, after)
	}
	if atReturn >= 50 {
		t.Errorf("made %d of the 50 requests before the break took effect", atReturn)
	}
}

// The cancellation the fix adds must not leak into the result: a pass that
// runs to the end is not a cancelled one, and reading ctx.Err() on the derived
// context after cancel() would say otherwise.
func TestDigestsSeqCompleteRunEndsWithoutError(t *testing.T) {
	c := newMockClient(t, digestRoute(nil, nil))

	got, errs := runDigests(t, c.DigestsSeq(context.Background(), slices.Values(digestIDs(25)),
		DigestOptions{PackSize: 10, Concurrency: 3}), nil)

	if len(errs) != 0 {
		t.Fatalf("a complete pass reported %v", errs)
	}
	if len(got) != 25 {
		t.Fatalf("got %d digests, want 25", len(got))
	}
	for i, d := range got {
		if d.AppID != fmt.Sprintf("com.app%03d", i) {
			t.Fatalf("digest %d is for %s", i, d.AppID)
		}
		if d.Err != nil || !d.Listed || d.GenreID != "GAME_CASUAL" {
			t.Fatalf("%s: %+v", d.AppID, d)
		}
	}
}

// Packs are answered concurrently and yielded in the order they were formed. A
// caller writing to a database wants a stable sequence, not whichever request
// finished first, so the ordering is asserted against a route that deliberately
// answers out of order.
func TestDigestsSeqPreservesPackOrderAcrossConcurrentWorkers(t *testing.T) {
	// The first request to arrive is held until a third distinct request has
	// come in. No sleeps: with four workers the third request is guaranteed to
	// be dispatched while the first is still held.
	var mu sync.Mutex
	var arrived int
	release := make(chan struct{})
	route := digestRoute(nil, nil)
	gated := func(req *http.Request) (mockResponse, bool) {
		if req.URL.Path != pathBatch {
			return mockResponse{}, false
		}
		mu.Lock()
		arrived++
		n := arrived
		mu.Unlock()
		if n == 3 {
			close(release)
		}
		resp, ok := route(req)
		if n == 1 {
			<-release
		}
		return resp, ok
	}

	ids := digestIDs(200)
	c := newMockClient(t, gated)
	got, errs := runDigests(t, c.DigestsSeq(context.Background(), slices.Values(ids),
		DigestOptions{PackSize: 10, Concurrency: 4}), nil)

	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(got) != len(ids) {
		t.Fatalf("got %d digests, want %d", len(got), len(ids))
	}
	for i, d := range got {
		if d.AppID != ids[i] {
			t.Fatalf("digest %d is %s, want %s: results are not in pack order", i, d.AppID, ids[i])
		}
	}
}

// Per-app failures ride in AppDigest.Err and the pass continues. This is the
// contract cmd/gpscrape's confirmGone rests on: an id that errored is not an
// id the store does not have.
func TestDigestsSeqPerAppErrorsDoNotEndTheSequence(t *testing.T) {
	var requests atomic.Int64
	c := newMockClient(t, func(req *http.Request) (mockResponse, bool) {
		if req.URL.Path != pathBatch {
			return mockResponse{}, false
		}
		switch requests.Add(1) {
		case 1: // the whole request fails
			return mockResponse{Status: http.StatusInternalServerError}, true
		case 3: // one frame of the pack never arrives
			return digestRoute(nil, func(id string) (string, bool) {
				return sparseGenre, id != "com.app005"
			})(req)
		default:
			return digestRoute(nil, nil)(req)
		}
	})

	got, errs := runDigests(t, c.DigestsSeq(context.Background(), slices.Values(digestIDs(6)),
		DigestOptions{PackSize: 2}), nil)

	if len(errs) != 0 {
		t.Fatalf("a per-app failure ended the sequence: %v", errs)
	}
	if len(got) != 6 {
		t.Fatalf("got %d digests, want all 6", len(got))
	}
	for _, i := range []int{0, 1} {
		if got[i].Err == nil {
			t.Errorf("%s: a failed request was reported as an answer", got[i].AppID)
		}
		if got[i].Listed {
			t.Errorf("%s: Listed is true although nothing was learned", got[i].AppID)
		}
	}
	for _, i := range []int{2, 3, 4} {
		if got[i].Err != nil || !got[i].Listed || got[i].GenreID != "GAME_CASUAL" {
			t.Errorf("%s: %+v, want a listed genre", got[i].AppID, got[i])
		}
	}
	if got[5].Err == nil || !strings.Contains(got[5].Err.Error(), "no frame returned") {
		t.Errorf("com.app005: Err = %v, want a missing-frame error", got[5].Err)
	}
	if got[5].Listed {
		t.Error("com.app005: a dropped frame was read as an app that is not listed")
	}
}

// Absence is the number a catalog pass acts on -- it is what makes a row
// "gone" -- so it must count only ids the store answered for.
func TestDigestsSeqDroppedFrameIsNotAbsent(t *testing.T) {
	ids := []string{"com.a", "com.b", "com.c"}

	for _, tc := range []struct {
		name       string
		answer     func(id string) (string, bool)
		wantErr    bool
		wantAbsent int
	}{
		{
			name:       "a frame that never arrived",
			answer:     func(id string) (string, bool) { return sparseGenre, id != "com.b" },
			wantErr:    true,
			wantAbsent: 0,
		},
		{
			name: "a frame with a null payload",
			answer: func(id string) (string, bool) {
				return map[bool]string{true: "", false: sparseGenre}[id == "com.b"], true
			},
			wantErr:    false,
			wantAbsent: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var last DigestProgress
			c := newMockClient(t, digestRoute(nil, tc.answer))
			got, errs := runDigests(t, c.DigestsSeq(context.Background(), slices.Values(ids),
				DigestOptions{PackSize: 3, Progress: func(p DigestProgress) { last = p }}), nil)

			if len(errs) != 0 {
				t.Fatalf("unexpected errors: %v", errs)
			}
			if len(got) != 3 {
				t.Fatalf("got %d digests, want 3", len(got))
			}
			if (got[1].Err != nil) != tc.wantErr {
				t.Errorf("com.b: Err = %v, want error: %v", got[1].Err, tc.wantErr)
			}
			if got[1].Listed {
				t.Error("com.b: Listed is true")
			}
			if last.Absent != tc.wantAbsent {
				t.Errorf("Progress.Absent = %d, want %d", last.Absent, tc.wantAbsent)
			}
			if last.Requests != 1 || last.Apps != 3 {
				t.Errorf("Progress = %+v, want 1 request over 3 apps", last)
			}
		})
	}
}

// A cancelled parent is terminal and belongs in the sequence's own error slot,
// which is the one thing that slot carries.
func TestDigestsSeqParentCancelIsTerminal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var once sync.Once
	route := digestRoute(nil, nil)
	c := newMockClient(t, func(req *http.Request) (mockResponse, bool) {
		resp, ok := route(req)
		once.Do(cancel)
		return resp, ok
	})

	_, errs := runDigests(t, c.DigestsSeq(ctx, slices.Values(digestIDs(50)),
		DigestOptions{PackSize: 10, Concurrency: 2}), nil)

	if len(errs) != 1 {
		t.Fatalf("got %d errors, want exactly 1: %v", len(errs), errs)
	}
	if !errors.Is(errs[0], context.Canceled) {
		t.Errorf("terminal error = %v, want context.Canceled", errs[0])
	}
}

// A blank id is rejected before the request rather than spending a slot in it,
// so the batch behaves the way the singular call documents.
func TestDigestsSeqRejectsEmptyIDWithoutSpendingASlot(t *testing.T) {
	var asked [][]string
	var mu sync.Mutex
	route := digestRoute(nil, nil)
	c := newMockClient(t, func(req *http.Request) (mockResponse, bool) {
		if req.URL.Path == pathBatch {
			mu.Lock()
			asked = append(asked, digestRequest(req))
			mu.Unlock()
		}
		return route(req)
	})

	got, errs := runDigests(t, c.DigestsSeq(context.Background(),
		slices.Values([]string{"com.a", "", "com.b"}), DigestOptions{PackSize: 3}), nil)

	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(got) != 3 {
		t.Fatalf("got %d digests, want 3", len(got))
	}
	if got[1].Err == nil || !strings.Contains(got[1].Err.Error(), "appID is required") {
		t.Errorf("the empty id reported %v", got[1].Err)
	}
	if len(asked) != 1 {
		t.Fatalf("made %d requests, want 1", len(asked))
	}
	if !slices.Equal(asked[0], []string{"com.a", "com.b"}) {
		t.Errorf("the request carried %v, want the two real ids only", asked[0])
	}
}

// The selector is the whole reason a digest is cheap: 178-243 bytes an app
// against 15,880. Asking for all 49 fields here would keep every test passing
// and quietly undo the point of the file.
func TestDigestsSeqAsksForOnlyTheSelectedFields(t *testing.T) {
	var body string
	var mu sync.Mutex
	route := digestRoute(nil, nil)
	c := newMockClient(t, func(req *http.Request) (mockResponse, bool) {
		if req.URL.Path == pathBatch {
			mu.Lock()
			body = captureBody(req)
			mu.Unlock()
		}
		return route(req)
	})

	got, errs := runDigests(t, c.DigestsSeq(context.Background(), slices.Values([]string{"com.a"}),
		DigestOptions{}), nil)
	if len(errs) != 0 || len(got) != 1 {
		t.Fatalf("got %d digests, errors %v", len(got), errs)
	}
	if got[0].Genre != "Casual" || got[0].GenreID != "GAME_CASUAL" {
		t.Errorf("zero Fields did not default to DigestGenre: %+v", got[0])
	}

	decoded, err := url.QueryUnescape(strings.TrimPrefix(body, "f.req="))
	if err != nil {
		t.Fatalf("unescape: %v", err)
	}
	if !strings.Contains(decoded, `[null,null,[[80]],`) {
		t.Errorf("the request does not carry the lean selector:\n%s", decoded)
	}
	if strings.Contains(decoded, `[[1,9,10`) {
		t.Error("the request carries the 49-field selector a full lookup uses")
	}
}

func TestDigestsSeqPackSizeOverride(t *testing.T) {
	var requests atomic.Int64
	c := newMockClient(t, digestRoute(&requests, nil))

	got, errs := runDigests(t, c.DigestsSeq(context.Background(), slices.Values(digestIDs(5)),
		DigestOptions{PackSize: 2}), nil)

	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if n := requests.Load(); n != 3 {
		t.Errorf("made %d requests for 5 ids at PackSize 2, want 3", n)
	}
	for i, d := range got {
		if want := fmt.Sprintf("com.app%03d", i); d.AppID != want {
			t.Errorf("digest %d is %s, want %s", i, d.AppID, want)
		}
	}
}

func TestDigestsSeqDefaultsLangAndCountry(t *testing.T) {
	for _, tc := range []struct {
		name           string
		opts           DigestOptions
		wantHL, wantGL string
	}{
		{name: "defaults", opts: DigestOptions{}, wantHL: "en", wantGL: "us"},
		{name: "explicit", opts: DigestOptions{Lang: "de", Country: "at"}, wantHL: "de", wantGL: "at"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var query url.Values
			var mu sync.Mutex
			route := digestRoute(nil, nil)
			c := newMockClient(t, func(req *http.Request) (mockResponse, bool) {
				if req.URL.Path == pathBatch {
					mu.Lock()
					query = req.URL.Query()
					mu.Unlock()
				}
				return route(req)
			})

			if _, errs := runDigests(t, c.DigestsSeq(context.Background(),
				slices.Values([]string{"com.a"}), tc.opts), nil); len(errs) != 0 {
				t.Fatalf("unexpected errors: %v", errs)
			}
			if query.Get("hl") != tc.wantHL || query.Get("gl") != tc.wantGL {
				t.Errorf("asked hl=%s gl=%s, want hl=%s gl=%s",
					query.Get("hl"), query.Get("gl"), tc.wantHL, tc.wantGL)
			}
		})
	}
}

// The pack size is derived from a byte budget, not guessed, and a selector
// nobody costed must not silently become a pack of thousands.
func TestDigestPackSizeAndFieldNumbers(t *testing.T) {
	if got := digestPackSize(DigestGenre); got != 1024 {
		t.Errorf("digestPackSize(DigestGenre) = %d, want 1024", got)
	}
	if got := digestPackSize(0); got != 1 {
		t.Errorf("digestPackSize(0) = %d, want 1: an empty selector has no budget to spend", got)
	}
	for mask := DigestFields(0); mask <= DigestGenre; mask++ {
		if got := digestPackSize(mask); got > 2048 {
			t.Errorf("digestPackSize(%d) = %d, above the 2048 ceiling", mask, got)
		}
	}
	if got := digestFieldNumbers(DigestGenre); !slices.Equal(got, []int{80}) {
		t.Errorf("digestFieldNumbers(DigestGenre) = %v, want [80]", got)
	}
	if got := digestFieldNumbers(0); len(got) != 0 {
		t.Errorf("digestFieldNumbers(0) = %v, want empty", got)
	}
}

func TestParseDigestShapes(t *testing.T) {
	for _, tc := range []struct {
		name      string
		payload   string
		wantGenre string
		wantID    string
		wantErr   string
	}{
		{name: "sparse", payload: sparseGenre, wantGenre: "Casual", wantID: "GAME_CASUAL"},
		{name: "dense", payload: denseGenre(t), wantGenre: "Casual", wantID: "GAME_CASUAL"},
		{name: "bare map node", payload: mapNode, wantGenre: "Casual", wantID: "GAME_CASUAL"},
		{name: "echo matches the id asked for", payload: sparseEchoSelf, wantGenre: "Casual", wantID: "GAME_CASUAL"},
		{name: "the field asked for is absent", payload: sparseWrongKey},
		{name: "no app node", payload: noAppNode, wantErr: "no app node"},
		{name: "not JSON", payload: `{`, wantErr: "parse com.x"},
		{name: "answer for another app", payload: sparseEchoOther, wantErr: "com.other"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseDigest(tc.payload, "com.x", []int{80})
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("parseDigest = %+v, want an error naming %q", got, tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error %q does not mention %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseDigest: %v", err)
			}
			if got.AppID != "com.x" || !got.Listed {
				t.Errorf("got %+v, want com.x listed", got)
			}
			if got.Genre != tc.wantGenre || got.GenreID != tc.wantID {
				t.Errorf("got genre %q/%q, want %q/%q", got.Genre, got.GenreID, tc.wantGenre, tc.wantID)
			}
		})
	}

	// An answer for another app must not be reported with the asked-for id
	// attached: that is the one failure mode a batch has and a single call
	// does not.
	if _, err := parseDigest(sparseEchoOther, "com.x", []int{80}); err == nil ||
		!strings.Contains(err.Error(), "com.x") {
		t.Errorf("the mispairing error does not name the slot it landed in: %v", err)
	}
}

// getPath cannot do this job: handed a map it looks the index up as a string,
// so asking it for 79 on a sparse node reads field 79 -- a different field,
// silently, with a plausible-looking value.
func TestDigestFieldReadsSparseAndDensePositions(t *testing.T) {
	sparse := []any{map[string]any{"80": "v"}}
	if got := digestField(sparse, 80); got != "v" {
		t.Errorf("sparse field 80 = %v, want v", got)
	}
	if got := digestField(sparse, 79); got != nil {
		t.Errorf("sparse field 79 = %v, want nil: it must not fall through to the positional branch", got)
	}

	dense := make([]any, 80)
	dense[78] = "at-79"
	dense[79] = "at-80"
	if got := digestField(dense, 80); got != "at-80" {
		t.Errorf("dense field 80 = %v, want at-80", got)
	}
	if got := digestField(dense, 79); got != "at-79" {
		t.Errorf("dense field 79 = %v, want at-79", got)
	}
	if got := digestField(dense, 81); got != nil {
		t.Errorf("dense field 81 = %v, want nil (past the end)", got)
	}

	if got := digestField(map[string]any{"80": "v"}, 80); got != "v" {
		t.Errorf("map node field 80 = %v, want v", got)
	}
	if got := digestField("not a node", 80); got != nil {
		t.Errorf("a string node yielded %v, want nil", got)
	}
}

// The echo is the only thing that can catch a frame paired with the wrong
// call, so every shape that is not a package id has to read as "no echo"
// rather than as a mismatch.
func TestDigestEcho(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
		want    string
	}{
		{"no meta node", `[null,[null,null,[]]]`, ""},
		{"meta without key 12", `[null,[null,null,[],{"11":[["x"]]}]]`, ""},
		{"key 12 empty", `[null,[null,null,[],{"12":[]}]]`, ""},
		{"key 12 holds an empty pair", `[null,[null,null,[],{"12":[[]]}]]`, ""},
		{"key 12 holds a number", `[null,[null,null,[],{"12":[[123]]}]]`, ""},
		{"key 12 holds the id", `[null,[null,null,[],{"12":[["com.x"]]}]]`, "com.x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var ds5 any
			if err := json.Unmarshal([]byte(tc.payload), &ds5); err != nil {
				t.Fatalf("fixture is not JSON: %v", err)
			}
			if got := digestEcho(ds5); got != tc.want {
				t.Errorf("digestEcho = %q, want %q", got, tc.want)
			}
		})
	}
}
