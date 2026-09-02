package main

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math"
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
	for _, id := range []string{"com.a", "com.b", "com.noise1", "com.noise2"} {
		if err := enc.Encode(newRecord{AppID: id, Seen: "first"}); err != nil {
			t.Fatal(err)
		}
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

	// A deadline nothing can meet, so every lookup fails the way a rate limit
	// makes them fail rather than the way a missing app does.
	err := catalogGenres([]string{"-dir", dir, "-ids", ids, "-timeout", "1ns"})
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
