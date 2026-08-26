package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"slices"
	"sort"
	"strings"
	"testing"

	googleplayscraper "github.com/kryuchenko/google-play-scraper/v2"
)

// parseShards is the only place the CLI interprets a syntax of its own, and
// it decides which of 83k shards a sweep touches. A silent misreading here
// costs hours of traffic against the wrong subset.
func TestParseShards(t *testing.T) {
	tests := []struct {
		spec string
		want []int
	}{
		{"", nil},
		{"5", []int{5}},
		{"0,5,7", []int{0, 5, 7}},
		{"0-3", []int{0, 1, 2, 3}},
		{"7-7", []int{7}},
		{"0-2,9,20-21", []int{0, 1, 2, 9, 20, 21}},
		// Whitespace and stray separators come from shell quoting and from
		// pasting a failed-shard list out of a log.
		{" 1 , 3 - 4 ", []int{1, 3, 4}},
		{"1,,2", []int{1, 2}},
	}

	for _, tt := range tests {
		got, err := parseShards(tt.spec)
		if err != nil {
			t.Errorf("parseShards(%q): unexpected error: %v", tt.spec, err)
			continue
		}
		if !slices.Equal(got, tt.want) {
			t.Errorf("parseShards(%q) = %v, want %v", tt.spec, got, tt.want)
		}
	}
}

func TestParseShardsRejectsNonsense(t *testing.T) {
	// A reversed range is the interesting one: it is the shape a typo takes
	// ("99-9" for "9-99"), and silently sweeping nothing would look like a
	// catalog that had gone empty.
	for _, spec := range []string{"abc", "1-x", "x-1", "99-9", "1.5"} {
		if got, err := parseShards(spec); err == nil {
			t.Errorf("parseShards(%q) = %v, want an error", spec, got)
		}
	}
}

func TestParseSort(t *testing.T) {
	for _, name := range []string{"newest", "NEWEST", "rating", "helpfulness"} {
		if _, err := parseSort(name); err != nil {
			t.Errorf("parseSort(%q): %v", name, err)
		}
	}
	if _, err := parseSort("oldest"); err == nil {
		t.Error("parseSort(\"oldest\") accepted an unknown sort")
	}
}

// The flag package stops parsing at the first non-flag argument, so
// `gpscrape availability com.x -countries us` would silently sweep all 177
// countries. That is the bug this exists to prevent coming back: it is
// invisible in the output, and the only symptom is a run that costs far more
// than it should.
func TestFlagsAreReadOnEitherSideOfPositionals(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"flags first", []string{"-countries", "us,de", "com.example.app"}},
		{"flags last", []string{"com.example.app", "-countries", "us,de"}},
		{"flags around", []string{"-lang", "fr", "com.example.app", "-countries", "us,de"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &common{fs: flag.NewFlagSet("availability", flag.ContinueOnError)}
			c.fs.StringVar(&c.lang, "lang", "en", "")
			countries := c.fs.String("countries", "", "")
			if err := c.parse(tt.args); err != nil {
				t.Fatalf("parse: %v", err)
			}

			if *countries != "us,de" {
				t.Errorf("-countries = %q, want \"us,de\"", *countries)
			}
			got, err := c.arg(0, "appID")
			if err != nil {
				t.Fatalf("positional argument was lost: %v", err)
			}
			if got != "com.example.app" {
				t.Errorf("positional = %q, want \"com.example.app\"", got)
			}
		})
	}
}

func TestArgReportsWhatIsMissing(t *testing.T) {
	c := &common{fs: flag.NewFlagSet("reviews", flag.ContinueOnError)}
	if err := c.parse(nil); err != nil {
		t.Fatalf("parse: %v", err)
	}

	_, err := c.arg(0, "appID")
	if err == nil {
		t.Fatal("a missing argument was accepted")
	}
	// The message has to name the argument and the command; "missing argument"
	// on its own sends the reader to the source.
	for _, want := range []string{"appID", "reviews"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// A failed chunk must still produce one output line per app, carrying the
// error. Dropping the line instead would make apps disappear between input and
// output, which is the hardest kind of gap to notice in a batch job.
func TestPermissionsRecordsKeepsFailedApps(t *testing.T) {
	got := permissionsRecords([]googleplayscraper.PermissionsResult{
		{AppID: "com.ok", Permissions: []googleplayscraper.Permission{{Type: "Camera", Permission: "take pictures"}}},
		{AppID: "com.bad", Err: errors.New("request failed: 500")},
	})

	if len(got) != 2 {
		t.Fatalf("got %d records, want one per app", len(got))
	}
	if got[0].AppID != "com.ok" || len(got[0].Permissions) != 1 || got[0].Error != "" {
		t.Errorf("healthy app rendered as %+v", got[0])
	}
	if got[1].AppID != "com.bad" || got[1].Error != "request failed: 500" {
		t.Errorf("failed app rendered as %+v, want the error carried through", got[1])
	}
	if got[1].Permissions != nil {
		t.Errorf("failed app carried permissions: %v", got[1].Permissions)
	}
}

// The CLI must expose every collection the library knows. A constant added to
// the library and forgotten here is a capability nobody can reach from the
// command line, which is exactly the split this tool exists to avoid.
func TestCLIExposesEveryCollection(t *testing.T) {
	want := []googleplayscraper.Collection{
		googleplayscraper.CollectionTopFree,
		googleplayscraper.CollectionTopPaid,
		googleplayscraper.CollectionGrossing,
		googleplayscraper.CollectionNewFree,
		googleplayscraper.CollectionNewPaid,
		googleplayscraper.CollectionMoversShakers,
	}
	reachable := map[googleplayscraper.Collection]string{}
	for name, col := range collections {
		reachable[col] = name
	}
	for _, col := range want {
		if _, ok := reachable[col]; !ok {
			t.Errorf("%s is not reachable from the command line", col)
		}
	}
	if len(collections) != len(want) {
		t.Errorf("the CLI offers %d collections, the library has %d", len(collections), len(want))
	}
}

// The flag help lists the accepted names, so it must not drift from the map.
func TestCollectionNamesListsThemAll(t *testing.T) {
	names := collectionNames()
	for name := range collections {
		if !strings.Contains(names, name) {
			t.Errorf("collectionNames() omits %q: %s", name, names)
		}
	}
	if !sort.StringsAreSorted(strings.Split(names, ", ")) {
		t.Errorf("collectionNames() is unsorted, so the help text reshuffles between builds: %s", names)
	}
}

// -kind partitions the category list: game and app together must be the whole
// of it, with nothing counted twice.
func TestCategoryKindPartitions(t *testing.T) {
	all := googleplayscraper.AllCategories
	games := filterCategories(all, func(c googleplayscraper.Category) bool { return c.IsGame() })
	apps := filterCategories(all, func(c googleplayscraper.Category) bool { return !c.IsGame() })

	if len(games)+len(apps) != len(all) {
		t.Errorf("game %d + app %d != all %d", len(games), len(apps), len(all))
	}
	if len(games) == 0 || len(apps) == 0 {
		t.Fatalf("a side is empty: game %d, app %d", len(games), len(apps))
	}
	seen := map[googleplayscraper.Category]bool{}
	for _, c := range append(append([]googleplayscraper.Category{}, games...), apps...) {
		if seen[c] {
			t.Errorf("%s appears in both halves", c)
		}
		seen[c] = true
	}
}

// filterCategories must not write through the slice it was given.
func TestFilterCategoriesDoesNotAliasItsInput(t *testing.T) {
	in := []googleplayscraper.Category{"GAME_ACTION", "TOOLS", "GAME_PUZZLE"}
	before := append([]googleplayscraper.Category{}, in...)
	_ = filterCategories(in, func(c googleplayscraper.Category) bool { return c.IsGame() })
	for i := range in {
		if in[i] != before[i] {
			t.Errorf("input mutated at %d: %s became %s", i, before[i], in[i])
		}
	}
}

// The emitter buffers when the destination is not a terminal, which means a
// missing flush silently truncates output. These pin both halves of that.
func TestEmitterBuffersAndFlushes(t *testing.T) {
	var buf bytes.Buffer
	e := newEmitter(&buf)
	if e.buf == nil {
		t.Fatal("a non-terminal destination should be buffered")
	}
	for i := range 3 {
		if err := e.emit(map[string]int{"n": i}); err != nil {
			t.Fatal(err)
		}
	}
	if buf.Len() != 0 {
		t.Errorf("output reached the writer before the flush: %q", buf.String())
	}
	if err := e.flush(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(strings.TrimSpace(buf.String()), "\n") + 1; got != 3 {
		t.Errorf("flushed %d records, want 3: %q", got, buf.String())
	}
	// Flushing twice must be harmless: the deferred flush and the returned one
	// both run on the ordinary path.
	if err := e.flush(); err != nil {
		t.Errorf("second flush: %v", err)
	}
}

// A large record set must survive the buffer boundary intact — the failure
// mode of a wrong flush is a truncated final record, not an empty file.
func TestEmitterDoesNotTruncateAcrossTheBufferBoundary(t *testing.T) {
	var buf bytes.Buffer
	e := newEmitter(&buf)
	const n = 5000
	for i := range n {
		if err := e.emit(map[string]any{"appId": "com.example.package", "i": i}); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.flush(); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != n {
		t.Fatalf("got %d lines, want %d", len(lines), n)
	}
	// Every line must be complete JSON, especially the last.
	for _, i := range []int{0, n / 2, n - 1} {
		var rec map[string]any
		if err := json.Unmarshal([]byte(lines[i]), &rec); err != nil {
			t.Errorf("line %d is not complete JSON: %v\n%s", i, err, lines[i])
		}
	}
}

// Reviews are the one place where the language parameter partitions rather
// than filters: the corpora do not overlap, so reading one language reads one
// slice of an app's reviews and the union is the way to read all of them.
// -langs is what makes that one invocation.
func TestReviewLangsResolution(t *testing.T) {
	// No -langs is the ordinary single-corpus read, and must stay exactly that.
	got, err := reviewLangs("", "en")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"en"}) {
		t.Errorf("no -langs gave %v, want the -lang value alone", got)
	}

	got, err = reviewLangs("all", "en")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(googleplayscraper.ReviewLanguages) || len(got) < 40 {
		t.Errorf(`"all" gave %d languages, want the measured list`, len(got))
	}
	// A clone, not the package slice: a caller that sorts or truncates the
	// result must not reorder the library's list for everyone else.
	got[0] = "zz"
	if googleplayscraper.ReviewLanguages[0] == "zz" {
		t.Error(`"all" handed out the library's own slice`)
	}

	got, err = reviewLangs(" EN , ru,de ,, ru ", "xx")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"en", "ru", "de"}) {
		t.Errorf("got %v; want case-folded, trimmed, blanks dropped and repeats collapsed", got)
	}

	if _, err := reviewLangs(" , ,", "en"); err == nil {
		t.Error("a list with no languages in it was accepted")
	}
}

// The corpus a review came from has to travel with it. Once several are merged
// they are otherwise indistinguishable, and a consumer cannot tell a gap in
// coverage from a gap in the store.
func TestReviewRecordCarriesItsLanguage(t *testing.T) {
	b, err := json.Marshal(reviewRecord{
		Review: googleplayscraper.Review{ID: "r1", Score: 4, Text: "fine"},
		Lang:   "ru",
	})
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back["lang"] != "ru" {
		t.Errorf("lang = %v", back["lang"])
	}
	// Embedded, so the review's own fields stay at the top level and the
	// record is a superset of what a single-language read always produced.
	if back["id"] != "r1" || back["score"] != float64(4) {
		t.Errorf("the review's fields were nested or lost: %s", b)
	}
}

// The keyword used to be matched on the raw string while every real code was
// lowered and trimmed, so -langs ALL fell through to the comma split, became
// the single code "all", and was sent as hl=all: one bogus corpus, exit 0, no
// warning, instead of seventy-one.
func TestReviewLangsAllIsCaseInsensitive(t *testing.T) {
	full := len(googleplayscraper.ReviewLanguages)
	for _, spelling := range []string{"all", "ALL", "All", " all ", "\tAll\n"} {
		got, err := reviewLangs(spelling, "en")
		if err != nil {
			t.Errorf("%q: %v", spelling, err)
			continue
		}
		if len(got) != full {
			t.Errorf("%q gave %d languages, want all %d", spelling, len(got), full)
		}
	}
	// A language actually called "all" does not exist, but a list containing
	// the word alongside others is a list, not the keyword.
	if got, _ := reviewLangs("all,ru", "en"); len(got) == full {
		t.Error(`"all,ru" was treated as the keyword rather than as a list`)
	}
}

// The throttle, the adaptive controller and the retry budget are per-Client
// state. A fresh client per call meant a fresh throttle per call, and reviews
// over several languages built one per language -- so every language's first
// request fired immediately, and -langs all was 71 requests back to back
// whatever -throttle said.
func TestClientIsBuiltOncePerRun(t *testing.T) {
	c := newCommon("reviews")
	if err := c.parse([]string{"-throttle", "1s"}); err != nil {
		t.Fatal(err)
	}
	first := c.client()
	if first == nil {
		t.Fatal("no client")
	}
	for range 5 {
		if got := c.client(); got != first {
			t.Fatal("client() built a second client; per-client throttle state would restart with it")
		}
	}
}
