//go:build unix

package main

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// The write-failure injection below leans on RLIMIT_FSIZE, which Windows does
// not have. The sweep it exercises is the same on every platform; only the way
// of making a write fail is not, so this file is the one test the Windows
// build of the package skips.

// limitFileSize caps regular-file writes for the rest of the test. It is
// process-wide, so no test in this package runs in parallel and the limit is
// restored in Cleanup. Go ignores SIGXFSZ (it is _SigNotify with no default
// action), so the write returns EFBIG rather than killing the binary; the
// explicit Ignore is belt and braces.
func limitFileSize(t *testing.T, limit uint64) {
	t.Helper()
	var orig syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &orig); err != nil {
		t.Skipf("getrlimit: %v", err)
	}
	cur := orig
	cur.Cur = limit
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &cur); err != nil {
		t.Skipf("setrlimit: %v", err)
	}
	signal.Ignore(syscall.SIGXFSZ)
	t.Cleanup(func() {
		signal.Reset(syscall.SIGXFSZ)
		_ = syscall.Setrlimit(syscall.RLIMIT_FSIZE, &orig)
	})
}

// bufio errors are sticky, so the first failed write meant every remaining
// shard was fetched, parsed and thrown away -- up to 83,000 requests after the
// disk filled. The run did end in an error, hours later.
func TestSweepStopsFetchingOnTheFirstWriteError(t *testing.T) {
	store := newFakeStore(t)
	useFakeClient(t, store.transport())
	dir := t.TempDir()

	// Enough ids in the first shard that the 1MB buffer flushes while it is
	// still being written, so the failure happens mid-shard rather than at the
	// final checkpoint.
	big := make([]string, 60000)
	for i := range big {
		big[i] = fmt.Sprintf("com.example.app%06d", i)
	}
	f := newSitemapFixture(t, store, fixtureGenA, [][]string{
		big, {"com.b"}, {"com.c"}, {"com.d"},
	})

	limitFileSize(t, 128<<10)

	_, _, err := runVerb(t, cmdSync, "-dir", dir, "-concurrency", "1")
	if err == nil {
		t.Fatal("a sweep that could not write its ids reported success")
	}
	if !strings.Contains(err.Error(), "write") {
		t.Errorf("the error does not name the write that failed: %v", err)
	}
	// The point of the fix: no shard is fetched after the first write failure.
	var fetched int
	for i := range 4 {
		fetched += store.hitCount(f.shardPath(i))
	}
	if fetched != 1 {
		t.Errorf("%d shards were fetched; only the one that failed to write should have been", fetched)
	}
	for _, name := range []string{
		"snapshot-" + fixtureGenA.ID() + ".txt.gz",
		"manifest-" + fixtureGenA.ID() + ".json",
	} {
		if _, serr := os.Stat(filepath.Join(dir, name)); serr == nil {
			t.Errorf("%s was written by a sweep that failed", name)
		}
	}
	// The run stays resumable.
	for _, name := range []string{
		"partial-" + fixtureGenA.ID() + ".txt",
		"done-" + fixtureGenA.ID() + ".log",
	} {
		if _, serr := os.Stat(filepath.Join(dir, name)); serr != nil {
			t.Errorf("%s is missing; the progress that was made is not resumable", name)
		}
	}
}
