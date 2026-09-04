package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// Interrupt handling, which is the one behaviour a long run depends on.
//
// The first signal has to keep meaning what it has always meant -- cancel,
// unwind, leave what was written valid -- and the second has to stop being
// swallowed. Both halves are tested through watchInterrupts with a channel
// rather than by signalling the test binary, so the state machine is exercised
// on every platform and in a fixed order; interrupt_unix_test.go then drives
// the same machine with real signals to prove the wiring in context().

// stubExit replaces the process-exit seam and returns the codes it was called
// with. A test that let this reach os.Exit would report nothing at all.
func stubExit(t *testing.T) <-chan int {
	t.Helper()
	codes := make(chan int, 1)
	prev := exit
	exit = func(code int) { codes <- code }
	t.Cleanup(func() { exit = prev })
	return codes
}

// waited is how long a test waits for something that should already have
// happened. It is a failure deadline, not a delay: every wait below is on a
// channel that the code under test closes or sends on immediately.
const waited = 10 * time.Second

func TestTheFirstInterruptCancelsAndTheSecondExits(t *testing.T) {
	codes := stubExit(t)
	sig := make(chan os.Signal, 2)
	done := make(chan struct{})
	defer close(done)
	ctx, cancel := context.WithCancel(context.Background())
	ended := make(chan struct{})

	stderr := captureStderr(t, func() {
		go func() {
			defer close(ended)
			watchInterrupts(sig, done, cancel)
		}()

		sig <- os.Interrupt
		select {
		case <-ctx.Done():
		case <-time.After(waited):
			t.Error("the first signal did not cancel the context")
			return
		}
		// The whole point of the first one: the command is left to unwind, so
		// the ids already fetched reach the file and the done log stays
		// consistent with the partial beside it.
		select {
		case code := <-codes:
			t.Errorf("the first signal exited with %d instead of letting the run unwind", code)
			return
		default:
		}

		sig <- os.Interrupt
		select {
		case code := <-codes:
			if code != 130 {
				t.Errorf("exit code %d, want 130 (the shell's spelling of SIGINT)", code)
			}
		case <-time.After(waited):
			t.Error("the second signal was absorbed; a stuck run cannot be stopped from the terminal")
			return
		}
		<-ended
	})

	if want := "gpscrape: interrupted again, exiting now"; !strings.Contains(stderr, want) {
		t.Errorf("stderr = %q, want it to contain %q", stderr, want)
	}
}

// The watcher outliving its command would be a goroutine per verb held on a
// signal that is never coming, and -- worse -- a handler that could still call
// exit after the run it belonged to had finished.
func TestTheInterruptWatcherStopsWithTheCommand(t *testing.T) {
	t.Run("before any signal", func(t *testing.T) {
		sig := make(chan os.Signal, 2)
		done := make(chan struct{})
		ended := make(chan struct{})
		go func() {
			defer close(ended)
			watchInterrupts(sig, done, func() { t.Error("cancelled without a signal") })
		}()

		close(done)
		assertEnded(t, ended)
	})

	// The second state matters as much as the first: after one Ctrl-C the
	// watcher is parked waiting for another, and that is exactly where a
	// command that unwinds cleanly leaves it.
	t.Run("after the first signal", func(t *testing.T) {
		codes := stubExit(t)
		sig := make(chan os.Signal, 2)
		done := make(chan struct{})
		ended := make(chan struct{})
		cancelled := make(chan struct{})
		go func() {
			defer close(ended)
			watchInterrupts(sig, done, func() { close(cancelled) })
		}()

		sig <- os.Interrupt
		select {
		case <-cancelled:
		case <-time.After(waited):
			t.Fatal("the first signal did not cancel")
		}
		close(done)
		assertEnded(t, ended)
		select {
		case code := <-codes:
			t.Errorf("the watcher exited with %d on its way out", code)
		default:
		}
	})
}

func assertEnded(t *testing.T, ended <-chan struct{}) {
	t.Helper()
	select {
	case <-ended:
	case <-time.After(waited):
		t.Fatal("the watcher outlived stop()")
	}
}

// stop is a context.CancelFunc, and those are documented as safe to call more
// than once. It closes a channel to end the watcher, so getting this wrong is
// a panic rather than a leak -- and every verb defers it beside another one.
func TestContextStopIsIdempotent(t *testing.T) {
	c := newCommon("test")
	ctx, stop := c.context()
	stop()
	stop()
	select {
	case <-ctx.Done():
	case <-time.After(waited):
		t.Error("stop() left the context live")
	}

	// And with -timeout, where stop wraps a second cancel.
	c = newCommon("test")
	if err := c.parse([]string{"-timeout", "1h"}); err != nil {
		t.Fatal(err)
	}
	ctx, stop = c.context()
	stop()
	stop()
	select {
	case <-ctx.Done():
	case <-time.After(waited):
		t.Error("stop() left the timeout context live")
	}
}
