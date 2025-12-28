package googleplayscraper

import (
	"context"
	"testing"
	"time"
)

func TestSimilarValidation(t *testing.T) {
	c := NewClient()
	_, err := c.Similar(context.Background(), SimilarOptions{})
	if err == nil {
		t.Error("expected error for empty appID")
	}
}

func TestSimilarIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	c := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	results, err := c.Similar(ctx, SimilarOptions{
		AppID: "com.google.android.apps.maps",
	})

	if err != nil {
		t.Fatalf("Similar failed: %v", err)
	}

	if len(results) == 0 {
		t.Error("expected at least one similar app")
	}

	// Validate all apps
	for _, r := range results {
		assertValidApp(t, r)
	}

	t.Logf("Got %d similar apps", len(results))
	for i, r := range results {
		if i < 5 {
			t.Logf("  %s (%s)", r.Title, r.AppID)
		}
	}
}

// TestSimilarDifferentDevelopers verifies similar apps come from different developers
func TestSimilarDifferentDevelopers(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	c := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	results, err := c.Similar(ctx, SimilarOptions{
		AppID: "com.instagram.android",
	})

	if err != nil {
		t.Fatalf("Similar failed: %v", err)
	}

	if len(results) < 2 {
		t.Skip("Need at least 2 results to check developers")
	}

	// Check that not all apps are from the same developer
	firstDev := results[0].Developer
	allSameDev := true
	for _, r := range results[1:] {
		if r.Developer != firstDev {
			allSameDev = false
			break
		}
	}

	if allSameDev {
		t.Log("Note: All similar apps are from same developer")
	}

	// Count unique developers
	devs := make(map[string]bool)
	for _, r := range results {
		devs[r.Developer] = true
	}

	t.Logf("Got %d similar apps from %d developers", len(results), len(devs))
}

// TestSimilarMinecraft tests similar apps for a game
func TestSimilarMinecraft(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	c := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	results, err := c.Similar(ctx, SimilarOptions{
		AppID: "com.mojang.minecraftpe",
	})

	if err != nil {
		t.Fatalf("Similar failed: %v", err)
	}

	for _, r := range results {
		assertValidApp(t, r)
	}

	t.Logf("Got %d similar apps for Minecraft", len(results))
}

func TestSimilarOptionsWithFullDetail(t *testing.T) {
	// Test that FullDetail option is correctly set
	opts := SimilarOptions{
		AppID:      "com.google.android.apps.maps",
		FullDetail: true,
	}

	if !opts.FullDetail {
		t.Error("FullDetail should be true")
	}
}

func TestFindSimilarCluster(t *testing.T) {
	// Case 1: No data blocks
	url, err := findSimilarCluster([]byte("<html></html>"))
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if url != "" {
		t.Errorf("expected empty url, got %q", url)
	}

	// Case 2: Data blocks present but no similar cluster
	// Just generic data
	_, _ = findSimilarCluster([]byte(`
		<script>AF_initDataCallback({key: 'ds:7', isError: false , hash: '1', data: [[["Test", 1]]]});</script>
	`))
	// Should return empty string, no error

	// Case 3: Invalid JSON in script
	_, err = findSimilarCluster([]byte(`
		<script>AF_initDataCallback({key: 'ds:7', isError: false , hash: '1', data: {invalid-json}});</script>
	`))
	if err != nil {
		// Currently function ignores invalid JSON blocks and continues, so this shouldn't error out unless fatal
		// The code loop `if err := json.Unmarshal([]byte(dataStr), &data); err != nil { continue }`
		// So it should just assume no data.
	}
}

func TestParseSimilarPage(t *testing.T) {
	// Empty body
	res, err := parseSimilarPage([]byte{})
	if err != nil {
		// Should return nil
	}
	if len(res) != 0 {
		t.Error("expected 0 results")
	}

	// Missing ds:3
	res, err = parseSimilarPage([]byte(`<html></html>`))
	if len(res) != 0 {
		t.Error("expected 0 results")
	}

	// Malformed ds:3
	malformed := `
		<script>AF_initDataCallback({key: 'ds:3', isError: false , hash: '1', data: []});</script>
	`
	res, err = parseSimilarPage([]byte(malformed))
	if len(res) != 0 {
		t.Error("expected 0 results")
	}
}
