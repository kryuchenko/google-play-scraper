package main

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	googleplayscraper "github.com/kryuchenko/google-play-scraper"
)

// The snapshot directory's lock.
//
// Two things have to hold together and they pull in opposite directions: a
// second writer must be refused while the first is running, and a lock left by
// a run that died must not shut the directory for good. Everything below is
// one of those two, or the line between them -- a lock recorded on another
// host, which this machine cannot decide about either way.

func thisHost(t *testing.T) string {
	t.Helper()
	h, err := os.Hostname()
	if err != nil {
		t.Fatalf("hostname: %v", err)
	}
	return h
}

// plantLock writes a lock file as though another run had taken it.
func plantLock(t *testing.T, dir string, o lockOwner) {
	t.Helper()
	b, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, lockPath(dir), string(b)+"\n")
}

// deadPID returns a pid that named a process on this machine and no longer
// does. Started and reaped rather than invented: a number picked out of the air
// is either alive -- in which case the test proves the opposite of what it says
// -- or unallocatable somewhere. The test binary is the one executable every
// platform running these tests is certain to have, and -test.run=^$ makes it
// match no test and exit immediately.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start a process to reap: %v", err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Wait()
	return pid
}

func TestLockRefusesASecondHolderAndReleasesOnce(t *testing.T) {
	dir := t.TempDir()

	lock, err := lockDir(dir)
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}

	// The file says who holds it. That record is the whole staleness policy:
	// without a pid and a host there is nothing to decide with later.
	owner, err := readLockOwner(lockPath(dir))
	if err != nil {
		t.Fatalf("read back the lock: %v", err)
	}
	if owner.PID != os.Getpid() || owner.Host != thisHost(t) {
		t.Errorf("lock records %+v, want this process on this host", owner)
	}
	if _, perr := time.Parse(time.RFC3339, owner.Started); perr != nil {
		t.Errorf("started = %q, which does not parse as RFC3339: %v", owner.Started, perr)
	}

	_, err = lockDir(dir)
	if err == nil {
		t.Fatal("a second run took a lock that was already held")
	}
	// The refusal has to be actionable on its own: a cron job's operator gets
	// this line and nothing else.
	for _, want := range []string{
		strconv.Itoa(os.Getpid()), thisHost(t), owner.Started, lockPath(dir), "Delete",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}

	lock.release()
	if _, serr := os.Stat(lockPath(dir)); !os.IsNotExist(serr) {
		t.Errorf("release left %s behind", lockName)
	}
	// Idempotent, because the ordinary path releases twice: the verb's defer
	// and the exit hook. The second release must not take a lock off whoever
	// holds it now.
	again, err := lockDir(dir)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	lock.release()
	if _, serr := os.Stat(lockPath(dir)); serr != nil {
		t.Error("a stale release removed the lock a later run was holding")
	}
	again.release()
}

// The refusal tells a person how to clear a lock by hand, so a person will,
// and sometimes while the run that took it is still alive. The next run then
// gets in underneath, and this one's release would remove *its* lock -- one
// mistake turning into two concurrent sweeps, which is the thing being
// prevented.
func TestReleaseLeavesALockItNoLongerOwns(t *testing.T) {
	dir := t.TempDir()
	lock, err := lockDir(dir)
	if err != nil {
		t.Fatalf("lock: %v", err)
	}

	// Someone deleted the file and another run took the directory.
	replacement := lockOwner{PID: os.Getpid() + 1, Host: thisHost(t), Started: "2026-09-02T00:00:00Z"}
	plantLock(t, dir, replacement)

	lock.release()
	held, err := readLockOwner(lockPath(dir))
	if err != nil || held != replacement {
		t.Errorf("release removed the lock another run was holding: %+v (%v)", held, err)
	}
}

// A crashed sweep must not shut the directory. The pid is the evidence, and it
// is only evidence on the machine that recorded it.
func TestLockReclaimsADeadHoldersLock(t *testing.T) {
	dir := t.TempDir()
	dead := deadPID(t)
	plantLock(t, dir, lockOwner{
		PID: dead, Host: thisHost(t), Started: "2026-09-01T00:00:00Z",
	})

	var lock *dirLock
	var err error
	stderr := captureStderr(t, func() { lock, err = lockDir(dir) })
	if err != nil {
		t.Fatalf("a lock left by a dead process was not reclaimed: %v", err)
	}
	defer lock.release()

	if !strings.Contains(stderr, "no longer running") || !strings.Contains(stderr, strconv.Itoa(dead)) {
		t.Errorf("stderr does not say a stale lock was removed:\n%s", stderr)
	}
	owner, err := readLockOwner(lockPath(dir))
	if err != nil {
		t.Fatalf("read back the lock: %v", err)
	}
	if owner.PID != os.Getpid() {
		t.Errorf("lock still records %d; this run took it over", owner.PID)
	}
}

// The other half of the same policy. These directories are usually on shared
// storage, where a pid from another machine says nothing at all -- and a dead
// pid here can be a live run there.
func TestLockKeepsALockRecordedOnAnotherHost(t *testing.T) {
	dir := t.TempDir()
	planted := lockOwner{
		PID: deadPID(t), Host: thisHost(t) + "-elsewhere", Started: "2026-09-01T00:00:00Z",
	}
	plantLock(t, dir, planted)

	if _, err := lockDir(dir); err == nil {
		t.Fatal("took a lock recorded on another host on the strength of a pid")
	} else if !strings.Contains(err.Error(), planted.Host) {
		t.Errorf("the refusal does not name the host holding it: %v", err)
	}

	after, err := readLockOwner(lockPath(dir))
	if err != nil || after != planted {
		t.Errorf("the lock file was modified: %+v (%v)", after, err)
	}
}

// A kill between the create and its write leaves an empty file. Refusing is
// the conservative side of that: the alternative treats a lock a live run took
// a microsecond ago as garbage.
func TestLockRefusesALockFileItCannotRead(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"empty", ""},
		{"not json", "held by the nightly job\n"},
		{"no pid", `{"host":"somewhere","started":"2026-09-01T00:00:00Z"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFixtureFile(t, lockPath(dir), tc.body)

			_, err := lockDir(dir)
			if err == nil {
				t.Fatal("a lock file that cannot be read was treated as absent")
			}
			if !strings.Contains(err.Error(), "Delete the file") {
				t.Errorf("the refusal does not say what to do: %v", err)
			}
			if b, rerr := os.ReadFile(lockPath(dir)); rerr != nil || string(b) != tc.body {
				t.Error("the unreadable lock file was modified")
			}
		})
	}
}

// A -dir that does not exist is a typo, and it used to be found out an hour
// later at the write. The lock is the first thing that touches the directory,
// so it is where that gets said.
func TestLockNamesAMissingDirectory(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-there")
	_, err := lockDir(missing)
	if err == nil {
		t.Fatal("locked a directory that does not exist")
	}
	if !strings.Contains(err.Error(), "no such directory") || !strings.Contains(err.Error(), missing) {
		t.Errorf("the error does not name the missing directory: %v", err)
	}
}

// ---- the verbs that take it ----

func TestSweepRefusesALockedDirectory(t *testing.T) {
	_, _, dir := newSweepFixture(t)
	// This process is alive by construction, which is what makes the refusal
	// the one under test rather than the staleness path.
	held := lockOwner{PID: os.Getpid(), Host: thisHost(t), Started: "2026-09-01T00:00:00Z"}
	plantLock(t, dir, held)

	_, _, err := runVerb(t, cmdSync, "-dir", dir, "-concurrency", "2")
	if err == nil {
		t.Fatal("a second sweep ran against a directory another run was writing")
	}
	if !strings.Contains(err.Error(), strconv.Itoa(os.Getpid())) ||
		!strings.Contains(err.Error(), lockName) {
		t.Errorf("the refusal does not name the holder or the file: %v", err)
	}

	// Refused means nothing was touched, including the lock itself: a run that
	// is turned away must not release somebody else's lock on its way out.
	after, rerr := readLockOwner(lockPath(dir))
	if rerr != nil || after != held {
		t.Errorf("the held lock was modified: %+v (%v)", after, rerr)
	}
	for _, name := range []string{
		"manifest-" + fixtureGenA.ID() + ".json",
		"partial-" + fixtureGenA.ID() + ".txt",
		"state.json",
	} {
		if _, serr := os.Stat(filepath.Join(dir, name)); serr == nil {
			t.Errorf("a refused sweep wrote %s", name)
		}
	}
}

func TestSweepClearsALockLeftByADeadProcessAndReleasesItsOwn(t *testing.T) {
	_, _, dir := newSweepFixture(t)
	dead := deadPID(t)
	plantLock(t, dir, lockOwner{PID: dead, Host: thisHost(t), Started: "2026-09-01T00:00:00Z"})

	_, stderr, err := runVerb(t, cmdSync, "-dir", dir, "-concurrency", "2")
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if !strings.Contains(stderr, "no longer running") {
		t.Errorf("the sweep took over a stale lock without saying so:\n%s", stderr)
	}
	if _, serr := os.Stat(filepath.Join(dir, "manifest-"+fixtureGenA.ID()+".json")); serr != nil {
		t.Errorf("the sweep did not run: %v", serr)
	}
	assertUnlocked(t, dir)
}

// Every exit path gives the lock back, not only the one that worked. A sweep
// that refuses to publish a manifest is a run that ended, and leaving the
// directory locked behind it would make one bad shard cost the next run too.
func TestAFailedSweepReleasesTheLock(t *testing.T) {
	store, f, dir := newSweepFixture(t)
	store.set(f.shardPath(2), http.StatusInternalServerError, nil)

	if _, _, err := runVerb(t, cmdSync, "-dir", dir, "-concurrency", "2"); err == nil {
		t.Fatal("a sweep with a permanently failing shard reported success")
	}
	assertUnlocked(t, dir)
}

// The lock is held for the length of the run, not merely created at the start
// of it: the second sweep here is refused while the first is inside a shard
// fetch, and goes through once that fetch has returned.
func TestASecondSweepIsRefusedWhileTheFirstIsRunning(t *testing.T) {
	store, f, dir := newSweepFixture(t)

	inShard := make(chan struct{})
	releaseShard := make(chan struct{})
	var once sync.Once
	body := shardBody(t, f, 0)
	store.setFunc(f.shardPath(0), func(*http.Request) (int, []byte) {
		once.Do(func() { close(inShard) })
		<-releaseShard
		return http.StatusOK, body
	})

	// One capture around both runs: the streams are process-wide, so two
	// concurrent runVerbs would fight over them.
	var firstErr, secondErr error
	stderr := captureStderr(t, func() {
		_ = captureStdout(t, func() {
			firstDone := make(chan struct{})
			go func() {
				defer close(firstDone)
				firstErr = cmdSync([]string{"-dir", dir, "-concurrency", "2"})
			}()

			<-inShard // the first sweep is past the lock and fetching
			secondErr = cmdSync([]string{"-dir", dir, "-concurrency", "2"})
			close(releaseShard)
			<-firstDone
		})
	})

	if firstErr != nil {
		t.Fatalf("the first sweep failed: %v (stderr: %s)", firstErr, stderr)
	}
	if secondErr == nil {
		t.Fatal("a second sweep ran alongside the first")
	}
	if !strings.Contains(secondErr.Error(), strconv.Itoa(os.Getpid())) {
		t.Errorf("the refusal does not name the running holder: %v", secondErr)
	}
	assertUnlocked(t, dir)
}

// genres writes the genre table into -dir and reads the snapshot it describes,
// so it is the other writer the lock has to cover.
func TestGenresTakesAndReleasesTheLock(t *testing.T) {
	store := newFakeStore(t)
	useFakeClient(t, store.transport())
	newDigestStub(t, threeStorefronts).install(store)
	dir, ids := genresFixture(t)

	held := lockOwner{PID: os.Getpid(), Host: thisHost(t), Started: "2026-09-01T00:00:00Z"}
	plantLock(t, dir, held)

	if _, _, err := runGenres(t, dir, ids); err == nil {
		t.Fatal("genres ran against a directory a sweep was writing")
	} else if !strings.Contains(err.Error(), lockName) {
		t.Errorf("the refusal does not name the lock file: %v", err)
	}
	before, _ := loadGenres(dir)

	if err := os.Remove(lockPath(dir)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runGenres(t, dir, ids); err != nil {
		t.Fatalf("catalog genres: %v", err)
	}
	assertUnlocked(t, dir)

	// And the refused run really was refused before it could write: the table
	// only moved on the run that held the lock.
	after, _ := loadGenres(dir)
	if len(before) != 3 || after["com.alive"] != "GAME_CASUAL" {
		t.Errorf("table before = %v, after = %v", before, after)
	}
}

func assertUnlocked(t *testing.T, dir string) {
	t.Helper()
	if _, err := os.Stat(lockPath(dir)); !os.IsNotExist(err) {
		t.Errorf("%s survived the run (%v)", lockName, err)
	}
}

// shardBody renders the fixture's shard i exactly as newSitemapFixture served
// it, for a test that has to replace the handler with one of its own.
func shardBody(t *testing.T, f *sitemapFixture, i int) []byte {
	t.Helper()
	locs := make([]string, len(f.ids[i]))
	for j, id := range f.ids[i] {
		locs[j] = googleplayscraper.BaseURL + "/store/apps/details?id=" + id
	}
	return gzipTestBytes(t, urlsetTestXML(locs...))
}
