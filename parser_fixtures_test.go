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
	// The Maps fixture's [18] node is [2], so Available must be true. This pins
	// the fix that replaced the old hardcoded Available=true with a value derived
	// from the availability node.
	if !app.Available {
		t.Error("Available is false, want true (Maps fixture [18]=[2])")
	}
}

// TestParseAppPageUnavailableFixture checks the region-locked fixture: a 200 page
// whose [18] node is empty must parse as Available=false. It is the negative
// counterpart that proves Available is no longer a constant.
func TestParseAppPageUnavailableFixture(t *testing.T) {
	app, err := parseAppPage(readFixture(t, "app_unavailable_region.html"), "com.vzw.hss.myverizon", "https://play.google.com/store/apps/details?id=com.vzw.hss.myverizon")
	if err != nil {
		t.Fatalf("parseAppPage: %v", err)
	}
	if app.Available {
		t.Error("Available is true, want false (region-locked fixture [18]=[])")
	}
}

// TestParseAppPageGameFixture asserts the fields that only populate on a rich
// game listing: trailer media, in-app-purchase range, ad support, recent changes
// and the EU DSA trader contact details. These are absent from the Maps fixture,
// so this fixture is the one that keeps their parse paths verifiably alive.
// The region-locked fixture is a developer with a name-based listing link
// (/store/apps/developer?id=Verizon+Consumer+Group): DeveloperID is the name
// Developer takes, DeveloperNumericID the numeric id Google keeps for them.
func TestParseAppPageNamedDeveloper(t *testing.T) {
	app, err := parseAppPage(readFixture(t, "app_unavailable_region.html"), "fixture", "https://play.google.com/store/apps/details?id=fixture")
	if err != nil {
		t.Fatalf("parseAppPage: %v", err)
	}
	if app.DeveloperID != "Verizon Consumer Group" {
		t.Errorf("DeveloperID = %q, want the decoded name Verizon Consumer Group", app.DeveloperID)
	}
	if app.DeveloperNumericID != "8204699366240019704" {
		t.Errorf("DeveloperNumericID = %q, want 8204699366240019704", app.DeveloperNumericID)
	}
}

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
	if !strings.HasPrefix(app.VideoImage, "https://i.ytimg.com/") {
		t.Errorf("VideoImage = %q, want the i.ytimg.com trailer thumbnail", app.VideoImage)
	}
	if app.VideoImage == app.HeaderImage {
		t.Error("VideoImage repeats HeaderImage")
	}
	if app.DeveloperID != "6577204690045492686" {
		t.Errorf("DeveloperID = %q, want the bare numeric id 6577204690045492686", app.DeveloperID)
	}
	if app.DeveloperNumericID != "6577204690045492686" {
		t.Errorf("DeveloperNumericID = %q, want 6577204690045492686", app.DeveloperNumericID)
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

// The search fixture ("minecraft") shows Minecraft as a featured card above the
// grid. It must come first and appear once.
func TestParseSearchPageFixtureFeatured(t *testing.T) {
	results, _, err := parseSearchPage(readFixture(t, "search_page.html"), 20)
	if err != nil {
		t.Fatalf("parseSearchPage: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("no results")
	}
	got := results[0]
	want := SearchResult{
		AppID:     "com.mojang.minecraftpe",
		Title:     "Minecraft: Dream it, Build it!",
		Developer: "Mojang",
		Currency:  "USD",
		Price:     6.99,
		URL:       "https://play.google.com/store/apps/details?id=com.mojang.minecraftpe",
	}
	if got.AppID != want.AppID || got.Title != want.Title || got.Developer != want.Developer ||
		got.Currency != want.Currency || got.Price != want.Price || got.Free || got.URL != want.URL {
		t.Errorf("featured result = %+v, want %+v", got, want)
	}
	if got.Icon == "" || got.ScoreText == "" || got.Score == 0 {
		t.Errorf("featured result missing icon or score: %+v", got)
	}
	for i, r := range results[1:] {
		if r.AppID == want.AppID {
			t.Errorf("featured app repeated at %d", i+1)
		}
	}
}

func TestParseFeaturedResultAbsent(t *testing.T) {
	if r, ok := parseFeaturedResult([]any{nil, []any{nil, []any{}}}); ok {
		t.Errorf("parseFeaturedResult on a page without a card = %+v, want none", r)
	}
}

func TestAppIDFromDetailsURL(t *testing.T) {
	tests := map[string]string{
		"https://play.google.com/store/apps/details?id=com.netease.idv.googleplay":   "com.netease.idv.googleplay",
		"https://play.google.com/store/apps/details?id=sinet.startup.inDriver&hl=en": "sinet.startup.inDriver",
		"/store/apps/details?id=com.example":                                         "com.example",
		"https://play.google.com/store/apps/developer?id=NetEase+Games":              "",
		"": "",
	}
	for in, want := range tests {
		if got := appIDFromDetailsURL(in); got != want {
			t.Errorf("appIDFromDetailsURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseListBatchFixture(t *testing.T) {
	data, err := decodeBatchEnvelope(readFixture(t, "list_vyae2.bin"))
	if err != nil {
		t.Fatalf("decodeBatchEnvelope: %v", err)
	}
	apps, ok := getPath(data, 0, 1, 0, 28, 0).([]any)
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

func TestParseQnKhObFixture(t *testing.T) {
	// One real qnKhOb feed response (GAME_ACTION). Proves the apps array at
	// [0][21][0] is extracted offline; a drift there surfaces here rather than as
	// a silent empty page in production. The token at [0][3][0] is the dead echo
	// token (asserted for response-shape drift only — pagination does not use it).
	data, err := decodeBatchEnvelope(readFixture(t, "qnkhob_page.bin"))
	if err != nil {
		t.Fatalf("decodeBatchEnvelope: %v", err)
	}

	results, echoToken := parseQnKhObResponse(data)
	if len(results) < 1 {
		t.Fatalf("got %d apps, want >= 1", len(results))
	}
	for i, r := range results {
		if r.AppID == "" {
			t.Errorf("result %d has empty AppID", i)
		}
	}
	if echoToken == "" {
		t.Error("echo token empty — response-shape path [0][3][0] may have changed")
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
	results, err := parseClusterPage(readFixture(t, "cluster_page.html"))
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
