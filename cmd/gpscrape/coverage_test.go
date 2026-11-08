package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"bufio"

	googleplayscraper "github.com/kryuchenko/google-play-scraper"
)

// Tests for paths mutation testing showed no test ran: two whole verbs, the
// tails of the snapshot diff, the merge's refusals, and the error paths that
// decide which file a message names or what is left on disk.

// catalog check answers from the store's generation and the directory's
// newest manifest: not up to date before a sweep, up to date after one, and
// not up to date when what is on disk is only a sample.
func TestCatalogCheckAgainstTheDirectory(t *testing.T) {
	_, _, dir := newSweepFixture(t)
	check := func() checkRecord {
		t.Helper()
		out, _, err := runVerb(t, catalogCheck, "-dir", dir)
		if err != nil {
			t.Fatalf("catalog check: %v", err)
		}
		var rec checkRecord
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &rec); err != nil {
			t.Fatalf("%v: %q", err, out)
		}
		return rec
	}

	rec := check()
	if rec.Generation != fixtureGenA.ID() || rec.UpToDate || rec.Have != "" {
		t.Errorf("before a sweep: %+v", rec)
	}
	if _, _, err := runVerb(t, cmdSync, "-dir", dir, "-concurrency", "2"); err != nil {
		t.Fatal(err)
	}
	if rec := check(); !rec.UpToDate || rec.Have != fixtureGenA.ID() {
		t.Errorf("after a sweep: %+v", rec)
	}
	if _, _, err := runVerb(t, cmdSync, "-dir", dir, "-sample", "50", "-force"); err != nil {
		t.Fatal(err)
	}
	if rec := check(); rec.UpToDate || rec.HaveSample != 50 {
		t.Errorf("after a 50%% sample of the same generation: %+v; a sample is not the generation", rec)
	}
	if _, _, err := runVerb(t, catalogCheck, "-dir", dir, "extra"); err == nil {
		t.Error("a positional argument was accepted")
	}
}

// catalog size reads the shards and reports an estimate; on the fixture it
// reads every shard, so the estimate is exact and meets its target.
func TestCatalogSizeOnTheFixture(t *testing.T) {
	_, f, _ := newSweepFixture(t)
	out, _, err := runVerb(t, catalogSize, "-pilot", "2", "-precision", "50%")
	if err != nil {
		t.Fatalf("catalog size: %v", err)
	}
	var rec sizeRecord
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &rec); err != nil {
		t.Fatalf("%v: %q", err, out)
	}
	total := 0
	for _, ids := range f.ids {
		total += len(ids)
	}
	if rec.Generation != fixtureGenA.ID() || rec.ShardsTotal != len(f.ids) || rec.Apps <= 0 || rec.Apps > 2*total {
		t.Errorf("record = %+v (fixture has %d shards, %d ids)", rec, len(f.ids), total)
	}

	if _, _, err := runVerb(t, catalogSize, "-exact"); err == nil || !strings.Contains(err.Error(), "catalog sweep") {
		t.Errorf("-exact: %v, want a pointer to catalog sweep", err)
	}
	if _, _, err := runVerb(t, catalogSize, "-precision", "lots"); err == nil {
		t.Error("an unparseable -precision was accepted")
	}
}

// The diff's tails: ids left on one side after the other ends are all added
// or all removed, and the streamed diff agrees with the slice one.
func TestDiffSnapshotsTails(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, ids ...string) string {
		p := filepath.Join(dir, name)
		if _, err := writeSnapshot(p, ids); err != nil {
			t.Fatal(err)
		}
		return p
	}
	from, to := fixtureGenA, fixtureGenB
	for name, c := range map[string]struct{ old, new []string }{
		"old runs longer": {[]string{"a", "b", "x", "y"}, []string{"a", "b"}},
		"new runs longer": {[]string{"a"}, []string{"a", "m", "n"}},
		"disjoint":        {[]string{"a", "c"}, []string{"b", "d"}},
		"empty old":       {nil, []string{"a"}},
		"empty new":       {[]string{"a"}, nil},
	} {
		o := write(name+"-old.txt.gz", c.old...)
		n := write(name+"-new.txt.gz", c.new...)
		got, err := diffSnapshots(from, to, o, n)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		want := diff(from, to, c.old, c.new)
		if !slices.Equal(got.Added, want.Added) || !slices.Equal(got.Removed, want.Removed) {
			t.Errorf("%s: got +%v -%v, want +%v -%v", name, got.Added, got.Removed, want.Added, want.Removed)
		}
	}

	// A snapshot that is not gzip, or is missing, is an error naming it.
	bad := filepath.Join(dir, "bad.txt.gz")
	if err := os.WriteFile(bad, []byte("not gzip"), 0o644); err != nil {
		t.Fatal(err)
	}
	good := write("good.txt.gz", "a")
	if _, err := diffSnapshots(from, to, bad, good); err == nil || !strings.Contains(err.Error(), "bad.txt.gz") {
		t.Errorf("corrupt old snapshot: %v", err)
	}
	if _, err := diffSnapshots(from, to, good, filepath.Join(dir, "missing.txt.gz")); err == nil || !strings.Contains(err.Error(), "missing.txt.gz") {
		t.Errorf("missing new snapshot: %v", err)
	}
}

// catalog diff's argument and manifest checks.
func TestCatalogDiffRefusals(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runVerb(t, catalogDiff, "-dir", dir, "only-one.txt.gz"); err == nil || !strings.Contains(err.Error(), "two snapshot files") {
		t.Errorf("one argument: %v", err)
	}
	a := filepath.Join(dir, "snapshot-"+fixtureGenA.ID()+".txt.gz")
	if _, err := writeSnapshot(a, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	odd := filepath.Join(dir, "renamed.txt.gz")
	if _, err := writeSnapshot(odd, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runVerb(t, catalogDiff, "-dir", dir, a, odd); err == nil || !strings.Contains(err.Error(), "renamed.txt.gz") {
		t.Errorf("a snapshot whose name gives no generation: %v", err)
	}
	b := filepath.Join(dir, "snapshot-"+fixtureGenB.ID()+".txt.gz")
	if _, err := writeSnapshot(b, []string{"b"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runVerb(t, catalogDiff, "-dir", dir, a, b); err == nil || !strings.Contains(err.Error(), "no manifest") {
		t.Errorf("snapshots without manifests: %v", err)
	}
}

// The settlement refuses a first-pass file that does not match what the
// first pass recorded, rather than guessing.
func TestSettleTableRefusesAMergeFileOutOfStep(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "genres.tsv")
	writeTable(t, path, "a\tX")
	for name, c := range map[string]struct {
		merge  string
		absent []string
		want   string
	}{
		"absent id not the next one": {"A\ta\tX\n", []string{"b"}, "out of step"},
		"unknown mark":               {"Q\ta\tX\n", nil, "unknown mark"},
	} {
		merged := filepath.Join(dir, "merge")
		if err := os.WriteFile(merged, []byte(c.merge), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := settleTable(merged, path, c.absent, func(int) (settled, string) { return settledGone, "" }, false)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
		if got := readTable(t, path); !slices.Equal(got, []string{"a\tX"}) {
			t.Errorf("%s: table replaced: %q", name, got)
		}
	}
}

// A table line with no tab is not a row; the walk steps over it.
func TestTableCursorSkipsLinesWithoutATab(t *testing.T) {
	path := filepath.Join(t.TempDir(), "genres.tsv")
	writeTable(t, path, "a\tX", "not a row", "b\tY")
	prev, err := openTable(path)
	if err != nil {
		t.Fatal(err)
	}
	defer prev.close()
	var got []string
	for ; prev.ok; prev.next() {
		got = append(got, prev.id)
	}
	if !slices.Equal(got, []string{"a", "b"}) || prev.err() != nil {
		t.Errorf("rows %v, err %v", got, prev.err())
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

// A failure flushing the ids inside writeDoneLine is marked as the id file's,
// so the message names partial-<gen>.txt, not the done log.
func TestWriteDoneLineMarksAnIDFileFailure(t *testing.T) {
	ids := bufio.NewWriterSize(failWriter{}, 16)
	done := bufio.NewWriterSize(io.Discard, 16)
	_, _ = ids.WriteString("com.example.app\n")
	err := writeDoneLine(ids, done, strings.Repeat("u", 40), 1)
	var flush errFlushIDs
	if !errors.As(err, &flush) || !strings.Contains(err.Error(), "disk full") || errors.Unwrap(err) == nil {
		t.Errorf("err = %v (%T), want an errFlushIDs wrapping the write error", err, err)
	}
	if err := writeDoneLine(bufio.NewWriter(io.Discard), bufio.NewWriterSize(failWriter{}, 16), strings.Repeat("u", 40), 1); err == nil || errors.As(err, &flush) {
		t.Errorf("a done-log failure: %v, want an error that is not the id file's", err)
	}
}

// A file the sweep cannot open for repair is an error, not a resume onto a
// torn line.
func TestTruncateTornLineOnAReadOnlyFile(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permissions do not stop this user here")
	}
	path := filepath.Join(t.TempDir(), "partial.txt")
	if err := os.WriteFile(path, []byte("a\ntorn"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := truncateTornLine(path); err == nil {
		t.Error("a read-only file was reported repaired")
	}
}

// The sweep stops, naming the file, when its id file cannot be opened.
func TestSweepNamesAnUnopenablePartialFile(t *testing.T) {
	_, f, dir := newSweepFixture(t)
	partial := filepath.Join(dir, "partial-"+f.gen.ID()+".txt")
	if err := os.MkdirAll(filepath.Join(partial, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, _, err := runVerb(t, cmdSync, "-dir", dir, "-concurrency", "2")
	if err == nil || !strings.Contains(err.Error(), "partial-") {
		t.Errorf("err = %v, want one naming the partial file", err)
	}
}

// catalog size checks its estimate against the directory's complete snapshot
// of the same generation: equal when every shard was read, and a snapshot
// short of it -- ids lost without an error -- is flagged.
func TestCatalogSizeChecksTheSnapshot(t *testing.T) {
	store := newFakeStore(t)
	useFakeClient(t, store.transport())
	newSitemapFixture(t, store, fixtureGenA, [][]string{{"com.a", "com.b"}, {"com.c"}, {"com.d", "com.e"}, {"com.f"}})
	dir := t.TempDir()
	if _, _, err := runVerb(t, cmdSync, "-dir", dir, "-concurrency", "2"); err != nil {
		t.Fatal(err)
	}
	size := func() sizeRecord {
		t.Helper()
		out, _, err := runVerb(t, catalogSize, "-dir", dir, "-pilot", "4", "-precision", "50%")
		if err != nil {
			t.Fatal(err)
		}
		var rec sizeRecord
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &rec); err != nil {
			t.Fatal(err)
		}
		return rec
	}
	rec := size()
	if rec.SnapshotIDs != 6 || rec.SnapshotAgrees == nil || !*rec.SnapshotAgrees || rec.Warning != "" {
		t.Errorf("a complete snapshot: %+v", rec)
	}

	// A snapshot that lost an id.
	man := filepath.Join(dir, "manifest-"+fixtureGenA.ID()+".json")
	var m manifest
	readJSON(t, man, &m)
	m.IDs = 5
	if err := writeJSON(man, m); err != nil {
		t.Fatal(err)
	}
	rec = size()
	if rec.SnapshotAgrees == nil || *rec.SnapshotAgrees || !strings.Contains(rec.Warning, "lost without an error") {
		t.Errorf("a snapshot one id short: %+v", rec)
	}

	// -dir "" skips the check.
	out, _, err := runVerb(t, catalogSize, "-dir", "", "-pilot", "4", "-precision", "50%")
	if err != nil || strings.Contains(out, "snapshot_") {
		t.Errorf("-dir \"\": %v %s", err, out)
	}
}

func TestSnapshotAgrees(t *testing.T) {
	est := googleplayscraper.CatalogSize{Apps: 1000, HalfWidth: 19.6} // sigma 10
	for ids, want := range map[int]bool{1000: true, 1030: true, 970: true, 1031: false, 969: false} {
		if got := snapshotAgrees(ids, est); got != want {
			t.Errorf("snapshotAgrees(%d) = %v, want %v", ids, got, want)
		}
	}
	exact := googleplayscraper.CatalogSize{Apps: 10, Exact: true}
	if !snapshotAgrees(10, exact) || snapshotAgrees(9, exact) {
		t.Error("an exact count must match exactly")
	}
}

// A delta that cannot be computed -- the previous snapshot corrupt -- is
// recorded in the new generation's manifest, not only warned about.
func TestSweepRecordsASkippedDeltaInTheManifest(t *testing.T) {
	_, f, dir := newSweepFixture(t)
	if _, _, err := runVerb(t, cmdSync, "-dir", dir, "-concurrency", "2"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "snapshot-"+fixtureGenA.ID()+".txt.gz"), []byte("corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.roll(t, fixtureGenB, fourShards())
	if _, _, err := runVerb(t, cmdSync, "-dir", dir, "-concurrency", "2"); err != nil {
		t.Fatal(err)
	}
	var m manifest
	readJSON(t, filepath.Join(dir, "manifest-"+fixtureGenB.ID()+".json"), &m)
	if !strings.Contains(m.DeltaError, "cannot diff against") {
		t.Errorf("manifest = %+v, want delta_error naming the previous snapshot", m)
	}
}

// The two verbs that run for hours default to -adaptive; the rest do not.
func TestLongVerbsDefaultToAdaptive(t *testing.T) {
	for name, run := range map[string]func([]string) error{"catalog sweep": cmdSync, "catalog genres": catalogGenres, "catalog size": catalogSize} {
		describing, described = true, nil
		_ = run(nil)
		describing = false
		if described == nil {
			t.Fatalf("%s did not register its flags", name)
		}
		want := name != "catalog size"
		if got := described.Lookup("adaptive").DefValue == "true"; got != want {
			t.Errorf("%s: -adaptive default %v, want %v", name, got, want)
		}
	}
}

// -adaptive-min lowers the controller's floor below -throttle only when it
// is lower; otherwise the floor is -throttle.
func TestProbeFloor(t *testing.T) {
	for _, c := range []struct {
		throttle, min, want time.Duration
	}{
		{200 * time.Millisecond, 0, 200 * time.Millisecond},
		{200 * time.Millisecond, 40 * time.Millisecond, 40 * time.Millisecond},
		{200 * time.Millisecond, 500 * time.Millisecond, 200 * time.Millisecond},
	} {
		cm := &common{throttle: c.throttle, adaptiveMin: c.min}
		if got := cm.probeFloor(); got != c.want {
			t.Errorf("throttle %v, -adaptive-min %v: floor %v, want %v", c.throttle, c.min, got, c.want)
		}
	}
}
