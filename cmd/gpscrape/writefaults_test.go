package main

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/kryuchenko/google-play-scraper/records"
)

// The write-fault matrix: every file `catalog sweep` and `catalog genres`
// write, against every way a write can fail -- the "repeated situations"
// checklist for file writes, run offline. `catalog diff` writes no file.
//
// Each cell is one run with one fault injected through openWrite and
// renameFile (writefile.go), never by filling a disk. A cell expects one of:
//
//   - fail: the run returns a store_error naming the file; nothing already
//     published (the previous snapshot and manifest, the genre table) changes;
//     no .tmp is left; the lock is gone. With the fault cleared, a rerun
//     resumes or restarts and leaves the directory, and stdout, the same as a
//     clean run's on the same fixture.
//   - ok: the fault does not touch what the run publishes, and the run
//     succeeds with a clean run's result.
//   - n/a: the write never happens; the reason is in the table.

type faultKind int

const (
	faultNoSpace    faultKind = iota // ENOSPC on every write
	faultShortWrite                  // half the first write lands, then ENOSPC
	faultSync                        // fsync reports EIO
	faultClose                       // close reports EIO
	faultRename                      // the rename into place is refused
	faultPermission                  // created in a directory made read-only for it
	faultReplaced                    // at its first fsync (or close), another file takes its name
)

var faultNames = []string{"no space", "short write", "sync error", "close error",
	"rename fails", "permission denied", "removed or replaced"}

const (
	expectFail = "fail"
	expectOK   = "ok"
)

// writeRow is one file. match picks it out by base name (filepath.Match),
// nth says which open of it gets the fault, names is what the error must
// contain, and cells holds the expectation for each faultKind: expectFail,
// expectOK, or "n/a: why".
type writeRow struct {
	file  string
	match string
	nth   int
	names string
	cells [7]string
}

const (
	naRename = "n/a: written in place, never renamed"
	naSync   = "n/a: the lock is never fsynced"
)

var allFail = [7]string{expectFail, expectFail, expectFail, expectFail, expectFail, expectFail, expectFail}

func lockCells() [7]string {
	// Replaced: the lock is read again only at release, which leaves a lock
	// it can read as somebody else's and removes one it cannot.
	return [7]string{expectFail, expectFail, naSync, expectFail, naRename, expectFail, expectOK}
}

func appendCells(replaced string) [7]string {
	return [7]string{expectFail, expectFail, expectFail, expectFail, naRename, expectFail, replaced}
}

var sweepRows = []writeRow{
	// The ids are read back by name at finish.
	{"partial id file", "partial-*.txt", 1, "partial-", appendCells(expectFail)},
	// Read only by a resume; a finished run deletes it.
	{"done log", "done-*.log", 1, "done-", appendCells(expectOK)},
	// The second save is the final checkpoint; the first is before any request.
	{"state.json", "state.json.tmp", 2, "state.json", allFail},
	{"manifest", "manifest-*.json.tmp", 1, "manifest-", allFail},
	{"snapshot", "snapshot-*.txt.gz.tmp", 1, "snapshot-", allFail},
	{"delta", "delta-*.json.tmp", 1, "delta-", allFail},
	{"lock", lockName, 1, lockName, lockCells()},
}

var genresRows = []writeRow{
	{"genre table", "genres.tsv.tmp", 1, "genres.tsv.tmp", allFail},
	{"first-pass merge file", "genres.tsv.merge", 1, "genres.tsv.merge", appendCells(expectFail)},
	// The third checkpoint: two earlier ones are there to resume from.
	{"checkpoint", "genres.tsv.resume.json.tmp", 3, "genres.tsv.resume.json", allFail},
	{"lock", lockName, 1, lockName, lockCells()},
}

// ---- injection ----

type injector struct {
	kind  faultKind
	match string
	nth   int

	mu    sync.Mutex
	opens int
	armed string // the path whose rename is refused
	fired bool
}

func (inj *injector) fire() {
	inj.mu.Lock()
	inj.fired = true
	inj.mu.Unlock()
}

func (inj *injector) hasFired() bool {
	inj.mu.Lock()
	defer inj.mu.Unlock()
	return inj.fired
}

// install swaps the seams for the run and returns the function that puts
// them back: the fault cleared.
func (inj *injector) install(t *testing.T) (clear func()) {
	t.Helper()
	realOpen, realRename := openWrite, renameFile
	openWrite = func(name string, flag int, perm fs.FileMode) (writeFile, error) {
		if ok, _ := filepath.Match(inj.match, filepath.Base(name)); !ok {
			return realOpen(name, flag, perm)
		}
		inj.mu.Lock()
		inj.opens++
		hit := inj.opens == inj.nth
		if hit && inj.kind == faultRename {
			inj.armed = name
		}
		inj.mu.Unlock()
		if !hit || inj.kind == faultRename {
			return realOpen(name, flag, perm)
		}
		if inj.kind == faultPermission {
			inj.fire()
			return openInReadOnlyDir(t, name, flag, perm, realOpen)
		}
		f, err := realOpen(name, flag, perm)
		if err != nil {
			return nil, err
		}
		return &faultyFile{writeFile: f, name: name, inj: inj}, nil
	}
	renameFile = func(from, to string) error {
		inj.mu.Lock()
		refuse := inj.kind == faultRename && from == inj.armed
		inj.mu.Unlock()
		if refuse {
			inj.fire()
			return &os.LinkError{Op: "rename", Old: from, New: to, Err: syscall.EXDEV}
		}
		return realRename(from, to)
	}
	var once sync.Once
	clear = func() { once.Do(func() { openWrite, renameFile = realOpen, realRename }) }
	t.Cleanup(clear)
	return clear
}

// openInReadOnlyDir makes the file's directory read-only for this one create
// and gives it back straight after, so the permission error is the
// operating system's own and the rest of the run is unaffected.
func openInReadOnlyDir(t *testing.T, name string, flag int, perm fs.FileMode,
	open func(string, int, fs.FileMode) (writeFile, error),
) (writeFile, error) {
	dir := filepath.Dir(name)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Errorf("chmod %s: %v", dir, err)
	}
	defer func() {
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Errorf("chmod %s back: %v", dir, err)
		}
	}()
	return open(name, flag, perm)
}

// faultyFile is a real file whose writes, fsync or close fail on cue.
type faultyFile struct {
	writeFile
	name     string
	inj      *injector
	failing  bool // a write has failed; the disk stays full
	replaced bool
}

func (f *faultyFile) Write(p []byte) (int, error) {
	switch {
	case f.failing || f.inj.kind == faultNoSpace:
		f.inj.fire()
		return 0, &fs.PathError{Op: "write", Path: f.name, Err: syscall.ENOSPC}
	case f.inj.kind == faultShortWrite:
		f.inj.fire()
		f.failing = true
		n, _ := f.writeFile.Write(p[:len(p)/2])
		return n, &fs.PathError{Op: "write", Path: f.name, Err: syscall.ENOSPC}
	}
	return f.writeFile.Write(p)
}

func (f *faultyFile) Sync() error {
	switch f.inj.kind {
	case faultSync:
		f.inj.fire()
		return &fs.PathError{Op: "sync", Path: f.name, Err: syscall.EIO}
	case faultReplaced:
		f.replace()
	}
	return f.writeFile.Sync()
}

func (f *faultyFile) Close() error {
	switch f.inj.kind {
	case faultClose:
		f.inj.fire()
		_ = f.writeFile.Close()
		return &fs.PathError{Op: "close", Path: f.name, Err: syscall.EIO}
	case faultReplaced:
		f.replace()
	}
	return f.writeFile.Close()
}

// replace does what another process could: removes the file and puts a
// different one under its name, while this run still has the first open.
func (f *faultyFile) replace() {
	if f.replaced {
		return
	}
	f.replaced = true
	f.inj.fire()
	_ = os.Remove(f.name)
	_ = os.WriteFile(f.name, []byte("not this run's file\n"), 0o644)
}

// ---- checks ----

func skipCellHere(t *testing.T, kind faultKind) {
	t.Helper()
	switch {
	case kind == faultPermission && (runtime.GOOS == "windows" || os.Geteuid() == 0):
		t.Skip("directory permissions do not stop this user here")
	case kind == faultReplaced && runtime.GOOS == "windows":
		t.Skip("Windows does not let another process remove an open file")
	}
}

// dirContents is every file in dir with its bytes.
func dirContents(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out[e.Name()] = string(b)
	}
	return out
}

func manifestWithoutTime(t *testing.T, b []byte) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	delete(m, "completed_at")
	nb, _ := json.Marshal(m)
	return string(nb)
}

// normalized is dirContents with each manifest's completed_at left out: what
// a clean run and a recovered one must agree on.
func normalized(t *testing.T, dir string) map[string]string {
	t.Helper()
	c := dirContents(t, dir)
	for name, b := range c {
		if strings.HasPrefix(name, "manifest-") {
			c[name] = manifestWithoutTime(t, []byte(b))
		}
	}
	return c
}

func diffContents(t *testing.T, got, want map[string]string) {
	t.Helper()
	var names []string
	for n := range got {
		names = append(names, n)
	}
	for n := range want {
		if _, ok := got[n]; !ok {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	for _, n := range names {
		g, inGot := got[n]
		w, inWant := want[n]
		switch {
		case !inWant:
			t.Errorf("%s is left that a clean run does not leave", n)
		case !inGot:
			t.Errorf("%s is missing", n)
		case g != w:
			t.Errorf("%s differs from a clean run's (%d bytes, want %d)", n, len(g), len(w))
		}
	}
}

// checkFailed is the fail expectation, before the rerun.
func checkFailed(t *testing.T, row writeRow, err error, dir string, published map[string]string) {
	t.Helper()
	if err == nil {
		t.Fatal("the run succeeded")
	}
	if code := records.Classify(err); code != records.CodeStore {
		t.Errorf("code %s, want %s: %v", code, records.CodeStore, err)
	}
	if !strings.Contains(err.Error(), row.names) {
		t.Errorf("the error does not name %s: %v", row.names, err)
	}
	now := dirContents(t, dir)
	for name, b := range published {
		if now[name] != b {
			t.Errorf("published %s changed", name)
		}
	}
	for name := range now {
		if strings.HasPrefix(name, "manifest-") {
			if _, was := published[name]; !was {
				t.Errorf("%s was published by a failed run", name)
			}
		}
	}
}

func checkAfterRun(t *testing.T, dir string, inj *injector) {
	t.Helper()
	if !inj.hasFired() {
		t.Fatalf("the fault never fired: nothing opened %s as open number %d", inj.match, inj.nth)
	}
	assertNoTempFiles(t, dir)
	if _, err := os.Stat(lockPath(dir)); err == nil {
		t.Errorf("%s is still held", lockName)
	}
}

// ---- sweep ----

func sweepShardsB() [][]string {
	return [][]string{{"com.a", "com.c"}, {"com.c", "com.f"}, {"com.g"}, {"com.e", "com.h"}}
}

// sweepAB sweeps generation A cleanly into dir and publishes B: the cell's
// run is the sweep of B, with A's snapshot and manifest as the published
// state it must not damage and a delta to write.
func sweepAB(t *testing.T) string {
	t.Helper()
	store := newFakeStore(t)
	useFakeClient(t, store.transport())
	f := newSitemapFixture(t, store, fixtureGenA, fourShards())
	dir := t.TempDir()
	if _, stderr, err := runVerb(t, cmdSync, "-dir", dir, "-concurrency", "2"); err != nil {
		t.Fatalf("sweep A: %v\n%s", err, stderr)
	}
	f.roll(t, fixtureGenB, sweepShardsB())
	return dir
}

func TestWriteFaultsSweep(t *testing.T) {
	refDir := sweepAB(t)
	refOut, stderr, err := runVerb(t, cmdSync, "-dir", refDir, "-concurrency", "2")
	if err != nil {
		t.Fatalf("clean sweep B: %v\n%s", err, stderr)
	}
	want := normalized(t, refDir)
	wantOut := manifestWithoutTime(t, []byte(refOut))

	for _, row := range sweepRows {
		for kind, expect := range row.cells {
			t.Run(row.file+"/"+faultNames[kind], func(t *testing.T) {
				if reason, na := strings.CutPrefix(expect, "n/a: "); na {
					t.Skip(reason)
				}
				skipCellHere(t, faultKind(kind))
				dir := sweepAB(t)
				published := dirContents(t, dir)

				inj := &injector{kind: faultKind(kind), match: row.match, nth: row.nth}
				clearFault := inj.install(t)
				out, stderr, err := runVerb(t, cmdSync, "-dir", dir, "-concurrency", "2")
				clearFault()
				checkAfterRun(t, dir, inj)

				if expect == expectFail {
					checkFailed(t, row, err, dir, published)
					out, stderr, err = runVerb(t, cmdSync, "-dir", dir, "-concurrency", "2")
				}
				if err != nil {
					t.Fatalf("run after the fault: %v\n%s", err, stderr)
				}
				if got := manifestWithoutTime(t, []byte(out)); got != wantOut {
					t.Errorf("stdout %s, want %s", got, wantOut)
				}
				diffContents(t, normalized(t, dir), want)
			})
		}
	}
}

// ---- genres ----

func TestWriteFaultsGenres(t *testing.T) {
	smallCheckpoints(t)
	wantTable, wantIDs := reference(t)

	for _, row := range genresRows {
		for kind, expect := range row.cells {
			t.Run(row.file+"/"+faultNames[kind], func(t *testing.T) {
				if reason, na := strings.CutPrefix(expect, "n/a: "); na {
					t.Skip(reason)
				}
				skipCellHere(t, faultKind(kind))
				newResumeStore(t)
				dir := t.TempDir()
				writeResumeFixture(t, dir)
				published := dirContents(t, dir)

				inj := &injector{kind: faultKind(kind), match: row.match, nth: row.nth}
				clearFault := inj.install(t)
				out, stderr, err := runResumeGenres(t, dir)
				clearFault()
				checkAfterRun(t, dir, inj)

				all := out
				if expect == expectFail {
					checkFailed(t, row, err, dir, published)
					out, stderr, err = runResumeGenres(t, dir)
					all += "\n" + out
				}
				if err != nil {
					t.Fatalf("run after the fault: %v\n%s", err, stderr)
				}
				got, rerr := os.ReadFile(genresPath(dir))
				if rerr != nil {
					t.Fatal(rerr)
				}
				if !bytes.Equal(got, wantTable) {
					t.Errorf("table differs from a clean run's (%d bytes, want %d)", len(got), len(wantTable))
				}
				if ids := reportedIDs(t, all); !slices.Equal(ids, wantIDs) {
					t.Errorf("reported %d ids across the runs, want %d", len(ids), len(wantIDs))
				}
				// Nothing but the fixture and the table: no merge file or
				// checkpoint left behind.
				now := dirContents(t, dir)
				delete(now, filepath.Base(genresPath(dir)))
				for name, b := range published {
					if name != filepath.Base(genresPath(dir)) && now[name] != b {
						t.Errorf("%s changed", name)
					}
					delete(now, name)
				}
				for name := range now {
					t.Errorf("%s is left behind", name)
				}
			})
		}
	}
}
