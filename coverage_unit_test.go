package googleplayscraper

import (
	"reflect"
	"testing"
)

// These tests drive the coverage bookkeeping helpers that resultset_test.go does
// not already cover: withCoverageDefaults, coverageRun.record/result, capReached,
// topUnseededSeeds, sourceLabel and defaultSearchTerms. They run entirely on
// hand-built state with no network access.

func TestWithCoverageDefaults(t *testing.T) {
	in := CoverageOptions{Category: CategoryGameAction}
	got := withCoverageDefaults(in)

	if !reflect.DeepEqual(got.Collections, []Collection{CollectionTopFree, CollectionTopPaid, CollectionGrossing}) {
		t.Errorf("Collections default = %v", got.Collections)
	}
	if !reflect.DeepEqual(got.Locales, []Locale{{Country: "us", Lang: "en"}}) {
		t.Errorf("Locales default = %v", got.Locales)
	}
	if len(got.SearchTerms) == 0 {
		t.Error("SearchTerms default is empty for GAME_ACTION")
	}
	if got.GraphSeeds != defaultGraphSeeds {
		t.Errorf("GraphSeeds = %d, want %d", got.GraphSeeds, defaultGraphSeeds)
	}
	if got.MaxApps != defaultMaxApps {
		t.Errorf("MaxApps = %d, want %d", got.MaxApps, defaultMaxApps)
	}
	if got.SaturationWindow != defaultSaturationWindow {
		t.Errorf("SaturationWindow = %d, want %d", got.SaturationWindow, defaultSaturationWindow)
	}
	if got.SaturationThreshold != defaultSaturationThreshold {
		t.Errorf("SaturationThreshold = %v, want %v", got.SaturationThreshold, defaultSaturationThreshold)
	}

	// The caller's empty input must not be mutated.
	if in.Collections != nil || in.Locales != nil || in.MaxApps != 0 {
		t.Error("withCoverageDefaults mutated the caller's options")
	}
}

func TestWithCoverageDefaultsNegativeMaxAppsKept(t *testing.T) {
	// A negative MaxApps means "unlimited" and must be preserved, not defaulted.
	got := withCoverageDefaults(CoverageOptions{MaxApps: -1})
	if got.MaxApps != -1 {
		t.Errorf("MaxApps = %d, want -1 (unlimited preserved)", got.MaxApps)
	}
}

func TestSourceLabel(t *testing.T) {
	loc := Locale{Country: "us", Lang: "en"}
	tests := []struct {
		kind, detail, extra string
		want                string
	}{
		{"list", "TOP_FREE", "", "list:TOP_FREE@us/en"},
		{"list", "TOP_FREE", "AGE_RANGE1", "list:TOP_FREE:AGE_RANGE1@us/en"},
		{"search:all", "shooter", "", "search:all:shooter@us/en"},
		{"clusterurls", "", "", "clusterurls@us/en"},
	}
	for _, tt := range tests {
		if got := sourceLabel(tt.kind, tt.detail, loc, tt.extra); got != tt.want {
			t.Errorf("sourceLabel(%q,%q,_,%q) = %q, want %q", tt.kind, tt.detail, tt.extra, got, tt.want)
		}
	}
}

// newTestRun builds a coverageRun with defaults applied, ready to feed record().
func newTestRun(opts CoverageOptions) *coverageRun {
	return &coverageRun{
		opts:    withCoverageDefaults(opts),
		results: newResultSet(),
	}
}

func sr(id string, score float64) SearchResult {
	return SearchResult{AppID: id, Score: score}
}

func TestRecordCountsNewDedupAndResult(t *testing.T) {
	run := newTestRun(CoverageOptions{})
	run.record("s1", []SearchResult{sr("a", 1), sr("b", 2)}, nil, false)
	run.record("s2", []SearchResult{sr("b", 2), sr("c", 3)}, nil, false) // b is a dup

	res := run.result()
	if got := len(res.Apps); got != 3 {
		t.Errorf("unique apps = %d, want 3", got)
	}
	if res.RequestsMade != 2 {
		t.Errorf("RequestsMade = %d, want 2", res.RequestsMade)
	}
	if res.SourcesRun != 2 {
		t.Errorf("SourcesRun = %d, want 2", res.SourcesRun)
	}
	if res.PerSourceNew["s1"] != 2 {
		t.Errorf("PerSourceNew[s1] = %d, want 2", res.PerSourceNew["s1"])
	}
	if res.PerSourceNew["s2"] != 1 {
		t.Errorf("PerSourceNew[s2] = %d, want 1 (b was a dup)", res.PerSourceNew["s2"])
	}
}

func TestRecordErrorNotesSourceWithZero(t *testing.T) {
	run := newTestRun(CoverageOptions{})
	run.record("failed", nil, &StatusError{Code: 500}, false)

	res := run.result()
	if v, ok := res.PerSourceNew["failed"]; !ok || v != 0 {
		t.Errorf("errored source PerSourceNew = (%d,%v), want (0,true)", v, ok)
	}
	if len(res.Apps) != 0 {
		t.Errorf("apps = %d, want 0 on error", len(res.Apps))
	}
	// An errored source still counts as a request attempted.
	if res.RequestsMade != 1 {
		t.Errorf("RequestsMade = %d, want 1", res.RequestsMade)
	}
}

func TestCapReached(t *testing.T) {
	run := newTestRun(CoverageOptions{MaxApps: 2})
	if run.capReached() {
		t.Fatal("cap reached with empty result set")
	}
	run.record("s", []SearchResult{sr("a", 1)}, nil, false)
	if run.capReached() {
		t.Fatal("cap reached at 1 of 2")
	}
	run.record("s", []SearchResult{sr("b", 1)}, nil, false)
	if !run.capReached() {
		t.Fatal("cap not reached at 2 of 2")
	}

	// MaxApps < 0 means unlimited: never reached.
	unlimited := newTestRun(CoverageOptions{MaxApps: -1})
	unlimited.record("s", []SearchResult{sr("a", 1), sr("b", 1)}, nil, false)
	if unlimited.capReached() {
		t.Error("unlimited run reported cap reached")
	}
}

func TestTopUnseededSeeds(t *testing.T) {
	run := newTestRun(CoverageOptions{})
	run.seeded = map[string]bool{"b": true}
	run.record("s", []SearchResult{sr("a", 4.5), sr("b", 4.9), sr("c", 3.0), sr("d", 4.0)}, nil, false)

	seeds := run.topUnseededSeeds(2)
	if len(seeds) != 2 {
		t.Fatalf("got %d seeds, want 2", len(seeds))
	}
	// Highest score first, excluding already-seeded "b".
	if seeds[0].AppID != "a" || seeds[1].AppID != "d" {
		t.Errorf("seeds = [%s,%s], want [a,d]", seeds[0].AppID, seeds[1].AppID)
	}
}

func TestDefaultSearchTerms(t *testing.T) {
	if terms := defaultSearchTerms(CategoryGamePuzzle); len(terms) == 0 {
		t.Error("known category GAME_PUZZLE returned no terms")
	}
	if terms := defaultSearchTerms(Category("NO_SUCH_CATEGORY")); terms != nil {
		t.Errorf("unknown category returned %v, want nil", terms)
	}
}

// The review language list is measured, not copied from a locale table, and
// two properties have to hold or a multi-language read wastes requests and
// misses corpora.
//
// Aliases are the waste: tg and tk are served the Russian corpus verbatim, ga
// and cy the English one, so including them costs requests and returns nothing
// new. Kazakh is the miss: the first version of this list skipped it, which
// made "all languages" silently exclude an entire country's reviews.
func TestReviewLanguagesHasNoAliasesAndCoversCentralAsia(t *testing.T) {
	seen := map[string]int{}
	for _, l := range ReviewLanguages {
		seen[l]++
	}
	for l, n := range seen {
		if n > 1 {
			t.Errorf("%q appears %d times; the union would fetch it twice", l, n)
		}
	}

	// Measured aliases. Each was checked against the corpus it duplicates and
	// found identical id for id.
	for _, alias := range []string{"tg", "tk", "ga", "cy"} {
		if _, present := seen[alias]; present {
			t.Errorf("%q is an alias of a corpus already in the list", alias)
		}
	}

	// Languages whose absence was a real gap rather than a matter of taste.
	for _, want := range []string{"kk", "az", "uz", "ky", "ka", "hy", "be"} {
		if _, present := seen[want]; !present {
			t.Errorf("%q is a distinct corpus and is missing", want)
		}
	}

	if len(ReviewLanguages) < 60 {
		t.Errorf("list has %d codes; the measured set was larger", len(ReviewLanguages))
	}
}
