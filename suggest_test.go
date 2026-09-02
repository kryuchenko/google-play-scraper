package googleplayscraper

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
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

	// Case 2: Valid JSON but missing suggestions array structure.
	// outer[0] has fewer than 3 elements, so parsing returns no suggestions
	// and no error.
	suggestions, err := parseSuggestResponse([]byte(`[[]]`))
	if err != nil {
		t.Errorf("unexpected error for missing suggestions structure: %v", err)
	}
	if len(suggestions) != 0 {
		t.Errorf("expected 0 suggestions, got %d", len(suggestions))
	}

	// Case 3: Proper structure
	// parseSuggestResponse expects outer JSON: [["wrb.fr", "rpcId", "INNER_JSON_STRING", "generic"]]
	// INNER_JSON_STRING: [ [ ["suggestion1"], ["suggestion2"] ] ]
	// suggest.go:82 suggestions := getPath(data, 0, 0)

	innerJSON := `[[[["suggestion1"], ["suggestion2"]]]]`
	// We need to escape quotes in innerJSON for the outer JSON string
	validBody := fmt.Sprintf(`)]}'
[["wrb.fr","rpcId","%s","generic"]]`, strings.ReplaceAll(innerJSON, `"`, `\"`))

	suggestions, err = parseSuggestResponse([]byte(validBody))
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

// Terms are packed into requests the same way apps are, and each term must get
// its own answer back regardless of the order Google returns them in.
func TestSuggestManyPacksTermsAndKeepsOrder(t *testing.T) {
	termRe := regexp.MustCompile(`\[\\"([^"\\]+)\\"\]`)
	var requests int
	c := newMockClient(t, func(req *http.Request) (mockResponse, bool) {
		if req.URL.Path != pathBatch {
			return mockResponse{}, false
		}
		requests++
		body, _ := io.ReadAll(req.Body)
		decoded, _ := url.QueryUnescape(strings.TrimPrefix(string(body), "f.req="))

		byIndex := map[string]string{}
		var order []string
		for i, m := range termRe.FindAllStringSubmatch(decoded, -1) {
			byIndex[fmt.Sprint(i)] = fmt.Sprintf(`[[[[%q]]]]`, "suggestion-for-"+m[1])
			order = append([]string{fmt.Sprint(i)}, order...) // reversed
		}
		return mockResponse{Body: framesEnvelope("IJ4APc", byIndex, order)}, true
	})

	var terms []string
	for i := range 70 {
		terms = append(terms, fmt.Sprintf("term%d", i))
	}

	got := c.SuggestMany(context.Background(), terms, SuggestOptions{})
	if requests != 3 {
		t.Errorf("made %d requests for 70 terms, want 3", requests)
	}
	for i, r := range got {
		if r.Err != nil {
			t.Fatalf("%s: %v", r.Term, r.Err)
		}
		if r.Term != terms[i] {
			t.Fatalf("result %d is for %q, want %q", i, r.Term, terms[i])
		}
		want := "suggestion-for-" + terms[i]
		if len(r.Suggestions) != 1 || r.Suggestions[0] != want {
			t.Errorf("%s got %v, want [%s] -- answers paired to the wrong term", r.Term, r.Suggestions, want)
		}
	}
}

// The live equality check: packing terms must change the request count and
// nothing else.
func TestSuggestManyMatchesIndividualFetches(t *testing.T) {
	if testing.Short() {
		t.Skip("network test")
	}
	terms := []string{"chess", "music", "maps", "photo editor", "language"}

	client := NewClient(WithThrottle(300 * time.Millisecond))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	one := make([][]string, len(terms))
	for i, term := range terms {
		s, err := client.Suggest(ctx, SuggestOptions{Term: term})
		if err != nil {
			t.Fatalf("%s: %v", term, err)
		}
		if len(s) == 0 {
			t.Fatalf("%q returned no suggestions; the comparison would be vacuous", term)
		}
		one[i] = s
	}

	many := client.SuggestMany(ctx, terms, SuggestOptions{})
	for i, r := range many {
		if r.Err != nil {
			t.Errorf("%s: %v", r.Term, r.Err)
			continue
		}
		// Autocomplete is not a stable function of the term: two requests
		// seconds apart legitimately return different tails, and demanding
		// equality makes this fail for reasons that have nothing to do with
		// batching. What must hold is that the batched answer is this term's
		// answer -- so require substantial overlap with the individual fetch,
		// which a mispaired result would not have.
		shared := 0
		for _, sug := range r.Suggestions {
			if slices.Contains(one[i], sug) {
				shared++
			}
		}
		n := max(len(one[i]), len(r.Suggestions))
		if n == 0 || float64(shared)/float64(n) < 0.5 {
			t.Errorf("%q: batched shares %d of %d suggestions with the individual fetch\n  one:  %v\n  many: %v",
				terms[i], shared, n, one[i], r.Suggestions)
		}
	}
}
