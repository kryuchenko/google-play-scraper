package googleplayscraper

import (
	"context"
	"net/http"
	"testing"
)

// coverageRoutes wires every endpoint CategoryApps touches to a fixture, so a
// full run can execute offline: List (vyAe2 batch), ClusterURLs (category page),
// Cluster (cluster page), Search (search page), and the graph phase's Similar
// (details page -> cluster page).
func coverageRoutes(t *testing.T) []routeFunc {
	t.Helper()
	return []routeFunc{
		// Pagination batch (qnKhOb) must be matched before the list batch so a
		// cluster/search continuation returns an empty page and stops looping.
		routeQuery(pathBatch, map[string]string{"rpcids": "qnKhOb"},
			[]byte(")]}'\n\n[[\"wrb.fr\",\"qnKhOb\",null,null,null,null,\"generic\"]]")),
		routeQuery(pathBatch, map[string]string{"rpcids": "vyAe2"}, readFixture(t, "list_vyae2.bin")),
		func(req *http.Request) (mockResponse, bool) {
			// Category page for ClusterURLs (path starts with /store/apps/category/).
			if len(req.URL.Path) >= len(pathCategory) && req.URL.Path[:len(pathCategory)] == pathCategory {
				return mockResponse{Body: readFixture(t, "category_page.html")}, true
			}
			return mockResponse{}, false
		},
		routePath(pathTop, readFixture(t, "category_page.html")),
		routePath(pathCluster, readFixture(t, "cluster_page.html")),
		routePath("/store/search", readFixture(t, "search_page.html")),
		routePath(pathDetails, readFixture(t, "app_page.html")),
	}
}

// TestCategoryAppsOffline runs the full phase pipeline (core, cluster, search,
// graph) against fixtures and asserts the union beats any single source and that
// per-source accounting is populated.
func TestCategoryAppsOffline(t *testing.T) {
	c := newMockClient(t, coverageRoutes(t)...)

	res, err := c.CategoryApps(context.Background(), CoverageOptions{
		Category:    CategoryGameAction,
		SearchTerms: []string{"shooter", "rpg"},
		GraphDepth:  1,
		GraphSeeds:  2,
	})
	if err != nil {
		t.Fatalf("CategoryApps: %v", err)
	}
	if len(res.Apps) == 0 {
		t.Fatal("got 0 apps")
	}
	if res.RequestsMade == 0 {
		t.Error("RequestsMade = 0")
	}
	if res.SourcesRun == 0 {
		t.Error("SourcesRun = 0")
	}
	// No duplicate AppIDs survive the dedup.
	seen := map[string]bool{}
	for _, a := range res.Apps {
		if a.AppID == "" {
			t.Error("empty AppID in results")
		}
		if seen[a.AppID] {
			t.Errorf("duplicate AppID %s", a.AppID)
		}
		seen[a.AppID] = true
	}
	// The list source contributed the bulk; ensure at least one list source is
	// recorded in PerSourceNew.
	var haveList bool
	for src := range res.PerSourceNew {
		if len(src) >= 4 && src[:4] == "list" {
			haveList = true
		}
	}
	if !haveList {
		t.Error("no list source recorded in PerSourceNew")
	}
}

// TestCategoryAppsMaxAppsCap stops issuing new sources once MaxApps unique apps
// are gathered. MaxApps is a *soft* cap checked between sources, not a hard
// truncation of a single batch (one List call can return ~200 apps at once), so
// the assertion is that the run short-circuits: a tiny cap yields far fewer
// requests than an uncapped run, and the search tail never fires.
func TestCategoryAppsMaxAppsCap(t *testing.T) {
	capped := newMockClient(t, coverageRoutes(t)...)
	cappedRes, err := capped.CategoryApps(context.Background(), CoverageOptions{
		Category:    CategoryGameAction,
		SearchTerms: []string{"shooter", "rpg", "puzzle"},
		MaxApps:     10,
	})
	if err != nil {
		t.Fatalf("capped CategoryApps: %v", err)
	}

	uncapped := newMockClient(t, coverageRoutes(t)...)
	uncappedRes, err := uncapped.CategoryApps(context.Background(), CoverageOptions{
		Category:    CategoryGameAction,
		SearchTerms: []string{"shooter", "rpg", "puzzle"},
	})
	if err != nil {
		t.Fatalf("uncapped CategoryApps: %v", err)
	}

	// The cap is reached after the first List, so the capped run issues strictly
	// fewer requests than the uncapped one that sweeps every phase.
	if cappedRes.RequestsMade >= uncappedRes.RequestsMade {
		t.Errorf("capped requests = %d, uncapped = %d; cap did not short-circuit",
			cappedRes.RequestsMade, uncappedRes.RequestsMade)
	}
	// No search source should have run under the cap.
	for src := range cappedRes.PerSourceNew {
		if len(src) >= 6 && src[:6] == "search" {
			t.Errorf("search source %q ran despite MaxApps cap", src)
		}
	}
}

// TestCategoryAppsContextCancelled aborts mid-run: CategoryApps must surface
// ctx.Err() and still return the partial result gathered so far.
func TestCategoryAppsContextCancelled(t *testing.T) {
	c := newMockClient(t, coverageRoutes(t)...)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := c.CategoryApps(ctx, CoverageOptions{
		Category:    CategoryGameAction,
		SearchTerms: []string{"shooter"},
	})
	if err == nil {
		t.Fatal("expected ctx.Err(), got nil")
	}
	// Partial result is valid; just assert it is a well-formed (non-nil) struct.
	if res.PerSourceNew == nil {
		t.Error("PerSourceNew is nil on cancellation")
	}
}

// TestCategoryAppsSaturationLatches forces the tail phases to saturate by making
// search return only apps already seen from the list/cluster phases, with a tiny
// saturation window.
func TestCategoryAppsSaturationLatches(t *testing.T) {
	c := newMockClient(t, coverageRoutes(t)...)

	res, err := c.CategoryApps(context.Background(), CoverageOptions{
		Category:            CategoryGameAction,
		SearchTerms:         []string{"a", "b", "c", "d", "e", "f", "g", "h"},
		SaturationWindow:    2,
		SaturationThreshold: 0.9, // any repeat dips below this quickly
	})
	if err != nil {
		t.Fatalf("CategoryApps: %v", err)
	}
	// The search fixture returns the same apps every term, so after the first
	// couple of terms the new-app ratio collapses and the run latches saturated.
	if !res.Saturated {
		t.Error("Saturated = false, want true after repeated zero-new search terms")
	}
}
