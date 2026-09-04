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

// A term with no suggestions and a term whose answer never arrived are
// different facts, and only the first is one about the term. Read as the same
// thing, a dropped response makes a live keyword look dead.
func TestSuggestDroppedFrameIsAnError(t *testing.T) {
	c := newMockClient(t, routePath(pathBatch, droppedFrame()))

	got, err := c.Suggest(context.Background(), SuggestOptions{Term: "chess"})
	if err == nil {
		t.Fatal("a response carrying no frame was reported as no suggestions")
	}
	if !strings.Contains(err.Error(), "chess") {
		t.Errorf("error %q does not name the term it is about", err)
	}
	if got != nil {
		t.Errorf("got %v alongside the error, want nil", got)
	}
}

// A present frame with a null payload is the store answering "nothing for this
// term", which is data. The two cases have to stay apart, so this pins the
// other side of the same rule.
func TestSuggestPresentButEmptyFrameIsNoSuggestions(t *testing.T) {
	c := newMockClient(t, routePath(pathBatch,
		framesEnvelope("IJ4APc", map[string]string{"0": ""}, []string{"0"})))

	got, err := c.Suggest(context.Background(), SuggestOptions{Term: "zzqqxx"})
	if err != nil {
		t.Fatalf("an answered term was reported as a failure: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want no suggestions", got)
	}
}

// In a batch the failure has to stay in its own slot: a missing frame must not
// shift its neighbours' answers, which is what pairing by index is for.
func TestSuggestManyMissingFrameIsPerTermError(t *testing.T) {
	c := newMockClient(t, routePath(pathBatch,
		framesEnvelope("IJ4APc", map[string]string{"1": `[[[["a"]]]]`}, []string{"1"})))

	got := c.SuggestMany(context.Background(), []string{"gone", "kept"}, SuggestOptions{})
	if len(got) != 2 {
		t.Fatalf("got %d results, want 2", len(got))
	}
	if got[0].Err == nil {
		t.Error("the term whose frame never arrived was reported as having no suggestions")
	}
	if got[1].Err != nil {
		t.Fatalf("the answered term failed: %v", got[1].Err)
	}
	if len(got[1].Suggestions) != 1 || got[1].Suggestions[0] != "a" {
		t.Errorf("kept got %v, want [a]: a missing frame shifted its neighbour", got[1].Suggestions)
	}
}

// The same rule for permissions, where a dropped frame used to be reported as
// "no data returned for X" -- a statement about the app, made on the strength
// of a short response.
func TestPermissionsDroppedFrameIsNotAMissingApp(t *testing.T) {
	c := newMockClient(t, routePath(pathBatch, droppedFrame()))

	if _, err := c.Permissions(context.Background(), PermissionsOptions{AppID: "com.x"}); err == nil {
		t.Fatal("a response carrying no frame was accepted")
	} else if !strings.Contains(err.Error(), "no frame") {
		t.Errorf("error %q reads as the app's own answer, not as a missing frame", err)
	}

	got := c.PermissionsMany(context.Background(), []string{"com.a", "com.b"}, PermissionsOptions{})
	for _, r := range got {
		if r.Err == nil {
			t.Errorf("%s: a dropped frame was accepted as an answer", r.AppID)
			continue
		}
		if !strings.Contains(r.Err.Error(), "no frame") {
			t.Errorf("%s: error %q reads as the app's own answer", r.AppID, r.Err)
		}
	}
}
