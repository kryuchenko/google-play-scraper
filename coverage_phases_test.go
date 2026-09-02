package googleplayscraper

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// TestCategoryAppsClusterURLsErrorContinues makes ClusterURLs fail (500) while
// the rest of the pipeline succeeds. The cluster phase must swallow the error
// (counting the request) and the run must still complete with apps from the
// other phases.
func TestCategoryAppsClusterURLsErrorContinues(t *testing.T) {
	routes := []routeFunc{
		routeQuery(pathBatch, map[string]string{"rpcids": "qnKhOb"},
			[]byte(")]}'\n\n[[\"wrb.fr\",\"qnKhOb\",null,null,null,null,\"generic\"]]")),
		routeQuery(pathBatch, map[string]string{"rpcids": "vyAe2"}, readFixture(t, "list_vyae2.bin")),
		// Category page (ClusterURLs) errors out.
		func(req *http.Request) (mockResponse, bool) {
			if len(req.URL.Path) >= len(pathCategory) && req.URL.Path[:len(pathCategory)] == pathCategory {
				return mockResponse{Status: http.StatusInternalServerError}, true
			}
			return mockResponse{}, false
		},
		routePathStatus(pathTop, http.StatusInternalServerError),
		routePath("/store/search", readFixture(t, "search_page.html")),
	}
	c := newMockClient(t, routes...)

	res, err := c.CategoryApps(context.Background(), CoverageOptions{
		Category:    CategoryGameAction,
		SearchTerms: []string{"shooter"},
	})
	if err != nil {
		t.Fatalf("CategoryApps: %v", err)
	}
	if len(res.Apps) == 0 {
		t.Error("got 0 apps; cluster failure should not abort the run")
	}
}

// TestCategoryAppsGraphDeveloperWalk forces the graph phase's developer walk to
// fire. The list and cluster phases return nothing, so the only collected app is
// the search seed — which carries a high Score and a DeveloperID (populated via
// the qnKhOb continuation row). That makes it the top graph seed, and because it
// has a DeveloperID the developer walk runs alongside the similar walk.
func TestCategoryAppsGraphDeveloperWalk(t *testing.T) {
	emptyHTML := htmlWithDataBlocks(map[string]string{"ds:4": `[]`})

	// Page-1 search returns one app + a token; the qnKhOb page returns the same
	// app enriched with a Score and a DeveloperID. Merging fills both fields.
	page1 := searchHTMLPage(t, []string{"com.seed"}, "more")
	richRow := make([]any, 13)
	richRow[2] = "Seed App"
	richRow[12] = []any{"com.seed"}
	richRow[6] = []any{[]any{nil, nil, []any{nil, []any{"5.0", float64(5.0)}}}}
	richRow[4] = []any{[]any{[]any{
		"Seed Dev",
		[]any{nil, nil, nil, nil, []any{nil, nil, "https://play.google.com/store/apps/dev?id=DEV9"}},
	}}}
	data := []any{[]any{[]any{
		[]any{richRow}, nil, nil, nil, nil, nil, nil,
	}}}
	rawMore, _ := json.Marshal(data)
	morePayload := batchEnvelope("qnKhOb", string(rawMore))

	var developerHit, similarHit bool
	routes := []routeFunc{
		func(req *http.Request) (mockResponse, bool) {
			if req.URL.Path == pathBatch && req.URL.Query().Get("rpcids") == "qnKhOb" {
				return mockResponse{Body: morePayload}, true
			}
			return mockResponse{}, false
		},
		// List batch is empty, and the HTML fallback yields nothing either.
		routeQuery(pathBatch, map[string]string{"rpcids": "vyAe2"},
			[]byte(")]}'\n\n[[\"wrb.fr\",\"vyAe2\",null,null,null,null,\"generic\"]]")),
		routePath(pathTop, emptyHTML),
		// ClusterURLs returns no clusters.
		func(req *http.Request) (mockResponse, bool) {
			if len(req.URL.Path) >= len(pathCategory) && req.URL.Path[:len(pathCategory)] == pathCategory {
				return mockResponse{Body: emptyHTML}, true
			}
			return mockResponse{}, false
		},
		routePath("/store/search", page1),
		// Details page for the Similar walk: serve the app page (carries a
		// Similar cluster link).
		func(req *http.Request) (mockResponse, bool) {
			if req.URL.Path == pathDetails {
				similarHit = true
				return mockResponse{Body: readFixture(t, "app_page.html")}, true
			}
			return mockResponse{}, false
		},
		routePath(pathCluster, readFixture(t, "similar_page.html")),
		func(req *http.Request) (mockResponse, bool) {
			if req.URL.Path == pathDev || req.URL.Path == pathDevName {
				developerHit = true
				return mockResponse{Body: readFixture(t, "developer_page.html")}, true
			}
			return mockResponse{}, false
		},
	}
	c := newMockClient(t, routes...)

	res, err := c.CategoryApps(context.Background(), CoverageOptions{
		Category:    CategoryGameAction,
		SearchTerms: []string{"shooter"},
		GraphDepth:  1,
		GraphSeeds:  5,
	})
	if err != nil {
		t.Fatalf("CategoryApps: %v", err)
	}
	if !similarHit {
		t.Error("similar walk never fired")
	}
	if !developerHit {
		t.Error("developer walk never fired despite a seed with DeveloperID")
	}
	if len(res.Apps) == 0 {
		t.Error("got 0 apps")
	}
}

// fakeFeedPaginator is an offline FeedPaginator for coverage tests: it returns a
// fixed batch and counts its invocations.
type fakeFeedPaginator struct {
	out  []SearchResult
	hits int
}

func (f *fakeFeedPaginator) PaginateFeed(_ context.Context, _ FeedRequest) ([]SearchResult, error) {
	f.hits++
	return f.out, nil
}

// TestBrowserFeedPhaseUnionsNewApps drives browserFeedPhase with a paginator
// that surfaces an app no other phase has. The phase must fetch the category
// page, run the paginator, and fold the new app into the union.
func TestBrowserFeedPhaseUnionsNewApps(t *testing.T) {
	// Category page carries one app in its grid; the browser scroll adds another.
	categoryPage := clusterHTMLPage(t, []string{"com.grid"}, "GAME")

	routes := []routeFunc{
		routeQuery(pathBatch, map[string]string{"rpcids": "qnKhOb"},
			[]byte(")]}'\n\n[[\"wrb.fr\",\"qnKhOb\",null,null,null,null,\"generic\"]]")),
		routeQuery(pathBatch, map[string]string{"rpcids": "vyAe2"}, readFixture(t, "list_vyae2.bin")),
		func(req *http.Request) (mockResponse, bool) {
			if len(req.URL.Path) >= len(pathCategory) && req.URL.Path[:len(pathCategory)] == pathCategory {
				return mockResponse{Body: categoryPage}, true
			}
			return mockResponse{}, false
		},
		routePathStatus(pathTop, http.StatusInternalServerError),
		routePath("/store/search", readFixture(t, "search_page.html")),
	}
	c := newMockClient(t, routes...)

	paginator := &fakeFeedPaginator{out: []SearchResult{
		{AppID: "com.grid"},                       // already in the grid — must not double-count
		{AppID: "com.browseronly", Title: "Deep"}, // new, only the browser scroll finds it
	}}

	res, err := c.CategoryApps(context.Background(), CoverageOptions{
		Category:             CategoryGameAction,
		SearchTerms:          []string{"shooter"},
		ClusterFeedMode:      FeedBrowser,
		ClusterFeedPaginator: paginator,
	})
	if err != nil {
		t.Fatalf("CategoryApps: %v", err)
	}

	if paginator.hits == 0 {
		t.Fatal("browser feed paginator never ran")
	}
	if !containsAppID(res.Apps, "com.browseronly") {
		t.Error("browser-only app missing from the union; browserFeedPhase did not contribute it")
	}
}

// TestBrowserFeedPhaseNilPaginatorNoOp confirms the phase is a no-op (no
// category browser fetch, no error) when no paginator is configured.
func TestBrowserFeedPhaseNilPaginatorNoOp(t *testing.T) {
	run := &coverageRun{
		client:  newMockClient(t),
		opts:    CoverageOptions{Category: CategoryGameAction, ClusterFeedMode: FeedBrowser},
		results: newResultSet(),
	}
	if err := run.browserFeedPhase(context.Background()); err != nil {
		t.Fatalf("browserFeedPhase with nil paginator: %v", err)
	}
	if run.requests != 0 {
		t.Errorf("nil-paginator phase made %d requests, want 0 (no-op)", run.requests)
	}
}

func containsAppID(rs []SearchResult, id string) bool {
	for _, r := range rs {
		if r.AppID == id {
			return true
		}
	}
	return false
}

// FullDetail enrichment fetches a batch now rather than a page per result, but
// the per-item contract is unchanged: an app the batch could not return keeps
// its original un-enriched value, in its own slot, and does not cost the rest
// of the listing.
func TestEnrichmentFallsBackPerItem(t *testing.T) {
	appRe := regexp.MustCompile(`\[\\"([^"\\]+)\\",7\]`)
	c := newMockClient(t, func(req *http.Request) (mockResponse, bool) {
		if req.URL.Path != pathBatch {
			return mockResponse{}, false
		}
		body, _ := io.ReadAll(req.Body)
		decoded, _ := url.QueryUnescape(strings.TrimPrefix(string(body), "f.req="))

		byIndex := map[string]string{}
		var order []string
		for i, m := range appRe.FindAllStringSubmatch(decoded, -1) {
			// com.broken gets the null payload Google returns for an id it
			// does not know; the others get a title.
			payload := minimalDS5("Enriched " + m[1])
			if m[1] == "com.broken" {
				payload = ""
			}
			byIndex[fmt.Sprint(i)] = payload
			order = append([]string{fmt.Sprint(i)}, order...) // reversed, as Google does
		}
		return mockResponse{Body: framesEnvelope("Ws7gDc", byIndex, order)}, true
	})

	results := []SearchResult{
		{AppID: "com.a", Title: "Original A"},
		{AppID: "com.broken", Title: "Original Broken"},
		{AppID: "com.b", Title: "Original B"},
	}
	got, err := c.enrichSearchResults(context.Background(), results, "en", "us")
	if err != nil {
		t.Fatalf("enrichSearchResults: %v", err)
	}
	if len(got) != len(results) {
		t.Fatalf("got %d results, want %d", len(got), len(results))
	}
	if got[1].Title != "Original Broken" {
		t.Errorf("the failed app was not left alone: Title = %q", got[1].Title)
	}
	for i, want := range map[int]string{0: "Enriched com.a", 2: "Enriched com.b"} {
		if got[i].Title != want {
			t.Errorf("slot %d Title = %q, want %q", i, got[i].Title, want)
		}
	}
}

// TestFindAppsInDataStructuralScan exercises the recursive findAppsInData scan
// used when the known fixed paths miss the apps grid.
func TestFindAppsInDataStructuralScan(t *testing.T) {
	// An app entry parseSearchResultNew recognizes: AppID at [0][0].
	app := func(id string) any {
		return []any{[]any{id}, nil, nil, "Title " + id}
	}
	// Bury the apps grid somewhere the fixed paths do not address.
	data := []any{
		"noise",
		[]any{
			[]any{
				[]any{app("com.a"), app("com.b"), app("com.c")},
			},
		},
	}
	got := findAppsInData(data)
	if len(got) != 3 {
		t.Fatalf("findAppsInData found %d apps, want 3", len(got))
	}
}
