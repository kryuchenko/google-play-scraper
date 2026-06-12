package googleplayscraper

import (
	"context"
	"encoding/json"
	"net/http"
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
	richRow := make([]interface{}, 13)
	richRow[2] = "Seed App"
	richRow[12] = []interface{}{"com.seed"}
	richRow[6] = []interface{}{[]interface{}{nil, nil, []interface{}{nil, []interface{}{"5.0", float64(5.0)}}}}
	richRow[4] = []interface{}{[]interface{}{[]interface{}{
		"Seed Dev",
		[]interface{}{nil, nil, nil, nil, []interface{}{nil, nil, "https://play.google.com/store/apps/dev?id=DEV9"}},
	}}}
	data := []interface{}{[]interface{}{[]interface{}{
		[]interface{}{richRow}, nil, nil, nil, nil, nil, nil,
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

// TestEnrichOneFallbackOnError verifies enrichOne keeps the original result when
// the App() detail fetch fails for that item.
func TestEnrichOneFallbackOnError(t *testing.T) {
	c := newMockClient(t, routePathStatus(pathDetails, http.StatusNotFound))

	original := SearchResult{AppID: "com.x", Title: "Original Title"}
	got := c.enrichOne(context.Background(), original, "en", "us")
	if got.Title != "Original Title" {
		t.Errorf("enrichOne fallback Title = %q, want Original Title", got.Title)
	}
}

// TestFindAppsInDataStructuralScan exercises the recursive findAppsInData scan
// used when the known fixed paths miss the apps grid.
func TestFindAppsInDataStructuralScan(t *testing.T) {
	// An app entry parseSearchResultNew recognizes: AppID at [0][0].
	app := func(id string) interface{} {
		return []interface{}{[]interface{}{id}, nil, nil, "Title " + id}
	}
	// Bury the apps grid somewhere the fixed paths do not address.
	data := []interface{}{
		"noise",
		[]interface{}{
			[]interface{}{
				[]interface{}{app("com.a"), app("com.b"), app("com.c")},
			},
		},
	}
	got := findAppsInData(data)
	if len(got) != 3 {
		t.Fatalf("findAppsInData found %d apps, want 3", len(got))
	}
}
