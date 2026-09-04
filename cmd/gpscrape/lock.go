package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// One writer at a time in a snapshot directory.
//
// The directory is not a pile of independent files, it is one dataset kept in
// several: state.json says which shards a run has reached, partial-<gen>.txt
// and done-<gen>.log are the two halves of the resume protocol whose ordering
// is the whole reason work is not lost, and the manifest is a claim about the
// snapshot beside it. Two sweeps of the same -dir append to the same partial
// file, each overwrites the other's state, and the done log ends up describing
// neither run; a `genres` pass merges the table while a sweep republishes the
// snapshot it was derived from. What comes out is not a corrupt file that
// something would notice, it is a plausible dataset that describes no moment
// the store was ever in -- and downstream, a catalog that appeared to shrink.
//
// So the directory takes a lock. The interesting half of a lock file is what
// happens after a crash: a sweep runs for hours and gets killed -- a second
// Ctrl-C (see watchInterrupts, which exits without unwinding for exactly that
// reason), an OOM kill, a reboot -- and a lock that survives that turns one
// crash into a directory nobody can sweep again until they read the source.
// The file therefore records who holds it, so a lock left by a process that is
// gone is recognised and removed, and one held by a process that is running is
// refused with enough detail to act on.

// lockName is the lock file, named in the same family as the state it sits
// beside. Not keyed to a generation: what it protects is the directory, and
// two sweeps of *different* generations into one directory is precisely the
// collision to prevent.
const lockName = "sweep.lock"

func lockPath(dir string) string { return filepath.Join(dir, lockName) }

// lockOwner is what the lock file contains: enough to decide whether the
// holder is still alive, and to name it in a refusal a human has to act on.
type lockOwner struct {
	PID     int    `json:"pid"`
	Host    string `json:"host"`
	Started string `json:"started"` // RFC3339, UTC
}

// dirLock is a held lock and the record that was written for it.
type dirLock struct {
	path  string
	owner lockOwner
	once  sync.Once
}

// release removes the lock, if it is still this run's lock.
//
// Both halves of that are guards against removing somebody else's. Once,
// because the ordinary path releases twice -- the verb's defer and the exit
// hook that covers a panic -- and by the second one the directory may have
// been taken by another run. Still ours, because the refusal below tells a
// person how to clear a lock by hand, and a person who does that while this
// run is alive lets the next one in underneath it.
func (l *dirLock) release() {
	l.once.Do(func() {
		if held, err := readLockOwner(l.path); err == nil && held != l.owner {
			return
		}
		_ = os.Remove(l.path)
	})
}

// lockDir takes dir's lock, or says who holds it.
func lockDir(dir string) (*dirLock, error) {
	l := &dirLock{path: lockPath(dir)}

	err := l.create()
	if errors.Is(err, fs.ErrExist) {
		// Held, or left behind by something that died. Only a lock this
		// machine can prove is dead gets removed; anything else is refused.
		if err = l.clearIfStale(dir); err != nil {
			return nil, err
		}
		// Retried once, not in a loop: the file was just removed, so a second
		// EEXIST means a live run took it in between, and racing that is the
		// thing the lock exists to prevent.
		if err = l.create(); errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("%s was taken by another run while this one was "+
				"clearing a lock left by a process that had died; only one run at a "+
				"time may write to %s", l.path, dir)
		}
	}
	if errors.Is(err, fs.ErrNotExist) {
		// The directory itself is missing, so there is nothing to lock and
		// nothing to read or write either. Said here rather than an hour later
		// at the write, which is where a mistyped -dir used to surface.
		return nil, fmt.Errorf("%s: no such directory (a snapshot directory is what "+
			"`gpscrape catalog sweep -dir %s` makes)", dir, dir)
	}
	if err != nil {
		return nil, fmt.Errorf("lock %s: %w", l.path, err)
	}

	// Registered as well as returned. A panic in a verb runs no defers, and
	// main's deferred runExitHooks is the last thing that still executes;
	// without this a panicking sweep locked the directory for good. Doing it
	// here rather than at each call site is so that no caller can take the
	// lock and forget the half that gives it back.
	atExit(l.release)
	return l, nil
}

// create makes the lock file, or fails with fs.ErrExist because someone else
// has it. O_EXCL is the exclusion: the create either wins or reports that it
// did not, with no window in which two callers both believe they won.
func (l *dirLock) create() error {
	host, _ := os.Hostname() // "" is only ever compared against itself
	owner := lockOwner{
		PID:     os.Getpid(),
		Host:    host,
		Started: time.Now().UTC().Format(time.RFC3339),
	}
	body, err := json.Marshal(owner)
	if err != nil {
		return err
	}

	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	// A lock file this run cannot describe is worse than no lock: the next run
	// would find a file it cannot read and refuse. Remove it and report.
	if _, err := f.Write(append(body, '\n')); err != nil {
		_ = f.Close()
		_ = os.Remove(l.path)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(l.path)
		return err
	}
	l.owner = owner
	return nil
}

// clearIfStale removes a lock whose holder this machine can prove is gone, and
// otherwise returns the refusal.
//
// "This machine" is load-bearing. A pid recorded on another host says nothing
// about a pid here, and on a shared filesystem -- which is how these
// directories are usually published -- deciding on a pid match alone would let
// one machine delete the lock a live run on another machine is holding. Same
// host and a pid that is not running is the only case that can be settled
// without asking a person.
func (l *dirLock) clearIfStale(dir string) error {
	owner, err := readLockOwner(l.path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// Released between the create and this read. The retry takes it.
		return nil
	case err != nil:
		// Unreadable, which includes the empty file left by a kill between
		// the create above and its write. Refusing is the conservative side of
		// that: the alternative treats a lock a live run took a microsecond
		// ago as garbage.
		return fmt.Errorf("%s exists but is not a lock this build can read (%v); "+
			"a run holds it, or one was killed while taking it. Delete the file "+
			"only if you know no other gpscrape is writing to %s", l.path, err, dir)
	}

	host, _ := os.Hostname()
	if owner.Host == host && !processAlive(owner.PID) {
		// Said out loud. Silently taking a lock off another process is the
		// kind of thing whose one wrong decision has to be findable in a log.
		fmt.Fprintf(os.Stderr, "removing %s: it was left by process %d, which is no longer running\n",
			l.path, owner.PID)
		if err := os.Remove(l.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove stale %s: %w", l.path, err)
		}
		return nil
	}

	return fmt.Errorf("%s is locked by process %d on %s, started %s; "+
		"only one run at a time may write to it. Delete %s only if you know "+
		"that process is gone", dir, owner.PID, owner.Host, owner.Started, l.path)
}

// readLockOwner reads and validates a lock file. A record without a pid cannot
// be checked for liveness and cannot be named in a refusal, so it is not a
// lock record -- it is a file that happens to be JSON.
func readLockOwner(path string) (lockOwner, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return lockOwner{}, err
	}
	var o lockOwner
	if err := json.Unmarshal(b, &o); err != nil {
		return lockOwner{}, err
	}
	if o.PID <= 0 {
		return lockOwner{}, fmt.Errorf("no pid in the record")
	}
	return o, nil
}
