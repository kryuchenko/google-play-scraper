package googleplayscraper

import (
	"context"
	"net/http"
	"testing"
)

// These tests drive the public orchestrators (App, Search, List, Cluster,
// Developer, Similar, DataSafety, Suggest, Permissions) end-to-end through the
// routing mock, against captured/synthesized fixtures. They cover the request
// construction, response handling and option defaulting that the parser-only
// fixture tests do not reach. No live network is used.

const (
	pathDetails  = "/store/apps/details"
	pathBatch    = "/_/PlayStoreUi/data/batchexecute"
	pathTop      = "/store/apps/top"
	pathCategory = "/store/apps/category/"
	pathDev      = "/store/apps/dev"
	pathDevName  = "/store/apps/developer"
	pathDataSafe = "/store/apps/datasafety"
	pathCluster  = "/store/apps/collection/cluster"
)

func TestAppOrchestrator(t *testing.T) {
	c := newMockClient(t, routePath(pathDetails, readFixture(t, "app_page.html")))

	app, err := c.App(context.Background(), "com.google.android.apps.maps", AppOptions{})
	if err != nil {
		t.Fatalf("App: %v", err)
	}
	if app.Title != "Google Maps" {
		t.Errorf("Title = %q, want Google Maps", app.Title)
	}
	if !app.Available {
		t.Error("Available = false, want true")
	}
	if app.AppID != "com.google.android.apps.maps" {
		t.Errorf("AppID = %q", app.AppID)
	}
}

func TestAppEmptyAppID(t *testing.T) {
	if _, err := NewClient().App(context.Background(), "", AppOptions{}); err == nil {
		t.Fatal("expected error for empty appID")
	}
}

func TestAppRequestError(t *testing.T) {
	c := newMockClient(t, routePathStatus(pathDetails, http.StatusNotFound))
	if _, err := c.App(context.Background(), "com.x", AppOptions{}); err == nil {
		t.Fatal("expected error when details page 404s")
	}
}

func TestSearchOrchestrator(t *testing.T) {
	c := newMockClient(t, routePath("/store/search", readFixture(t, "search_page.html")))

	results, err := c.Search(context.Background(), SearchOptions{Term: "maps", Num: 20})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("got 0 results")
	}
	for i, r := range results {
		if r.AppID == "" {
			t.Errorf("result %d has empty AppID", i)
		}
	}
}

func TestSearchEmptyTerm(t *testing.T) {
	if _, err := NewClient().Search(context.Background(), SearchOptions{}); err == nil {
		t.Fatal("expected error for empty term")
	}
}

func TestSearchFullDetailEnriches(t *testing.T) {
	// The search page yields results; FullDetail then fetches each app's details
	// page. The mock serves both endpoints, so enrichSearchResults/enrichOne run.
	c := newMockClient(t,
		routePath("/store/search", readFixture(t, "search_page.html")),
		routePath(pathDetails, readFixture(t, "app_page.html")),
	)

	results, err := c.Search(context.Background(), SearchOptions{Term: "maps", Num: 3, FullDetail: true})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("got 0 results")
	}
	// Every enriched result carries the fixture app's Title/Summary because each
	// detail fetch returns the Maps page.
	for i, r := range results {
		if r.Title != "Google Maps" {
			t.Errorf("result %d Title = %q, want Google Maps (enriched)", i, r.Title)
		}
	}
}

func TestListViaBatch(t *testing.T) {
	// listViaBatch posts to batchexecute with rpcids=vyAe2 and parses list_vyae2.bin.
	c := newMockClient(t,
		routeQuery(pathBatch, map[string]string{"rpcids": "vyAe2"}, readFixture(t, "list_vyae2.bin")),
	)

	results, err := c.List(context.Background(), ListOptions{Collection: CollectionTopFree, Num: 50})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(results) < 50 {
		t.Fatalf("got %d apps, want >= 50", len(results))
	}
	for i, r := range results {
		if r.AppID == "" {
			t.Errorf("result %d has empty AppID", i)
		}
	}
}

func TestListFallsBackToHTML(t *testing.T) {
	// The batch RPC returns an empty (null-payload) envelope, so List falls back
	// to listViaHTML against the top-charts page.
	emptyBatch := []byte(")]}'\n\n[[\"wrb.fr\",\"vyAe2\",null,null,null,null,\"generic\"]]")
	c := newMockClient(t,
		routeQuery(pathBatch, map[string]string{"rpcids": "vyAe2"}, emptyBatch),
		routePath(pathTop, readFixture(t, "top_charts_page.html")),
	)

	results, err := c.List(context.Background(), ListOptions{Collection: CollectionTopFree, Num: 50})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(results) < 10 {
		t.Fatalf("HTML fallback got %d apps, want >= 10", len(results))
	}
}

func TestListUnknownCollection(t *testing.T) {
	if _, err := NewClient().List(context.Background(), ListOptions{Collection: Collection("BOGUS")}); err == nil {
		t.Fatal("expected error for unknown collection")
	}
}

func TestClusterURLsOrchestrator(t *testing.T) {
	c := newMockClient(t, routePath(pathCategory+"GAME", readFixture(t, "category_page.html")))

	clusters, err := c.ClusterURLs(context.Background(), ClusterURLsOptions{Category: "GAME"})
	if err != nil {
		t.Fatalf("ClusterURLs: %v", err)
	}
	if len(clusters) == 0 {
		t.Fatal("got 0 clusters")
	}
	for i, cl := range clusters {
		if cl.URL == "" {
			t.Errorf("cluster %d has empty URL", i)
		}
	}
}

func TestClusterURLsDefaultsToTop(t *testing.T) {
	// With no Category, ClusterURLs probes /store/apps/top instead.
	c := newMockClient(t, routePath(pathTop, readFixture(t, "category_page.html")))
	if _, err := c.ClusterURLs(context.Background(), ClusterURLsOptions{}); err != nil {
		t.Fatalf("ClusterURLs: %v", err)
	}
}

func TestClusterOrchestrator(t *testing.T) {
	c := newMockClient(t, routePath(pathCluster, readFixture(t, "cluster_page.html")))

	results, err := c.Cluster(context.Background(), ClusterOptions{Path: "/store/apps/collection/cluster?x=1", Num: 10})
	if err != nil {
		t.Fatalf("Cluster: %v", err)
	}
	if len(results) != 10 {
		t.Fatalf("got %d apps, want 10 (trimmed to Num)", len(results))
	}
}

func TestClusterEmptyPath(t *testing.T) {
	if _, err := NewClient().Cluster(context.Background(), ClusterOptions{}); err == nil {
		t.Fatal("expected error for empty cluster path")
	}
}

func TestDeveloperNumericIDOffline(t *testing.T) {
	// A numeric DevID routes to /store/apps/dev and parses the developer fixture.
	c := newMockClient(t, routePath(pathDev, readFixture(t, "developer_page.html")))

	results, err := c.Developer(context.Background(), DeveloperOptions{DevID: "5700313618786177705"})
	if err != nil {
		t.Fatalf("Developer: %v", err)
	}
	if len(results) < 5 {
		t.Fatalf("got %d apps, want >= 5", len(results))
	}
}

func TestDeveloperNameRoutesOffline(t *testing.T) {
	// A non-numeric DevID routes to /store/apps/developer. The developer fixture
	// is the numeric layout, so parsing yields nothing — but the path selection
	// and request flow are what we exercise here.
	c := newMockClient(t, routePath(pathDevName, readFixture(t, "developer_page.html")))
	if _, err := c.Developer(context.Background(), DeveloperOptions{DevID: "Google LLC"}); err != nil {
		t.Fatalf("Developer: %v", err)
	}
}

func TestDeveloperEmptyID(t *testing.T) {
	if _, err := NewClient().Developer(context.Background(), DeveloperOptions{}); err == nil {
		t.Fatal("expected error for empty DevID")
	}
}

func TestSimilarOrchestrator(t *testing.T) {
	// Similar fetches the app page (to find the cluster URL), then the cluster
	// page. app_page.html carries a Similar cluster link; similar_page.html holds
	// the apps. Both arrive on distinct paths.
	c := newMockClient(t,
		routePath(pathDetails, readFixture(t, "app_page.html")),
		routePath(pathCluster, readFixture(t, "similar_page.html")),
	)

	results, err := c.Similar(context.Background(), SimilarOptions{AppID: "com.google.android.apps.maps"})
	if err != nil {
		t.Fatalf("Similar: %v", err)
	}
	if len(results) < 5 {
		t.Fatalf("got %d apps, want >= 5", len(results))
	}
}

func TestSimilarClusterNotFound(t *testing.T) {
	// An app page with no Similar cluster yields a clear error.
	page := htmlWithDataBlocks(map[string]string{
		"ds:5": `[[null,[null,null,[["com.x",7]]]]]`,
	})
	c := newMockClient(t, routePath(pathDetails, page))
	if _, err := c.Similar(context.Background(), SimilarOptions{AppID: "com.x"}); err == nil {
		t.Fatal("expected 'similar apps not found' error")
	}
}

func TestSimilarEmptyAppID(t *testing.T) {
	if _, err := NewClient().Similar(context.Background(), SimilarOptions{}); err == nil {
		t.Fatal("expected error for empty appID")
	}
}

func TestDataSafetyOrchestrator(t *testing.T) {
	c := newMockClient(t, routePath(pathDataSafe, readFixture(t, "datasafety_page.html")))

	ds, err := c.DataSafety(context.Background(), DataSafetyOptions{AppID: "com.google.android.apps.maps"})
	if err != nil {
		t.Fatalf("DataSafety: %v", err)
	}
	if ds == nil {
		t.Fatal("DataSafety is nil")
	}
	if len(ds.SharedData) == 0 && len(ds.CollectedData) == 0 && len(ds.SecurityPractices) == 0 && ds.PrivacyPolicyURL == "" {
		t.Error("DataSafety is entirely empty")
	}
}

func TestDataSafetyEmptyAppID(t *testing.T) {
	if _, err := NewClient().DataSafety(context.Background(), DataSafetyOptions{}); err == nil {
		t.Fatal("expected error for empty appID")
	}
}

func TestSuggestOrchestrator(t *testing.T) {
	c := newMockClient(t,
		routeQuery(pathBatch, map[string]string{"rpcids": "IJ4APc"}, readFixture(t, "suggest_batch.bin")),
	)

	got, err := c.Suggest(context.Background(), SuggestOptions{Term: "mine"})
	if err != nil {
		t.Fatalf("Suggest: %v", err)
	}
	want := []string{"minecraft", "minecraft pe", "minecraft mod", "roblox"}
	if len(got) != len(want) {
		t.Fatalf("got %d suggestions, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("suggestion %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSuggestEmptyTerm(t *testing.T) {
	if _, err := NewClient().Suggest(context.Background(), SuggestOptions{}); err == nil {
		t.Fatal("expected error for empty term")
	}
}

func TestPermissionsOrchestrator(t *testing.T) {
	c := newMockClient(t,
		routeQuery(pathBatch, map[string]string{"rpcids": "xdSrCf"}, readFixture(t, "permissions_batch.bin")),
	)

	perms, err := c.Permissions(context.Background(), PermissionsOptions{AppID: "com.x"})
	if err != nil {
		t.Fatalf("Permissions: %v", err)
	}
	if len(perms) != 5 {
		t.Fatalf("got %d permissions, want 5", len(perms))
	}
	// The first group carries an explicit type name; the "other" group falls back
	// to the default "Other" type.
	var haveLocation, haveOther bool
	for _, p := range perms {
		if p.Type == "Location" {
			haveLocation = true
		}
		if p.Type == "Other" {
			haveOther = true
		}
	}
	if !haveLocation {
		t.Error("expected a permission with Type=Location")
	}
	if !haveOther {
		t.Error("expected a permission with the default Type=Other")
	}
}

func TestPermissionsEmptyAppID(t *testing.T) {
	if _, err := NewClient().Permissions(context.Background(), PermissionsOptions{}); err == nil {
		t.Fatal("expected error for empty appID")
	}
}
