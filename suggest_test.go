package googleplayscraper

import (
	"context"
	"fmt"
	"strings"
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

func TestParseSuggestResponse(t *testing.T) {
	// Case 1: Invalid JSON (should fail)
	_, err := parseSuggestResponse([]byte("invalid-json"))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}

	// Case 2: Valid JSON but missing suggestions array structure
	// Expected: [0][0][0] = "suggestion"
	_, err = parseSuggestResponse([]byte(`[[]]`))
	// Depending on implementation, might return empty or error.
	// suggest.go:44 unmarshals to array.
	// If structures nested don't match, it usually returns nil suggestions without error or error if strict.

	// Case 3: Proper structure
	// parseSuggestResponse expects outer JSON: [["wrb.fr", "rpcId", "INNER_JSON_STRING", "generic"]]
	// INNER_JSON_STRING: [ [ ["suggestion1"], ["suggestion2"] ] ]
	// suggest.go:82 suggestions := getPath(data, 0, 0)

	innerJSON := `[[[["suggestion1"], ["suggestion2"]]]]`
	// We need to escape quotes in innerJSON for the outer JSON string
	validBody := fmt.Sprintf(`)]}'
[["wrb.fr","rpcId","%s","generic"]]`, strings.ReplaceAll(innerJSON, `"`, `\"`))

	suggestions, err := parseSuggestResponse([]byte(validBody))
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(suggestions) != 2 {
		t.Errorf("expected 2 suggestions, got %d", len(suggestions))
	}
	if len(suggestions) > 0 && suggestions[0] != "suggestion1" {
		t.Errorf("expected suggestion1, got %s", suggestions[0])
	}
}
