package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	googleplayscraper "github.com/kryuchenko/google-play-scraper"
)

func TestCategoryList(t *testing.T) {
	games, err := categoryList("game")
	if err != nil {
		t.Fatal(err)
	}
	if len(games) == 0 {
		t.Fatal("no game categories")
	}
	for _, c := range games {
		if !c.IsGame() {
			t.Errorf("%s is not a game category", c)
		}
	}
	// The parent GAME listing duplicates its children's, so it is excluded --
	// including it would double the requests for nothing.
	if slices.Contains(games, googleplayscraper.CategoryGame) {
		t.Error("the parent GAME category was included; its listing duplicates the children's")
	}

	all, err := categoryList("all")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) <= len(games) {
		t.Errorf("all (%d) should exceed games (%d)", len(all), len(games))
	}

	explicit, err := categoryList("game_puzzle, tools")
	if err != nil {
		t.Fatal(err)
	}
	if len(explicit) != 2 || explicit[0] != "GAME_PUZZLE" || explicit[1] != "TOOLS" {
		t.Errorf("explicit list = %v", explicit)
	}
	if _, err := categoryList(" , "); err == nil {
		t.Error("an empty list was accepted")
	}
}

func TestGenerationOfSnapshot(t *testing.T) {
	g, ok := generationOfSnapshot("/data/catalog/snapshot-2026-08-23_1787500934.txt.gz")
	if !ok || g.Date != "2026-08-23" || g.Run != "1787500934" {
		t.Errorf("got %+v ok=%v", g, ok)
	}
	if _, ok := generationOfSnapshot("/data/catalog/notasnapshot"); ok {
		t.Error("a name with no generation was accepted")
	}
}

// The recall accounting is the reason `diff` exists: it is the number that
// decides how often the expensive sweep has to run.
func TestSignalAccounting(t *testing.T) {
	dir := t.TempDir()

	// Four apps were signalled; two of them turn up in the sweep's additions.
	log, err := os.Create(filepath.Join(dir, "signal.log"))
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(log)
	// Inside the window the two generations span. Records carry the time they
	// were seen, and precision only means anything if both sides of the ratio
	// come from the same period.
	inWindow := "2026-08-24T12:00:00Z"
	for _, id := range []string{"com.a", "com.b", "com.noise1", "com.noise2"} {
		if err := enc.Encode(newRecord{AppID: id, Seen: "first", At: inWindow}); err != nil {
			t.Fatal(err)
		}
	}
	// Older than the window: this is the one that used to inflate the
	// denominator for ever, so that precision fell as the log aged whatever
	// the signal was doing.
	if err := enc.Encode(newRecord{AppID: "com.ancient", Seen: "first", At: "2026-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	_ = log.Close()

	added := []string{"com.a", "com.b", "com.c", "com.d", "com.e"}
	from := googleplayscraper.Generation{Date: "2026-08-23", Run: "1787500934"} // 2026-08-23 16:02 UTC
	to := googleplayscraper.Generation{Date: "2026-08-25", Run: "1787673734"}   // two days later

	st := signalAccounting(dir, added, from, to)
	if st == nil {
		t.Fatal("no accounting produced")
	}
	if st.MatchedAdded != 2 {
		t.Errorf("matched %d, want 2", st.MatchedAdded)
	}
	if st.Observed != 4 {
		t.Errorf("observed %d, want the 4 inside the window -- the older record must not count", st.Observed)
	}
	if st.Recall != 2.0/5.0 {
		t.Errorf("recall %.3f, want 0.400 -- two of five additions were signalled", st.Recall)
	}
	if st.Precision != 2.0/4.0 {
		t.Errorf("precision %.3f, want 0.500", st.Precision)
	}
	if st.UnsignalledAdded != 3 {
		t.Errorf("unsignalled %d, want 3", st.UnsignalledAdded)
	}
	// Three additions the signal never mentioned, over two days: this is the
	// rate a sweep schedule should be set from.
	if got := st.UnsignalledPerDay; got < 1.4 || got > 1.6 {
		t.Errorf("unsignalledPerDay %.2f, want about 1.5", got)
	}

	// No log is not the same as a signal that found nothing. Reporting recall
	// zero would read as "the signal is useless" rather than "nobody asked".
	if st := signalAccounting(t.TempDir(), added, from, to); st != nil {
		t.Errorf("an absent signal log produced accounting: %+v", st)
	}
}

func TestGenresTableRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := genresPath(dir)
	want := map[string]string{"com.b": "TOOLS", "com.a": "GAME_PUZZLE", "com.c": ""}
	if err := saveGenres(path, want); err != nil {
		t.Fatal(err)
	}
	got, gotPath := loadGenres(dir)
	if gotPath != path {
		t.Errorf("path = %s, want %s", gotPath, path)
	}
	for id, genre := range want {
		if got[id] != genre {
			t.Errorf("%s = %q, want %q", id, got[id], genre)
		}
	}

	// Sorted, so a rewrite of unchanged data produces an identical file and a
	// version-controlled or rsynced directory sees no churn.
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if !slices.IsSorted(lines) {
		t.Errorf("the table is unsorted:\n%s", body)
	}

	// A missing table is empty, not an error: the first run has none.
	if got, _ := loadGenres(t.TempDir()); len(got) != 0 {
		t.Errorf("a missing table produced %d entries", len(got))
	}
}

func TestScannerSeqSkipsBlanksAndStops(t *testing.T) {
	src := strings.NewReader("com.a\n\n  com.b  \n\ncom.c\n")
	var got []string
	for id := range scannerSeq(src) {
		got = append(got, id)
	}
	if want := []string{"com.a", "com.b", "com.c"}; !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}

	// Breaking out must stop the scan rather than run it to completion.
	var n int
	for range scannerSeq(strings.NewReader("a\nb\nc\n")) {
		n++
		break
	}
	if n != 1 {
		t.Errorf("yielded %d after break", n)
	}
}

// `catalog ids` with no -shards sweeps 83,445 shards, so anything that routes
// there by accident is a four-hour run instead of an error. A leading flag
// used to do exactly that.
func TestCatalogRefusesALeadingFlag(t *testing.T) {
	err := cmdCatalogGroup([]string{"-shards", "0-99"})
	if err == nil {
		t.Fatal("a leading flag was accepted; it would have swept the whole catalog")
	}
	if !strings.Contains(err.Error(), "catalog ids") {
		t.Errorf("the error does not suggest the verb: %v", err)
	}

	if err := cmdCatalogGroup(nil); err == nil {
		t.Error("a bare `catalog` was accepted")
	}
	if err := cmdCatalogGroup([]string{"nosuchverb"}); err == nil {
		t.Error("an unknown verb was accepted")
	}
}

// Every verb in the usage text must dispatch, and every verb that dispatches
// must be in the usage text.
func TestCatalogVerbsMatchTheUsage(t *testing.T) {
	for verb := range catalogVerbs {
		if !strings.Contains(catalogUsage, verb) {
			t.Errorf("verb %q dispatches but is missing from the usage text", verb)
		}
	}
	// And the other direction: the usage lists nothing that does not exist.
	for _, line := range strings.Split(catalogUsage, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || strings.HasSuffix(fields[0], ":") {
			continue
		}
		if _, ok := catalogVerbs[fields[0]]; !ok {
			t.Errorf("the usage text offers %q, which does not dispatch", fields[0])
		}
	}
}

// A 1% sample and a full sweep produce files of the same shape, so nothing
// about them stops the comparison -- and its answer, "99% of the store was
// removed", is indistinguishable from a real catastrophe.
func TestDiffRefusesMismatchedCoverage(t *testing.T) {
	dir := t.TempDir()
	full := googleplayscraper.Generation{Date: "2026-08-16", Run: "100"}
	part := googleplayscraper.Generation{Date: "2026-08-23", Run: "200"}

	write := func(g googleplayscraper.Generation, pct float64, ids []string) string {
		snap := filepath.Join(dir, "snapshot-"+g.ID()+".txt.gz")
		if _, err := writeSnapshot(snap, ids); err != nil {
			t.Fatal(err)
		}
		m := manifest{Generation: g, File: filepath.Base(snap), IDs: len(ids), SamplePct: pct}
		if err := writeJSON(filepath.Join(dir, "manifest-"+g.ID()+".json"), m); err != nil {
			t.Fatal(err)
		}
		return snap
	}

	a := write(full, 0, []string{"com.a", "com.b", "com.c"})
	b := write(part, 1, []string{"com.a"})

	err := catalogDiff([]string{"-dir", dir, a, b})
	if err == nil {
		t.Fatal("a 1% sample was diffed against a full sweep")
	}
	if !strings.Contains(err.Error(), "sampling") {
		t.Errorf("the error does not explain why: %v", err)
	}

	if got := sampleOf(dir, part); got != 1 {
		t.Errorf("sampleOf = %v, want 1", got)
	}
	// A snapshot written before sampling existed has no such field, and every
	// one of those was complete.
	if got := sampleOf(dir, googleplayscraper.Generation{Date: "1999-01-01", Run: "1"}); got != 0 {
		t.Errorf("a missing manifest reported %v coverage, want 0", got)
	}
}

// A sample is a measurement only if it repeats. Same seed, same shards.
func TestSampleShardsIsDeterministicAndProportionate(t *testing.T) {
	const total = 83445
	a := sampleShards(total, 1, 42)
	b := sampleShards(total, 1, 42)
	if !slices.Equal(a, b) {
		t.Error("the same seed picked different shards")
	}
	if c := sampleShards(total, 1, 43); slices.Equal(a, c) {
		t.Error("a different seed picked the same shards")
	}
	if got, want := len(a), total/100; got < want-1 || got > want+1 {
		t.Errorf("1%% of %d gave %d shards, want about %d", total, got, want)
	}
	if !slices.IsSorted(a) {
		t.Error("the picked indices are unsorted")
	}
	// Indices must be in range and distinct.
	seen := map[int]bool{}
	for _, i := range a {
		if i < 0 || i >= total {
			t.Fatalf("index %d out of range", i)
		}
		if seen[i] {
			t.Fatalf("index %d picked twice", i)
		}
		seen[i] = true
	}

	// 0 and 100 both mean "everything", and are expressed by sweeping the lot.
	if sampleShards(total, 0, 1) != nil || sampleShards(total, 100, 1) != nil {
		t.Error("0 or 100 percent should not produce a subset")
	}
	// A percentage too small to reach one shard still sweeps one.
	if got := len(sampleShards(total, 0.0001, 1)); got != 1 {
		t.Errorf("a tiny percentage gave %d shards, want 1", got)
	}
}

// The seed derives from the generation so a re-run samples the same shards,
// and a new build samples afresh.
func TestSeedFromGeneration(t *testing.T) {
	a := googleplayscraper.Generation{Date: "2026-08-23", Run: "1787500934"}
	b := googleplayscraper.Generation{Date: "2026-08-30", Run: "1788100000"}

	// Two values built independently, so this asserts the seed depends on the
	// generation's identity and nothing else -- not on the clock, not on a
	// package-level counter, and not on the Shards field, which changes
	// between builds and would otherwise reshuffle a resumed sample.
	same := googleplayscraper.Generation{Date: "2026-08-23", Run: "1787500934", Shards: 83445}
	if seedFromGeneration(a) != seedFromGeneration(same) {
		t.Error("the seed depends on something beyond the generation's identity")
	}
	if seedFromGeneration(a) == seedFromGeneration(b) {
		t.Error("different generations gave the same seed")
	}
	if seedFromGeneration(a) < 0 {
		t.Error("negative seed")
	}
}

// -precision takes a percentage, and the two spellings people actually type
// differ by a factor of a hundred. Reading "1" as a fraction rather than a
// percent turns a ninety-second run into half a full sweep; reading "0.005"
// as a percent turns a careful request into a sloppy one. Neither mistake
// announces itself, so both are pinned here.
func TestParsePercentReadsBothSpellings(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want float64
	}{
		{"1%", 0.01},
		{"1", 0.01},     // a bare 1 can only have meant one percent
		{"0.5%", 0.005}, // half a percent
		{"0.005", 0.005},
		{"  2% ", 0.02},
		{"0.1%", 0.001},
	} {
		got, err := parsePercent(tc.in)
		if err != nil {
			t.Errorf("%q: %v", tc.in, err)
			continue
		}
		if math.Abs(got-tc.want) > 1e-12 {
			t.Errorf("%q = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestParsePercentRefusesNonsense(t *testing.T) {
	for _, in := range []string{"", "abc", "0", "0%", "-1%", "100%", "1000%", "%"} {
		if got, err := parsePercent(in); err == nil {
			t.Errorf("%q was accepted as %v; a target of that size is not a target", in, got)
		}
	}
}

// `size` used to take -exact, and dropping it is only an improvement if the
// person who types it is told where to go. "flag provided but not defined"
// sends them looking for a spelling instead of the command that does the job.
func TestCatalogSizeSendsExactToSweep(t *testing.T) {
	err := catalogSize([]string{"-exact"})
	if err == nil {
		t.Fatal("-exact was accepted")
	}
	if !strings.Contains(err.Error(), "catalog sweep") {
		t.Errorf("error does not name the command that does this: %v", err)
	}
}

// A sampled snapshot is a measurement, not a copy of the catalog. Every place
// that asks "do we already have this generation?" has to know the difference,
// and for a while only one of the four did -- so a 0.001% sweep silently
// convinced the tool that the generation was done, the full sweep never ran,
// `check` reported upToDate, and `genres` rewrote its table from the sample.
//
// This walks all of them against one directory, because the failure was not
// that any single check was wrong: it was that the guard lived in one of four
// places and nothing held them together.
func TestASampledSnapshotIsNotTheGeneration(t *testing.T) {
	dir := t.TempDir()
	gen := googleplayscraper.Generation{Date: "2026-08-23", Run: "1787500934", Shards: 83445}

	snap := filepath.Join(dir, "snapshot-"+gen.ID()+".txt.gz")
	if _, err := writeSnapshot(snap, []string{"com.a", "com.b"}); err != nil {
		t.Fatal(err)
	}
	m := manifest{Generation: gen, File: filepath.Base(snap), IDs: 2, SamplePct: 0.001}
	if err := writeJSON(filepath.Join(dir, "manifest-"+gen.ID()+".json"), m); err != nil {
		t.Fatal(err)
	}

	// 1. the manifest itself
	if m.complete() {
		t.Error("a 0.001% manifest reported itself complete")
	}
	full := manifest{Generation: gen, IDs: 2}
	if !full.complete() {
		t.Error("a manifest with no sampling reported itself incomplete")
	}

	// 2. latestManifest must carry the field through, or none of the callers
	// can see it however carefully they look.
	got, ok := latestManifest(dir)
	if !ok {
		t.Fatal("latestManifest found nothing")
	}
	if got.SamplePct != 0.001 {
		t.Errorf("latestManifest lost SamplePct: %v", got.SamplePct)
	}
	if got.complete() {
		t.Error("latestManifest returned a manifest that calls itself complete")
	}

	// 3. diff, which is where the guard started. A sample against itself is
	// the same shards and is fine; the guard is about comparing populations
	// that were not drawn the same way.
	if err := catalogDiff([]string{"-dir", dir, snap, snap}); err != nil {
		t.Errorf("diff refused a snapshot against itself: %v", err)
	}

	// 4. the id source must report the coverage, so genres can say the counts
	// describe a sample rather than the store.
	ids, closeIDs, coverage, err := idSource(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	closeIDs()
	_ = ids
	if coverage != 0.001 {
		t.Errorf("idSource reported %v coverage, want 0.001", coverage)
	}

	// A caller-supplied list is not a sampled snapshot and must not be
	// labelled as one.
	list := filepath.Join(dir, "ids.txt")
	if err := os.WriteFile(list, []byte("com.a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, closeList, listCoverage, err := idSource(dir, list)
	if err != nil {
		t.Fatal(err)
	}
	closeList()
	if listCoverage != 0 {
		t.Errorf("a supplied id list reported %v coverage, want 0", listCoverage)
	}
}

// A dropped flag name reads perfectly: `catalog ids 0-9` parses, discards the
// argument, and sweeps all 83,445 shards. The dispatcher already refuses a
// leading flag where a verb belongs; this is the other half.
func TestCatalogVerbsRefusePositionalArguments(t *testing.T) {
	for _, tc := range []struct {
		verb string
		run  func([]string) error
	}{
		{"ids", catalogIDs},
		{"check", catalogCheck},
		{"size", catalogSize},
		{"genres", catalogGenres},
		{"new", catalogNew},
		{"sweep", cmdSync},
		{"apps", catalogApps},
	} {
		err := tc.run([]string{"0-9"})
		if err == nil {
			t.Errorf("catalog %s accepted a bare positional argument", tc.verb)
			continue
		}
		if !strings.Contains(err.Error(), "takes no arguments") {
			t.Errorf("catalog %s: %v", tc.verb, err)
		}
	}

	// diff does take two, and must keep taking them.
	if err := catalogDiff([]string{"-dir", t.TempDir(), "a.gz", "b.gz"}); err == nil ||
		strings.Contains(err.Error(), "takes no arguments") {
		t.Errorf("diff stopped accepting its two file arguments: %v", err)
	}
}

// The genre table is written over wholesale, so anything the run did not see
// is deleted. Building it from scratch each time made two ordinary situations
// destructive: a transient error dropped an app, and -ids on a subset shrank
// the table to that subset. Both come back as "first_seen" next time -- a
// change that did not happen.
func TestGenresTableKeepsWhatThisRunDidNotSee(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "genres.tsv")

	before := map[string]string{
		"com.a": "GAME_PUZZLE",
		"com.b": "GAME_ACTION",
		"com.c": "PRODUCTIVITY",
	}
	if err := saveGenres(path, before); err != nil {
		t.Fatal(err)
	}

	// What one run over a subset produces: it saw only com.a, and only that
	// entry may move.
	prev, _ := loadGenres(dir)
	cur := maps.Clone(prev)
	cur["com.a"] = "GAME_ARCADE" // changed
	// com.b: a transient error this run -- left alone
	// com.c: never asked about -- left alone
	if err := saveGenres(path, cur); err != nil {
		t.Fatal(err)
	}

	after, _ := loadGenres(dir)
	if len(after) != 3 {
		t.Fatalf("table went from 3 entries to %d; apps this run did not see were deleted", len(after))
	}
	if after["com.a"] != "GAME_ARCADE" {
		t.Errorf("com.a = %q, want the new genre", after["com.a"])
	}
	if after["com.b"] != "GAME_ACTION" {
		t.Errorf("com.b = %q; an app that errored lost its genre", after["com.b"])
	}
	if after["com.c"] != "PRODUCTIVITY" {
		t.Errorf("com.c = %q; an app outside the id list lost its genre", after["com.c"])
	}

	// An app that no longer resolves must still be removed -- the merge must
	// not make "gone" unreportable.
	delete(cur, "com.b")
	if err := saveGenres(path, cur); err != nil {
		t.Fatal(err)
	}
	gone, _ := loadGenres(dir)
	if _, still := gone["com.b"]; still {
		t.Error("an app deleted from the table survived the write")
	}
}

// Equal coverage is not enough once either side is a sample.
//
// The seed is derived from the generation id on purpose -- shard indices name
// different shards in different builds, so carrying a seed across a rebuild
// would draw a set that has stopped meaning anything. The consequence is that
// two 1% samples of two generations cover almost disjoint sets of apps by
// construction, and diffing them reported every id in one as removed and every
// id in the other as added: equal percentages, nothing in common, and a number
// that reads as a catastrophic but entirely plausible catalog event.
func TestDiffRefusesSamplesFromDifferentGenerations(t *testing.T) {
	dir := t.TempDir()
	a := googleplayscraper.Generation{Date: "2026-08-19", Run: "100"}
	b := googleplayscraper.Generation{Date: "2026-08-23", Run: "200"}

	write := func(g googleplayscraper.Generation, pct float64, ids []string) string {
		snap := filepath.Join(dir, "snapshot-"+g.ID()+".txt.gz")
		if _, err := writeSnapshot(snap, ids); err != nil {
			t.Fatal(err)
		}
		m := manifest{Generation: g, File: filepath.Base(snap), IDs: len(ids), SamplePct: pct}
		if err := writeJSON(filepath.Join(dir, "manifest-"+g.ID()+".json"), m); err != nil {
			t.Fatal(err)
		}
		return snap
	}

	// Same percentage, different builds: the shards drawn do not overlap.
	sampA := write(a, 1, []string{"com.a1", "com.a2", "com.a3"})
	sampB := write(b, 1, []string{"com.b1", "com.b2", "com.b3"})
	err := catalogDiff([]string{"-dir", dir, sampA, sampB})
	if err == nil {
		t.Fatal("diffed two 1% samples of different generations: every id would read as added and removed")
	}
	if !strings.Contains(err.Error(), "generation") {
		t.Errorf("the error does not explain why: %v", err)
	}

	// Complete snapshots of two builds are exactly what diff is for.
	fullA := write(googleplayscraper.Generation{Date: "2026-08-19", Run: "300"}, 0, []string{"com.x", "com.y"})
	fullB := write(googleplayscraper.Generation{Date: "2026-08-23", Run: "400"}, 0, []string{"com.x", "com.z"})
	if err := catalogDiff([]string{"-dir", dir, fullA, fullB}); err != nil {
		t.Errorf("refused two complete snapshots, which is the ordinary case: %v", err)
	}
}

// A snapshot whose name does not identify its generation has no manifest to
// consult, so its coverage cannot be checked -- and an unchecked diff is
// exactly the one that reports the sampling as a catalog change. It used to
// continue with a zero Generation, whose ID() prints as "_".
func TestDiffRefusesSnapshotsOfUnknownProvenance(t *testing.T) {
	dir := t.TempDir()
	var paths []string
	for i, ids := range [][]string{{"com.a"}, {"com.b"}} {
		p := filepath.Join(dir, fmt.Sprintf("renamed-%d.txt.gz", i))
		if _, err := writeSnapshot(p, ids); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}

	err := catalogDiff([]string{"-dir", dir, paths[0], paths[1]})
	if err == nil {
		t.Fatal("diffed two files of unknown coverage")
	}
	if strings.Contains(err.Error(), `"_"`) {
		t.Errorf("the error leaks the zero generation instead of explaining: %v", err)
	}
}

// The genre table is what makes "give me every game in the store" answerable,
// and until `apps` existed nothing could read it back -- which is also why it
// was so easy to destroy by accident: it had no reader inside the tool.
func TestCatalogAppsFiltersByGenre(t *testing.T) {
	dir := t.TempDir()
	if err := saveGenres(filepath.Join(dir, "genres.tsv"), map[string]string{
		"com.puzzle":  "GAME_PUZZLE",
		"com.action":  "GAME_ACTION",
		"com.puzzle2": "GAME_PUZZLE",
		"com.notes":   "PRODUCTIVITY",
		"com.tool":    "TOOLS",
	}); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) []string {
		t.Helper()
		out := captureStdout(t, func() {
			if err := catalogApps(append([]string{"-dir", dir}, args...)); err != nil {
				t.Fatal(err)
			}
		})
		var lines []string
		for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
			if l != "" {
				lines = append(lines, l)
			}
		}
		return lines
	}

	games := run("-genre", "GAME_*", "-ids-only")
	want := []string{"com.action", "com.puzzle", "com.puzzle2"}
	if !slices.Equal(games, want) {
		t.Errorf("GAME_* = %v, want %v (sorted, and no non-games)", games, want)
	}

	one := run("-genre", "GAME_PUZZLE", "-ids-only")
	if !slices.Equal(one, []string{"com.puzzle", "com.puzzle2"}) {
		t.Errorf("GAME_PUZZLE = %v", one)
	}

	if all := run("-ids-only"); len(all) != 5 {
		t.Errorf("no -genre returned %d ids, want all 5", len(all))
	}

	// The records carry the genre, so a consumer can bucket without a second
	// pass over the table.
	recs := run("-genre", "GAME_ACTION")
	if len(recs) != 1 || !strings.Contains(recs[0], `"genreId":"GAME_ACTION"`) {
		t.Errorf("record output = %v", recs)
	}
}

func TestGenreMatcherAcceptsOnlyATrailingStar(t *testing.T) {
	m, err := genreMatcher("GAME_*")
	if err != nil {
		t.Fatal(err)
	}
	if !m("GAME_PUZZLE") || !m("GAME_") || m("PRODUCTIVITY") {
		t.Error("GAME_* did not match the game family and only it")
	}

	exact, err := genreMatcher("TOOLS")
	if err != nil {
		t.Fatal(err)
	}
	if !exact("TOOLS") || exact("TOOLS_X") {
		t.Error("an exact genre matched something else")
	}

	// An empty pattern keeps everything rather than nothing: the flag is a
	// filter, and an absent filter filters nothing.
	all, err := genreMatcher("")
	if err != nil || !all("ANYTHING") {
		t.Errorf("empty pattern: %v", err)
	}

	for _, bad := range []string{"GA*ME*", "*GAME", "*"} {
		if _, err := genreMatcher(bad); err == nil && bad != "*" {
			t.Errorf("%q was accepted; only a trailing * is supported", bad)
		}
	}
}

// `catalog apps` with no table must say how to build one rather than printing
// nothing and exiting 0 -- "no games in the store" and "you have not swept
// yet" are not the same answer.
func TestCatalogAppsWithNoTableExplainsItself(t *testing.T) {
	err := catalogApps([]string{"-dir", t.TempDir()})
	if err == nil {
		t.Fatal("an empty directory produced an empty list and no error")
	}
	if !strings.Contains(err.Error(), "catalog sweep") || !strings.Contains(err.Error(), "catalog genres") {
		t.Errorf("the error does not name the two steps that build the table: %v", err)
	}
}

// captureStdout runs f with os.Stdout redirected to a pipe and returns what it
// wrote. The commands emit to os.Stdout directly, which is the right thing for
// a tool whose output is a pipe, and this is the cost of testing it.
func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		var b strings.Builder
		_, _ = io.Copy(&b, r)
		done <- b.String()
	}()

	f()

	os.Stdout = saved
	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out
}

// Every verb that reads the snapshot directory has to tell a sampled snapshot
// from a complete one, and the way that guard failed the first time was not
// that any single check was wrong: four were fixed together, and the very next
// commit added `apps` without one. So this walks the verbs rather than naming
// the four that existed when it was written -- a new reader either passes it
// or is caught here.
func TestEveryCatalogReaderChecksCoverage(t *testing.T) {
	gen := googleplayscraper.Generation{Date: "2026-08-23", Run: "1787500934", Shards: 83445}

	// A directory holding a *sampled* snapshot, a manifest that says so, and a
	// genre table derived from it.
	setup := func(t *testing.T) string {
		t.Helper()
		dir := t.TempDir()
		snap := filepath.Join(dir, "snapshot-"+gen.ID()+".txt.gz")
		if _, err := writeSnapshot(snap, []string{"com.a", "com.b"}); err != nil {
			t.Fatal(err)
		}
		m := manifest{Generation: gen, File: filepath.Base(snap), IDs: 2, SamplePct: 0.001}
		if err := writeJSON(filepath.Join(dir, "manifest-"+gen.ID()+".json"), m); err != nil {
			t.Fatal(err)
		}
		if err := saveGenres(filepath.Join(dir, "genres.tsv"), map[string]string{
			"com.a": "GAME_PUZZLE", "com.b": "TOOLS",
		}); err != nil {
			t.Fatal(err)
		}
		return dir
	}

	// `apps` must refuse outright: its whole output is the app list, and
	// returning 0.001% of the store as "the game index" is the wrong answer
	// that looks like a right one.
	t.Run("apps refuses", func(t *testing.T) {
		dir := setup(t)
		err := catalogApps([]string{"-dir", dir, "-genre", "GAME_*", "-ids-only"})
		if err == nil {
			t.Fatal("returned a sampled table as the app list")
		}
		if !strings.Contains(err.Error(), "sample") {
			t.Errorf("the error does not say why: %v", err)
		}
		// And a complete snapshot must still work, or the guard is just a wall.
		full := setup(t)
		if err := writeJSON(filepath.Join(full, "manifest-"+gen.ID()+".json"),
			manifest{Generation: gen, File: "snapshot-" + gen.ID() + ".txt.gz", IDs: 2}); err != nil {
			t.Fatal(err)
		}
		if err := catalogApps([]string{"-dir", full, "-ids-only"}); err != nil {
			t.Errorf("refused a complete snapshot: %v", err)
		}
	})

	// `check` reports it rather than refusing: answering the question is the
	// command's whole job, and the answer is "no, not really".
	t.Run("check reports it", func(t *testing.T) {
		dir := setup(t)
		m, ok := latestManifest(dir)
		if !ok || m.complete() {
			t.Fatalf("latestManifest lost the sampling: ok=%v complete=%v", ok, m.complete())
		}
	})

	// `diff` refuses a mismatch, which is where the guard started.
	t.Run("diff refuses a mismatch", func(t *testing.T) {
		dir := setup(t)
		snap := filepath.Join(dir, "snapshot-"+gen.ID()+".txt.gz")
		other := googleplayscraper.Generation{Date: "2026-08-19", Run: "100"}
		full := filepath.Join(dir, "snapshot-"+other.ID()+".txt.gz")
		if _, err := writeSnapshot(full, []string{"com.a"}); err != nil {
			t.Fatal(err)
		}
		if err := writeJSON(filepath.Join(dir, "manifest-"+other.ID()+".json"),
			manifest{Generation: other, File: filepath.Base(full), IDs: 1}); err != nil {
			t.Fatal(err)
		}
		if err := catalogDiff([]string{"-dir", dir, full, snap}); err == nil {
			t.Error("diffed a complete snapshot against a sampled one")
		}
	})
}

// Resume state is keyed to the generation AND the sampling, because a shard
// list is a function of both. Keyed to the generation alone, a done log from a
// wider run made the pending slice a negative length -- a panic -- and when it
// did not panic it produced a manifest claiming a coverage its snapshot did
// not have.
func TestResumeStateIsKeyedToTheSampleAsWellAsTheGeneration(t *testing.T) {
	gen := googleplayscraper.Generation{Date: "2026-08-23", Run: "1787500934", Shards: 83445}
	path := filepath.Join(t.TempDir(), "state.json")
	saved := syncState{Generation: gen, IDs: 131, SamplePct: 0.01, SampleSeed: 7}
	if err := writeJSON(path, saved); err != nil {
		t.Fatal(err)
	}

	if _, ok := loadState(path, gen, sampling{Pct: 0.01, Seed: 7}); !ok {
		t.Error("refused to resume its own run")
	}
	for _, other := range []sampling{
		{Pct: 0.02, Seed: 7}, // different fraction
		{Pct: 0.01, Seed: 9}, // different shards
		{},                   // a full sweep resuming onto a sample
	} {
		if s, ok := loadState(path, gen, other); ok {
			t.Errorf("resumed %+v onto state for 0.01%%/seed 7: %+v", other, s)
		}
	}
}

// A run in which nothing resolved is not a run that found nothing. Building the
// table only from what resolved meant that a rate-limited pass wrote an empty
// table over a good one and exited 0 -- reproduced end to end, where all 1,740
// lookups failed and the summary still read "1740 apps". The sweep already
// refuses to publish a manifest when shards fail; this is the same rule.
func TestGenresRefusesToPublishWhenNothingResolved(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "genres.tsv")
	before := map[string]string{
		"com.spotify.music": "MUSIC_AND_AUDIO",
		"com.whatsapp":      "COMMUNICATION",
	}
	if err := saveGenres(path, before); err != nil {
		t.Fatal(err)
	}
	ids := filepath.Join(dir, "ids.txt")
	if err := os.WriteFile(ids, []byte("com.spotify.music\ncom.whatsapp\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A storefront that answers 500, which is how a rate limit fails. It used
	// to be `-timeout 1ns`, which only failed because the dial lost a race
	// with the deadline -- a test that depended on the network being slow.
	store := newFakeStore(t)
	useFakeClient(t, store.transport())
	stub := newDigestStub(t, func(string, string) (string, bool) { return "", true })
	stub.fail500["us"] = true
	stub.install(store)

	_, _, err := runVerb(t, catalogGenres, "-dir", dir, "-ids", ids)
	if err == nil {
		t.Fatal("a run in which nothing resolved reported success")
	}

	after, _ := loadGenres(dir)
	if len(after) != len(before) {
		t.Fatalf("table went from %d entries to %d; a failed run overwrote it", len(before), len(after))
	}
	for id, genre := range before {
		if after[id] != genre {
			t.Errorf("%s = %q, want %q -- a failed lookup changed the table", id, after[id], genre)
		}
	}
}

// `catalog apps` refuses a sampled table by default, because handing back
// 0.05%% of the store as "the app list" is the wrong answer that looks like a
// right one. But reading a sample *as a sample* is a real thing to want -- it
// is how the share of the catalog that is games was measured -- so there has to
// be a way to say so, and saying so has to show up in the output.
func TestCatalogAppsAllowSampleIsExplicit(t *testing.T) {
	gen := googleplayscraper.Generation{Date: "2026-08-23", Run: "1787500934", Shards: 83445}
	dir := t.TempDir()
	snap := filepath.Join(dir, "snapshot-"+gen.ID()+".txt.gz")
	if _, err := writeSnapshot(snap, []string{"com.a"}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(dir, "manifest-"+gen.ID()+".json"),
		manifest{Generation: gen, File: filepath.Base(snap), IDs: 1, SamplePct: 0.05}); err != nil {
		t.Fatal(err)
	}
	if err := saveGenres(filepath.Join(dir, "genres.tsv"),
		map[string]string{"com.a": "GAME_PUZZLE"}); err != nil {
		t.Fatal(err)
	}

	if err := catalogApps([]string{"-dir", dir, "-ids-only"}); err == nil {
		t.Error("a sampled table was returned as the app list without asking")
	}

	out := captureStdout(t, func() {
		if err := catalogApps([]string{"-dir", dir, "-ids-only", "-allow-sample"}); err != nil {
			t.Errorf("-allow-sample was refused: %v", err)
		}
	})
	if strings.TrimSpace(out) != "com.a" {
		t.Errorf("output = %q, want the one id", strings.TrimSpace(out))
	}
}

// A summary is not something a database can act on. Keeping a local copy
// current means knowing which ids moved, and the counts alone never said. The
// sweep had always written them to delta-<from>-to-<to>.json, but reaching that
// meant computing a filename inside a directory whose layout this tool calls
// its own business.
func TestDiffEmitsTheIdsThatChanged(t *testing.T) {
	dir := t.TempDir()
	from := googleplayscraper.Generation{Date: "2026-08-19", Run: "100"}
	to := googleplayscraper.Generation{Date: "2026-08-23", Run: "200"}

	write := func(g googleplayscraper.Generation, ids []string) {
		snap := filepath.Join(dir, "snapshot-"+g.ID()+".txt.gz")
		if _, err := writeSnapshot(snap, ids); err != nil {
			t.Fatal(err)
		}
		if err := writeJSON(filepath.Join(dir, "manifest-"+g.ID()+".json"),
			manifest{Generation: g, File: filepath.Base(snap), IDs: len(ids)}); err != nil {
			t.Fatal(err)
		}
	}
	write(from, []string{"com.a", "com.b", "com.c", "com.d"})
	write(to, []string{"com.a", "com.c", "com.e", "com.f"})

	out := captureStdout(t, func() {
		if err := catalogDiff([]string{"-dir", dir, "-changes"}); err != nil {
			t.Fatal(err)
		}
	})

	added, removed := map[string]bool{}, map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		var r changeRecord
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("not JSON: %q", line)
		}
		if r.From != from.ID() || r.To != to.ID() {
			t.Errorf("%s carries %s..%s, want %s..%s", r.AppID, r.From, r.To, from.ID(), to.ID())
		}
		switch r.Change {
		case "added":
			added[r.AppID] = true
		case "removed":
			removed[r.AppID] = true
		default:
			t.Errorf("%s: unknown change %q", r.AppID, r.Change)
		}
	}

	if !added["com.e"] || !added["com.f"] || len(added) != 2 {
		t.Errorf("added = %v, want com.e and com.f", added)
	}
	if !removed["com.b"] || !removed["com.d"] || len(removed) != 2 {
		t.Errorf("removed = %v, want com.b and com.d", removed)
	}

	// Without the flag the output is still the one-line summary, because that
	// is the cheap answer to "did anything happen" and a generation roll makes
	// the other one millions of lines.
	summary := captureStdout(t, func() {
		if err := catalogDiff([]string{"-dir", dir}); err != nil {
			t.Fatal(err)
		}
	})
	if n := len(strings.Split(strings.TrimSpace(summary), "\n")); n != 1 {
		t.Errorf("the default emitted %d lines, want one summary record", n)
	}
	if !strings.Contains(summary, `"added":2`) {
		t.Errorf("summary lost its counts: %s", summary)
	}
}

// Reading the app list filters and prints; it does not need random lookups.
// Doing it through loadGenres cost 422MB of resident memory on the real
// 3.2M-row table to produce 9MB of ids, and the table only grows.
func TestStreamGenresReadsWithoutBuildingTheTable(t *testing.T) {
	dir := t.TempDir()
	want := map[string]string{
		"com.a": "GAME_PUZZLE",
		"com.b": "TOOLS",
		"com.c": "GAME_ACTION",
	}
	if err := saveGenres(filepath.Join(dir, "genres.tsv"), want); err != nil {
		t.Fatal(err)
	}

	got := map[string]string{}
	var order []string
	path, rows, err := streamGenres(dir, func(id, genre string) {
		got[id] = genre
		order = append(order, id)
	})
	if err != nil {
		t.Fatal(err)
	}
	if rows != len(want) {
		t.Errorf("rows = %d, want %d", rows, len(want))
	}
	if !maps.Equal(got, want) {
		t.Errorf("streamed %v, want %v", got, want)
	}
	if !slices.IsSorted(order) {
		t.Errorf("rows arrived in %v; saveGenres writes them sorted", order)
	}
	if filepath.Base(path) != "genres.tsv" {
		t.Errorf("path = %q", path)
	}

	// A missing table is nothing to read, not an error: `catalog apps` turns
	// zero rows into a message naming the two commands that build one.
	_, rows, err = streamGenres(t.TempDir(), func(string, string) {})
	if err != nil || rows != 0 {
		t.Errorf("missing table: rows=%d err=%v", rows, err)
	}
}

// The genre table merges rather than overwrites, which is what stops a
// transient error deleting an app -- and means nothing ever removes a row for
// an id that left the sitemap without being seen to go. -prune is the removal
// path, and it is refused on a subset, because a list of ids you chose cannot
// tell you what is gone.
func TestGenresPruneNeedsTheWholeSnapshot(t *testing.T) {
	dir := t.TempDir()
	ids := filepath.Join(dir, "ids.txt")
	if err := os.WriteFile(ids, []byte("com.spotify.music\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := saveGenres(filepath.Join(dir, "genres.tsv"),
		map[string]string{"com.spotify.music": "MUSIC_AND_AUDIO", "com.whatsapp": "COMMUNICATION"}); err != nil {
		t.Fatal(err)
	}

	err := catalogGenres([]string{"-dir", dir, "-prune", "-ids", ids})
	if err == nil {
		t.Fatal("-prune with -ids was accepted; it would delete every row outside the list")
	}
	if !strings.Contains(err.Error(), "whole snapshot") {
		t.Errorf("the error does not say why: %v", err)
	}
	// And the table is untouched, since the run never started.
	after, _ := loadGenres(dir)
	if len(after) != 2 {
		t.Errorf("table has %d rows, want the 2 it started with", len(after))
	}
}

func TestSplitCountries(t *testing.T) {
	got := splitCountries(" DE , in,br ,, de ")
	if !slices.Equal(got, []string{"de", "in", "br"}) {
		t.Errorf("got %v; want lowercased, trimmed, blanks dropped, repeats collapsed", got)
	}
	if n := len(splitCountries("")); n != 0 {
		t.Errorf("empty spec gave %d countries; it means trust -country alone", n)
	}
}

// ---- catalog genres, offline ----
//
// confirmGone was at 0% and its only test hit production behind a Short gate,
// so it never ran in CI. What it decides is whether a live row gets deleted, so
// it is the last function in this package that should have been untested.

// genresFixture is the table and id list every confirm-gone test starts from:
// three apps in TOOLS, one of which is alive elsewhere, one genuinely gone, and
// one whose lookups fail.
func genresFixture(t *testing.T) (dir, idsPath string) {
	t.Helper()
	dir = t.TempDir()
	if err := saveGenres(genresPath(dir), map[string]string{
		"com.alive": "TOOLS",
		"com.dead":  "TOOLS",
		"com.flaky": "TOOLS",
	}); err != nil {
		t.Fatal(err)
	}
	idsPath = filepath.Join(dir, "ids.txt")
	if err := os.WriteFile(idsPath, []byte("com.alive\ncom.dead\ncom.flaky\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, idsPath
}

// threeStorefronts answers the way the live store does for these three ids:
// nothing anywhere for com.dead, a listing in de for com.alive, and a dropped
// frame -- the answer that never arrived -- for com.flaky.
func threeStorefronts(gl, id string) (string, bool) {
	switch {
	case id == "com.alive" && gl != "us":
		return genrePayload("GAME_CASUAL", "Casual"), true
	case id == "com.flaky":
		return "", false // no frame: not evidence about the app
	default:
		return "", true // a present frame with no listing
	}
}

// runGenres runs catalogGenres over the fixture store, with `us` repeated in
// -confirm-gone on purpose: the primary storefront must be listed once.
func runGenres(t *testing.T, dir, idsPath string, extra ...string) (map[string]genreRecord, string, error) {
	t.Helper()
	args := append([]string{
		"-dir", dir, "-ids", idsPath, "-all", "-country", "us",
		"-confirm-gone", "us,de,in", "-concurrency", "2",
	}, extra...)
	out, stderr, err := runVerb(t, catalogGenres, args...)
	return genreRecords(t, out), stderr, err
}

func genreRecords(t *testing.T, out string) map[string]genreRecord {
	t.Helper()
	byID := map[string]genreRecord{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var r genreRecord
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("stdout line is not a genre record: %q (%v)", line, err)
		}
		byID[r.AppID] = r
	}
	return byID
}

// An app listed in one country is still an app. Of 200 ids the pipeline had
// classified as dead, two were alive elsewhere.
func TestConfirmGoneAliveElsewhereKeepsItsGenre(t *testing.T) {
	store := newFakeStore(t)
	useFakeClient(t, store.transport())
	stub := newDigestStub(t, threeStorefronts)
	stub.install(store)
	dir, ids := genresFixture(t)

	recs, _, err := runGenres(t, dir, ids)
	if err != nil {
		t.Fatalf("catalog genres: %v", err)
	}

	alive := recs["com.alive"]
	if alive.Change != "changed" || alive.GenreID != "GAME_CASUAL" {
		t.Errorf("com.alive = %+v, want a changed record carrying the genre de answered with", alive)
	}
	if alive.Country != "de" {
		t.Errorf("country = %q, want the storefront that answered", alive.Country)
	}
	// Once found, an id is not asked again: the confirm pass narrows.
	if stub.askedFor("in", "com.alive") {
		t.Error("com.alive was re-asked in `in` after `de` answered for it")
	}
	table, _ := loadGenres(dir)
	if table["com.alive"] != "GAME_CASUAL" {
		t.Errorf("table has com.alive = %q, want the rescued genre", table["com.alive"])
	}
}

// A request that failed is not evidence about the app. Calling those gone
// deleted a live row on the strength of a 503.
func TestConfirmGoneFailedEverywhereIsNotGone(t *testing.T) {
	store := newFakeStore(t)
	useFakeClient(t, store.transport())
	newDigestStub(t, threeStorefronts).install(store)
	dir, ids := genresFixture(t)

	recs, _, err := runGenres(t, dir, ids)
	if err != nil {
		t.Fatalf("catalog genres: %v", err)
	}

	flaky := recs["com.flaky"]
	if flaky.Change != "error" {
		t.Errorf("com.flaky = %q, want \"error\": no storefront answered about it", flaky.Change)
	}
	if flaky.Error == "" {
		t.Error("an error record with no error in it")
	}
	table, _ := loadGenres(dir)
	if table["com.flaky"] != "TOOLS" {
		t.Errorf("com.flaky = %q in the table, want the row left as it was", table["com.flaky"])
	}

	dead := recs["com.dead"]
	if dead.Change != "gone" {
		t.Errorf("com.dead = %q, want \"gone\": every storefront asked answered and none has it", dead.Change)
	}
	// "gone" carries its own scope, and the primary storefront is named once
	// even though -confirm-gone repeated it.
	if dead.Country != "us,de,in" {
		t.Errorf("country = %q, want \"us,de,in\" -- each storefront that answered, once", dead.Country)
	}
	if _, still := table["com.dead"]; still {
		t.Error("an app no storefront can see stayed in the table")
	}
}

// A storefront outage in the confirm pass must not read as a catalog change.
// The failed==seen guard used to count only main-pass errors, so an outage here
// wrote the deletions anyway.
func TestConfirmGoneAStorefrontOutageProducesNoRemovals(t *testing.T) {
	store := newFakeStore(t)
	useFakeClient(t, store.transport())
	stub := newDigestStub(t, threeStorefronts)
	stub.fail500["in"] = true
	stub.install(store)
	dir, ids := genresFixture(t)

	recs, _, err := runGenres(t, dir, ids)
	if err != nil {
		t.Fatalf("catalog genres: %v", err)
	}

	for id, rec := range recs {
		if rec.Change == "gone" {
			t.Errorf("%s was called gone although `in` never answered about it", id)
		}
	}
	table, _ := loadGenres(dir)
	for _, id := range []string{"com.dead", "com.flaky"} {
		if table[id] != "TOOLS" {
			t.Errorf("%s = %q in the table, want the row a failed storefront cannot change", id, table[id])
		}
		if recs[id].Change != "error" {
			t.Errorf("%s = %q, want \"error\"", id, recs[id].Change)
		}
	}
	// The one that did answer is still rescued: an outage in the last
	// storefront does not discard what the earlier ones said.
	if recs["com.alive"].GenreID != "GAME_CASUAL" {
		t.Errorf("com.alive = %+v, want the genre de answered with", recs["com.alive"])
	}
}

// A run in which nothing resolved is not a run that found nothing.
func TestConfirmGoneAllStorefrontsFailingDoesNotTouchTheTable(t *testing.T) {
	store := newFakeStore(t)
	useFakeClient(t, store.transport())
	stub := newDigestStub(t, func(string, string) (string, bool) { return "", true })
	stub.fail500["de"] = true
	stub.fail500["in"] = true
	stub.install(store)
	dir, ids := genresFixture(t)

	recs, stderr, err := runGenres(t, dir, ids)
	if err == nil {
		t.Fatal("a run where no storefront answered reported success")
	}
	if !strings.Contains(err.Error(), "left untouched") {
		t.Errorf("the error does not say the table was spared: %v", err)
	}
	for id, rec := range recs {
		if rec.Change == "gone" {
			t.Errorf("%s was called gone by a run that could read nothing", id)
		}
	}
	table, _ := loadGenres(dir)
	if len(table) != 3 {
		t.Errorf("table has %d rows, want the 3 it started with", len(table))
	}
	if strings.Contains(stderr, "3 apps, 0 changed, 0 gone\n") {
		t.Errorf("the summary reads like success:\n%s", stderr)
	}
}

// -confirm-gone "" trusts -country alone, which is what its help says. The
// refactor must keep that path working.
func TestGenresMainPassStillWorksWithoutConfirmGone(t *testing.T) {
	store := newFakeStore(t)
	useFakeClient(t, store.transport())
	stub := newDigestStub(t, func(gl, id string) (string, bool) {
		if id == "com.alive" {
			return genrePayload("GAME_CASUAL", "Casual"), true
		}
		return "", true
	})
	stub.install(store)
	dir, ids := genresFixture(t)

	out, _, err := runVerb(t, catalogGenres, "-dir", dir, "-ids", ids, "-all",
		"-country", "us", "-confirm-gone", "", "-concurrency", "2")
	if err != nil {
		t.Fatalf("catalog genres: %v", err)
	}
	byID := genreRecords(t, out)
	if byID["com.alive"].Change != "changed" {
		t.Errorf("com.alive = %+v, want changed", byID["com.alive"])
	}
	if got := byID["com.dead"]; got.Change != "gone" || got.Country != "us" {
		t.Errorf("com.dead = %+v, want gone scoped to us alone", got)
	}
	if n := stub.requests("de") + stub.requests("in"); n != 0 {
		t.Errorf("%d requests went to other storefronts although -confirm-gone was empty", n)
	}
}

// The pairing logic on its own, without the command around it: a common built
// here can carry its own client, so no hook is needed.
func TestConfirmGoneReportsWhatAnsweredAndWhatDidNot(t *testing.T) {
	store := newFakeStore(t)
	stub := newDigestStub(t, threeStorefronts)
	stub.install(store)

	c := &common{
		cached: googleplayscraper.NewClient(
			googleplayscraper.WithHTTPClient(&http.Client{Transport: store.transport()})),
		lang: "en", country: "us", concurrency: 2,
	}
	res, err := confirmGone(context.Background(), c,
		[]string{"com.alive", "com.dead", "com.flaky"}, []string{"us", "de", "in"}, nil)
	if err != nil {
		t.Fatalf("confirmGone: %v", err)
	}
	if got, ok := res.foundIn["com.alive"]; !ok || got.country != "de" || got.genre != "GAME_CASUAL" {
		t.Errorf("foundIn[com.alive] = %+v (ok=%v), want de/GAME_CASUAL", got, ok)
	}
	if _, ok := res.foundIn["com.dead"]; ok {
		t.Error("com.dead was reported as found somewhere")
	}
	if res.unconfirmed["com.flaky"] == nil {
		t.Error("com.flaky failed everywhere and is not marked unconfirmed")
	}
	if res.unconfirmed["com.dead"] != nil {
		t.Error("com.dead was answered about everywhere and must not be unconfirmed")
	}
	// checked excludes the primary storefront: it is what "gone" scopes itself
	// to, and it must not name one the run never reached.
	if !slices.Equal(res.checked, []string{"de", "in"}) {
		t.Errorf("checked = %v, want the storefronts actually asked", res.checked)
	}
}

// ---- catalog new ----

// listStub answers the vyAe2 list RPC per category: the captured fixture for
// the categories that work, 500 for the rest. The default collection new_free
// has no section in the legacy HTML page, so List refuses there without
// fetching one -- no other route is needed.
func listStub(t *testing.T, store *fakeStore, ok map[googleplayscraper.Category]bool) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "testdata", "list_vyae2.bin"))
	if err != nil {
		t.Fatal(err)
	}
	store.setFunc(pathBatch, func(r *http.Request) (int, []byte) {
		raw, rerr := io.ReadAll(r.Body)
		if rerr != nil {
			t.Fatalf("read request body: %v", rerr)
		}
		decoded, uerr := url.QueryUnescape(string(raw))
		if uerr != nil {
			t.Fatalf("unescape f.req: %v", uerr)
		}
		for cat, good := range ok {
			if good && strings.Contains(decoded, string(cat)) {
				return http.StatusOK, body
			}
		}
		return http.StatusInternalServerError, nil
	})
}

// Exit 0 with an empty stdout was indistinguishable from "the store published
// nothing new", and the signal log grew a zero-line entry either way.
func TestCatalogNewFailsWhenEveryCategoryFails(t *testing.T) {
	store := newFakeStore(t)
	useFakeClient(t, store.transport())
	listStub(t, store, nil)
	dir := t.TempDir()

	out, _, err := runVerb(t, catalogNew, "-dir", dir, "-categories", "GAME_ACTION,GAME_PUZZLE")
	if err == nil {
		t.Fatal("a run in which every category failed reported success")
	}
	if !strings.Contains(err.Error(), "all 2 categories failed") {
		t.Errorf("the error does not name the count: %v", err)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("stdout = %q, want nothing", out)
	}
	if b, rerr := os.ReadFile(filepath.Join(dir, "signal.log")); rerr == nil && len(b) > 0 {
		t.Errorf("signal.log grew %d bytes from a run that observed nothing", len(b))
	}
}

// One failed category out of seventeen is an ordinary day.
func TestCatalogNewKeepsGoingPastOneFailedCategory(t *testing.T) {
	store := newFakeStore(t)
	useFakeClient(t, store.transport())
	listStub(t, store, map[googleplayscraper.Category]bool{googleplayscraper.CategoryGameAction: true})
	dir := t.TempDir()

	out, stderr, err := runVerb(t, catalogNew, "-dir", dir, "-categories", "GAME_ACTION,GAME_PUZZLE")
	if err != nil {
		t.Fatalf("catalog new: %v", err)
	}
	recs := jsonLines(t, out)
	if len(recs) == 0 {
		t.Fatal("no records for the category that answered")
	}
	for _, rec := range recs {
		if rec["seen"] != "first" || rec["category"] != "GAME_ACTION" {
			t.Errorf("record = %v, want first sightings from GAME_ACTION", rec)
		}
	}
	if !strings.Contains(stderr, "GAME_PUZZLE") {
		t.Errorf("stderr does not name the category that failed:\n%s", stderr)
	}
	if b, rerr := os.ReadFile(filepath.Join(dir, "signal.log")); rerr != nil || len(b) == 0 {
		t.Error("the observations did not reach the signal log")
	}

	// The log is what makes "first" mean first: a second run sees the same ids
	// as known and emits nothing.
	again, _, err := runVerb(t, catalogNew, "-dir", dir, "-categories", "GAME_ACTION,GAME_PUZZLE")
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if strings.TrimSpace(again) != "" {
		t.Errorf("a second run reported ids as new again:\n%s", again)
	}
}

// ---- flags a verb cannot honour ----

// `catalog apps` and `catalog diff` read files that are already on disk. A
// caller who passed -country expecting another storefront got the same table
// and no warning, and -h is where that caller looks first.
func TestLocalVerbsRegisterNoNetworkFlags(t *testing.T) {
	// Never probe by passing the flag: the flag sets use ExitOnError, so an
	// undefined flag calls os.Exit(2) and takes the test binary with it.
	for _, name := range []string{"catalog apps", "catalog diff"} {
		c := newLocalCommon(name)
		for _, flagName := range []string{"throttle", "concurrency", "adaptive", "lang", "country", "timeout"} {
			if c.fs.Lookup(flagName) != nil {
				t.Errorf("%s registers -%s, which it cannot honour", name, flagName)
			}
		}
		for _, flagName := range []string{"debug", "log-file", "trace"} {
			if c.fs.Lookup(flagName) == nil {
				t.Errorf("%s no longer registers -%s", name, flagName)
			}
		}
	}
	// And the verbs themselves are built that way.
	for _, tc := range []struct {
		name string
		run  func([]string) error
	}{
		{"apps", catalogApps},
		{"diff", catalogDiff},
	} {
		store := newFakeStore(t)
		useFakeClient(t, store.transport())
		// Any request at all is a test failure: the store's unrouted handler
		// says so, and these verbs make none.
		_, _, _ = runVerb(t, tc.run, "-dir", t.TempDir())
	}
}

// The genre table is written through a temporary file, and one left behind is
// a half-written table sitting beside the good one.
func TestWriteGenreLinesLeavesNoTmpBehind(t *testing.T) {
	dir := t.TempDir()
	path := genresPath(dir)
	genreOf := func(id string) string { return "TOOLS" }

	if err := writeGenreLines(path, []string{"com.a", "com.b"}, genreOf); err != nil {
		t.Fatalf("writeGenreLines: %v", err)
	}
	assertNoTempFiles(t, dir)

	// A temporary path that cannot be created, so the write fails after the
	// good table is already on disk.
	if err := os.Mkdir(path+".tmp", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeGenreLines(path, []string{"com.c"}, genreOf); err == nil {
		t.Fatal("writeGenreLines reported success with an unusable temporary path")
	}
	table, _ := loadGenres(dir)
	if len(table) != 2 {
		t.Errorf("table has %d rows, want the 2 the failed write must not have touched", len(table))
	}
}
