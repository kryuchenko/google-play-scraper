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
