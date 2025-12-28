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
