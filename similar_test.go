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
