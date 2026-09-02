package main

import (
	"encoding/json"
	"fmt"
	googleplayscraper "github.com/kryuchenko/google-play-scraper"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestParseGeneration(t *testing.T) {
	const url = "https://play.google.com/sitemaps/play_sitemaps_2026-08-23_1787500934-00042-of-83445.xml.gz"

	got, err := googleplayscraper.ParseGeneration(url)
	if err != nil {
		t.Fatalf("parseGeneration: %v", err)
	}
	if got.Date != "2026-08-23" || got.Run != "1787500934" || got.Shards != 83445 {
		t.Errorf("parseGeneration = %+v", got)
	}
	if got.ID() != "2026-08-23_1787500934" {
		t.Errorf("id() = %q", got.ID())
	}
}

// The generation id is what decides whether a sweep resumes or starts over.
// Returning a zero value for an unrecognised URL would make every run look
// like a new generation and re-sweep 83k shards forever, so this has to fail
// loudly instead.
func TestParseGenerationRejectsUnrecognisedNames(t *testing.T) {
	for _, url := range []string{
		"https://play.google.com/sitemaps/sitemaps-index-0.xml",
		"https://play.google.com/sitemaps/play_sitemaps_2026-08-23.xml.gz",
		"https://play.google.com/sitemaps/shard-1.xml.gz",
		"",
	} {
		if got, err := googleplayscraper.ParseGeneration(url); err == nil {
			t.Errorf("googleplayscraper.ParseGeneration(%q) = %+v, want an error", url, got)
		}
	}
}

func TestDiff(t *testing.T) {
	from := googleplayscraper.Generation{Date: "2026-08-16", Run: "1"}
	to := googleplayscraper.Generation{Date: "2026-08-23", Run: "2"}

	tests := []struct {
		name        string
		old, new    []string
		wantAdded   []string
		wantRemoved []string
	}{
		{"no change", []string{"a", "b"}, []string{"a", "b"}, nil, nil},
		{"appeared", []string{"a", "c"}, []string{"a", "b", "c"}, []string{"b"}, nil},
		{"vanished", []string{"a", "b", "c"}, []string{"a", "c"}, nil, []string{"b"}},
		{"from empty", nil, []string{"a", "b"}, []string{"a", "b"}, nil},
		{"to empty", []string{"a", "b"}, nil, nil, []string{"a", "b"}},
		// Tails on either side are where an off-by-one in a merge shows up,
		// and a dropped tail would silently under-report new apps.
		{"tail added", []string{"a"}, []string{"a", "y", "z"}, []string{"y", "z"}, nil},
		{"tail removed", []string{"a", "y", "z"}, []string{"a"}, nil, []string{"y", "z"}},
		{"disjoint", []string{"a", "b"}, []string{"c", "d"}, []string{"c", "d"}, []string{"a", "b"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := diff(from, to, tt.old, tt.new)
			if !slices.Equal(d.Added, orEmpty(tt.wantAdded)) {
				t.Errorf("Added = %v, want %v", d.Added, tt.wantAdded)
			}
			if !slices.Equal(d.Removed, orEmpty(tt.wantRemoved)) {
				t.Errorf("Removed = %v, want %v", d.Removed, tt.wantRemoved)
			}
		})
	}
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func TestSnapshotRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "snapshot-test.txt.gz")
	ids := []string{"com.a", "com.b", "com.c"}

	sum, err := writeSnapshot(path, ids)
	if err != nil {
		t.Fatalf("writeSnapshot: %v", err)
	}
	if len(sum) != 64 {
		t.Errorf("sha256 = %q, want 64 hex characters", sum)
	}

	got, err := readSnapshot(path)
	if err != nil {
		t.Fatalf("readSnapshot: %v", err)
	}
	if !slices.Equal(got, ids) {
		t.Errorf("round trip = %v, want %v", got, ids)
	}
}

// Resuming onto state from a previous googleplayscraper.Generation would sweep shard URLs
// Google has already replaced, and every one of them would 404. The mismatch
// has to be treated as "no state" rather than as something to reconcile.
func TestLoadStateRejectsAnotherGeneration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	older := googleplayscraper.Generation{Date: "2026-08-16", Run: "111", Shards: 2}
	current := googleplayscraper.Generation{Date: "2026-08-23", Run: "222", Shards: 2}

	if err := saveState(path, syncState{
		Generation: older,
		Failed:     []string{"https://example/play_sitemaps_2026-08-16_111-00001-of-2.xml.gz"},
		IDs:        7,
	}); err != nil {
		t.Fatalf("saveState: %v", err)
	}

	if _, ok := loadState(path, current, sampling{}); ok {
		t.Error("state from an older generation was accepted for resume")
	}
	got, ok := loadState(path, older, sampling{})
	if !ok {
		t.Fatal("state from the matching generation was rejected")
	}
	if got.IDs != 7 || len(got.Failed) != 1 {
		t.Errorf("round trip lost data: %+v", got)
	}
}

// A half-written state file parses as garbage and would discard a whole
// sweep's progress, so writes go through a temporary file and a rename.
func TestWriteJSONIsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")

	if err := writeJSON(path, manifest{IDs: 1}); err != nil {
		t.Fatalf("writeJSON: %v", err)
	}
	if err := writeJSON(path, manifest{IDs: 2}); err != nil {
		t.Fatalf("writeJSON (overwrite): %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temporary file %s was left behind", e.Name())
		}
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m manifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("result does not parse: %v", err)
	}
	if m.IDs != 2 {
		t.Errorf("IDs = %d, want 2", m.IDs)
	}
}

// Newest is decided by generation id, not by file mtime: a git checkout or a
// copy rewrites mtimes, and picking the wrong baseline would produce a delta
// that reports the whole catalog as new.
func TestLatestManifestPicksNewestGeneration(t *testing.T) {
	dir := t.TempDir()

	write := func(g googleplayscraper.Generation) {
		m := manifest{Generation: g, File: "snapshot-" + g.ID() + ".txt.gz"}
		if err := writeJSON(filepath.Join(dir, "manifest-"+g.ID()+".json"), m); err != nil {
			t.Fatal(err)
		}
	}
	// Written newest-first, so mtime order is the opposite of googleplayscraper.Generation order.
	write(googleplayscraper.Generation{Date: "2026-08-23", Run: "300"})
	time.Sleep(5 * time.Millisecond)
	write(googleplayscraper.Generation{Date: "2026-08-16", Run: "200"})
	time.Sleep(5 * time.Millisecond)
	write(googleplayscraper.Generation{Date: "2026-08-09", Run: "100"})

	got, ok := latestManifest(dir)
	if !ok {
		t.Fatal("latestManifest found nothing")
	}
	if got.Generation.Date != "2026-08-23" {
		t.Errorf("latest = %s, want 2026-08-23", got.Generation)
	}
}

func TestLatestManifestOnEmptyDir(t *testing.T) {
	if _, ok := latestManifest(t.TempDir()); ok {
		t.Error("latestManifest reported a manifest in an empty directory")
	}
}

// finish is the whole payoff of a sweep: it turns the append-ordered partial
// file into a sorted, deduplicated snapshot, records what it did, and diffs
// against the previous run. It needs no network, so it can be tested directly.
func TestFinishProducesSnapshotManifestAndDelta(t *testing.T) {
	dir := t.TempDir()

	prevGen := googleplayscraper.Generation{Date: "2026-08-16", Run: "111", Shards: 2}
	prevIDs := []string{"com.gone", "com.stays"}
	prevFile := "snapshot-" + prevGen.ID() + ".txt.gz"
	if _, err := writeSnapshot(filepath.Join(dir, prevFile), prevIDs); err != nil {
		t.Fatal(err)
	}
	prev := manifest{Generation: prevGen, File: prevFile, IDs: len(prevIDs)}

	// Shards arrive concurrently, so the partial file is unsorted and can
	// repeat an id that two shards both listed.
	gen := googleplayscraper.Generation{Date: "2026-08-23", Run: "222", Shards: 2}
	partial := filepath.Join(dir, "partial-"+gen.ID()+".txt")
	if err := os.WriteFile(partial, []byte("com.new\ncom.stays\ncom.new\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := finish(dir, gen, partial, syncState{Generation: gen}, prev, true, time.Now(), sampling{}); err != nil {
		t.Fatalf("finish: %v", err)
	}

	ids, err := readSnapshot(filepath.Join(dir, "snapshot-"+gen.ID()+".txt.gz"))
	if err != nil {
		t.Fatalf("read new snapshot: %v", err)
	}
	if want := []string{"com.new", "com.stays"}; !slices.Equal(ids, want) {
		t.Errorf("snapshot = %v, want %v (sorted and deduplicated)", ids, want)
	}

	var m manifest
	readJSON(t, filepath.Join(dir, "manifest-"+gen.ID()+".json"), &m)
	if m.IDs != 2 || m.SHA256 == "" || m.File == "" {
		t.Errorf("manifest = %+v", m)
	}

	var d delta
	readJSON(t, filepath.Join(dir, "delta-"+prevGen.ID()+"-to-"+gen.ID()+".json"), &d)
	if !slices.Equal(d.Added, []string{"com.new"}) {
		t.Errorf("Added = %v, want [com.new]", d.Added)
	}
	if !slices.Equal(d.Removed, []string{"com.gone"}) {
		t.Errorf("Removed = %v, want [com.gone]", d.Removed)
	}

	// A finished sweep leaves nothing to resume from; stale state would make
	// the next run think it was mid-sweep.
	for _, leftover := range []string{partial, filepath.Join(dir, "state.json")} {
		if _, err := os.Stat(leftover); err == nil {
			t.Errorf("%s survived a completed sweep", filepath.Base(leftover))
		}
	}
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Base(path), err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("parse %s: %v", filepath.Base(path), err)
	}
}

// The run id looks like a unix timestamp. A lexicographic compare works until
// it gains a digit, and then silently inverts: the newest snapshot stops being
// recognised as newest and every sweep diffs against the wrong baseline.
func TestNewerThanComparesRunNumerically(t *testing.T) {
	older := googleplayscraper.Generation{Date: "2026-08-23", Run: "999999999"}
	newer := googleplayscraper.Generation{Date: "2026-08-23", Run: "1000000000"} // shorter string, larger number

	if newer.Compare(older) <= 0 {
		t.Error("run ids compared as strings; a digit-count change inverts the order")
	}
	if older.Compare(newer) >= 0 {
		t.Error("the comparison is not antisymmetric")
	}
	// Date wins over run: a new day is a new generation regardless of the id.
	if (googleplayscraper.Generation{Date: "2026-08-24", Run: "1"}).Compare(
		googleplayscraper.Generation{Date: "2026-08-23", Run: "9999999999"}) <= 0 {
		t.Error("date should outrank run id")
	}
}

// A -force rerun of the generation already on disk must not diff it against
// itself: an empty delta would claim a period of no change that never
// happened, and it would overwrite nothing but still look like a real result.
func TestFinishSkipsSelfDelta(t *testing.T) {
	dir := t.TempDir()
	gen := googleplayscraper.Generation{Date: "2026-08-23", Run: "222", Shards: 1}

	file := "snapshot-" + gen.ID() + ".txt.gz"
	if _, err := writeSnapshot(filepath.Join(dir, file), []string{"com.a"}); err != nil {
		t.Fatal(err)
	}
	prev := manifest{Generation: gen, File: file, IDs: 1}

	partial := filepath.Join(dir, "partial-"+gen.ID()+".txt")
	if err := os.WriteFile(partial, []byte("com.a\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := finish(dir, gen, partial, syncState{Generation: gen}, prev, true, time.Now(), sampling{}); err != nil {
		t.Fatalf("finish: %v", err)
	}

	matches, _ := filepath.Glob(filepath.Join(dir, "delta-*.json"))
	if len(matches) != 0 {
		t.Errorf("wrote %v; a generation must not be diffed against itself", matches)
	}
}

// sortStrings switches to a parallel path above a size threshold, and the
// merge is hand-written. Both are places where "sorted" can quietly become
// "almost sorted", which nothing downstream would notice until a delta
// reported half the catalog as new.
func TestSortStringsMatchesSlicesSort(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 11))
	for _, n := range []int{0, 1, 2, 17, 1 << 16, 1<<16 + 1, 300_000} {
		in := make([]string, n)
		for i := range in {
			in[i] = fmt.Sprintf("com.example.app%d", r.IntN(n*2+1))
		}
		want := slices.Clone(in)
		slices.Sort(want)

		got := sortStrings(slices.Clone(in))
		if !slices.Equal(got, want) {
			t.Fatalf("n=%d: sortStrings disagrees with slices.Sort", n)
		}
		if !slices.IsSorted(got) {
			t.Fatalf("n=%d: result is not sorted", n)
		}
	}
}

// Streaming the previous snapshot instead of loading it saves ~160MB at
// catalog scale, but it replaces a two-slice merge with a scanner and an
// index, which is easy to get wrong at the ends.
func TestDiffAgainstSnapshotMatchesInMemoryDiff(t *testing.T) {
	from := googleplayscraper.Generation{Date: "2026-08-16", Run: "1"}
	to := googleplayscraper.Generation{Date: "2026-08-23", Run: "2"}

	cases := []struct{ old, new []string }{
		{nil, nil},
		{[]string{"a", "b"}, []string{"a", "b"}},
		{[]string{"a", "c"}, []string{"a", "b", "c"}},
		{[]string{"a", "b", "c"}, []string{"a", "c"}},
		{nil, []string{"a", "b"}},
		{[]string{"a", "b"}, nil},
		{[]string{"a"}, []string{"a", "y", "z"}},
		{[]string{"a", "y", "z"}, []string{"a"}},
		{[]string{"a", "b"}, []string{"c", "d"}},
	}

	for i, c := range cases {
		dir := t.TempDir()
		path := filepath.Join(dir, "snap.txt.gz")
		if _, err := writeSnapshot(path, c.old); err != nil {
			t.Fatal(err)
		}

		want := diff(from, to, c.old, c.new)
		got, err := diffAgainstSnapshot(path, from, to, c.new)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if !slices.Equal(got.Added, want.Added) {
			t.Errorf("case %d: Added = %v, want %v", i, got.Added, want.Added)
		}
		if !slices.Equal(got.Removed, want.Removed) {
			t.Errorf("case %d: Removed = %v, want %v", i, got.Removed, want.Removed)
		}
	}
}

// The radix sort is byte-oriented and hand-written, so the inputs that break
// it are the ones with structure rather than random ones: shared prefixes,
// strings that are prefixes of each other, empty strings, and bytes above
// ASCII where the +1 encoding in byteAt matters.
func TestSortStringsHandlesAwkwardInput(t *testing.T) {
	cases := [][]string{
		{"", "a", "", "aa", "a"},
		{"com.a", "com", "com.a.b", "com.", "com"},
		{"\xff", "\x00", "\x7f", "\x80"},
		{"com.example.app", "com.example.app", "com.example.app"},
	}
	// A long shared prefix drives the recursion deep, which is where an
	// off-by-one in the bucket offsets would show up.
	long := make([]string, 200_000)
	for i := range long {
		long[i] = fmt.Sprintf("com.example.very.long.shared.prefix.indeed.%06d", i%50_000)
	}
	cases = append(cases, long)

	// Every string identical: one bucket holds everything at every depth.
	same := make([]string, 100_000)
	for i := range same {
		same[i] = "com.example.identical"
	}
	cases = append(cases, same)

	for i, in := range cases {
		want := slices.Clone(in)
		slices.Sort(want)
		got := sortStrings(slices.Clone(in))
		if !slices.Equal(got, want) {
			t.Errorf("case %d (n=%d): result differs from slices.Sort", i, len(in))
		}
	}
}

// sortStrings must not allocate: that is the reason it exists rather than the
// parallel merge it replaced, which allocated 178MB at catalog scale.
func TestSortStringsDoesNotAllocate(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 5))
	a := make([]string, 200_000)
	for i := range a {
		a[i] = fmt.Sprintf("com.example.app%d", r.IntN(400_000))
	}

	var m1, m2 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m1)
	sortStrings(a)
	runtime.ReadMemStats(&m2)

	// Goroutine stacks and the semaphore are not free, but the sort itself
	// must not be copying the data. A megabyte is orders of magnitude below
	// what a merge of this size would need.
	if grew := m2.TotalAlloc - m1.TotalAlloc; grew > 1<<20 {
		t.Errorf("sortStrings allocated %d bytes; it is meant to sort in place", grew)
	}
}

// The parallel writer emits several gzip members concatenated. That is legal
// -- a decompressor must treat them as one stream -- but it is unusual enough
// that it deserves proving against a real decompressor rather than only
// against the writer's own assumptions.
func TestWriteSnapshotParallelIsReadableAndComplete(t *testing.T) {
	// Above the threshold where writeSnapshot takes the parallel path.
	ids := make([]string, 200_000)
	for i := range ids {
		ids[i] = fmt.Sprintf("com.example.app%07d", i)
	}
	path := filepath.Join(t.TempDir(), "snap.txt.gz")

	sum, err := writeSnapshot(path, ids)
	if err != nil {
		t.Fatalf("writeSnapshot: %v", err)
	}
	if len(sum) != 64 {
		t.Errorf("sha256 = %q, want 64 hex characters", sum)
	}

	got, err := readSnapshot(path)
	if err != nil {
		t.Fatalf("readSnapshot: %v", err)
	}
	if !slices.Equal(got, ids) {
		t.Errorf("round trip returned %d ids, want %d", len(got), len(ids))
	}

	// And by something that is not this package: a member boundary handled
	// wrongly would truncate silently at the first one.
	if _, err := exec.LookPath("gunzip"); err == nil {
		out, gerr := exec.Command("sh", "-c", "gunzip -c "+path+" | wc -l").Output()
		if gerr != nil {
			t.Errorf("system gunzip could not read the file: %v", gerr)
		} else if n := strings.TrimSpace(string(out)); n != fmt.Sprint(len(ids)) {
			t.Errorf("system gunzip found %s lines, want %d", n, len(ids))
		}
	}
}

// The serial and parallel paths must produce the same content. Sizes may
// differ slightly since a dictionary is not carried across members; the ids
// may not.
func TestWriteSnapshotPathsAgree(t *testing.T) {
	ids := make([]string, 200_000)
	for i := range ids {
		ids[i] = fmt.Sprintf("com.example.app%07d", i)
	}
	dir := t.TempDir()

	par := filepath.Join(dir, "par.gz")
	ser := filepath.Join(dir, "ser.gz")
	if _, err := writeSnapshot(par, ids); err != nil {
		t.Fatal(err)
	}
	if _, err := writeSnapshotSerial(ser, ids); err != nil {
		t.Fatal(err)
	}

	a, err := readSnapshot(par)
	if err != nil {
		t.Fatal(err)
	}
	b, err := readSnapshot(ser)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(a, b) {
		t.Error("the parallel and serial writers produced different content")
	}
}

// The done log is what makes a sweep resumable now, and it is read back after
// a crash, so the shapes a crash leaves behind have to be handled: a truncated
// final line, blank lines, and a log for a generation that has since rolled.
func TestDoneLogSurvivesATruncatedTail(t *testing.T) {
	dir := t.TempDir()
	gen := googleplayscraper.Generation{Date: "2026-08-23", Run: "222"}
	path := doneLogPath(dir, gen)

	// Two complete records, then a line cut off mid-URL by a kill.
	body := "https://play.google.com/sitemaps/a.xml.gz\n" +
		"https://play.google.com/sitemaps/b.xml.gz\n" +
		"https://play.google.com/sitemaps/c.xm"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	done, err := readDoneLog(path)
	if err != nil {
		t.Fatalf("readDoneLog: %v", err)
	}
	// The truncated entry may be read or not, but it must never match a real
	// shard URL, so the shard it came from stays pending either way. What
	// matters is that the complete records survive.
	for _, want := range []string{
		"https://play.google.com/sitemaps/a.xml.gz",
		"https://play.google.com/sitemaps/b.xml.gz",
	} {
		if _, ok := done[want]; !ok {
			t.Errorf("complete record %s was lost", want)
		}
	}
	if _, ok := done["https://play.google.com/sitemaps/c.xml.gz"]; ok {
		t.Error("a truncated record was accepted as a completed shard")
	}
}

func TestDoneLogMissingIsNotAnError(t *testing.T) {
	done, err := readDoneLog(filepath.Join(t.TempDir(), "absent.log"))
	if err != nil {
		t.Fatalf("a missing done log should read as empty, got %v", err)
	}
	if len(done) != 0 {
		t.Errorf("got %d entries from a missing log", len(done))
	}
}
