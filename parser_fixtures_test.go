package googleplayscraper

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests run the parsers against captured live responses in testdata/,
// exercising the full parsing path with zero network access. They are the
// offline counterpart to the live integration tests and run under -short.
//
// Regenerate the fixtures with: go run ./internal/fixturegen

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func TestParseAppPageFixture(t *testing.T) {
	app, err := parseAppPage(readFixture(t, "app_page.html"), "com.google.android.apps.maps", "https://play.google.com/store/apps/details?id=com.google.android.apps.maps")
	if err != nil {
		t.Fatalf("parseAppPage: %v", err)
	}
	if app.Title != "Google Maps" {
		t.Errorf("Title: got %q, want %q", app.Title, "Google Maps")
	}
	if app.GenreID == "" {
		t.Error("GenreID is empty")
	}
}

// TestParseAppPageGameFixture asserts the fields that only populate on a rich
// game listing: trailer media, in-app-purchase range, ad support, recent changes
// and the EU DSA trader contact details. These are absent from the Maps fixture,
// so this fixture is the one that keeps their parse paths verifiably alive.
func TestParseAppPageGameFixture(t *testing.T) {
	app, err := parseAppPage(readFixture(t, "app_page_game.html"), "com.king.candycrushsaga", "https://play.google.com/store/apps/details?id=com.king.candycrushsaga")
	if err != nil {
		t.Fatalf("parseAppPage: %v", err)
	}

	if app.Title == "" {
		t.Error("Title is empty")
	}
	if !app.OffersIAP {
		t.Error("OffersIAP is false, want true")
	}
	if app.IAPRange == "" {
		t.Error("IAPRange is empty")
	}
	if !app.AdSupported {
		t.Error("AdSupported is false, want true")
	}
	if app.RecentChanges == "" {
		t.Error("RecentChanges is empty")
	}
	if app.Video == "" {
		t.Error("Video is empty")
	}
	if app.VideoImage == "" {
		t.Error("VideoImage is empty")
	}
	if app.PreviewVideo == "" {
		t.Error("PreviewVideo is empty")
	}
	if app.HeaderImage == "" {
		t.Error("HeaderImage is empty")
	}
	if app.DeveloperInternalID == "" {
		t.Error("DeveloperInternalID is empty")
	}
	if app.DeveloperLegalName == "" {
		t.Error("DeveloperLegalName is empty")
	}
	if app.DeveloperLegalEmail == "" {
		t.Error("DeveloperLegalEmail is empty")
	}
	if app.DeveloperLegalAddress == "" {
		t.Error("DeveloperLegalAddress is empty")
	}
	if strings.Contains(app.DeveloperLegalAddress, "\n") {
		t.Error("DeveloperLegalAddress still contains raw newlines")
	}
}

func TestParseSearchPageFixture(t *testing.T) {
	results, _, err := parseSearchPage(readFixture(t, "search_page.html"), 20)
	if err != nil {
		t.Fatalf("parseSearchPage: %v", err)
	}
	if len(results) < 10 {
		t.Fatalf("got %d results, want >= 10", len(results))
	}
	for i, r := range results {
		if r.AppID == "" {
			t.Errorf("result %d has empty AppID", i)
		}
	}
}

func TestParseListBatchFixture(t *testing.T) {
	data, err := decodeBatchEnvelope(readFixture(t, "list_vyae2.bin"))
	if err != nil {
		t.Fatalf("decodeBatchEnvelope: %v", err)
	}
	apps, ok := getPath(data, 0, 1, 0, 28, 0).([]interface{})
	if !ok {
		t.Fatal("apps array not found at [0][1][0][28][0]")
	}

	var results []SearchResult
	for _, app := range apps {
		if r := parseClusterListApp(app); r.AppID != "" {
			results = append(results, r)
		}
	}
	if len(results) < 50 {
		t.Fatalf("got %d apps, want >= 50", len(results))
	}
}

func TestParseListPageFixture(t *testing.T) {
	// The HTML fallback (listViaHTML -> parseListPage) is exercised here against
	// a captured /store/apps top-charts page so it stays verifiably alive: a
	// silent drift in Google's markup would surface as zero results here rather
	// than as a masked empty list in production.
	body := readFixture(t, "top_charts_page.html")
	results, err := parseListPage(body, ListOptions{Collection: CollectionTopFree, Num: 50})
	if err != nil {
		t.Fatalf("parseListPage: %v", err)
	}
	if len(results) < 10 {
		t.Fatalf("got %d apps, want >= 10", len(results))
	}
	for i, r := range results {
		if r.AppID == "" {
			t.Errorf("result %d has empty AppID", i)
		}
	}
}

func TestParseClusterURLsFixture(t *testing.T) {
	clusters := parseClusterURLs(readFixture(t, "category_page.html"))
	if len(clusters) < 1 {
		t.Fatalf("got %d clusters, want >= 1", len(clusters))
	}
	for i, c := range clusters {
		if c.URL == "" {
			t.Errorf("cluster %d has empty URL", i)
		}
	}
}

func TestParseClusterPageFixture(t *testing.T) {
	results, _, err := parseClusterPage(readFixture(t, "cluster_page.html"))
	if err != nil {
		t.Fatalf("parseClusterPage: %v", err)
	}
	if len(results) < 10 {
		t.Fatalf("got %d apps, want >= 10", len(results))
	}
	for i, r := range results {
		if r.AppID == "" {
			t.Errorf("result %d has empty AppID", i)
		}
	}
}

func TestParseReviewsFixture(t *testing.T) {
	result, err := parseReviewsResponse(readFixture(t, "reviews_batch.bin"), "com.google.android.apps.maps")
	if err != nil {
		t.Fatalf("parseReviewsResponse: %v", err)
	}
	if len(result.Reviews) < 1 {
		t.Fatal("got 0 reviews, want >= 1")
	}
	hasText := false
	for _, r := range result.Reviews {
		if r.ID == "" {
			t.Error("review has empty ID")
		}
		if r.Text != "" {
			hasText = true
		}
	}
	if !hasText {
		t.Error("no review carried any text")
	}
}

func TestParseDeveloperPageFixture(t *testing.T) {
	// The fixture uses a numeric developer ID, so Google serves the
	// /store/apps/dev layout (isNumericID == true).
	results, err := parseDeveloperPage(readFixture(t, "developer_page.html"), true, 60)
	if err != nil {
		t.Fatalf("parseDeveloperPage: %v", err)
	}
	if len(results) < 5 {
		t.Fatalf("got %d apps, want >= 5", len(results))
	}
	for i, r := range results {
		if r.AppID == "" {
			t.Errorf("result %d has empty AppID", i)
		}
	}
}

func TestParseSimilarPageFixture(t *testing.T) {
	results, err := parseSimilarPage(readFixture(t, "similar_page.html"))
	if err != nil {
		t.Fatalf("parseSimilarPage: %v", err)
	}
	if len(results) < 5 {
		t.Fatalf("got %d apps, want >= 5", len(results))
	}
	for i, r := range results {
		if r.AppID == "" {
			t.Errorf("result %d has empty AppID", i)
		}
	}
}

func TestParseDataSafetyPageFixture(t *testing.T) {
	ds, err := parseDataSafetyPage(readFixture(t, "datasafety_page.html"))
	if err != nil {
		t.Fatalf("parseDataSafetyPage: %v", err)
	}
	if ds == nil {
		t.Fatal("DataSafety is nil")
	}
	if len(ds.SharedData) == 0 && len(ds.CollectedData) == 0 && len(ds.SecurityPractices) == 0 && ds.PrivacyPolicyURL == "" {
		t.Error("DataSafety is entirely empty")
	}
}
