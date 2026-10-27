package googleplayscraper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

// minimalDS5 builds the smallest ds:5 block extractAppData will read a title
// out of: the title sits at [1][2][0][0].
func minimalDS5(title string) string {
	return fmt.Sprintf(`[null,[null,null,[[%q]]]]`, title)
}

// The template must substitute the app id and stay valid JSON. A format verb in
// the wrong place would produce a body Google rejects, and the failure would
// look like a network problem rather than a broken constant.
func TestAppDetailsRPCEmbedsTheAppID(t *testing.T) {
	call := appDetailsRPC("com.example.app")
	if call.id != "Ws7gDc" {
		t.Errorf("rpc id = %q, want Ws7gDc", call.id)
	}

	var req []any
	if err := json.Unmarshal([]byte(call.payload), &req); err != nil {
		t.Fatalf("payload is not valid JSON: %v\n%s", err, call.payload)
	}
	// The package id lives at [5][0][0].
	got := req[5].([]any)[0].([]any)[0]
	if got != "com.example.app" {
		t.Errorf("app id landed at [5][0][0] as %v, want com.example.app", got)
	}
	if strings.Contains(call.payload, "com.google.android.apps.maps") {
		t.Error("payload still carries the app the template was captured from")
	}
}

// Details are the highest-volume operation, so the pack ratio matters most
// here. Each app must also get its own answer back: the mock returns frames
// reversed, and a positional pairing would swap them.
func TestAppsManyPacksAppsIntoFewRequests(t *testing.T) {
	appRe := regexp.MustCompile(`\[\\"([^"\\]+)\\",7\]`)
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
			byIndex[fmt.Sprint(i)] = minimalDS5("title-of-" + m[1])
			order = append([]string{fmt.Sprint(i)}, order...) // reversed
		}
		return mockResponse{Body: framesEnvelope("Ws7gDc", byIndex, order)}, true
	})

	var appIDs []string
	for i := range 70 {
		appIDs = append(appIDs, fmt.Sprintf("com.example.app%d", i))
	}

	got := c.AppsMany(context.Background(), appIDs, AppOptions{})
	if requests != 3 {
		t.Errorf("made %d requests for 70 apps, want 3", requests)
	}
	for i, r := range got {
		if r.Err != nil {
			t.Fatalf("%s: %v", r.AppID, r.Err)
		}
		if r.AppID != appIDs[i] {
			t.Fatalf("result %d is for %s, want %s", i, r.AppID, appIDs[i])
		}
		if want := "title-of-" + appIDs[i]; r.App.Title != want {
			t.Errorf("%s got title %q, want %q -- answers paired to the wrong app",
				appIDs[i], r.App.Title, want)
		}
		if r.App.URL != appPageURL(appIDs[i], "en", "us") {
			t.Errorf("%s: URL = %q, want the canonical page address", appIDs[i], r.App.URL)
		}
	}
}

func TestAppsManyReportsAMissingPayload(t *testing.T) {
	c := newMockClient(t,
		func(req *http.Request) (mockResponse, bool) {
			if req.URL.Path != pathBatch {
				return mockResponse{}, false
			}
			// Only com.b is ever answered, first time and on the retry.
			body, _ := io.ReadAll(req.Body)
			if !strings.Contains(string(body), "com.b") {
				return mockResponse{Body: framesEnvelope("Ws7gDc", map[string]string{}, nil)}, true
			}
			return mockResponse{Body: framesEnvelope("Ws7gDc",
				map[string]string{"1": minimalDS5("second")}, []string{"1"})}, true
		},
		routePathStatus("/store/apps/details", http.StatusNotFound),
	)

	got := c.AppsMany(context.Background(), []string{"com.a", "com.b"}, AppOptions{})
	if !errors.Is(got[0].Err, ErrNotFound) {
		t.Errorf("an app with no frame and a 404 page: err = %v, want ErrNotFound", got[0].Err)
	}
	if got[1].Err != nil || got[1].App.Title != "second" {
		t.Errorf("the answered app came back wrong: %+v", got[1])
	}
}

// batchAnswering answers a Ws7gDc pack with a title for every app id in it,
// except the ids skip says to leave out on that call. It counts calls.
func batchAnswering(calls *int, skip func(call int, id string) bool) routeFunc {
	appRe := regexp.MustCompile(`\[\\"([^"\\]+)\\",7\]`)
	return func(req *http.Request) (mockResponse, bool) {
		if req.URL.Path != pathBatch {
			return mockResponse{}, false
		}
		*calls++
		body, _ := io.ReadAll(req.Body)
		decoded, _ := url.QueryUnescape(strings.TrimPrefix(string(body), "f.req="))
		byIndex := map[string]string{}
		var order []string
		for i, m := range appRe.FindAllStringSubmatch(decoded, -1) {
			if skip(*calls, m[1]) {
				continue
			}
			byIndex[fmt.Sprint(i)] = minimalDS5("title-of-" + m[1])
			order = append(order, fmt.Sprint(i))
		}
		return mockResponse{Body: framesEnvelope("Ws7gDc", byIndex, order)}, true
	}
}

func appIDsN(n int) []string {
	var ids []string
	for i := range n {
		ids = append(ids, fmt.Sprintf("com.example.app%d", i))
	}
	return ids
}

// Issue #18: an item Google drops from one pack usually comes back on a second
// request. That second request is for the dropped items only.
func TestAppsManyRetriesEmptyItems(t *testing.T) {
	var calls int
	c := newMockClient(t, batchAnswering(&calls, func(call int, id string) bool {
		return call == 1 && (id == "com.example.app3" || id == "com.example.app7")
	}))
	got := c.AppsMany(context.Background(), appIDsN(10), AppOptions{})
	for _, r := range got {
		if r.Err != nil || r.App.Title != "title-of-"+r.AppID {
			t.Errorf("%s: %+v", r.AppID, r)
		}
	}
	if calls != 2 {
		t.Errorf("%d batch requests, want 2: the run and one retry", calls)
	}
}

// An item still empty after the retry is settled by its details page: 200
// means the app exists (here, the Maps fixture), 404 means ErrNotFound.
func TestAppsManySettlesStillEmptyItemsByPage(t *testing.T) {
	var calls int
	page := readFixture(t, "app_page.html")
	c := newMockClient(t,
		batchAnswering(&calls, func(_ int, id string) bool {
			return id == "com.example.app1" || id == "com.example.app2"
		}),
		func(req *http.Request) (mockResponse, bool) {
			if req.URL.Path != "/store/apps/details" {
				return mockResponse{}, false
			}
			if req.URL.Query().Get("id") == "com.example.app1" {
				return mockResponse{Body: page}, true
			}
			return mockResponse{Status: http.StatusNotFound}, true
		},
	)
	got := c.AppsMany(context.Background(), appIDsN(6), AppOptions{})
	if got[1].Err != nil || got[1].App == nil || got[1].App.Title != "Google Maps" {
		t.Errorf("app1 should come from its page: %+v", got[1])
	}
	if !errors.Is(got[2].Err, ErrNotFound) {
		t.Errorf("app2: err = %v, want ErrNotFound", got[2].Err)
	}
	if got[0].Err != nil || got[3].Err != nil {
		t.Error("answered apps should be untouched")
	}
}

// When most of a run comes back empty, Google is declining the batch, not
// dropping items. Asking again item by item would multiply that traffic.
func TestAppsManyDoesNotRetryAMostlyEmptyRun(t *testing.T) {
	var calls int
	c := newMockClient(t, batchAnswering(&calls, func(_ int, id string) bool {
		return id != "com.example.app0"
	}))
	got := c.AppsMany(context.Background(), appIDsN(10), AppOptions{})
	if calls != 1 {
		t.Errorf("%d batch requests, want 1", calls)
	}
	if !errors.Is(got[5].Err, errEmptyPayload) {
		t.Errorf("err = %v, want the empty-payload error left as is", got[5].Err)
	}
}

// Recovery requests draw on the retry budget: with one token, the retry pack
// goes out and no page requests follow.
func TestAppsManyRecoveryUsesRetryBudget(t *testing.T) {
	var calls, pages int
	c := newMockClient(t,
		batchAnswering(&calls, func(_ int, id string) bool { return id == "com.example.app4" }),
		func(req *http.Request) (mockResponse, bool) {
			if req.URL.Path != "/store/apps/details" {
				return mockResponse{}, false
			}
			pages++
			return mockResponse{Status: http.StatusNotFound}, true
		},
	)
	WithRetry(RetryPolicy{Budget: 0.01, Burst: 1})(c)
	c.AppsMany(context.Background(), appIDsN(10), AppOptions{})
	if calls != 2 || pages != 0 {
		t.Errorf("batch requests %d, page requests %d; want 2 and 0", calls, pages)
	}
}

// ── live ───────────────────────────────────────────────────────────────────

// The reverse-engineering rests on one claim: the Ws7gDc payload is what the
// page would have put in ds:5. If that holds, every static field parses the
// same either way, and only counters that genuinely move between two requests
// differ. This is the test that keeps that claim honest.
func TestAppsManyMatchesAppExceptLiveCounters(t *testing.T) {
	if testing.Short() {
		t.Skip("network test")
	}
	appIDs := []string{"com.spotify.music", "com.duolingo", "com.ebay.mobile"}

	c := NewClient(WithThrottle(300 * time.Millisecond))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	many := c.AppsMany(ctx, appIDs, AppOptions{})
	for i, id := range appIDs {
		single, err := c.App(ctx, id, AppOptions{})
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if many[i].Err != nil {
			t.Fatalf("%s batched: %v", id, many[i].Err)
		}
		batched := *many[i].App

		if batched.Title == "" || batched.Ratings == 0 {
			t.Fatalf("%s: batched result is empty; the comparison would be vacuous", id)
		}

		// Ratings, review counts and the histogram tick upward continuously on
		// a popular app, so two requests seconds apart legitimately disagree.
		// Everything else must match exactly.
		relClose := func(a, b int) bool {
			if a == b {
				return true
			}
			d := a - b
			if d < 0 {
				d = -d
			}
			return float64(d)/float64(max(a, 1)) < 0.001
		}
		if !relClose(single.Ratings, batched.Ratings) {
			t.Errorf("%s: ratings %d vs %d differ by more than drift", id, single.Ratings, batched.Ratings)
		}
		if !relClose(single.Reviews, batched.Reviews) {
			t.Errorf("%s: review count %d vs %d differ by more than drift", id, single.Reviews, batched.Reviews)
		}
		for k := range single.Histogram {
			if !relClose(single.Histogram[k], batched.Histogram[k]) {
				t.Errorf("%s: histogram[%d] %d vs %d differ by more than drift",
					id, k, single.Histogram[k], batched.Histogram[k])
			}
		}

		a, b := *single, batched
		a.Ratings, b.Ratings = 0, 0
		a.Reviews, b.Reviews = 0, 0
		a.Histogram, b.Histogram = [5]int{}, [5]int{}
		a.Score, b.Score = 0, 0 // moves with the histogram
		if !reflect.DeepEqual(a, b) {
			ja, _ := json.Marshal(a)
			jb, _ := json.Marshal(b)
			t.Errorf("%s: batched details differ from the page beyond live counters\n  page: %s\n  rpc:  %s",
				id, ja, jb)
		}
	}
}

// ws7gdcRequestTemplate was copied out of a recorded page. Google can change
// the request its own front end sends, and if it does, this template starts
// asking for fields by an outdated numbering. The page publishes the current
// request, so drift is detectable rather than something to discover from
// mysteriously empty results.
func TestWs7gDcTemplateStillMatchesThePage(t *testing.T) {
	if testing.Short() {
		t.Skip("network test")
	}
	const appID = "com.google.android.apps.maps"

	c := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	body, err := c.get(ctx, appPageURL(appID, "en", "us"))
	if err != nil {
		t.Fatalf("fetch app page: %v", err)
	}

	// Reassembled from the pieces the code actually uses -- the shape and the
	// full field list -- so a refactor that splits them cannot quietly lose
	// this guard. If either half drifts from what the page sends, this fails.
	want := extractDataServiceRequest(t, body, "ds:5")
	got := ws7gdcRPC(appID, ws7gdcFullFields).payload

	var wantAny, gotAny any
	if err := json.Unmarshal([]byte(want), &wantAny); err != nil {
		t.Fatalf("page request is not JSON: %v", err)
	}
	if err := json.Unmarshal([]byte(got), &gotAny); err != nil {
		t.Fatalf("template is not JSON: %v", err)
	}
	if !reflect.DeepEqual(wantAny, gotAny) {
		t.Errorf("ws7gdcRequestTemplate has drifted from what the page sends.\n"+
			"Re-capture it from AF_dataServiceRequests['ds:5'].request.\n  page:     %s\n  template: %s",
			want, got)
	}
}

// extractDataServiceRequest pulls the request body the page declares for a ds:
// key out of its AF_dataServiceRequests map, by walking balanced brackets --
// the value is JSON but the map around it is JavaScript.
func extractDataServiceRequest(t *testing.T, body []byte, dsKey string) string {
	t.Helper()
	marker := fmt.Sprintf("'%s' : {id:'", dsKey)
	i := strings.Index(string(body), marker)
	if i < 0 {
		t.Fatalf("no AF_dataServiceRequests entry for %s", dsKey)
	}
	rest := string(body)[i:]
	j := strings.Index(rest, "request:")
	if j < 0 {
		t.Fatalf("%s entry has no request", dsKey)
	}
	rest = rest[j+len("request:"):]

	depth := 0
	for k, ch := range rest {
		switch ch {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return rest[:k+1]
			}
		}
	}
	t.Fatalf("%s request is not bracket-balanced", dsKey)
	return ""
}

// Review of #19: a list that repeats an id must resolve every occurrence, and
// two workers must never write one slot (run with -race).
func TestAppsManyRecoveryWithRepeatedIDs(t *testing.T) {
	var calls int
	ids := append(appIDsN(10), "com.example.app3", "com.example.app3")
	c := newMockClient(t, batchAnswering(&calls, func(call int, id string) bool {
		return call == 1 && id == "com.example.app3"
	}))
	got := c.AppsMany(context.Background(), ids, AppOptions{})
	for k, r := range got {
		if r.Err != nil {
			t.Errorf("item %d (%s): %v", k, r.AppID, r.Err)
		}
	}
}

// Review of #19: a refusal while recovering is the caller's business. It
// lands on the item, ends recovery (no page request follows) and is what
// isBlockErr sees.
func TestAppsManyRecoveryReportsARefusal(t *testing.T) {
	var calls, pages int
	c := newMockClient(t,
		func(req *http.Request) (mockResponse, bool) {
			if req.URL.Path != pathBatch {
				return mockResponse{}, false
			}
			calls++
			if calls > 1 {
				return mockResponse{Status: http.StatusTooManyRequests}, true
			}
			return batchAnswering(new(int), func(_ int, id string) bool { return id == "com.example.app2" })(req)
		},
		func(req *http.Request) (mockResponse, bool) {
			if req.URL.Path != "/store/apps/details" {
				return mockResponse{}, false
			}
			pages++
			return mockResponse{Status: http.StatusNotFound}, true
		},
	)
	got := c.AppsMany(context.Background(), appIDsN(10), AppOptions{})
	if !isBlockErr(got[2].Err) {
		t.Errorf("err = %v, want the 429 from the retry", got[2].Err)
	}
	if pages != 0 {
		t.Errorf("%d page requests after a refusal, want 0", pages)
	}
}

// Review of #19: recovery requests are paid as retries and must not earn
// budget back as first attempts.
func TestAppsManyRecoveryDoesNotRefillTheBudget(t *testing.T) {
	var calls int
	c := newMockClient(t,
		batchAnswering(&calls, func(_ int, id string) bool { return id == "com.example.app4" }),
		routePathStatus("/store/apps/details", http.StatusNotFound),
	)
	WithRetry(RetryPolicy{Budget: 0.5, Burst: 2})(c)
	c.AppsMany(context.Background(), appIDsN(10), AppOptions{})
	// 2 to start, +0.5 for the run (capped at 2), -1 for the retry pack, -1
	// for the page: nothing left, and nothing credited back.
	c.budgetLock.Lock()
	tokens := c.budgetTokens
	c.budgetLock.Unlock()
	if tokens != 0 {
		t.Errorf("budget = %.2f after recovery, want 0", tokens)
	}
}
