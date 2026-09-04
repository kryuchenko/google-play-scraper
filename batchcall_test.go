package googleplayscraper

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// framesEnvelope renders a batchexecute response carrying one wrb.fr frame per
// entry of order, each taking its payload from byIndex. order is explicit so a
// test can serve the frames in an order the caller did not ask for, which is
// what the live endpoint does.
func framesEnvelope(rpcID string, byIndex map[string]string, order []string) []byte {
	var frames []string
	for _, idx := range order {
		payload, _ := json.Marshal(byIndex[idx])
		frames = append(frames,
			fmt.Sprintf(`["wrb.fr",%q,%s,null,null,null,%q]`, rpcID, payload, idx))
	}
	return []byte(")]}'\n\n[" + strings.Join(frames, ",") + "]")
}

// The property the whole batching design rests on: Google answers in whatever
// order it finishes, so answers must be paired to calls by the echoed index.
// Pairing them positionally returns another app's data, which is why this test
// serves the frames backwards.
func TestBatchCallPairsAnswersByIndexNotPosition(t *testing.T) {
	payloads := map[string]string{
		"0": `["answer-for-call-0"]`,
		"1": `["answer-for-call-1"]`,
		"2": `["answer-for-call-2"]`,
	}
	c := newMockClient(t, func(req *http.Request) (mockResponse, bool) {
		if req.URL.Path != pathBatch {
			return mockResponse{}, false
		}
		// Deliberately reversed.
		return mockResponse{Body: framesEnvelope("xdSrCf", payloads, []string{"2", "0", "1"})}, true
	})

	calls := []rpcCall{
		{id: "xdSrCf", payload: `[0]`},
		{id: "xdSrCf", payload: `[1]`},
		{id: "xdSrCf", payload: `[2]`},
	}
	got, err := c.batchCall(context.Background(), "en", "us", calls)
	if err != nil {
		t.Fatalf("batchCall: %v", err)
	}
	for i := range calls {
		want := payloads[fmt.Sprint(i)]
		if got[i] != want {
			t.Errorf("call %d got %q, want %q -- answers paired by position, not index", i, got[i], want)
		}
	}
}

// A single call carries no index ambiguity, and recorded fixtures were captured
// when the code sent "1" or "generic". Matching one call to one frame must not
// depend on the index agreeing with today's numbering.
func TestBatchCallSingleCallIgnoresTheEchoedIndex(t *testing.T) {
	for _, idx := range []string{"0", "1", "generic"} {
		c := newMockClient(t, func(req *http.Request) (mockResponse, bool) {
			if req.URL.Path != pathBatch {
				return mockResponse{}, false
			}
			return mockResponse{Body: framesEnvelope("xdSrCf",
				map[string]string{idx: `["only-answer"]`}, []string{idx})}, true
		})
		got, err := c.batchCall(context.Background(), "en", "us",
			[]rpcCall{{id: "xdSrCf", payload: `[0]`}})
		if err != nil {
			t.Fatalf("index %q: batchCall: %v", idx, err)
		}
		if got[0] != `["only-answer"]` {
			t.Errorf("index %q: got %q, want the single frame", idx, got[0])
		}
	}
}

// An RPC that produced no frame must come back empty rather than shifting every
// later answer up by one.
func TestBatchCallMissingFrameLeavesAHole(t *testing.T) {
	c := newMockClient(t, func(req *http.Request) (mockResponse, bool) {
		if req.URL.Path != pathBatch {
			return mockResponse{}, false
		}
		return mockResponse{Body: framesEnvelope("xdSrCf",
			map[string]string{"0": `["a"]`, "2": `["c"]`}, []string{"0", "2"})}, true
	})

	got, err := c.batchCall(context.Background(), "en", "us", []rpcCall{
		{id: "xdSrCf", payload: `[0]`},
		{id: "xdSrCf", payload: `[1]`},
		{id: "xdSrCf", payload: `[2]`},
	})
	if err != nil {
		t.Fatalf("batchCall: %v", err)
	}
	want := []string{`["a"]`, "", `["c"]`}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// buildFReq must produce the bytes Google's endpoint already accepts. The
// hand-written literal here is the body the previous single-app implementation
// sent, decoded: if the builder drifts from it, live requests break.
func TestBuildFReqMatchesTheHandWrittenBody(t *testing.T) {
	got := buildFReq([]rpcCall{permissionsRPC("com.spotify.music")})
	want := `[[["xdSrCf","[[null,[\"com.spotify.music\",7],[]]]",null,"0"]]]`
	if got != want {
		t.Errorf("buildFReq =\n  %s\nwant\n  %s", got, want)
	}
}

// The rpcids query parameter lists each distinct RPC once, and calls of
// different kinds may share one request.
func TestBatchCallDeclaresDistinctRPCIDs(t *testing.T) {
	var gotIDs, gotBody string
	c := newMockClient(t, func(req *http.Request) (mockResponse, bool) {
		if req.URL.Path != pathBatch {
			return mockResponse{}, false
		}
		gotIDs = req.URL.Query().Get("rpcids")
		b, _ := io.ReadAll(req.Body)
		gotBody, _ = url.QueryUnescape(strings.TrimPrefix(string(b), "f.req="))
		return mockResponse{Body: framesEnvelope("xdSrCf",
			map[string]string{"0": `[]`, "1": `[]`, "2": `[]`}, []string{"0", "1", "2"})}, true
	})

	_, err := c.batchCall(context.Background(), "en", "us", []rpcCall{
		{id: "xdSrCf", payload: `[0]`},
		{id: "IJ4APc", payload: `[1]`},
		{id: "xdSrCf", payload: `[2]`},
	})
	if err != nil {
		t.Fatalf("batchCall: %v", err)
	}
	if gotIDs != "xdSrCf,IJ4APc" {
		t.Errorf("rpcids = %q, want each distinct id once in first-use order", gotIDs)
	}
	if n := strings.Count(gotBody, `"xdSrCf"`) + strings.Count(gotBody, `"IJ4APc"`); n != 3 {
		t.Errorf("body carried %d call tuples, want 3: %s", n, gotBody)
	}
}

func TestChunked(t *testing.T) {
	got := chunked([]int{1, 2, 3, 4, 5}, 2)
	if len(got) != 3 || len(got[0]) != 2 || len(got[2]) != 1 {
		t.Fatalf("chunked(5, 2) = %v, want runs of 2,2,1", got)
	}
	if chunked([]int{}, 2) != nil {
		t.Error("chunked of nothing should be nil")
	}
	if n := len(chunked(make([]int, 100), 0)); n != 4 {
		t.Errorf("chunked with size 0 made %d runs, want 4 at the default pack size", n)
	}
}

// A wrb.fr frame of exactly three elements is the shortest valid one: the tag,
// the rpcid, and the payload are all the decoders read. Both guards are written
// as `len(frame) < 3` so that length passes, and every fixture and mock here
// happens to produce seven-element frames, which left the boundary untested --
// mutation testing turned `< 3` into `<= 3` in both decoders and nothing
// failed. A three-element frame must decode, not be skipped.
func TestDecodersAcceptTheShortestValidFrame(t *testing.T) {
	body := []byte(")]}'\n\n[[\"wrb.fr\",\"xdSrCf\",\"[1,2,3]\"]]")

	t.Run("decodeBatchEnvelope", func(t *testing.T) {
		got, err := decodeBatchEnvelope(body)
		if err != nil {
			t.Fatalf("decodeBatchEnvelope: %v", err)
		}
		if len(got) != 3 {
			t.Errorf("payload = %v, want the three-element array decoded", got)
		}
	})

	t.Run("decodeBatchFrames", func(t *testing.T) {
		frames, err := decodeBatchFrames(body)
		if err != nil {
			t.Fatalf("decodeBatchFrames: %v", err)
		}
		// No index in a short frame: the only call is call zero.
		if got, ok := frames["0"]; !ok || got != "[1,2,3]" {
			t.Errorf("frames = %v, want the payload under index 0", frames)
		}
	})

	t.Run("batchCall", func(t *testing.T) {
		c := newMockClient(t, func(req *http.Request) (mockResponse, bool) {
			if req.URL.Path != pathBatch {
				return mockResponse{}, false
			}
			return mockResponse{Body: body}, true
		})
		got, err := c.batchCall(context.Background(), "en", "us",
			[]rpcCall{{id: "xdSrCf", payload: `[0]`}})
		if err != nil {
			t.Fatalf("batchCall: %v", err)
		}
		if got[0] != "[1,2,3]" {
			t.Errorf("payload = %q, want [1,2,3]", got[0])
		}
	})
}

// An empty entry in a batched call must be rejected in its own slot without
// spending a request, and without shifting anyone else's answer. The singular
// forms validate up front; the batched ones used to send the blank id and
// report "no data returned for " against a wasted RPC slot.
func TestBatchedFormsRejectEmptyEntriesWithoutSpendingASlot(t *testing.T) {
	appRe := regexp.MustCompile(`\[\\"([^"\\]+)\\",7\]`)

	t.Run("AppsMany", func(t *testing.T) {
		var sentIDs []string
		var requests int
		c := newMockClient(t, func(req *http.Request) (mockResponse, bool) {
			if req.URL.Path != pathBatch {
				return mockResponse{}, false
			}
			requests++
			body, _ := io.ReadAll(req.Body)
			decoded, _ := url.QueryUnescape(strings.TrimPrefix(string(body), "f.req="))
			byIndex := map[string]string{}
			var order []string
			for i, m := range appRe.FindAllStringSubmatch(decoded, -1) {
				sentIDs = append(sentIDs, m[1])
				byIndex[fmt.Sprint(i)] = fmt.Sprintf(`[null,[null,null,[[%q]]]]`, "title-of-"+m[1])
				order = append([]string{fmt.Sprint(i)}, order...)
			}
			return mockResponse{Body: framesEnvelope("Ws7gDc", byIndex, order)}, true
		})

		got := c.AppsMany(context.Background(), []string{"com.a", "", "com.b"}, AppOptions{})
		if len(got) != 3 {
			t.Fatalf("got %d results, want one per input", len(got))
		}
		if got[1].Err == nil {
			t.Error("the empty id reported no error")
		}
		if slices.Contains(sentIDs, "") {
			t.Errorf("the empty id was sent to Google: %v", sentIDs)
		}
		// The neighbours must still be right: a filtered call must not shift
		// the answers of the calls that were made.
		for i, want := range map[int]string{0: "title-of-com.a", 2: "title-of-com.b"} {
			if got[i].Err != nil {
				t.Fatalf("slot %d: %v", i, got[i].Err)
			}
			if got[i].App.Title != want {
				t.Errorf("slot %d title = %q, want %q", i, got[i].App.Title, want)
			}
		}
	})

	t.Run("all_entries_empty_makes_no_request", func(t *testing.T) {
		var requests int
		c := newMockClient(t, func(req *http.Request) (mockResponse, bool) {
			if req.URL.Path != pathBatch {
				return mockResponse{}, false
			}
			requests++
			return mockResponse{Body: framesEnvelope("Ws7gDc", nil, nil)}, true
		})
		got := c.AppsMany(context.Background(), []string{"", ""}, AppOptions{})
		if requests != 0 {
			t.Errorf("made %d requests for nothing but empty ids", requests)
		}
		for i, r := range got {
			if r.Err == nil {
				t.Errorf("slot %d reported no error", i)
			}
		}
	})

	t.Run("SuggestMany", func(t *testing.T) {
		c := newMockClient(t, func(req *http.Request) (mockResponse, bool) {
			if req.URL.Path != pathBatch {
				return mockResponse{}, false
			}
			return mockResponse{Body: framesEnvelope("IJ4APc",
				map[string]string{"0": `[[[["only"]]]]`}, []string{"0"})}, true
		})
		got := c.SuggestMany(context.Background(), []string{"", "chess"}, SuggestOptions{})
		if got[0].Err == nil {
			t.Error("the empty term reported no error")
		}
		if got[1].Err != nil || len(got[1].Suggestions) != 1 {
			t.Errorf("the real term came back wrong: %+v", got[1])
		}
	})
}

// A missing app must be an error, not an empty permission list. An app may
// legitimately declare no permissions, so silence cannot mean both.
func TestPermissionsManyReportsAMissingApp(t *testing.T) {
	c := newMockClient(t, func(req *http.Request) (mockResponse, bool) {
		if req.URL.Path != pathBatch {
			return mockResponse{}, false
		}
		// Google answers a non-existent id with a present frame carrying a
		// null payload, which decodeBatchFrames renders as "".
		return mockResponse{Body: framesEnvelope("xdSrCf",
			map[string]string{"0": ""}, []string{"0"})}, true
	})

	got := c.PermissionsMany(context.Background(), []string{"com.nope"}, PermissionsOptions{})
	if got[0].Err == nil {
		t.Fatal("a missing app reported success with no permissions")
	}
	if !strings.Contains(got[0].Err.Error(), "com.nope") {
		t.Errorf("error does not name the app: %v", got[0].Err)
	}
}

// Error messages carry caller-supplied ids, and callers pass odd things.
func TestErrorsDoNotEchoEnormousIDs(t *testing.T) {
	huge := strings.Repeat("x", 4000)
	c := newMockClient(t, func(req *http.Request) (mockResponse, bool) {
		if req.URL.Path != pathBatch {
			return mockResponse{}, false
		}
		return mockResponse{Body: framesEnvelope("Ws7gDc",
			map[string]string{"0": ""}, []string{"0"})}, true
	})
	got := c.AppsMany(context.Background(), []string{huge}, AppOptions{})
	if got[0].Err == nil {
		t.Fatal("no error for a missing app")
	}
	if n := len(got[0].Err.Error()); n > 200 {
		t.Errorf("error message is %d bytes; ids must be trimmed", n)
	}
}

// suggestTermsInRequest reads back the terms one packed request carried, in the
// order they were sent, so a route can answer each of them by name and a test
// can tell which pack it is looking at.
func suggestTermsInRequest(t *testing.T, req *http.Request) []string {
	t.Helper()
	termRe := regexp.MustCompile(`\[\[null,\[\\"([^"\\]+)\\"\]`)
	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	decoded, err := url.QueryUnescape(strings.TrimPrefix(string(body), "f.req="))
	if err != nil {
		t.Fatalf("unescape f.req: %v", err)
	}
	var terms []string
	for _, m := range termRe.FindAllStringSubmatch(decoded, -1) {
		terms = append(terms, m[1])
	}
	return terms
}

// suggestAnswer renders a frame per term, one suggestion each, naming the term
// it answers for. The frames are served in reverse so a positional pairing is
// caught rather than tolerated -- the live endpoint answers in whatever order
// it finishes.
func suggestAnswer(terms []string) []byte {
	byIndex := make(map[string]string, len(terms))
	order := make([]string, 0, len(terms))
	for i, term := range terms {
		byIndex[fmt.Sprint(i)] = fmt.Sprintf(`[[[[%q]]]]`, "sug-for-"+term)
		order = append([]string{fmt.Sprint(i)}, order...)
	}
	return framesEnvelope("IJ4APc", byIndex, order)
}

// manyTerms builds enough terms to fill packs of maxRPCsPerRequest exactly.
func manyTerms(packs int) []string {
	terms := make([]string, packs*maxRPCsPerRequest)
	for i := range terms {
		terms[i] = fmt.Sprintf("term-%03d", i)
	}
	return terms
}

// The *Many methods pack their calls and then have to send those packs. Sending
// them one at a time made WithConcurrency a no-op for the three highest-volume
// batched operations there are, so this pins that the packs actually fan out --
// and, just as importantly, that fanning out did not cost the positional
// contract every caller reads results through.
func TestPackedFanOutHonoursConcurrency(t *testing.T) {
	const packs = 8

	t.Run("packs overlap", func(t *testing.T) {
		// Overlap is observed rather than timed: every request blocks until a
		// second one joins it, so a client that sends them one at a time cannot
		// pass by being fast. The timeout is what turns that into a readable
		// failure instead of a hung suite.
		var (
			mu               sync.Mutex
			inFlight, maxSaw int
			joined           = make(chan struct{})
			once             sync.Once
		)
		c := newMockClient(t, func(req *http.Request) (mockResponse, bool) {
			if req.URL.Path != pathBatch {
				return mockResponse{}, false
			}
			terms := suggestTermsInRequest(t, req)

			mu.Lock()
			inFlight++
			maxSaw = max(maxSaw, inFlight)
			enough := inFlight >= 2
			mu.Unlock()
			if enough {
				once.Do(func() { close(joined) })
			}
			select {
			case <-joined:
			case <-time.After(5 * time.Second):
				// Nobody is coming. Release everyone rather than paying the
				// timeout once per pack: the assertion below already knows.
				once.Do(func() { close(joined) })
			}
			mu.Lock()
			inFlight--
			mu.Unlock()

			return mockResponse{Body: suggestAnswer(terms)}, true
		})
		WithConcurrency(4)(c)

		c.SuggestMany(context.Background(), manyTerms(packs), SuggestOptions{})

		mu.Lock()
		defer mu.Unlock()
		if maxSaw < 2 {
			t.Errorf("at most %d request was ever in flight; the packs are still sent one at a time", maxSaw)
		}
	})

	t.Run("results stay positional", func(t *testing.T) {
		c := newMockClient(t, func(req *http.Request) (mockResponse, bool) {
			if req.URL.Path != pathBatch {
				return mockResponse{}, false
			}
			return mockResponse{Body: suggestAnswer(suggestTermsInRequest(t, req))}, true
		})
		WithConcurrency(4)(c)

		terms := manyTerms(packs)
		got := c.SuggestMany(context.Background(), terms, SuggestOptions{})

		if len(got) != len(terms) {
			t.Fatalf("got %d results, want one per term (%d)", len(got), len(terms))
		}
		for i, term := range terms {
			if got[i].Term != term {
				t.Fatalf("out[%d].Term = %q, want %q", i, got[i].Term, term)
			}
			if got[i].Err != nil {
				t.Fatalf("out[%d]: %v", i, got[i].Err)
			}
			want := "sug-for-" + term
			if len(got[i].Suggestions) != 1 || got[i].Suggestions[0] != want {
				t.Fatalf("out[%d] = %v, want [%q]: an answer landed in another term's slot",
					i, got[i].Suggestions, want)
			}
		}
	})

	t.Run("a failed pack costs only its own terms", func(t *testing.T) {
		c := newMockClient(t, func(req *http.Request) (mockResponse, bool) {
			if req.URL.Path != pathBatch {
				return mockResponse{}, false
			}
			terms := suggestTermsInRequest(t, req)
			// The pack holding the very first term is the one that fails.
			if slices.Contains(terms, "term-000") {
				return mockResponse{Status: 500}, true
			}
			return mockResponse{Body: suggestAnswer(terms)}, true
		})
		WithConcurrency(4)(c)

		terms := manyTerms(packs)
		got := c.SuggestMany(context.Background(), terms, SuggestOptions{})

		for i := range maxRPCsPerRequest {
			if got[i].Err == nil {
				t.Fatalf("out[%d] reports no error although its request failed", i)
			}
		}
		for i := maxRPCsPerRequest; i < len(terms); i++ {
			if got[i].Err != nil {
				t.Fatalf("out[%d]: %v -- one failed pack took the others with it", i, got[i].Err)
			}
		}
	})

	t.Run("concurrency 1 is sequential", func(t *testing.T) {
		var (
			mu               sync.Mutex
			inFlight, maxSaw int
			requests         int
		)
		c := newMockClient(t, func(req *http.Request) (mockResponse, bool) {
			if req.URL.Path != pathBatch {
				return mockResponse{}, false
			}
			terms := suggestTermsInRequest(t, req)
			mu.Lock()
			requests++
			inFlight++
			maxSaw = max(maxSaw, inFlight)
			mu.Unlock()
			// Long enough that a second worker would be seen if there were one.
			time.Sleep(time.Millisecond)
			mu.Lock()
			inFlight--
			mu.Unlock()
			return mockResponse{Body: suggestAnswer(terms)}, true
		})
		WithConcurrency(1)(c)

		c.SuggestMany(context.Background(), manyTerms(packs), SuggestOptions{})

		mu.Lock()
		defer mu.Unlock()
		if maxSaw != 1 {
			t.Errorf("%d requests were in flight at once at concurrency 1", maxSaw)
		}
		if requests != packs {
			t.Errorf("sent %d requests for %d packs", requests, packs)
		}
	})

	// The packs that never ran would otherwise come back zero-valued: no
	// suggestions and no error, which for this method is a real answer meaning
	// "nobody searches for that". A cancelled sweep must not be readable as a
	// list of terms with no suggestions.
	t.Run("cancellation reaches the packs that never ran", func(t *testing.T) {
		c := newMockClient(t, func(req *http.Request) (mockResponse, bool) {
			if req.URL.Path != pathBatch {
				return mockResponse{}, false
			}
			return mockResponse{Body: suggestAnswer(suggestTermsInRequest(t, req))}, true
		})
		WithConcurrency(4)(c)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		terms := manyTerms(packs)
		got := c.SuggestMany(ctx, terms, SuggestOptions{})
		for i := range terms {
			if got[i].Err == nil {
				t.Fatalf("out[%d] came back with neither an answer nor an error after cancellation", i)
			}
		}
	})
}
