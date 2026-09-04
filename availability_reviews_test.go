package googleplayscraper

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// availabilityFrame is availabilityPage's counterpart for the RPC the
// availability probe now uses: a ds:5 payload delivered as a batchexecute frame
// instead of embedded in a megabyte of HTML.
//
// The probe asks for field 19 alone, and makeAppDataWith18 already builds what
// that returns: captured live, a one-field answer is a nineteen-element array
// of nulls with the availability node at index 18, not a shorter or differently
// keyed one.
func availabilityFrame(t *testing.T, marker int) []byte {
	t.Helper()
	appData := makeAppDataWith18(marker)
	ds5 := []any{nil, []any{nil, nil, appData}}
	raw, err := json.Marshal(ds5)
	if err != nil {
		t.Fatalf("marshal ds5: %v", err)
	}
	return framesEnvelope("Ws7gDc", map[string]string{"0": string(raw)}, []string{"0"})
}

// emptyFrame is what Google returns for an id it will not serve in this
// country -- a frame with a null payload, which is the signal the page used to
// give as a 404.
func emptyFrame() []byte {
	return framesEnvelope("Ws7gDc", map[string]string{"0": ""}, []string{"0"})
}

// TestAvailabilityMixedStatuses drives a sweep over a fixed country set where the
// mock returns a different outcome per country: available, not-in-region (200 +
// [18][0]=1), not-found (404) and a transport error (StatusFetchError). It pins
// every Status branch plus the Errors map and Checked aggregate.
func TestAvailabilityMixedStatuses(t *testing.T) {
	availBody := availabilityFrame(t, 2)
	regionBody := availabilityFrame(t, 1)

	route := func(req *http.Request) (mockResponse, bool) {
		if req.URL.Path != pathBatch {
			return mockResponse{}, false
		}
		switch req.URL.Query().Get("gl") {
		case "us":
			return mockResponse{Body: availBody}, true
		case "de":
			return mockResponse{Body: regionBody}, true
		case "jp":
			// the RPC's form of "not served here": a frame with no payload
			return mockResponse{Body: emptyFrame()}, true
		case "br":
			return mockResponse{Err: fmt.Errorf("simulated transport failure")}, true
		}
		return mockResponse{}, false
	}
	c := newMockClient(t, route)

	var progressCalls int
	res, err := c.Availability(context.Background(), "com.x", AvailabilityOptions{
		Countries: []string{"us", "de", "jp", "br"},
		Progress:  func(AvailabilityProgress) { progressCalls++ },
	})
	if err != nil {
		t.Fatalf("Availability: %v", err)
	}

	want := map[string]Status{
		"us": StatusAvailable,
		"de": StatusNotInRegion,
		"jp": StatusNotFound,
		"br": StatusFetchError,
	}
	for country, ws := range want {
		if res.Statuses[country] != ws {
			t.Errorf("Statuses[%s] = %v, want %v", country, res.Statuses[country], ws)
		}
	}
	if res.Errors["br"] == nil {
		t.Error("Errors[br] is nil, want the transport error")
	}
	if res.Errors["us"] != nil {
		t.Error("Errors[us] should be nil")
	}
	// Three conclusive (non-error) probes: us, de, jp.
	if res.Checked != 3 {
		t.Errorf("Checked = %d, want 3", res.Checked)
	}
	if res.GloballyRemoved {
		t.Error("GloballyRemoved = true, but not all conclusive probes were 404")
	}
	if progressCalls != 4 {
		t.Errorf("Progress called %d times, want 4", progressCalls)
	}
}

// TestAvailabilityGloballyRemoved latches GloballyRemoved when every conclusive
// probe is a 404.
func TestAvailabilityGloballyRemoved(t *testing.T) {
	c := newMockClient(t, routePath(pathBatch, emptyFrame()))

	res, err := c.Availability(context.Background(), "com.gone", AvailabilityOptions{
		Countries: []string{"us", "de", "jp"},
	})
	if err != nil {
		t.Fatalf("Availability: %v", err)
	}
	if !res.GloballyRemoved {
		t.Error("GloballyRemoved = false, want true (all 404)")
	}
	if res.Checked != 3 {
		t.Errorf("Checked = %d, want 3", res.Checked)
	}
}

// TestAvailabilitySoftBlock turns a 200 page with no app node into a
// StatusFetchError, exercising checkOne's "app data not found" branch.
func TestAvailabilitySoftBlock(t *testing.T) {
	c := newMockClient(t, routePath(pathBatch, framesEnvelope("Ws7gDc", map[string]string{"0": `"not-an-app"`}, []string{"0"})))

	res, err := c.Availability(context.Background(), "com.x", AvailabilityOptions{Countries: []string{"us"}})
	if err != nil {
		t.Fatalf("Availability: %v", err)
	}
	if res.Statuses["us"] != StatusFetchError {
		t.Errorf("Statuses[us] = %v, want error", res.Statuses["us"])
	}
	if res.Errors["us"] == nil {
		t.Error("Errors[us] is nil, want the soft-block error")
	}
}

// The distinction the availability probe used to lose: a frame that never
// arrived is not the store saying "no listing here". Reading it as one made
// GloballyRemoved latch on a short response, which is the difference between a
// dropped packet and a delisted app.
func TestCheckOneDistinguishesDroppedFromEmpty(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    []byte
		want    Status
		wantErr bool
	}{
		{"no frame at all", droppedFrame(), StatusFetchError, true},
		{"present, null payload", emptyFrame(), StatusNotFound, false},
		{"present, installable", availabilityFrame(t, 2), StatusAvailable, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newMockClient(t, routePath(pathBatch, tc.body))

			got, err := c.checkOne(context.Background(), "com.x", "us", "en")
			if got != tc.want {
				t.Errorf("checkOne = %v, want %v", got, tc.want)
			}
			if (err != nil) != tc.wantErr {
				t.Errorf("checkOne error = %v, want error: %v", err, tc.wantErr)
			}
		})
	}
}

// GloballyRemoved is the claim with the largest blast radius this package
// makes, and it must rest only on countries that answered.
func TestAvailabilityDroppedFrameIsNotARemoval(t *testing.T) {
	c := newMockClient(t, routePath(pathBatch, droppedFrame()))

	res, err := c.Availability(context.Background(), "com.x", AvailabilityOptions{
		Countries: []string{"us", "de", "jp"},
	})
	if err != nil {
		t.Fatalf("Availability: %v", err)
	}
	if res.GloballyRemoved {
		t.Error("GloballyRemoved = true after three dropped frames: no country answered")
	}
	if res.Checked != 0 {
		t.Errorf("Checked = %d, want 0: a dropped frame is not a conclusive probe", res.Checked)
	}
	if len(res.Errors) != 3 {
		t.Errorf("Errors has %d entries, want 3", len(res.Errors))
	}
	for _, cc := range []string{"us", "de", "jp"} {
		if res.Statuses[cc] != StatusFetchError {
			t.Errorf("Statuses[%s] = %v, want error", cc, res.Statuses[cc])
		}
	}
}

func TestAvailabilityMixedDroppedAndEmpty(t *testing.T) {
	availBody := availabilityFrame(t, 2)
	c := newMockClient(t, func(req *http.Request) (mockResponse, bool) {
		if req.URL.Path != pathBatch {
			return mockResponse{}, false
		}
		switch req.URL.Query().Get("gl") {
		case "us":
			return mockResponse{Body: availBody}, true
		case "de":
			return mockResponse{Body: droppedFrame()}, true
		case "jp":
			return mockResponse{Body: emptyFrame()}, true
		}
		return mockResponse{}, false
	})

	res, err := c.Availability(context.Background(), "com.x", AvailabilityOptions{
		Countries: []string{"us", "de", "jp"},
	})
	if err != nil {
		t.Fatalf("Availability: %v", err)
	}
	want := map[string]Status{"us": StatusAvailable, "de": StatusFetchError, "jp": StatusNotFound}
	for cc, status := range want {
		if res.Statuses[cc] != status {
			t.Errorf("Statuses[%s] = %v, want %v", cc, res.Statuses[cc], status)
		}
	}
	if res.Checked != 2 {
		t.Errorf("Checked = %d, want 2 (de never answered)", res.Checked)
	}
	if res.GloballyRemoved {
		t.Error("GloballyRemoved = true although the app is installable in us")
	}
	if len(res.Errors) != 1 || res.Errors["de"] == nil {
		t.Errorf("Errors = %v, want only de", res.Errors)
	}
}

// The status set is a wire format now, not an iota a reader has to decode
// against the order of a const block in availability.go.
func TestStatusMarshalsByName(t *testing.T) {
	got, err := json.Marshal(map[string]Status{"us": StatusAvailable, "cn": StatusNotInRegion})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if want := `{"cn":"not_in_region","us":"available"}`; string(got) != want {
		t.Errorf("marshalled %s, want %s", got, want)
	}

	// Both spellings read back, so a record written before this change still
	// decodes into the same Status.
	for input, want := range map[string]Status{
		`"available"`:     StatusAvailable,
		`"not_in_region"`: StatusNotInRegion,
		`"not_found"`:     StatusNotFound,
		`"error"`:         StatusFetchError,
		`"unknown"`:       StatusUnknown,
		`1`:               StatusAvailable,
		`3`:               StatusNotFound,
	} {
		var s Status
		if err := json.Unmarshal([]byte(input), &s); err != nil {
			t.Errorf("unmarshal %s: %v", input, err)
			continue
		}
		if s != want {
			t.Errorf("unmarshal %s = %v, want %v", input, s, want)
		}
	}
	var s Status
	if err := json.Unmarshal([]byte(`"delisted"`), &s); err == nil {
		t.Error("a name this package never writes decoded without an error")
	}
}

// AvailabilityResult marshals through its own MarshalJSON, so the statuses have
// to survive the embedded-alias trick that keeps Errors readable.
func TestAvailabilityResultCarriesStatusNames(t *testing.T) {
	res := AvailabilityResult{
		AppID:    "com.x",
		Statuses: map[string]Status{"us": StatusAvailable},
		Errors:   map[string]error{"de": fmt.Errorf("boom")},
		Checked:  1,
	}
	got, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{`"statuses":{"us":"available"}`, `"errors":{"de":"boom"}`} {
		if !strings.Contains(string(got), want) {
			t.Errorf("marshalled %s, missing %s", got, want)
		}
	}
}

func TestAvailabilityEmptyAppID(t *testing.T) {
	if _, err := NewClient().Availability(context.Background(), "", AvailabilityOptions{}); err == nil {
		t.Fatal("expected error for empty appID")
	}
}

// TestAvailabilityContextCancelled cancels the context before the sweep starts;
// the call must return ctx.Err() with a partial (possibly empty) result.
func TestAvailabilityContextCancelled(t *testing.T) {
	c := newMockClient(t, routePath(pathBatch, availabilityFrame(t, 2)))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := c.Availability(ctx, "com.x", AvailabilityOptions{Countries: []string{"us", "de", "fr"}})
	if err == nil {
		t.Fatal("expected ctx.Err(), got nil")
	}
	// Partial result: never more than the requested countries.
	if len(res.Statuses) > 3 {
		t.Errorf("got %d statuses, want <= 3", len(res.Statuses))
	}
}

// TestAvailableCountries verifies the convenience wrapper returns only the
// installable countries, sorted.
func TestAvailableCountries(t *testing.T) {
	availBody := availabilityFrame(t, 2)
	route := func(req *http.Request) (mockResponse, bool) {
		if req.URL.Path != pathBatch {
			return mockResponse{}, false
		}
		switch req.URL.Query().Get("gl") {
		case "us", "gb":
			return mockResponse{Body: availBody}, true
		case "de":
			return mockResponse{Status: http.StatusNotFound}, true
		}
		return mockResponse{}, false
	}
	c := newMockClient(t, route)

	got, err := c.AvailableCountries(context.Background(), "com.x", AvailabilityOptions{
		Countries: []string{"us", "de", "gb"},
	})
	if err != nil {
		t.Fatalf("AvailableCountries: %v", err)
	}
	if len(got) != 2 || got[0] != "gb" || got[1] != "us" {
		t.Errorf("AvailableCountries = %v, want [gb us]", got)
	}
}

// reviewsEnvelope wraps review rows + an optional next-page token in the oCPfdb
// payload that parseReviewsResponse expects: data[0] is the review list, data[1]
// is [_, token]. Each review row is [id, [user,...], score, _, text, ...].
func reviewsEnvelope(t *testing.T, ids []string, token string) []byte {
	t.Helper()
	rows := make([]any, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, []any{
			id,
			[]any{"User " + id},
			float64(5),
			nil,
			"review text " + id,
		})
	}
	tokenField := any(nil)
	if token != "" {
		tokenField = token
	}
	data := []any{rows, []any{nil, tokenField}}
	inner, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal reviews: %v", err)
	}
	return batchEnvelope("oCPfdb", string(inner))
}

func TestReviewsSinglePage(t *testing.T) {
	c := newMockClient(t,
		routePath(pathBatch, reviewsEnvelope(t, []string{"r1", "r2", "r3"}, "")),
	)

	res, err := c.Reviews(context.Background(), "com.x", ReviewOptions{})
	if err != nil {
		t.Fatalf("Reviews: %v", err)
	}
	if len(res.Reviews) != 3 {
		t.Fatalf("got %d reviews, want 3", len(res.Reviews))
	}
	if res.NextToken != "" {
		t.Errorf("NextToken = %q, want empty", res.NextToken)
	}
	if res.Reviews[0].ID != "r1" || res.Reviews[0].Text != "review text r1" {
		t.Errorf("first review = %+v", res.Reviews[0])
	}
}

func TestReviewsEmptyAppID(t *testing.T) {
	if _, err := NewClient().Reviews(context.Background(), "", ReviewOptions{}); err == nil {
		t.Fatal("expected error for empty appID")
	}
}

// pagedReviews returns a stateful route: the first call yields a page carrying a
// continuation token, every later call yields a final tokenless page. It models
// the two-step pagination ReviewsAll drives.
func pagedReviews(t *testing.T, firstIDs, secondIDs []string) routeFunc {
	t.Helper()
	first := reviewsEnvelope(t, firstIDs, "page2-token")
	second := reviewsEnvelope(t, secondIDs, "")
	var mu sync.Mutex
	var calls int
	return func(req *http.Request) (mockResponse, bool) {
		if req.URL.Path != pathBatch {
			return mockResponse{}, false
		}
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 1 {
			return mockResponse{Body: first}, true
		}
		return mockResponse{Body: second}, true
	}
}

func TestReviewsAllPaginatesAndStops(t *testing.T) {
	c := newMockClient(t, pagedReviews(t, []string{"a", "b"}, []string{"c", "d"}))

	all, err := c.ReviewsAll(context.Background(), "com.x", ReviewOptions{Count: 100})
	if err != nil {
		t.Fatalf("ReviewsAll: %v", err)
	}
	// Page 1 (a,b) + page 2 (c,d), then the empty-token page stops the loop.
	if len(all) != 4 {
		t.Fatalf("got %d reviews, want 4", len(all))
	}
	ids := map[string]bool{}
	for _, r := range all {
		ids[r.ID] = true
	}
	for _, want := range []string{"a", "b", "c", "d"} {
		if !ids[want] {
			t.Errorf("missing review %q", want)
		}
	}
}

func TestReviewsAllTrimsToCount(t *testing.T) {
	c := newMockClient(t, pagedReviews(t, []string{"a", "b", "c"}, []string{"d", "e"}))

	all, err := c.ReviewsAll(context.Background(), "com.x", ReviewOptions{Count: 4})
	if err != nil {
		t.Fatalf("ReviewsAll: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("got %d reviews, want exactly 4 (trimmed)", len(all))
	}
}

// reviewsPageSizeRoute answers reviews requests and records the page size each
// one asked for. The size rides in the f.req body as [2,<sort>,[<count>], which
// is the only place a request states how much it wants.
func reviewsPageSizeRoute(t *testing.T, sizes *[]int, first, rest []byte) routeFunc {
	t.Helper()
	countRe := regexp.MustCompile(`\[2,\d+,\[(\d+)`)
	var mu sync.Mutex
	var calls int
	return func(req *http.Request) (mockResponse, bool) {
		if req.URL.Path != pathBatch {
			return mockResponse{}, false
		}
		body, _ := io.ReadAll(req.Body)
		decoded, _ := url.QueryUnescape(strings.TrimPrefix(string(body), "f.req="))
		mu.Lock()
		defer mu.Unlock()
		if m := countRe.FindStringSubmatch(decoded); m != nil {
			n, _ := strconv.Atoi(m[1])
			*sizes = append(*sizes, n)
		}
		calls++
		if calls == 1 {
			return mockResponse{Body: first}, true
		}
		return mockResponse{Body: rest}, true
	}
}

// ReviewsAll delegates its pagination to the sequence, and the sequence asks
// for the biggest page there is because for it a page is only a request. That
// is the wrong size for a caller with a ceiling: ReviewsAll(Count: 10) used to
// transfer a thousand reviews to hand back ten.
func TestReviewsAllAsksForNoMoreThanItNeeds(t *testing.T) {
	ids := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l"}
	for _, tc := range []struct {
		name     string
		count    int
		wantSize int
	}{
		{"a small ask is a small page", 10, 10},
		{"the default ceiling", 0, 500},
		{"an ask past the page limit is clamped", 5000, reviewsPageMax},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sizes []int
			c := newMockClient(t, reviewsPageSizeRoute(t, &sizes,
				reviewsEnvelope(t, ids, "page2-token"), reviewsEnvelope(t, ids, "")))

			got, err := c.ReviewsAll(context.Background(), "com.x", ReviewOptions{Count: tc.count})
			if err != nil {
				t.Fatalf("ReviewsAll: %v", err)
			}
			if len(sizes) == 0 {
				t.Fatal("no request carried a page size")
			}
			if sizes[0] != tc.wantSize {
				t.Errorf("asked for %d reviews, want %d", sizes[0], tc.wantSize)
			}
			if tc.count == 10 {
				if len(sizes) != 1 {
					t.Errorf("made %d requests for 10 reviews, want 1", len(sizes))
				}
				if len(got) != 10 {
					t.Errorf("returned %d reviews, want exactly 10", len(got))
				}
			}
		})
	}
}

// The extraction that gave ReviewsAll its own page size must not have moved
// ReviewsSeq's: it still wants the largest page on every page, not just the
// first.
func TestReviewsSeqAsksForFullPagesOnEveryPage(t *testing.T) {
	ids := []string{"a", "b"}
	var sizes []int
	c := newMockClient(t, reviewsPageSizeRoute(t, &sizes,
		reviewsEnvelope(t, ids, "page2-token"), reviewsEnvelope(t, ids, "")))

	for _, err := range c.ReviewsSeq(context.Background(), "com.x", ReviewOptions{}) {
		if err != nil {
			t.Fatalf("ReviewsSeq: %v", err)
		}
	}

	if len(sizes) != 2 {
		t.Fatalf("made %d requests, want 2 (one page plus its continuation)", len(sizes))
	}
	for i, size := range sizes {
		if size != reviewsPageMax {
			t.Errorf("page %d asked for %d reviews, want reviewsPageMax (%d)", i+1, size, reviewsPageMax)
		}
	}
}

// Five failed rating passes is a run that observed nothing. It used to be
// reported as an app with no reviews: an empty slice and a nil error.
func TestReviewsComprehensiveFailsWhenEveryRatingFails(t *testing.T) {
	c := newMockClient(t, routePathStatus(pathBatch, http.StatusInternalServerError))

	got, err := c.ReviewsComprehensive(context.Background(), "com.x", ReviewOptions{})
	if err == nil {
		t.Fatal("every rating pass failed and the error was nil")
	}
	if len(got) != 0 {
		t.Errorf("got %d reviews from a run where every request failed", len(got))
	}
}

// The other half of the same rule: partial success stays success. Sweeping five
// ratings is worth doing precisely because four of them still answer.
func TestReviewsComprehensiveKeepsGoingPastAFailedRating(t *testing.T) {
	var mu sync.Mutex
	var calls int
	c := newMockClient(t, func(req *http.Request) (mockResponse, bool) {
		if req.URL.Path != pathBatch {
			return mockResponse{}, false
		}
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls <= 2 {
			return mockResponse{Status: http.StatusInternalServerError}, true
		}
		return mockResponse{Body: reviewsEnvelope(t, []string{fmt.Sprintf("unique-%d", calls)}, "")}, true
	})

	got, err := c.ReviewsComprehensive(context.Background(), "com.x", ReviewOptions{})
	if err != nil {
		t.Fatalf("three ratings answered and the run still failed: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("got %d reviews, want 3 (one per rating that answered)", len(got))
	}
}

// TestReviewsComprehensive sweeps all five rating filters; each returns one
// unique review plus one shared duplicate, so the de-dup keeps 5 unique + 1.
func TestReviewsComprehensive(t *testing.T) {
	var mu sync.Mutex
	var calls int
	c := newMockClient(t, func(req *http.Request) (mockResponse, bool) {
		if req.URL.Path != pathBatch {
			return mockResponse{}, false
		}
		mu.Lock()
		defer mu.Unlock()
		// ReviewsAll fetches one tokenless page per rating (5 ratings). Each page
		// contributes a unique id plus a shared "dup", so the de-dup keeps 5
		// uniques and the single shared review.
		calls++
		id := fmt.Sprintf("unique-%d", calls)
		return mockResponse{Body: reviewsEnvelope(t, []string{id, "dup"}, "")}, true
	})

	all, err := c.ReviewsComprehensive(context.Background(), "com.x", ReviewOptions{Count: 10})
	if err != nil {
		t.Fatalf("ReviewsComprehensive: %v", err)
	}
	// 5 ratings × (1 unique + 1 shared dup) de-duped => 5 unique + 1 shared = 6.
	if len(all) != 6 {
		t.Fatalf("got %d unique reviews, want 6", len(all))
	}
}

// Reviews built its own batchexecute URL and hand-escaped its own f.req body,
// the last endpoint in the package still doing so. Moving it onto the shared
// rpcCall/buildFReq path had to change the bytes on the wire in exactly three
// places and nowhere else, which is what this pins.
//
// The inner oCPfdb payload -- the part Google actually reads as a query -- is
// asserted verbatim, because it is the part that must not have moved. Verified
// live against com.spotify.music before and after: the same page-1 ids under a
// stable sort, and a token issued by either path followed by the other to the
// same page 2.
func TestReviewsRidesTheSharedBatchLayer(t *testing.T) {
	tests := []struct {
		name string
		opts ReviewOptions
		want string
	}{
		{
			name: "first page",
			opts: ReviewOptions{Sort: SortNewest, Count: 150},
			want: `[[["oCPfdb","[null,[2,2,[150],null,[null,null]],[\"com.x\",7]]",null,"0"]]]`,
		},
		{
			// The token is the third slot of the page group and the score the
			// second of the filter group; both are quoted by the shared builder
			// now rather than by hand, so a token carrying JSON-significant
			// characters is the case worth stating.
			name: "continuation with a score filter",
			opts: ReviewOptions{Sort: SortNewest, Count: 150, NextToken: `TOK+EN/1`, FilterScore: 3},
			want: `[[["oCPfdb","[null,[2,2,[150,null,\"TOK+EN/1\"],null,[null,3]],[\"com.x\",7]]",null,"0"]]]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotBody, gotRPCIDs, gotType string
			c := newMockClient(t, func(req *http.Request) (mockResponse, bool) {
				if req.URL.Path != pathBatch {
					return mockResponse{}, false
				}
				gotRPCIDs = req.URL.Query().Get("rpcids")
				gotType = req.Header.Get("Content-Type")
				body, _ := io.ReadAll(req.Body)
				gotBody, _ = url.QueryUnescape(strings.TrimPrefix(string(body), "f.req="))
				return mockResponse{Body: batchEnvelope("oCPfdb", "[[],null]")}, true
			})

			if _, err := c.Reviews(context.Background(), "com.x", tt.opts); err != nil {
				t.Fatalf("Reviews: %v", err)
			}

			// 1. The URL now names the RPC, because the shared path builds it
			// from the calls it was given.
			if gotRPCIDs != "oCPfdb" {
				t.Errorf("rpcids = %q, want oCPfdb", gotRPCIDs)
			}
			// 2. The content type gained the charset every other RPC sends.
			if !strings.Contains(gotType, "charset=UTF-8") {
				t.Errorf("Content-Type = %q, want the shared one with charset=UTF-8", gotType)
			}
			// 3. The call index is the position in the request ("0"), where
			// this endpoint used to send the literal "generic".
			if gotBody != tt.want {
				t.Errorf("f.req =\n  %s\nwant\n  %s", gotBody, tt.want)
			}
		})
	}
}

// Recorded fixtures carry whatever call index the code sent when they were
// captured -- "generic" for reviews, not today's "0". A response to a one-call
// request has exactly one possible answer, so it is read whatever index it
// echoes; requiring a match would invalidate every fixture in testdata.
func TestReviewsReadsAFrameWhateverIndexItEchoes(t *testing.T) {
	c := newMockClient(t, routePath(pathBatch,
		batchEnvelope("oCPfdb", `[[["r1",["U"],5,null,"text",[1704067200],1]],[null,"tok"]]`)))

	res, err := c.Reviews(context.Background(), "com.x", ReviewOptions{})
	if err != nil {
		t.Fatalf("Reviews: %v", err)
	}
	if len(res.Reviews) != 1 || res.Reviews[0].ID != "r1" {
		t.Fatalf("got %d reviews (%+v), want the one in the frame indexed \"generic\"", len(res.Reviews), res.Reviews)
	}
	if res.NextToken != "tok" {
		t.Errorf("NextToken = %q, want tok", res.NextToken)
	}
}
