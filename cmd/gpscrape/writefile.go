package main

import (
	"fmt"
	"io"
	"io/fs"
	"os"
)

// Every file this package writes in a snapshot directory -- the partial id
// file, the done log, state.json, the manifest, the snapshot, the delta, the
// genre table, its merge file and checkpoint, the lock -- is opened through
// openWrite and published through renameFile.
//
// They are variables for one reason: *os.File offers no way to make a write
// fail as a full disk does, an fsync report an I/O error, or a rename be
// refused, and the behaviour that matters most here -- what a failed write
// leaves on disk, and whether the rerun recovers -- is otherwise testable
// only by filling a real disk. writefaults_test.go swaps them; nothing else
// assigns them.
var (
	openWrite = func(name string, flag int, perm fs.FileMode) (writeFile, error) {
		f, err := os.OpenFile(name, flag, perm)
		if err != nil {
			return nil, err // not a typed nil inside the interface
		}
		return f, nil
	}
	renameFile = os.Rename
)

// writeFile is what the writers need of a file: *os.File, or a test's wrapper
// around one.
type writeFile interface {
	io.Writer
	Sync() error
	Close() error
	Stat() (fs.FileInfo, error)
}

// createFlags are os.Create's.
const createFlags = os.O_RDWR | os.O_CREATE | os.O_TRUNC

// stillOurs reports an error unless path still names the file f has open.
// Something that removed or replaced it while this run was writing leaves
// the run's bytes on an inode nothing can reach: renamed into place, or read
// back by name, the other file would be published as this run's work.
func stillOurs(f writeFile, path string) error {
	mine, err := f.Stat()
	if err != nil {
		return err
	}
	there, err := os.Stat(path)
	if err != nil || !os.SameFile(mine, there) {
		return fmt.Errorf("%s was removed or replaced while this run was writing it", path)
	}
	return nil
}
