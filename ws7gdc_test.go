package googleplayscraper

import (
	"context"
	"encoding/json"
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
	c := newMockClient(t, func(req *http.Request) (mockResponse, bool) {
		if req.URL.Path != pathBatch {
			return mockResponse{}, false
		}
		// Only the second app is answered.
		return mockResponse{Body: framesEnvelope("Ws7gDc",
			map[string]string{"1": minimalDS5("second")}, []string{"1"})}, true
	})

	got := c.AppsMany(context.Background(), []string{"com.a", "com.b"}, AppOptions{})
	if got[0].Err == nil {
		t.Error("an app with no frame returned no error")
	}
	if got[1].Err != nil || got[1].App.Title != "second" {
		t.Errorf("the answered app came back wrong: %+v", got[1])
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
