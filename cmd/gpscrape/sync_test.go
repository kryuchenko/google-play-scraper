package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	googleplayscraper "github.com/kryuchenko/google-play-scraper"
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

	if err := finish(dir, gen, partial, syncState{Generation: gen}, prev, true, time.Now(), sampling{}, 0); err != nil {
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

	if err := finish(dir, gen, partial, syncState{Generation: gen}, prev, true, time.Now(), sampling{}, 0); err != nil {
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

// ---- input the sweep did not write ----
//
// partial-<gen>.txt is appended to over hours and read back at the end, so
// between the writing and the reading it is just a file on disk. A truncated
// write, a filesystem that came back with zeroes, a -dir pointed at something
// else: the two readers below both had an unbounded appetite for that, one in
// recursion depth and one in line length.

// radixSort recurses once per byte of shared prefix. Package ids share three
// or four, so the cap is never reached on real input -- it is there for input
// that agrees for as far as it goes. What the cap must not do is change the
// answer, so that is what is checked: slices.Sort compares whole strings and
// finishes the job correctly from any depth.
func TestRadixSortIsCorrectPastItsDepthCap(t *testing.T) {
	// Far past radixMaxDepth, and more than radixCutoff strings so that the
	// recursion is entered rather than short-circuited by size.
	prefix := strings.Repeat("a", 4*radixMaxDepth)
	want := make([]string, 0, 4*radixCutoff)
	for i := range 4 * radixCutoff {
		want = append(want, prefix+fmt.Sprintf("%04d", i))
	}

	r := rand.New(rand.NewPCG(13, 17))
	shuffled := func() []string {
		out := slices.Clone(want)
		r.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
		return out
	}

	got := shuffled()
	radixSort(got, 0)
	if !slices.Equal(got, want) {
		t.Error("a deep shared prefix came out in the wrong order")
	}

	// And entered at a depth already past the cap, which is where the
	// per-bucket recursion below sortStrings arrives.
	got = shuffled()
	radixSort(got, radixMaxDepth+1)
	if !slices.Equal(got, want) {
		t.Error("sorting from beyond the cap gave the wrong order")
	}
}

// The same shape through sortStrings, which is what finish calls: the
// top-level partition and the parallel per-bucket recursion, on enough strings
// to take that path.
func TestSortStringsSurvivesACorruptDeepPrefix(t *testing.T) {
	prefix := strings.Repeat("z", 2000)
	n := 2*radixCutoff*runtime.GOMAXPROCS(0) + 1
	in := make([]string, 0, n)
	for i := range n {
		in = append(in, prefix+fmt.Sprintf("%06d", n-i))
	}
	want := slices.Clone(in)
	slices.Sort(want)

	if got := sortStrings(slices.Clone(in)); !slices.Equal(got, want) {
		t.Error("sortStrings disagrees with slices.Sort on strings with a 2000-byte shared prefix")
	}
}

// readLines reads the file whole and slices it, which is why it was the one
// reader here with no line limit: every bufio.Scanner in this file already had
// one. A package id is a few hundred bytes at the outside.
func TestReadLinesRefusesAnOversizedLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "partial-x.txt")
	writeFixtureFile(t, path, "com.a\n"+strings.Repeat("x", 2<<20)+"\ncom.b\n")

	_, err := readLines(path)
	if err == nil {
		t.Fatal("a 2MB line was read as an app id")
	}
	// The file and the line, because the caller's next move is to go and look
	// at it.
	for _, want := range []string{path, "line 2"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not name %q: %v", want, err)
		}
	}

	// A limit, not a suggestion: the line at it is read.
	writeFixtureFile(t, path, "com.a\n"+strings.Repeat("y", maxLineBytes)+"\n")
	lines, err := readLines(path)
	if err != nil {
		t.Fatalf("a line exactly at the limit was refused: %v", err)
	}
	if len(lines) != 2 {
		t.Errorf("read %d lines, want 2", len(lines))
	}
}

// Where the limit protects something. finish turns the partial file into the
// published snapshot, so an unbounded line becomes a snapshot entry a megabyte
// long and a manifest counting it as an app.
func TestFinishRefusesAPartialFileWithAnOversizedLine(t *testing.T) {
	dir := t.TempDir()
	gen := googleplayscraper.Generation{Date: "2026-08-23", Run: "222", Shards: 2}
	partial := filepath.Join(dir, "partial-"+gen.ID()+".txt")
	writeFixtureFile(t, partial, "com.a\n"+strings.Repeat("x", 2<<20)+"\n")

	err := finish(dir, gen, partial, syncState{Generation: gen}, manifest{}, false,
		time.Now(), sampling{}, 0)
	if err == nil {
		t.Fatal("finish published a snapshot from a file it could not have written")
	}
	for _, pattern := range []string{"snapshot-*", "manifest-*"} {
		if found, _ := filepath.Glob(filepath.Join(dir, pattern)); len(found) != 0 {
			t.Errorf("%s was published anyway: %v", pattern, found)
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

// Resume state for a generation that has rolled can never be used again: a
// done log is a list of shard URLs, and republishing replaces every one of
// them. Nothing deleted those files -- only the current generation's were
// cleaned -- so an interrupted sweep left about 99MB behind every time the
// catalog was republished, on the order of 9GB a year.
func TestStaleResumeStateIsRemoved(t *testing.T) {
	dir := t.TempDir()
	current := googleplayscraper.Generation{Date: "2026-08-23", Run: "1787500934"}
	rolled := googleplayscraper.Generation{Date: "2026-08-19", Run: "1787155334"}

	write := func(name string, size int) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	stalePartial := write("partial-"+rolled.ID()+".txt", 5000)
	staleDone := write("done-"+rolled.ID()+".log", 1000)
	livePartial := write("partial-"+current.ID()+".txt", 700)
	liveDone := write("done-"+current.ID()+".log", 300)
	// Something this tool did not name must be left alone, whatever it is.
	foreign := write("partial-notes.txt", 42)
	unrelated := write("readme.txt", 7)

	freed := removeStaleResumeState(dir, current)
	if freed != 6000 {
		t.Errorf("freed %d bytes, want the 6000 the rolled generation held", freed)
	}
	for _, p := range []string{stalePartial, staleDone} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s survived", filepath.Base(p))
		}
	}
	for _, p := range []string{livePartial, liveDone, foreign, unrelated} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was deleted and should not have been", filepath.Base(p))
		}
	}

	// Idempotent: a second pass has nothing left to do.
	if again := removeStaleResumeState(dir, current); again != 0 {
		t.Errorf("second pass freed %d bytes", again)
	}
}

// Snapshots are the caller's data, so they go only when asked -- and when
// asked, the newest survive. Newest is by run id, a build timestamp, so a
// copied directory keeps its meaning rather than depending on mtime.
func TestPruneSnapshotsKeepsTheNewest(t *testing.T) {
	dir := t.TempDir()
	gens := []googleplayscraper.Generation{
		{Date: "2026-08-01", Run: "100"},
		{Date: "2026-08-05", Run: "200"},
		{Date: "2026-08-11", Run: "300"},
		{Date: "2026-08-19", Run: "400"},
	}
	// The delta's real name, not the pruner's. The fixture used to write
	// delta-<gen>.json -- a name the writer has never produced -- so the test
	// agreed with the pruner instead of measuring it, and -keep left every
	// delta on disk for good.
	for i, g := range gens {
		names := []string{
			"snapshot-" + g.ID() + ".txt.gz",
			"manifest-" + g.ID() + ".json",
		}
		if i > 0 {
			names = append(names, filepath.Base(deltaName(gens[i-1], g)))
		}
		for _, name := range names {
			if err := os.WriteFile(filepath.Join(dir, name), make([]byte, 100), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	freed, removed := pruneSnapshots(dir, 2)
	if removed != 2 {
		t.Errorf("removed %d generations, want 2", removed)
	}
	// Two snapshots, two manifests, delta-A-to-B and delta-B-to-C: B's delta
	// goes with B, and A-to-B goes with either end.
	if freed != 600 {
		t.Errorf("freed %d bytes, want 600 (2 snapshots + 2 manifests + 2 deltas of 100 bytes)", freed)
	}
	// A delta between two surviving generations survives with them.
	if _, err := os.Stat(filepath.Join(dir, deltaName(gens[2], gens[3]))); err != nil {
		t.Errorf("%s was removed, but both its generations were kept", deltaName(gens[2], gens[3]))
	}
	for _, g := range gens[:2] {
		leftover, _ := filepath.Glob(filepath.Join(dir, "delta-*"+g.ID()+"*.json"))
		if len(leftover) > 0 {
			t.Errorf("deltas naming the pruned %s survived: %v", g.ID(), leftover)
		}
	}
	for _, g := range gens[2:] {
		if _, err := os.Stat(filepath.Join(dir, "snapshot-"+g.ID()+".txt.gz")); err != nil {
			t.Errorf("%s was removed but is one of the two newest", g.ID())
		}
	}
	for _, g := range gens[:2] {
		if _, err := os.Stat(filepath.Join(dir, "snapshot-"+g.ID()+".txt.gz")); err == nil {
			t.Errorf("%s survived a prune to 2", g.ID())
		}
	}

	// keep >= what is there does nothing, and keep 0 is "keep everything".
	if _, n := pruneSnapshots(dir, 10); n != 0 {
		t.Errorf("pruning to 10 removed %d", n)
	}
	if _, n := pruneSnapshots(dir, 0); n != 0 {
		t.Errorf("pruning to 0 removed %d; zero means keep every generation", n)
	}
}

// ---- the sweep, end to end and offline ----
//
// sweep, parallelShards and isGone were all at 0%: the command that makes
// 83,445 requests and writes the dataset had no test that ran it. The fixtures
// in fixtures_test.go publish a four-shard generation over an httptest server,
// so every path below -- resume, retry, roll, sampling, pruning, and the write
// failure that used to keep fetching -- runs in a few milliseconds and reaches
// no network.

var (
	fixtureGenA = googleplayscraper.Generation{Date: "2026-08-23", Run: "1787500934"}
	fixtureGenB = googleplayscraper.Generation{Date: "2026-08-27", Run: "1787900000"}
	fixtureGenC = googleplayscraper.Generation{Date: "2026-08-31", Run: "1788300000"}
)

// fourShards is the shape every sweep test starts from: overlapping ids across
// shards, so "sorted and deduplicated" is a property the snapshot has to have
// rather than one the fixture hands it.
func fourShards() [][]string {
	return [][]string{
		{"com.a", "com.b"},
		{"com.b", "com.c"},
		{"com.d"},
		{"com.e"},
	}
}

func newSweepFixture(t *testing.T) (*fakeStore, *sitemapFixture, string) {
	t.Helper()
	store := newFakeStore(t)
	useFakeClient(t, store.transport())
	return store, newSitemapFixture(t, store, fixtureGenA, fourShards()), t.TempDir()
}

func TestSweepCompleteWritesSnapshotManifestAndEmitsIt(t *testing.T) {
	store, f, dir := newSweepFixture(t)

	out, _, err := runVerb(t, cmdSync, "-dir", dir, "-concurrency", "2")
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}

	ids, err := readSnapshot(filepath.Join(dir, "snapshot-"+fixtureGenA.ID()+".txt.gz"))
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if want := []string{"com.a", "com.b", "com.c", "com.d", "com.e"}; !slices.Equal(ids, want) {
		t.Errorf("snapshot = %v, want %v (sorted and deduplicated)", ids, want)
	}

	var m manifest
	readJSON(t, filepath.Join(dir, "manifest-"+fixtureGenA.ID()+".json"), &m)
	if m.IDs != 5 || m.SamplePct != 0 {
		t.Errorf("manifest = %+v, want 5 ids and a complete sweep", m)
	}
	// The sha256 is the manifest's claim about the file beside it, and a
	// consumer that verifies a download checks exactly this.
	if got := sha256OfFile(t, filepath.Join(dir, m.File)); got != m.SHA256 {
		t.Errorf("manifest sha256 = %s, file hashes to %s", m.SHA256, got)
	}

	// One record on stdout, and it is the manifest: the flagship command's
	// only machine-readable answer.
	var emitted manifest
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1 {
		t.Fatalf("stdout has %d lines, want exactly one manifest record:\n%s", len(lines), out)
	}
	if err := json.Unmarshal([]byte(lines[0]), &emitted); err != nil {
		t.Fatalf("stdout is not JSON: %v", err)
	}
	if emitted.Generation.ID() != fixtureGenA.ID() || emitted.SHA256 != m.SHA256 || emitted.IDs != m.IDs {
		t.Errorf("emitted %+v, want the manifest %+v", emitted, m)
	}

	// Nothing to diff against, so nothing to write.
	if deltas, _ := filepath.Glob(filepath.Join(dir, "delta-*.json")); len(deltas) != 0 {
		t.Errorf("a first sweep wrote %v; there is no previous snapshot to diff against", deltas)
	}
	// A finished sweep leaves nothing to resume from.
	for _, name := range []string{
		"partial-" + fixtureGenA.ID() + ".txt",
		"done-" + fixtureGenA.ID() + ".log",
		"state.json",
	} {
		if _, serr := os.Stat(filepath.Join(dir, name)); serr == nil {
			t.Errorf("%s survived a completed sweep", name)
		}
	}
	assertNoTempFiles(t, dir)

	for i := range fourShards() {
		if got := store.hitCount(f.shardPath(i)); got != 1 {
			t.Errorf("shard %d fetched %d times, want 1", i, got)
		}
	}
	for _, path := range []string{"/robots.txt", indexPath0, indexPath1} {
		if got := store.hitCount(path); got != 1 {
			t.Errorf("%s fetched %d times, want 1", path, got)
		}
	}
}

func sha256OfFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// The delta is what periodic sweeping is for: a full snapshot is tens of
// megabytes and the answer to "what appeared this week" is a few hundred
// kilobytes.
func TestSweepSecondGenerationWritesTheDelta(t *testing.T) {
	store, f, dir := newSweepFixture(t)
	if _, _, err := runVerb(t, cmdSync, "-dir", dir, "-concurrency", "2"); err != nil {
		t.Fatalf("first sweep: %v", err)
	}

	next := fourShards()
	next[3] = []string{"com.f"} // com.e goes, com.f arrives
	f.roll(t, fixtureGenB, next)

	_, stderr, err := runVerb(t, cmdSync, "-dir", dir, "-concurrency", "2")
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}

	var d delta
	readJSON(t, filepath.Join(dir, deltaName(fixtureGenA, fixtureGenB)), &d)
	if !slices.Equal(d.Added, []string{"com.f"}) {
		t.Errorf("Added = %v, want [com.f]", d.Added)
	}
	if !slices.Equal(d.Removed, []string{"com.e"}) {
		t.Errorf("Removed = %v, want [com.e]", d.Removed)
	}
	if !strings.Contains(stderr, "delta: +1 -1") {
		t.Errorf("stderr does not report the delta:\n%s", stderr)
	}
	// Without -keep, history stays.
	if _, err := os.Stat(filepath.Join(dir, "snapshot-"+fixtureGenA.ID()+".txt.gz")); err != nil {
		t.Error("the previous snapshot was deleted without -keep")
	}
	if store.hitCount(f.shardPath(0)) != 1 {
		t.Error("the new generation's shards were fetched more than once")
	}
}

// `catalog sweep` used to exit 0 with an empty stdout when there was nothing to
// do, which a consumer cannot tell from a run that produced no output for some
// other reason.
func TestSweepUpToDateEmitsARecord(t *testing.T) {
	store, f, dir := newSweepFixture(t)

	snap := filepath.Join(dir, "snapshot-"+fixtureGenA.ID()+".txt.gz")
	sum, err := writeSnapshot(snap, []string{"com.a", "com.b"})
	if err != nil {
		t.Fatal(err)
	}
	have := manifest{
		Generation: fixtureGenA, File: filepath.Base(snap), IDs: 2, SHA256: sum,
		CompletedAt: "2026-08-23T09:00:00Z",
	}
	if err := writeJSON(filepath.Join(dir, "manifest-"+fixtureGenA.ID()+".json"), have); err != nil {
		t.Fatal(err)
	}

	out, stderr, err := runVerb(t, cmdSync, "-dir", dir)
	if err != nil {
		t.Fatalf("an up-to-date sweep failed: %v", err)
	}
	if !strings.Contains(stderr, "already have") {
		t.Errorf("stderr does not say why nothing happened:\n%s", stderr)
	}

	recs := jsonLines(t, out)
	if len(recs) != 1 {
		t.Fatalf("stdout has %d records, want the manifest already on disk:\n%s", len(recs), out)
	}
	var emitted manifest
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &emitted); err != nil {
		t.Fatal(err)
	}
	if emitted.Generation.ID() != fixtureGenA.ID() || emitted.SHA256 != sum {
		t.Errorf("emitted %+v, want the manifest on disk", emitted)
	}
	// completedAt is how a consumer tells "swept just now" from "already had
	// it", so it must be the stored one rather than now.
	if emitted.CompletedAt != have.CompletedAt {
		t.Errorf("completedAt = %q, want the stored %q", emitted.CompletedAt, have.CompletedAt)
	}
	for i := range fourShards() {
		if got := store.hitCount(f.shardPath(i)); got != 0 {
			t.Errorf("shard %d was fetched %d times by a sweep with nothing to do", i, got)
		}
	}
}

// A sweep is 83k requests, so an interrupted one continuing from where it
// stopped is the difference between a usable tool and one you only run in a
// screen session.
func TestSweepResumesFromTheDoneLog(t *testing.T) {
	store, f, dir := newSweepFixture(t)
	preFinished(t, dir, f, sampling{}, 0, 1)

	_, stderr, err := runVerb(t, cmdSync, "-dir", dir, "-concurrency", "2")
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}

	for i, want := range []int{0, 0, 1, 1} {
		if got := store.hitCount(f.shardPath(i)); got != want {
			t.Errorf("shard %d fetched %d times, want %d", i, got, want)
		}
	}
	if !strings.Contains(stderr, "resuming") || !strings.Contains(stderr, "2 of 4 shards left") {
		t.Errorf("stderr does not report the resume:\n%s", stderr)
	}
	ids, err := readSnapshot(filepath.Join(dir, "snapshot-"+fixtureGenA.ID()+".txt.gz"))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"com.a", "com.b", "com.c", "com.d", "com.e"}; !slices.Equal(ids, want) {
		t.Errorf("snapshot = %v, want %v: the partial ids plus the fetched ones", ids, want)
	}
}

// A shard list is a function of the generation *and* the sampling. Appending
// one run's results to another's produces a snapshot whose coverage nothing on
// disk describes.
func TestSweepRefusesAResumeOfAnotherSampling(t *testing.T) {
	store, f, dir := newSweepFixture(t)
	preFinished(t, dir, f, sampling{Pct: 50, Seed: 7}, 0, 1)
	// A planted id that belongs to no shard: if the stale partial is merged,
	// it lands in the snapshot and says so.
	partial := filepath.Join(dir, "partial-"+fixtureGenA.ID()+".txt")
	body, err := os.ReadFile(partial)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(partial, append(body, []byte("com.from.another.run\n")...), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := runVerb(t, cmdSync, "-dir", dir, "-concurrency", "2"); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	for i := range fourShards() {
		if got := store.hitCount(f.shardPath(i)); got != 1 {
			t.Errorf("shard %d fetched %d times; the run should have started over", i, got)
		}
	}
	ids, err := readSnapshot(filepath.Join(dir, "snapshot-"+fixtureGenA.ID()+".txt.gz"))
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(ids, "com.from.another.run") {
		t.Error("a partial file from a differently sampled run was merged into the snapshot")
	}
}

// A 404 mid-sweep means the generation rolled: the work list names files
// Google has replaced, so the run must restart rather than carry on against
// half of each build.
func TestSweepGenerationRollMidSweepRestarts(t *testing.T) {
	store, f, dir := newSweepFixture(t)
	store.set(f.shardPath(2), 404, nil)

	_, _, err := runVerb(t, cmdSync, "-dir", dir, "-concurrency", "1")
	if err == nil || !strings.Contains(err.Error(), "rolled") {
		t.Fatalf("err = %v, want one naming the generation roll", err)
	}
	if _, serr := os.Stat(filepath.Join(dir, "manifest-"+fixtureGenA.ID()+".json")); serr == nil {
		t.Error("a manifest was written for a sweep that hit a rolled generation")
	}
	done, err := readDoneLog(doneLogPath(dir, fixtureGenA))
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range []int{0, 1, 3} {
		if _, ok := done[googleplayscraper.BaseURL+f.shardPath(i)]; !ok {
			t.Errorf("shard %d finished but is not in the done log", i)
		}
	}

	// The new generation sweeps cleanly, and the resume state of the one that
	// rolled is removed rather than left behind for good.
	f.roll(t, fixtureGenB, fourShards())
	_, stderr, err := runVerb(t, cmdSync, "-dir", dir, "-concurrency", "2")
	if err != nil {
		t.Fatalf("sweep of the new generation: %v", err)
	}
	if _, serr := os.Stat(filepath.Join(dir, "manifest-"+fixtureGenB.ID()+".json")); serr != nil {
		t.Error("no manifest for the new generation")
	}
	if !strings.Contains(stderr, "resume state") {
		t.Errorf("stderr does not mention the resume state it removed:\n%s", stderr)
	}
	for _, name := range []string{
		"partial-" + fixtureGenA.ID() + ".txt",
		"done-" + fixtureGenA.ID() + ".log",
	} {
		if _, serr := os.Stat(filepath.Join(dir, name)); serr == nil {
			t.Errorf("%s survived; resume state for a rolled generation is unusable", name)
		}
	}
}

// Without the retry pass a single transient 503 silently costs ~44 app ids,
// and those ids surface as a phantom pair in the deltas: removed next
// generation, added the one after.
func TestSweepRetryPassRecoversATransientShard(t *testing.T) {
	store, f, dir := newSweepFixture(t)
	store.failNext(f.shardPath(2), 500, 1)

	_, stderr, err := runVerb(t, cmdSync, "-dir", dir, "-concurrency", "2")
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if got := store.hitCount(f.shardPath(2)); got != 2 {
		t.Errorf("the transient shard was fetched %d times, want 2 (the failure and the retry)", got)
	}
	if !strings.Contains(stderr, "retrying 1 failed shards") {
		t.Errorf("stderr does not report the retry pass:\n%s", stderr)
	}
	var m manifest
	readJSON(t, filepath.Join(dir, "manifest-"+fixtureGenA.ID()+".json"), &m)
	if m.FailedShard != 0 || m.IDs != 5 {
		t.Errorf("manifest = %+v, want a complete sweep with no failed shards", m)
	}
}

// A snapshot quietly missing shards looks exactly like a catalog that shrank,
// and nothing downstream can tell the difference.
func TestSweepRefusesAManifestWhenAShardKeepsFailing(t *testing.T) {
	store, f, dir := newSweepFixture(t)
	store.set(f.shardPath(2), 500, nil)

	_, _, err := runVerb(t, cmdSync, "-dir", dir, "-concurrency", "2")
	if err == nil || !strings.Contains(err.Error(), "after a retry pass") {
		t.Fatalf("err = %v, want a refusal naming the retry pass", err)
	}
	if got := store.hitCount(f.shardPath(2)); got != 2 {
		t.Errorf("the failing shard was fetched %d times, want 2 (once per pass, not more)", got)
	}
	if _, serr := os.Stat(filepath.Join(dir, "manifest-"+fixtureGenA.ID()+".json")); serr == nil {
		t.Error("a manifest was written for an incomplete sweep")
	}
	done, err := readDoneLog(doneLogPath(dir, fixtureGenA))
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 3 {
		t.Errorf("done log holds %d shards, want the 3 that succeeded", len(done))
	}
}

// The difference between a full snapshot and a sample of the next generation
// is the sampling, not the catalog. Written as a delta it reported 3,260 of
// 5,000 ids removed in the repro, and about 3.5M in production.
func TestSweepSampledAfterCompleteWritesNoDelta(t *testing.T) {
	if picked := sampleShards(4, 50, 1); len(picked) == 0 {
		t.Fatalf("sampleShards(4, 50, 1) picked nothing; the seed no longer exercises this test")
	}

	t.Run("complete then sampled", func(t *testing.T) {
		_, f, dir := newSweepFixture(t)
		if _, _, err := runVerb(t, cmdSync, "-dir", dir, "-concurrency", "2"); err != nil {
			t.Fatalf("first sweep: %v", err)
		}
		f.roll(t, fixtureGenB, fourShards())

		_, stderr, err := runVerb(t, cmdSync, "-dir", dir, "-concurrency", "2", "-sample", "50", "-seed", "1")
		if err != nil {
			t.Fatalf("sampled sweep: %v", err)
		}
		var m manifest
		readJSON(t, filepath.Join(dir, "manifest-"+fixtureGenB.ID()+".json"), &m)
		if m.SamplePct != 50 || m.SampleSeed != 1 {
			t.Errorf("manifest = %+v, want the sampling recorded", m)
		}
		assertNoDelta(t, dir, stderr)
	})

	t.Run("sampled then complete", func(t *testing.T) {
		_, f, dir := newSweepFixture(t)
		if _, _, err := runVerb(t, cmdSync, "-dir", dir, "-concurrency", "2",
			"-sample", "50", "-seed", "1"); err != nil {
			t.Fatalf("sampled sweep: %v", err)
		}
		f.roll(t, fixtureGenB, fourShards())

		_, stderr, err := runVerb(t, cmdSync, "-dir", dir, "-concurrency", "2")
		if err != nil {
			t.Fatalf("complete sweep: %v", err)
		}
		assertNoDelta(t, dir, stderr)
	})
}

func assertNoDelta(t *testing.T, dir, stderr string) {
	t.Helper()
	if deltas, _ := filepath.Glob(filepath.Join(dir, "delta-*.json")); len(deltas) != 0 {
		t.Errorf("a delta was written across differing coverage: %v", deltas)
	}
	if !strings.Contains(stderr, "no delta") ||
		!strings.Contains(stderr, "the sampling, not the catalog") {
		t.Errorf("stderr does not explain the skipped delta:\n%s", stderr)
	}
}

// finish's own view of the same rule, without a server: the sweep now applies
// exactly the conditions `catalog diff` refuses on.
func TestFinishSkipsTheDeltaWhenCoverageDiffers(t *testing.T) {
	for _, tc := range []struct {
		name           string
		prevPct, pct   float64
		wantDeltaFiles int
	}{
		{"complete then sampled", 0, 50, 0},
		{"sampled then complete", 50, 0, 0},
		{"two samples of two generations", 50, 50, 0},
		{"two complete snapshots", 0, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			prevFile := "snapshot-" + fixtureGenA.ID() + ".txt.gz"
			if _, err := writeSnapshot(filepath.Join(dir, prevFile), []string{"com.gone", "com.stays"}); err != nil {
				t.Fatal(err)
			}
			prev := manifest{Generation: fixtureGenA, File: prevFile, IDs: 2, SamplePct: tc.prevPct}

			partial := filepath.Join(dir, "partial-"+fixtureGenB.ID()+".txt")
			if err := os.WriteFile(partial, []byte("com.new\ncom.stays\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			_, _, err := runVerb(t, func([]string) error {
				return finish(dir, fixtureGenB, partial, syncState{Generation: fixtureGenB},
					prev, true, time.Now(), sampling{Pct: tc.pct}, 0)
			})
			if err != nil {
				t.Fatalf("finish: %v", err)
			}
			deltas, _ := filepath.Glob(filepath.Join(dir, "delta-*.json"))
			if len(deltas) != tc.wantDeltaFiles {
				t.Errorf("wrote %v, want %d delta files", deltas, tc.wantDeltaFiles)
			}
		})
	}
}

// -keep looked for delta-<gen>.json, a name the writer has never produced, so
// it deleted snapshots and manifests and left every delta behind for good.
func TestKeepPrunesSnapshotsManifestsAndDeltas(t *testing.T) {
	_, f, dir := newSweepFixture(t)

	sweepGen := func(gen googleplayscraper.Generation, args ...string) string {
		t.Helper()
		_, stderr, err := runVerb(t, cmdSync, append([]string{"-dir", dir, "-concurrency", "2"}, args...)...)
		if err != nil {
			t.Fatalf("sweep %s: %v", gen.ID(), err)
		}
		return stderr
	}

	sweepGen(fixtureGenA)
	f = f.roll(t, fixtureGenB, fourShards())
	sweepGen(fixtureGenB)
	f.roll(t, fixtureGenC, fourShards())
	stderr := sweepGen(fixtureGenC, "-keep", "2")

	if !strings.Contains(stderr, "kept the 2 newest") {
		t.Errorf("stderr does not report the prune:\n%s", stderr)
	}
	// The rule pinned here: a delta goes when either of the generations it
	// names goes, because it then describes a transition into or out of a
	// snapshot that is no longer on disk.
	for _, name := range []string{
		"snapshot-" + fixtureGenA.ID() + ".txt.gz",
		"manifest-" + fixtureGenA.ID() + ".json",
		filepath.Base(deltaName(fixtureGenA, fixtureGenB)),
	} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Errorf("%s survived -keep 2", name)
		}
	}
	for _, name := range []string{
		"snapshot-" + fixtureGenB.ID() + ".txt.gz",
		"manifest-" + fixtureGenB.ID() + ".json",
		"snapshot-" + fixtureGenC.ID() + ".txt.gz",
		"manifest-" + fixtureGenC.ID() + ".json",
		filepath.Base(deltaName(fixtureGenB, fixtureGenC)),
	} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s was deleted, but it belongs to the 2 newest generations", name)
		}
	}
}

// The manifest carries the snapshot's sha256 and is written afterwards, so a
// snapshot that is present but not durable makes the manifest a lie about
// hours of work.
func TestWriteSnapshotIsAtomic(t *testing.T) {
	ids := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("com.example.app%07d", i)
		}
		return out
	}

	for _, tc := range []struct {
		name string
		n    int
	}{
		{"serial", 100},
		{"parallel", 1<<16 + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.n > 1<<16 && runtime.GOMAXPROCS(0) < 2 {
				t.Skip("the parallel path needs more than one core")
			}
			dir := t.TempDir()
			path := filepath.Join(dir, "snapshot-x.txt.gz")
			sum, err := writeSnapshot(path, ids(tc.n))
			if err != nil {
				t.Fatalf("writeSnapshot: %v", err)
			}
			if got := sha256OfFile(t, path); got != sum {
				t.Errorf("returned sha256 %s, file hashes to %s", sum, got)
			}
			back, err := readSnapshot(path)
			if err != nil {
				t.Fatalf("readSnapshot: %v", err)
			}
			if len(back) != tc.n {
				t.Errorf("read back %d ids, wrote %d", len(back), tc.n)
			}
			assertNoTempFiles(t, dir)
		})
	}

	t.Run("a failed write leaves no snapshot", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "snapshot-x.txt.gz")
		// A directory where the temporary file goes: os.Create refuses it.
		if err := os.Mkdir(path+".tmp", 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := writeSnapshot(path, []string{"com.a"}); err == nil {
			t.Fatal("writeSnapshot reported success with an unusable temporary path")
		}
		if _, err := os.Stat(path); err == nil {
			t.Error("the final name exists after a failed write")
		}
	})
}

// A manifest is the only thing that makes a snapshot findable, so a run that
// cannot write one must not look like a finished sweep.
func TestFinishLeavesNoSnapshotWithoutAManifest(t *testing.T) {
	dir := t.TempDir()
	partial := filepath.Join(dir, "partial-"+fixtureGenA.ID()+".txt")
	if err := os.WriteFile(partial, []byte("com.a\ncom.b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The manifest's temporary path is a directory, so writeJSON cannot create it.
	if err := os.Mkdir(filepath.Join(dir, "manifest-"+fixtureGenA.ID()+".json.tmp"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, _, err := runVerb(t, func([]string) error {
		return finish(dir, fixtureGenA, partial, syncState{Generation: fixtureGenA},
			manifest{}, false, time.Now(), sampling{}, 0)
	})
	if err == nil {
		t.Fatal("finish reported success although the manifest could not be written")
	}
	if _, ok := latestManifest(dir); ok {
		t.Error("latestManifest found a manifest after the write failed")
	}
	// The run stays resumable: the partial file is what the next attempt
	// continues from.
	if _, serr := os.Stat(partial); serr != nil {
		t.Error("the partial file was removed although the sweep did not finish")
	}
	assertNoTempFiles(t, dir)
}

// 0 is the flag's own default and has always meant "all". Rejecting it would
// make the default invalid; what was wrong was the error text and the silence.
func TestSweepSampleZeroMeansTheWholeCatalog(t *testing.T) {
	t.Run("a share outside the range is refused before any request", func(t *testing.T) {
		store, _, dir := newSweepFixture(t)
		_, _, err := runVerb(t, cmdSync, "-dir", dir, "-sample=-5")
		if err == nil {
			t.Fatal("a negative share was accepted")
		}
		if !strings.Contains(err.Error(), "-sample") ||
			!strings.Contains(err.Error(), "0 for the whole catalog") {
			t.Errorf("the error does not say what 0 means: %v", err)
		}
		if got := store.hitCount("/robots.txt"); got != 0 {
			t.Errorf("robots.txt was fetched %d times before the flag was checked", got)
		}
	})

	t.Run("an explicit 0 says what it is about to do", func(t *testing.T) {
		store, f, dir := newSweepFixture(t)
		_, stderr, err := runVerb(t, cmdSync, "-dir", dir, "-concurrency", "2", "-sample", "0")
		if err != nil {
			t.Fatalf("sweep: %v", err)
		}
		if !strings.Contains(stderr, "-sample 0 means the whole catalog") {
			t.Errorf("stderr does not warn that 0 is not a small sample:\n%s", stderr)
		}
		for i := range fourShards() {
			if store.hitCount(f.shardPath(i)) != 1 {
				t.Errorf("shard %d was not swept by -sample 0", i)
			}
		}
	})

	t.Run("100 is the whole catalog, not a sample of it", func(t *testing.T) {
		_, _, dir := newSweepFixture(t)
		if _, _, err := runVerb(t, cmdSync, "-dir", dir, "-concurrency", "2", "-sample", "100"); err != nil {
			t.Fatalf("sweep: %v", err)
		}
		var m manifest
		readJSON(t, filepath.Join(dir, "manifest-"+fixtureGenA.ID()+".json"), &m)
		if m.SamplePct != 0 || m.IDs != 5 {
			t.Errorf("manifest = %+v, want a complete sweep", m)
		}
	})
}

// The line described a sweep that was not happening: a sampled run said it was
// filling the snapshot in.
func TestSweepDoesNotClaimFullWhenItIsSampled(t *testing.T) {
	_, _, dir := newSweepFixture(t)
	if err := writeJSON(filepath.Join(dir, "manifest-"+fixtureGenA.ID()+".json"), manifest{
		Generation: fixtureGenA, File: "snapshot-" + fixtureGenA.ID() + ".txt.gz",
		IDs: 1, SamplePct: 1,
	}); err != nil {
		t.Fatal(err)
	}

	_, stderr, err := runVerb(t, cmdSync, "-dir", dir, "-concurrency", "2", "-sample", "50", "-seed", "1")
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if strings.Contains(stderr, "sweeping it in full") {
		t.Errorf("a sampled run said it was sweeping in full:\n%s", stderr)
	}
	if !strings.Contains(stderr, "sweeping it at 50%") {
		t.Errorf("stderr does not say what coverage this run has:\n%s", stderr)
	}
}

// -check was a second surface over `catalog check`, with a poorer record and
// one more request. Refused by name rather than by flag's "flag provided but
// not defined", which would say nothing about where to go.
func TestSweepCheckIsGoneAndPointsAtCatalogCheck(t *testing.T) {
	for _, spelling := range []string{"-check", "--check"} {
		err := cmdSync([]string{spelling, "-dir", t.TempDir()})
		if err == nil {
			t.Fatalf("%s was accepted", spelling)
		}
		if !strings.Contains(err.Error(), "catalog check") {
			t.Errorf("%s: the error does not name the command that replaced it: %v", spelling, err)
		}
	}
}

// parallelShards hands the URL to the callback rather than an index, because
// that is what the resume state is keyed on -- and the sender must never block
// once the context is cancelled, or a stopped sweep hangs instead of stopping.
func TestParallelShardsDrainsAfterCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	urls := make([]string, 100)
	for i := range urls {
		urls[i] = fmt.Sprintf("https://example.invalid/shard-%02d", i)
	}

	var calls atomic.Int64
	done := make(chan error, 1)
	go func() {
		done <- parallelShards(ctx, urls, 4, func(context.Context, string) {
			if calls.Add(1) == 1 {
				cancel()
			}
		})
	}()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("parallelShards did not return: the sender is blocked on a channel nobody reads")
	}
	if n := calls.Load(); n >= int64(len(urls)) {
		t.Errorf("%d of %d urls were worked after the cancel", n, len(urls))
	}
}

// Zero shards is "a snapshot quietly missing shards" at its limit: -sample 100
// used to write a manifest saying the store held no apps, with exit 0.
func TestFinishRefusesASweepThatCollectedNothing(t *testing.T) {
	dir := t.TempDir()
	partial := filepath.Join(dir, "partial-"+fixtureGenA.ID()+".txt")
	if err := os.WriteFile(partial, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := runVerb(t, func([]string) error {
		return finish(dir, fixtureGenA, partial, syncState{Generation: fixtureGenA},
			manifest{}, false, time.Now(), sampling{}, 0)
	})
	if err == nil {
		t.Fatal("a sweep that collected nothing published a manifest")
	}
	if !strings.Contains(err.Error(), "not an empty catalog") {
		t.Errorf("the error reads as an empty store rather than a run that did no work: %v", err)
	}
	if _, serr := os.Stat(filepath.Join(dir, "manifest-"+fixtureGenA.ID()+".json")); serr == nil {
		t.Error("a manifest was written for a sweep with no ids")
	}
}

// An unreadable baseline costs the delta, not the sweep: hours of fetching
// must not be thrown away because the previous snapshot went missing.
func TestFinishKeepsTheSnapshotWhenTheBaselineCannotBeRead(t *testing.T) {
	dir := t.TempDir()
	partial := filepath.Join(dir, "partial-"+fixtureGenB.ID()+".txt")
	if err := os.WriteFile(partial, []byte("com.a\ncom.b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A manifest naming a snapshot that is not there.
	prev := manifest{Generation: fixtureGenA, File: "snapshot-" + fixtureGenA.ID() + ".txt.gz", IDs: 2}

	_, stderr, err := runVerb(t, func([]string) error {
		return finish(dir, fixtureGenB, partial, syncState{Generation: fixtureGenB},
			prev, true, time.Now(), sampling{}, 0)
	})
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if !strings.Contains(stderr, "cannot diff against") {
		t.Errorf("stderr does not say why there is no delta:\n%s", stderr)
	}
	if _, serr := os.Stat(filepath.Join(dir, "manifest-"+fixtureGenB.ID()+".json")); serr != nil {
		t.Error("the manifest was withheld because the baseline could not be read")
	}
	if deltas, _ := filepath.Glob(filepath.Join(dir, "delta-*.json")); len(deltas) != 0 {
		t.Errorf("a delta was written from a baseline that could not be read: %v", deltas)
	}
}

// loadState returns the previous run's Failed list, so a resumed sweep appended
// URLs it already held: the retry pass fetched them twice and `remaining` went
// negative.
func TestSweepDoesNotRetryTheSameShardTwice(t *testing.T) {
	store, f, dir := newSweepFixture(t)
	store.set(f.shardPath(2), 500, nil)

	// An interrupted run that finished shards 0 and 1 and already recorded
	// shard 2 as failed.
	preFinished(t, dir, f, sampling{}, 0, 1)
	state, ok := loadState(filepath.Join(dir, "state.json"), f.gen, sampling{})
	if !ok {
		t.Fatal("the planted state was not accepted")
	}
	state.Failed = []string{googleplayscraper.BaseURL + f.shardPath(2)}
	if err := saveState(filepath.Join(dir, "state.json"), state); err != nil {
		t.Fatal(err)
	}

	_, _, err := runVerb(t, cmdSync, "-dir", dir, "-concurrency", "1")
	if err == nil {
		t.Fatal("a sweep with a permanently failing shard reported success")
	}
	// One shard failed, however many times the list mentioned it.
	if !strings.Contains(err.Error(), "1 shards could not be fetched") {
		t.Errorf("err = %v, want it to count the failing shard once", err)
	}
	if got := store.hitCount(f.shardPath(2)); got != 2 {
		t.Errorf("the failing shard was fetched %d times, want 2: once per pass", got)
	}
}

// The degenerate shapes: no work, and a worker count that would make the
// dispatcher misbehave rather than simply run everything on one goroutine.
func TestParallelShardsHandlesDegenerateInput(t *testing.T) {
	if err := parallelShards(context.Background(), nil, 4, func(context.Context, string) {
		t.Error("fn was called with no urls")
	}); err != nil {
		t.Errorf("an empty url list returned %v", err)
	}

	var calls atomic.Int64
	if err := parallelShards(context.Background(), []string{"a", "b"}, 0,
		func(context.Context, string) { calls.Add(1) }); err != nil {
		t.Errorf("workers=0 returned %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("workers=0 ran %d of 2 urls; it must clamp to one worker, not none", calls.Load())
	}
}

// A file in the directory whose name is not manifest-<date>_<run>.json names no
// generation, and treating it as one would order the prune by a nonsense key.
func TestPruneSnapshotsIgnoresNamesThatAreNotGenerations(t *testing.T) {
	dir := t.TempDir()
	gens := []googleplayscraper.Generation{
		{Date: "2026-08-01", Run: "100"},
		{Date: "2026-08-05", Run: "200"},
	}
	for _, g := range gens {
		for _, name := range []string{"snapshot-" + g.ID() + ".txt.gz", "manifest-" + g.ID() + ".json"} {
			if err := os.WriteFile(filepath.Join(dir, name), make([]byte, 100), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, name := range []string{"manifest-.json", "manifest-nogeneration.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), make([]byte, 100), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if _, removed := pruneSnapshots(dir, 2); removed != 0 {
		t.Errorf("removed %d generations; the two real ones are both within -keep 2", removed)
	}
	for _, g := range gens {
		if _, err := os.Stat(filepath.Join(dir, "manifest-"+g.ID()+".json")); err != nil {
			t.Errorf("%s was pruned because an unparseable name was counted as a generation", g.ID())
		}
	}
}

// A sweep is 83,445 shards over hours, so progress is made durable as it goes
// rather than at the end: a crash costs the few hundred shards since the last
// checkpoint, not the run. 501 shards is one checkpoint plus a remainder,
// which is the smallest fixture that exercises both.
func TestSweepCheckpointsWhileItRuns(t *testing.T) {
	store := newFakeStore(t)
	useFakeClient(t, store.transport())
	dir := t.TempDir()

	shards := make([][]string, 501)
	for i := range shards {
		shards[i] = []string{fmt.Sprintf("com.example.shard%03d", i)}
	}
	newSitemapFixture(t, store, fixtureGenA, shards)

	_, stderr, err := runVerb(t, cmdSync, "-dir", dir, "-concurrency", "4")
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if !strings.Contains(stderr, "/501 shards") {
		t.Errorf("no progress line at the 500-shard checkpoint:\n%s", stderr)
	}
	var m manifest
	readJSON(t, filepath.Join(dir, "manifest-"+fixtureGenA.ID()+".json"), &m)
	if m.IDs != 501 {
		t.Errorf("manifest counts %d ids, want 501", m.IDs)
	}
}
