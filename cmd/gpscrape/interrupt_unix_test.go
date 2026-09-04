//go:build unix

package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The same two signals, delivered for real, through the wiring in context().
//
// The state machine is tested with a channel in interrupt_test.go; what is
// left to check here is the part a channel cannot stand in for: that
// signal.Notify is registered for both SIGINT and SIGTERM, that the channel is
// buffered deeply enough not to drop the second one, and that the context the
// verbs are handed is the one being cancelled. Sending the signals to this
// process is safe because the handler is installed before the first is sent
// and the test waits for each to land before sending the next.

func TestRealSignalsCancelThenExit(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			codes := stubExit(t)
			c := newCommon("test")

			stderr := captureStderr(t, func() {
				ctx, stop := c.context()
				defer stop()

				if err := syscall.Kill(os.Getpid(), sig); err != nil {
					t.Errorf("kill: %v", err)
					return
				}
				select {
				case <-ctx.Done():
				case <-time.After(waited):
					t.Error("the signal did not reach the context")
					return
				}

				if err := syscall.Kill(os.Getpid(), sig); err != nil {
					t.Errorf("second kill: %v", err)
					return
				}
				select {
				case code := <-codes:
					if code != 130 {
						t.Errorf("exit code %d, want 130", code)
					}
				case <-time.After(waited):
					t.Error("the second signal was absorbed")
				}
			})

			if want := "interrupted again, exiting now"; !strings.Contains(stderr, want) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, want)
			}
		})
	}
}

// An interrupted sweep is the ordinary way a sweep ends -- it is 83k requests
// and hours long -- so the lock has to come back on that path, not only on the
// paths that return an error or finish. It does because the cancel unwinds
// through cmdSync's defer like any other return.
func TestAnInterruptedSweepReleasesTheLock(t *testing.T) {
	store, f, dir := newSweepFixture(t)

	// The first shard interrupts the run and then waits for its own request to
	// be cancelled, which is what the sweep's context does to it when the
	// signal lands. No sleep and no polling: the request context is the thing
	// under test, and until it fires the sweep cannot finish without it.
	store.setFunc(f.shardPath(0), func(r *http.Request) (int, []byte) {
		if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
			t.Errorf("kill: %v", err)
			return http.StatusInternalServerError, nil
		}
		<-r.Context().Done()
		return http.StatusOK, nil
	})

	_, _, err := runVerb(t, cmdSync, "-dir", dir, "-concurrency", "2")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the cancellation main turns into exit 130", err)
	}
	assertUnlocked(t, dir)

	// And the interrupted run left something to resume from, which is the
	// reason releasing the lock matters: the next run has to be able to pick
	// it up.
	if _, serr := os.Stat(doneLogPath(dir, fixtureGenA)); serr != nil {
		t.Errorf("no done log to resume from: %v", serr)
	}
}
