package googleplayscraper

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
)

// availabilityFrame is availabilityPage's counterpart for the RPC the
// availability probe now uses: the same ds:5 payload, delivered as a
// batchexecute frame instead of embedded in a megabyte of HTML.
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
