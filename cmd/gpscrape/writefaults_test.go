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
	faultPermission                  // created or opened with its directory, and the file, made read-only
	faultReplaced                    // at its first fsync (or close), another file takes its name
	faultDirSync                     // the directory's fsync after the rename reports EIO
)

var faultNames = []string{"no space", "short write", "sync error", "close error",
	"rename fails", "permission denied", "removed or replaced", "directory sync fails"}

const (
	expectFail = "fail"
	expectOK   = "ok"
)

// writeRow is one file. match picks it out by base name (filepath.Match),
// nth says which open of it gets the fault, names is what the error must
// contain, and cells holds the expectation for each faultKind: expectFail,
// expectOK, or "n/a: why". plant, when set, prepares the directory before the
// run: resume state with a torn last line, for the rows about the repair.
type writeRow struct {
	file  string
	match string
	nth   int
	names string
	cells [8]string
	plant func(t *testing.T, dir string, f *sitemapFixture)
}

const (
	naRename  = "n/a: written in place, never renamed"
	naDirSync = "n/a: nothing is renamed, so there is no new name to make durable"
	naNoWrite = "n/a: the repair writes nothing; it cuts the file and fsyncs it"
)

var allFail = [8]string{expectFail, expectFail, expectFail, expectFail, expectFail, expectFail, expectFail, expectFail}

func lockCells() [8]string {
	// Replaced: the lock is read again only at release, which leaves a lock
	// it can read as somebody else's and removes one it cannot.
	return [8]string{expectFail, expectFail, expectFail, expectFail, naRename, expectFail, expectOK, expectFail}
}

func appendCells(replaced string) [8]string {
	return [8]string{expectFail, expectFail, expectFail, expectFail, naRename, expectFail, replaced, naDirSync}
}

var repairCells = [8]string{naNoWrite, naNoWrite, expectFail, expectFail, naRename, expectFail, expectFail, naDirSync}

// plantTorn leaves the state of a sweep of f killed after shard 0, with the
// last line of the file named by which cut in half.
func plantTorn(which func(dir string, f *sitemapFixture) string) func(*testing.T, string, *sitemapFixture) {
	return func(t *testing.T, dir string, f *sitemapFixture) {
		t.Helper()
		preFinished(t, dir, f, sampling{}, 0)
		path := which(dir, f)
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		writeFixtureFile(t, path, string(b)+"com.tor")
	}
}

var (
	partialOf = func(dir string, f *sitemapFixture) string {
		return filepath.Join(dir, "partial-"+f.gen.ID()+".txt")
	}
	doneOf = func(dir string, f *sitemapFixture) string { return doneLogPath(dir, f.gen) }
)

var sweepRows = []writeRow{
	// The ids are read back by name at finish.
	{"partial id file", "partial-*.txt", 1, "partial-", appendCells(expectFail), nil},
	// Read only by a resume; a finished run deletes it.
	{"done log", "done-*.log", 1, "done-", appendCells(expectOK), nil},
	// The open that cuts a torn last line on resume.
	{"partial id file repair", "partial-*.txt", 1, "partial-", repairCells, plantTorn(partialOf)},
	{"done log repair", "done-*.log", 1, "done-", repairCells, plantTorn(doneOf)},
	// The second save is the final checkpoint; the first is before any request.
	{"state.json", "state.json.tmp", 2, "state.json", allFail, nil},
	{"manifest", "manifest-*.json.tmp", 1, "manifest-", allFail, nil},
	{"snapshot", "snapshot-*.txt.gz.tmp", 1, "snapshot-", allFail, nil},
	{"delta", "delta-*.json.tmp", 1, "delta-", allFail, nil},
	{"lock", lockName, 1, lockName, lockCells(), nil},
}

var genresRows = []writeRow{
	{"genre table", "genres.tsv.tmp", 1, "genres.tsv", allFail, nil},
	{"first-pass merge file", "genres.tsv.merge", 1, "genres.tsv.merge", appendCells(expectFail), nil},
	// The third checkpoint: two earlier ones are there to resume from.
	{"checkpoint", "genres.tsv.resume.json.tmp", 3, "genres.tsv.resume.json", allFail, nil},
	{"lock", lockName, 1, lockName, lockCells(), nil},
}

// ---- injection ----

type injector struct {
	kind  faultKind
	match string
	nth   int

	mu        sync.Mutex
	opens     int
	armed     string // the path whose rename is refused
	armedDir  string // the directory whose next fsync fails
	published string // the name a directory-sync fault lets through
	fired     bool
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
		inj.mu.Lock()
		dirFault := inj.armedDir != "" && name == inj.armedDir
		if dirFault {
			inj.armedDir = ""
		}
		inj.mu.Unlock()
		if dirFault {
			f, err := realOpen(name, flag, perm)
			if err != nil {
				return nil, err
			}
			return &faultyFile{writeFile: f, name: name, inj: inj}, nil
		}
		if ok, _ := filepath.Match(inj.match, filepath.Base(name)); !ok {
			return realOpen(name, flag, perm)
		}
		inj.mu.Lock()
		inj.opens++
		hit := inj.opens == inj.nth
		if hit && inj.kind == faultRename {
			inj.armed = name
		}
		if hit && inj.kind == faultDirSync {
			inj.armedDir = filepath.Dir(name)
			inj.published = strings.TrimSuffix(filepath.Base(name), ".tmp")
		}
		inj.mu.Unlock()
		if !hit || inj.kind == faultRename || inj.kind == faultDirSync {
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

// openInReadOnlyDir makes the file's directory -- and the file, if it is
// there already -- read-only for this one open and gives them back straight
// after, so the permission error is the operating system's own and the rest
// of the run is unaffected.
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
	if _, err := os.Stat(name); err == nil {
		if err := os.Chmod(name, 0o444); err != nil {
			t.Errorf("chmod %s: %v", name, err)
		}
		defer func() {
			if err := os.Chmod(name, 0o644); err != nil {
				t.Errorf("chmod %s back: %v", name, err)
			}
		}()
	}
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
	case faultSync, faultDirSync: // only a directory is wrapped for faultDirSync
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
//
// A directory fsync fails after the rename, so the new file is in place,
// whole, and only its durability is in doubt: that one name may differ.
func checkFailed(t *testing.T, row writeRow, inj *injector, err error, dir string, published map[string]string) {
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
		if now[name] != b && name != inj.published {
			t.Errorf("published %s changed", name)
		}
	}
	for name := range now {
		if strings.HasPrefix(name, "manifest-") && name != inj.published {
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
func sweepAB(t *testing.T) (string, *sitemapFixture) {
	t.Helper()
	store := newFakeStore(t)
	useFakeClient(t, store.transport())
	f := newSitemapFixture(t, store, fixtureGenA, fourShards())
	dir := t.TempDir()
	if _, stderr, err := runVerb(t, cmdSync, "-dir", dir, "-concurrency", "2"); err != nil {
		t.Fatalf("sweep A: %v\n%s", err, stderr)
	}
	return dir, f.roll(t, fixtureGenB, sweepShardsB())
}

func TestWriteFaultsSweep(t *testing.T) {
	refDir, _ := sweepAB(t)
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
				dir, fB := sweepAB(t)
				published := dirContents(t, dir)
				if row.plant != nil {
					row.plant(t, dir, fB)
				}

				inj := &injector{kind: faultKind(kind), match: row.match, nth: row.nth}
				clearFault := inj.install(t)
				out, stderr, err := runVerb(t, cmdSync, "-dir", dir, "-concurrency", "2")
				clearFault()
				checkAfterRun(t, dir, inj)

				if expect == expectFail {
					checkFailed(t, row, inj, err, dir, published)
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
					checkFailed(t, row, inj, err, dir, published)
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

// The id file lost between runs -- removed, emptied, cut short -- while the
// done log still lists the shards whose ids it held. A resume used to skip
// those shards and publish a snapshot without their ids; it now starts the
// generation over, and the snapshot is whole.
func TestSweepRestartsWhenThePartialFileLostIDs(t *testing.T) {
	for _, c := range []struct {
		name   string
		damage func(t *testing.T, dir string, f *sitemapFixture)
	}{
		{"removed", func(t *testing.T, dir string, f *sitemapFixture) {
			if err := os.Remove(partialOf(dir, f)); err != nil {
				t.Fatal(err)
			}
		}},
		{"emptied", func(t *testing.T, dir string, f *sitemapFixture) {
			writeFixtureFile(t, partialOf(dir, f), "")
		}},
		{"cut short", func(t *testing.T, dir string, f *sitemapFixture) {
			writeFixtureFile(t, partialOf(dir, f), "com.a\n")
		}},
		// The checkpoint that would have counted them never landed: the
		// done log is ahead of state.json, which records no ids at all.
		{"removed, no count recorded", func(t *testing.T, dir string, f *sitemapFixture) {
			if err := os.Remove(partialOf(dir, f)); err != nil {
				t.Fatal(err)
			}
			if err := saveState(filepath.Join(dir, "state.json"), syncState{Generation: f.gen}); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, f, dir := newSweepFixture(t)
			preFinished(t, dir, f, sampling{}, 0, 1)
			c.damage(t, dir, f)

			_, stderr, err := runVerb(t, cmdSync, "-dir", dir, "-concurrency", "2")
			if err != nil {
				t.Fatalf("sweep: %v\n%s", err, stderr)
			}
			if !strings.Contains(stderr, "swept again from the start") {
				t.Errorf("stderr does not say the generation restarted:\n%s", stderr)
			}
			ids, err := readSnapshot(filepath.Join(dir, "snapshot-"+f.gen.ID()+".txt.gz"))
			if err != nil {
				t.Fatal(err)
			}
			if want := []string{"com.a", "com.b", "com.c", "com.d", "com.e"}; !slices.Equal(ids, want) {
				t.Errorf("snapshot = %v, want %v", ids, want)
			}
		})
	}
}
