package googleplayscraper

import (
	"context"
	"testing"
)

func TestSuggestValidation(t *testing.T) {
	c := NewClient()
	_, err := c.Suggest(context.Background(), SuggestOptions{})
	if err == nil {
		t.Error("expected error for empty term")
	}
}

func TestSuggestIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	c := NewClient()
	suggestions, err := c.Suggest(context.Background(), SuggestOptions{
		Term: "whats",
	})

	if err != nil {
		t.Fatalf("Suggest failed: %v", err)
	}

	t.Logf("Got %d suggestions for 'whats'", len(suggestions))
	for _, s := range suggestions {
		t.Logf("  %s", s)
	}
}
