package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDebugFlagLogsToTheNamedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "debug.log")
	c := newCommon("app")
	if err := c.parse([]string{"-debug", "-log-file", path, "-throttle", "150ms", "com.example"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if c.logger == nil {
		t.Fatal("-debug did not install a logger")
	}
	runExitHooks()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	for _, want := range []string{"msg=gpscrape", "command=app", "throttle=150ms"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("log lacks %q:\n%s", want, data)
		}
	}
}

func TestDebugComesFromTheEnvironmentToo(t *testing.T) {
	for value, want := range map[string]bool{"1": true, "true": true, "yes": true, "0": false, "false": false, "": false} {
		t.Setenv("GPSCRAPE_DEBUG", value)
		c := newCommon("app")
		c.logFile = filepath.Join(t.TempDir(), "debug.log") // keep the test's stderr clean
		if err := c.parse([]string{"com.example"}); err != nil {
			t.Fatalf("parse: %v", err)
		}
		if got := c.logger != nil; got != want {
			t.Errorf("GPSCRAPE_DEBUG=%q: logger installed = %v, want %v", value, got, want)
		}
		runExitHooks()
	}
}

func TestTraceFlagWritesARecordingAtExit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.trace")
	c := newCommon("app")
	if err := c.parse([]string{"-trace", path, "com.example"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if st, err := os.Stat(path); err != nil || st.Size() != 0 {
		t.Fatalf("before exit the trace file should exist and be empty: size=%v err=%v", st, err)
	}
	runExitHooks()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat trace: %v", err)
	}
	if st.Size() == 0 {
		t.Error("the flight recorder wrote nothing at exit")
	}
}

func TestExitHooksRunInReverseAndOnce(t *testing.T) {
	var order []int
	atExit(func() { order = append(order, 1) })
	atExit(func() { order = append(order, 2) })
	runExitHooks()
	runExitHooks()
	if len(order) != 2 || order[0] != 2 || order[1] != 1 {
		t.Errorf("hooks ran as %v, want [2 1]", order)
	}
}
