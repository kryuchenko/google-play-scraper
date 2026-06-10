package googleplayscraper

import (
	"context"
	"testing"
	"time"
)

// Note: Google Play web interface now shows curated sections
// rather than traditional Top Free/Paid/Grossing charts.
// The List function returns apps from these curated sections.

func TestListPopularApps(t *testing.T) {
	client := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	apps, err := client.List(ctx, ListOptions{
		Lang:    "en",
		Country: "us",
		Num:     10,
	})

	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	if len(apps) == 0 {
		t.Fatal("Expected at least one app")
	}

	t.Logf("Got %d popular apps", len(apps))

	for i, app := range apps {
		if i >= 5 {
			break
		}
		t.Logf("  %s (%s) - Score: %.1f", app.Title, app.AppID, app.Score)
	}

	// Verify first app has required fields
	first := apps[0]
	if first.AppID == "" {
		t.Error("First app missing AppID")
	}
	if first.Title == "" {
		t.Error("First app missing Title")
	}
}

func TestListDifferentSections(t *testing.T) {
	client := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Test different collection types (which map to different curated sections)
	collections := []Collection{CollectionTopFree, CollectionTopPaid, CollectionGrossing}

	for _, collection := range collections {
		apps, err := client.List(ctx, ListOptions{
			Collection: collection,
			Num:        5,
		})

		if err != nil {
			t.Fatalf("List(%s) error = %v", collection, err)
		}

		t.Logf("%s: Got %d apps", collection, len(apps))
		if len(apps) > 0 {
			t.Logf("  First: %s (%s)", apps[0].Title, apps[0].AppID)
		}
	}
}

func TestListAppsFromCategory(t *testing.T) {
	client := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	apps, err := client.List(ctx, ListOptions{
		Category: CategoryGame,
		Num:      10,
	})

	if err != nil {
		t.Fatalf("List(GAME) error = %v", err)
	}

	t.Logf("Got %d apps from games category", len(apps))

	for i, app := range apps {
		if i >= 5 {
			break
		}
		t.Logf("  %s (%s)", app.Title, app.AppID)
	}
}

func TestListDefaults(t *testing.T) {
	client := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Test with minimal options
	apps, err := client.List(ctx, ListOptions{})

	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	t.Logf("Got %d apps with default options", len(apps))

	if len(apps) == 0 {
		t.Fatal("Expected at least one app with defaults")
	}
}

func TestListLimitResults(t *testing.T) {
	client := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	apps, err := client.List(ctx, ListOptions{
		Num: 3,
	})

	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	if len(apps) > 3 {
		t.Errorf("Expected at most 3 apps, got %d", len(apps))
	}

	t.Logf("Got %d apps (limited to 3)", len(apps))
}

func TestAgeConstants(t *testing.T) {
	// Verify age constants have correct values
	if AgeAll != "" {
		t.Errorf("AgeAll should be empty, got %q", AgeAll)
	}
	if AgeFive != "AGE_RANGE1" {
		t.Errorf("AgeFive should be AGE_RANGE1, got %q", AgeFive)
	}
	if AgeSix != "AGE_RANGE2" {
		t.Errorf("AgeSix should be AGE_RANGE2, got %q", AgeSix)
	}
	if AgeNine != "AGE_RANGE3" {
		t.Errorf("AgeNine should be AGE_RANGE3, got %q", AgeNine)
	}
}

func TestListOptionsWithAge(t *testing.T) {
	// Test that Age option is correctly set
	opts := ListOptions{
		Age: AgeFive,
		Num: 10,
	}

	if opts.Age != AgeFive {
		t.Errorf("Age should be AgeFive, got %q", opts.Age)
	}
}

func TestListOptionsWithFullDetail(t *testing.T) {
	// Test that FullDetail option is correctly set
	opts := ListOptions{
		FullDetail: true,
		Num:        10,
	}

	if !opts.FullDetail {
		t.Error("FullDetail should be true")
	}
}

func TestParseListPage(t *testing.T) {
	// Case 1: Empty body
	res, err := parseListPage([]byte{}, ListOptions{Num: 10})
	// Expect nil, nil because regex won't match keys, loops finish, returns empty slice, no error
	if err != nil {
		t.Errorf("unexpected error for empty body: %v", err)
	}
	if len(res) != 0 {
		t.Error("expected 0 results")
	}

	// Case 2: Invalid JSON in data blocks
	// Should be ignored
	body := `<script>AF_initDataCallback({key: 'ds:3', isError: false , hash: '1', data: {invalid}});</script>`
	res, err = parseListPage([]byte(body), ListOptions{Num: 10})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(res) != 0 {
		t.Error("expected 0 results")
	}
}

func TestParseListApp(t *testing.T) {
	// Case 1: Valid app structure
	// format: [0,"AppID",... "Title",... ] - indices vary based on implementation

	// Real structure based on list.go:
	// AppID: [0][0]  <-- list.go:192: getPath(arr, 0, 0)
	// Title: [3]     <-- list.go:197: getPath(arr, 3)
	// Developer: [14] <-- list.go:207: getPath(arr, 14)

	// Constructing array
	item := make([]interface{}, 15) // need at least index 14
	item[3] = "Test Title"
	item[14] = "Dev Name"
	item[0] = []interface{}{"com.test.app"}

	res := parseListApp(item)
	if res.AppID != "com.test.app" {
		t.Errorf("expected com.test.app, got %q", res.AppID)
	}
	if res.Title != "Test Title" {
		t.Errorf("expected Test Title, got %q", res.Title)
	}

	// Case 2: Malformed input
	res2 := parseListApp("not-array")
	if res2.AppID != "" {
		t.Error("expected empty result for malformed input")
	}
}

func TestParseClusterListApp(t *testing.T) {
	// vyAe2 app entry: each field lives under index 0.
	// title [0,3], appId [0,0,0], icon [0,1,3,2], developer [0,14],
	// price [0,8,1,0,0], currency [0,8,1,0,1], url [0,10,4,2].
	// The entry's index 0 is one wide array holding every field.
	app := make([]interface{}, 15)
	app[0] = []interface{}{"com.test.app", float64(7)}
	app[1] = []interface{}{nil, nil, nil, []interface{}{nil, nil, "https://icon"}}
	app[3] = "Test App"
	app[4] = []interface{}{"4.5", 4.5}
	app[8] = []interface{}{nil, []interface{}{[]interface{}{float64(1990000), "USD"}}}
	app[10] = []interface{}{nil, nil, nil, nil, []interface{}{nil, nil, "/store/apps/details?id=com.test.app"}}
	app[13] = []interface{}{nil, "Some summary"}
	app[14] = "Test Dev"
	item := []interface{}{app}

	r := parseClusterListApp(item)
	if r.AppID != "com.test.app" {
		t.Errorf("AppID = %q, want com.test.app", r.AppID)
	}
	if r.Title != "Test App" {
		t.Errorf("Title = %q, want Test App", r.Title)
	}
	if r.Developer != "Test Dev" {
		t.Errorf("Developer = %q, want Test Dev", r.Developer)
	}
	if r.Icon != "https://icon" {
		t.Errorf("Icon = %q, want https://icon", r.Icon)
	}
	if r.Currency != "USD" {
		t.Errorf("Currency = %q, want USD", r.Currency)
	}
	if r.Price != 1.99 {
		t.Errorf("Price = %v, want 1.99", r.Price)
	}
	if r.Free {
		t.Error("Free should be false for a paid app")
	}
	if r.URL != BaseURL+"/store/apps/details?id=com.test.app" {
		t.Errorf("URL = %q, unexpected", r.URL)
	}

	// Free app: price 0 -> Free true.
	app[8] = []interface{}{nil, []interface{}{[]interface{}{float64(0), "USD"}}}
	if r := parseClusterListApp([]interface{}{app}); !r.Free || r.Price != 0 {
		t.Errorf("expected free app, got Free=%v Price=%v", r.Free, r.Price)
	}

	// Malformed input
	if r := parseClusterListApp("nope"); r.AppID != "" {
		t.Error("expected empty result for malformed input")
	}
}

// TestListBatchCollections exercises the vyAe2 RPC for every collection.
func TestListBatchCollections(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	client := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	for _, collection := range []Collection{CollectionTopFree, CollectionTopPaid, CollectionGrossing} {
		apps, err := client.List(ctx, ListOptions{
			Collection: collection,
			Category:   CategoryGame,
			Num:        500,
		})
		if err != nil {
			t.Fatalf("List(%s) error = %v", collection, err)
		}
		if len(apps) == 0 {
			t.Fatalf("List(%s) returned no apps", collection)
		}
		t.Logf("%s/GAME: %d apps, first: %s (%s)", collection, len(apps), apps[0].Title, apps[0].AppID)

		if apps[0].AppID == "" || apps[0].Title == "" {
			t.Errorf("%s: first app missing AppID/Title", collection)
		}
	}
}

// TestListAgeFilterIsNoOpOnBatch documents an empirically verified property of
// the vyAe2 batchexecute path: the "age" query parameter is ignored, so a
// filtered list is identical to an unfiltered one. This is a soft check — it
// logs the overlap and only fails if the two lists diverge, which would signal
// that Google started honouring the filter and the docs/godoc need revisiting.
// See ListOptions.Age for the full explanation.
func TestListAgeFilterIsNoOpOnBatch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	client := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	for _, cat := range []Category{CategoryFamily, CategoryGame} {
		base := ListOptions{Category: cat, Lang: "en", Country: "us", Num: 50}
		noAge, err := client.List(ctx, base)
		if err != nil {
			t.Fatalf("List(%s, no age) error = %v", cat, err)
		}
		filtered := base
		filtered.Age = AgeFive
		withAge, err := client.List(ctx, filtered)
		if err != nil {
			t.Fatalf("List(%s, AgeFive) error = %v", cat, err)
		}
		if len(noAge) == 0 || len(withAge) == 0 {
			t.Fatalf("List(%s) returned no apps (no-age=%d, age=%d)", cat, len(noAge), len(withAge))
		}

		seen := make(map[string]bool, len(noAge))
		for _, a := range noAge {
			seen[a.AppID] = true
		}
		overlap := 0
		for _, a := range withAge {
			if seen[a.AppID] {
				overlap++
			}
		}
		t.Logf("%s: no-age=%d age=%d overlap=%d", cat, len(noAge), len(withAge), overlap)

		if overlap != len(withAge) {
			t.Errorf("%s: Age filter appears to take effect on the batch path "+
				"(overlap %d of %d) — update ListOptions.Age godoc and README",
				cat, overlap, len(withAge))
		}
	}
}
