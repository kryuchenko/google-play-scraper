package googleplayscraper_test

import (
	"context"
	"testing"
	"time"

	googleplayscraper "github.com/kryuchenko/google-play-scraper"
)

// TestCategoryAppsBeatsCeiling proves the orchestrator unions many slices into
// a set far larger than the ~200-app per-request ceiling, with no duplicate app
// IDs. It is a network test and is skipped under -short.
func TestCategoryAppsBeatsCeiling(t *testing.T) {
	if testing.Short() {
		t.Skip("network test")
	}

	client := googleplayscraper.NewClient(googleplayscraper.WithThrottle(400 * time.Millisecond))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	result, err := client.CategoryApps(ctx, googleplayscraper.CoverageOptions{
		Category:   googleplayscraper.CategoryGameAction,
		Locales:    []googleplayscraper.Locale{{Country: "us", Lang: "en"}},
		GraphDepth: 0,
		MaxApps:    3000,
	})
	if err != nil {
		t.Fatalf("CategoryApps error: %v", err)
	}

	if result.RequestsMade == 0 {
		t.Error("RequestsMade = 0, expected the orchestrator to issue requests")
	}

	if len(result.Apps) <= 200 {
		t.Errorf("collected %d unique apps, want > 200 (must beat the per-request ceiling)", len(result.Apps))
	}

	seen := make(map[string]bool, len(result.Apps))
	for _, app := range result.Apps {
		if app.AppID == "" {
			t.Error("result contains an app with empty AppID")
			continue
		}
		if seen[app.AppID] {
			t.Errorf("duplicate AppID in results: %s", app.AppID)
		}
		seen[app.AppID] = true
	}

	t.Logf("CategoryApps: %d unique apps, %d sources, %d requests, saturated=%v",
		len(result.Apps), result.SourcesRun, result.RequestsMade, result.Saturated)
}
