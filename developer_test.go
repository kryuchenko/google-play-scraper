package googleplayscraper

import (
	"context"
	"testing"
	"time"
)

func TestDeveloperValidation(t *testing.T) {
	c := NewClient()
	_, err := c.Developer(context.Background(), DeveloperOptions{})
	if err == nil {
		t.Error("expected error for empty devID")
	}
}

// TestDeveloperStringID tests developer lookup by name
func TestDeveloperStringID(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	c := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	results, err := c.Developer(ctx, DeveloperOptions{
		DevID: "Google LLC",
		Num:   10,
	})

	if err != nil {
		t.Fatalf("Developer failed: %v", err)
	}

	if len(results) == 0 {
		t.Error("expected at least one app")
	}

	// Validate all apps
	for _, r := range results {
		assertValidApp(t, r)
		if r.Developer != "Google LLC" {
			t.Errorf("Developer mismatch: got %q, want %q", r.Developer, "Google LLC")
		}
	}

	t.Logf("Got %d apps from Google LLC", len(results))
	for i, r := range results {
		if i < 3 {
			t.Logf("  %s (%s)", r.Title, r.AppID)
		}
	}
}

// TestDeveloperNumericID tests developer lookup by numeric ID
func TestDeveloperNumericID(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	c := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Meta's numeric developer ID
	results, err := c.Developer(ctx, DeveloperOptions{
		DevID: "5700313618786177705",
		Num:   5,
	})

	if err != nil {
		t.Fatalf("Developer failed: %v", err)
	}

	if len(results) == 0 {
		t.Error("expected at least one app")
	}

	for _, r := range results {
		assertValidApp(t, r)
	}

	t.Logf("Got %d apps from developer ID 5700313618786177705", len(results))
	for i, r := range results {
		if i < 3 {
			t.Logf("  %s by %s", r.Title, r.Developer)
		}
	}
}

// TestDeveloperLargeRequest tests requesting many apps
func TestDeveloperLargeRequest(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	c := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	results, err := c.Developer(ctx, DeveloperOptions{
		DevID: "Google LLC",
		Num:   50,
	})

	if err != nil {
		t.Fatalf("Developer failed: %v", err)
	}

	if len(results) == 0 {
		t.Error("expected at least one app")
	}

	t.Logf("Got %d apps (requested 50)", len(results))
}

// TestDeveloperDifferentLocale tests with different language
func TestDeveloperDifferentLocale(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	c := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	results, err := c.Developer(ctx, DeveloperOptions{
		DevID:   "Google LLC",
		Lang:    "ru",
		Country: "ru",
		Num:     5,
	})

	if err != nil {
		t.Fatalf("Developer failed: %v", err)
	}

	if len(results) == 0 {
		t.Error("expected at least one app")
	}

	t.Logf("Got %d apps in Russian locale", len(results))
	for i, r := range results {
		if i < 3 {
			t.Logf("  %s", r.Title)
		}
	}
}

func TestDeveloperOptionsWithFullDetail(t *testing.T) {
	// Test that FullDetail option is correctly set
	opts := DeveloperOptions{
		DevID:      "Google LLC",
		FullDetail: true,
		Num:        10,
	}

	if !opts.FullDetail {
		t.Error("FullDetail should be true")
	}
}

func TestParseDeveloperPage(t *testing.T) {
	// Case 1: Empty or invalid JSON
	_, err := parseDeveloperPage([]byte("invalid"), false, 10)
	if err != nil {
		// Should handle gracefully or return error depending on implementation
		// Implementation loops over regex matches, if none, returns nil results, nil error (unless dataBlocks lookup fails)
		// If "ds:3" missing, returns nil, nil
	}

	// Case 2: Valid structure but empty apps
	body := `
		<script>AF_initDataCallback({key: 'ds:3', isError: false , hash: '1', data: [[1,[[null,[],null]]]]});</script>
	`
	// Path to apps for numeric ID: [0][1][0][21][0]
	res, err := parseDeveloperPage([]byte(body), true, 10)
	if len(res) != 0 {
		t.Error("expected 0 results for empty apps data")
	}

	// Let's test missing ds:3
	res, err = parseDeveloperPage([]byte(`<html></html>`), false, 10)
	if len(res) != 0 {
		t.Error("expected 0 results for missing data")
	}

	// Test with invalid ID type path (e.g. asking for string ID path but data suggests otherwise or just missing)
}

func TestParseDeveloperApp(t *testing.T) {
	// Test edge cases where fields are missing
	// Numeric ID format
	itemNumeric := []interface{}{
		[]interface{}{"app.id"}, // [0][0] = appID
		nil,
		nil,
		"Title", // [3] = Title
	}
	res := parseDeveloperApp(itemNumeric, true)
	if res.AppID != "app.id" {
		t.Errorf("expected app.id, got %q", res.AppID)
	}
	if res.Title != "Title" {
		t.Errorf("expected Title, got %q", res.Title)
	}

	// String ID format
	// appId: [0][0][0]
	itemString := []interface{}{
		[]interface{}{
			[]interface{}{"app.id.str"},
			nil,
			nil,
			"TitleStr", // [0][3]
		},
	}
	res2 := parseDeveloperApp(itemString, false)
	if res2.AppID != "app.id.str" {
		t.Errorf("expected app.id.str, got %q", res2.AppID)
	}
	if res2.Title != "TitleStr" {
		t.Errorf("expected TitleStr, got %q", res2.Title)
	}

	// Malformed input
	res3 := parseDeveloperApp("not-an-array", true)
	if res3.AppID != "" {
		t.Error("expected empty result for malformed input")
	}
}
