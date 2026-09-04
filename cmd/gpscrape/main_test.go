package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"sort"
	"strings"
	"testing"

	googleplayscraper "github.com/kryuchenko/google-play-scraper"
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
	if len(got) != len(googleplayscraper.ReviewLanguages()) || len(got) < 40 {
		t.Errorf(`"all" gave %d languages, want the measured list`, len(got))
	}
	// Still checked here now that the library returns a copy of its own: this
	// is the property the caller depends on -- sorting or truncating the
	// result must not reorder the list for everyone else -- and it is worth a
	// test on this side of the boundary whichever side implements it.
	got[0] = "zz"
	if googleplayscraper.ReviewLanguages()[0] == "zz" {
		t.Error(`"all" handed out a list the library kept a reference to`)
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
	full := len(googleplayscraper.ReviewLanguages())
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

// `gpscrape version` printed a bare sentence on the stream every other command
// keeps to one JSON object per line, and told a `go install ...@latest` user
// "devel" for the tagged release they had just installed.
func TestVersionIsAJSONRecord(t *testing.T) {
	saved := version
	t.Cleanup(func() { version = saved })

	t.Run("a stamped build reports its tag", func(t *testing.T) {
		version = "v9.9.9-test"
		out := captureStdout(t, func() {
			if err := cmdVersion(nil); err != nil {
				t.Errorf("cmdVersion: %v", err)
			}
		})
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if len(lines) != 1 {
			t.Fatalf("stdout has %d lines, want one JSON record:\n%s", len(lines), out)
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
			t.Fatalf("output is not JSON: %q (%v)", lines[0], err)
		}
		if rec["version"] != "v9.9.9-test" {
			t.Errorf("record = %v, want the stamped version", rec)
		}
	})

	t.Run("an unstamped build still answers", func(t *testing.T) {
		version = "devel"
		out := captureStdout(t, func() {
			if err := cmdVersion(nil); err != nil {
				t.Errorf("cmdVersion: %v", err)
			}
		})
		var rec struct {
			Version string `json:"version"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &rec); err != nil {
			t.Fatalf("output is not JSON: %q (%v)", out, err)
		}
		// Under `go test` the build info records no module version, so the
		// fallback has nothing better than "devel" to offer. What must hold is
		// that the field is never empty: a bug report that names no build is
		// worse than one that names the wrong one.
		if rec.Version == "" {
			t.Error("version is empty")
		}
	})
}

// appPermissionsStub answers both RPCs one fake store, dispatching on rpcids:
// Ws7gDc for `app`, xdSrCf for `permissions`.
func appPermissionsStub(t *testing.T, store *fakeStore, ok map[string]string) {
	t.Helper()
	store.setFunc(pathBatch, func(r *http.Request) (int, []byte) {
		ids := requestedIDs(t, r)
		rpc := r.URL.Query().Get("rpcids")

		byIndex := map[string]string{}
		var order []string
		for i, id := range ids {
			if rpc == "Ws7gDc" {
				payload, good := ok[id]
				if !good {
					continue // a dropped frame: the answer never arrived
				}
				byIndex[fmt.Sprint(i)] = payload
			} else {
				// A present frame with a null payload: the shape
				// TestPermissionsManyReportsAMissingApp uses.
				byIndex[fmt.Sprint(i)] = ""
			}
			order = append([]string{fmt.Sprint(i)}, order...) // reversed
		}
		return http.StatusOK, framesEnvelope(rpc, byIndex, order)
	})
}

// Batched `app` dropped a failed id from stdout and named it only on stderr, so
// a caller diffing the ids it asked for against the ids it got back saw apps
// vanish. `permissions` has emitted {"appId","error"} inline since it was
// written; that is the shape a script can act on.
func TestAppAndPermissionsAgreeOnTheFailureShape(t *testing.T) {
	store := newFakeStore(t)
	useFakeClient(t, store.transport())
	appPermissionsStub(t, store, map[string]string{
		"com.ok": `[null,[null,null,[["ok"]]]]`,
	})

	out, stderr, err := runVerb(t, cmdApp, "com.ok", "com.bad")
	if err != nil {
		t.Fatalf("cmdApp: %v; one app succeeded, so the run did not fail", err)
	}
	recs := jsonLines(t, out)
	if len(recs) != 2 {
		t.Fatalf("stdout has %d lines for 2 ids asked for:\n%s", len(recs), out)
	}
	if recs[0]["appId"] != "com.ok" {
		t.Errorf("first record = %v, want com.ok in the position it was asked in", recs[0])
	}
	appFailure := recs[1]
	if appFailure["appId"] != "com.bad" || appFailure["error"] == "" || len(appFailure) != 2 {
		t.Errorf("failure record = %v, want exactly {appId, error}", appFailure)
	}
	// The stderr line stays: a person watching should not need a jq filter.
	if !strings.Contains(stderr, "com.bad") {
		t.Errorf("stderr does not name the app that failed:\n%s", stderr)
	}

	permOut, _, permErr := runVerb(t, cmdPermissions, "com.bad")
	if permErr == nil {
		t.Error("permissions reported success although its only app failed")
	}
	permRecs := jsonLines(t, permOut)
	if len(permRecs) != 1 {
		t.Fatalf("permissions emitted %d lines for 1 id:\n%s", len(permRecs), permOut)
	}
	if !maps.Equal(keySet(appFailure), keySet(permRecs[0])) {
		t.Errorf("app emits %v and permissions emits %v for the same kind of failure",
			slices.Sorted(maps.Keys(appFailure)), slices.Sorted(maps.Keys(permRecs[0])))
	}

	t.Run("every app failing is still an error, and still emits a line each", func(t *testing.T) {
		out, _, err := runVerb(t, cmdApp, "com.bad", "com.bad2")
		if err == nil || !strings.Contains(err.Error(), "all 2 apps failed") {
			t.Errorf("err = %v, want one naming the count", err)
		}
		if recs := jsonLines(t, out); len(recs) != 2 {
			t.Errorf("stdout has %d lines, want one per id asked for:\n%s", len(recs), out)
		}
	})
}

func keySet(m map[string]any) map[string]struct{} {
	out := make(map[string]struct{}, len(m))
	for k := range m {
		out[k] = struct{}{}
	}
	return out
}

// flag treats the first backquoted word as the operand name, so backquoting
// the tool's name printed "-trace go tool trace" as the usage line.
func TestTraceFlagHelpNamesTheOperand(t *testing.T) {
	f := newCommon("x").fs.Lookup("trace")
	if f == nil {
		t.Fatal("no -trace flag")
	}
	name, usage := flag.UnquoteUsage(f)
	if name != "FILE" {
		t.Errorf("operand = %q, want FILE", name)
	}
	if strings.Contains(usage, "`") {
		t.Errorf("a backquote survived into the usage text: %q", usage)
	}
	if !strings.Contains(usage, "go tool trace") {
		t.Errorf("usage no longer says what the file is for: %q", usage)
	}
}

// `catalog ids` streams without keeping state, which makes it the cheapest
// path through the sitemap plumbing to check offline.
func TestCatalogIDsEmitsFromTheFixture(t *testing.T) {
	store := newFakeStore(t)
	useFakeClient(t, store.transport())
	f := newSitemapFixture(t, store, fixtureGenA, fourShards())

	out, _, err := runVerb(t, catalogIDs, "-shards", "0-1", "-ids-only")
	if err != nil {
		t.Fatalf("catalog ids: %v", err)
	}
	// Shards are fetched concurrently, so ids arrive in whatever order the
	// workers finish; the command promises the set, not the sequence.
	got := strings.Fields(out)
	slices.Sort(got)
	if want := []string{"com.a", "com.b", "com.b", "com.c"}; !slices.Equal(got, want) {
		t.Errorf("ids = %v, want the two shards' ids %v", got, want)
	}
	if store.hitCount(f.shardPath(2)) != 0 {
		t.Error("a -shards range fetched a shard outside it")
	}

	// A range that names no shard is a mistyped flag, not an empty catalog.
	if _, _, err := runVerb(t, catalogIDs, "-shards", "999"); err == nil {
		t.Error("a shard range outside the generation was accepted")
	}
}
