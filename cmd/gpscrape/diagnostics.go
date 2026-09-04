package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime/trace"
	"strings"
	"time"

	googleplayscraper "github.com/kryuchenko/google-play-scraper"
)

// Diagnostics: -debug, -log-file and -trace.
//
// A command-line run is unattended and a bug report about one arrives after
// the fact, so the switches here exist to make a run explicable later: which
// requests were made, what came back, why a retry happened, where the
// throttle moved, and -- for a stall -- what the goroutines were doing in the
// minutes before the end. Results stay on stdout; all of this goes to stderr
// or to a file named on the command line, so a pipeline never sees it.
//
// Nothing here logs a request or response body. A details page is a megabyte
// and a reviews page carries people's names; neither belongs in a file that
// gets attached to an issue.

// diagnostics turns on what -debug and -trace asked for. It runs once the
// flags are parsed and before the command does anything, so the log and the
// trace cover the whole run, and it registers what has to happen at exit.
func (c *common) diagnostics() error {
	if c.debug || debugFromEnv() {
		w := io.Writer(os.Stderr)
		if c.logFile != "" {
			f, err := os.OpenFile(c.logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
			if err != nil {
				return fmt.Errorf("-log-file: %w", err)
			}
			atExit(func() { _ = f.Close() })
			w = f
		}
		// LevelTrace rather than Debug: -debug means everything the client
		// can say, and per-request connection timings are the part that
		// distinguishes a slow server from a slow network.
		c.logger = slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: googleplayscraper.LevelTrace}))
		// The first record names the build and the settings, which is what a
		// reader of the log needs before any of the rest means anything.
		c.logger.Debug("gpscrape",
			"version", version, "command", c.fs.Name(),
			"throttle", c.throttle, "adaptive", c.adaptive,
			"concurrency", c.concurrency, "timeout", c.timeout)
	}

	if c.traceFile != "" {
		f, err := os.Create(c.traceFile)
		if err != nil {
			return fmt.Errorf("-trace: %w", err)
		}
		// A flight recorder rather than trace.Start. A catalog sweep runs for
		// hours, and a continuous trace of it is a file nothing can open. The
		// recorder keeps a bounded window of the most recent activity and
		// writes it once, at exit -- after SIGINT too, since that cancels the
		// context rather than killing the process -- so what lands on disk is
		// the last minutes before the run ended. For a short command that is
		// all of it.
		fr := trace.NewFlightRecorder(trace.FlightRecorderConfig{
			MinAge:   15 * time.Minute,
			MaxBytes: 64 << 20,
		})
		if err := fr.Start(); err != nil {
			_ = f.Close()
			return fmt.Errorf("-trace: %w", err)
		}
		atExit(func() {
			if _, err := fr.WriteTo(f); err != nil {
				fmt.Fprintf(os.Stderr, "gpscrape: -trace: %v\n", err)
			}
			fr.Stop()
			_ = f.Close()
		})
	}
	return nil
}

// debugFromEnv reads GPSCRAPE_DEBUG, the flag's spelling for a script or a CI
// job that cannot edit the command line. Anything but empty, 0 and false
// turns it on.
func debugFromEnv() bool {
	switch strings.ToLower(os.Getenv("GPSCRAPE_DEBUG")) {
	case "", "0", "false", "no", "off":
		return false
	}
	return true
}

// exitHooks run in main after the command returns, on every path, in reverse
// order of registration like defers. They exist because the flags that need
// them are parsed inside the command, after main has stopped being able to
// see what was asked for.
var exitHooks []func()

func atExit(f func()) { exitHooks = append(exitHooks, f) }

func runExitHooks() {
	for i := len(exitHooks) - 1; i >= 0; i-- {
		exitHooks[i]()
	}
	exitHooks = nil
}
