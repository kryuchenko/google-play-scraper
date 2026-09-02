package googleplayscraper

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/trace"
	"strings"
	"testing"
	"time"
)

// TestTraceInstrumentation is the guard for the execution-tracing contract
// described in trace.go. Instrumentation is the kind of code that rots
// silently: nothing fails when a region stops being opened, the trace just
// gets less useful, and nobody notices until they need it. So this records a
// real trace over a real (local) request and asserts the vocabulary is there.
//
// It also pins the property that makes the whole scheme work: the task id
// reaches the worker goroutines in parallelIndexed through the context. If
// that ever breaks, requests stop being attributable to the operation that
// caused them, and the trace becomes a flat list of HTTP calls.
func TestTraceInstrumentation(t *testing.T) {
	if trace.IsEnabled() {
		t.Skip("a trace is already running (go test -trace); cannot start a second one")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "test.trace")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create trace file: %v", err)
	}
	if err := trace.Start(f); err != nil {
		_ = f.Close()
		t.Fatalf("start trace: %v", err)
	}

	// A throttle small enough not to slow the test down, but non-zero: the
	// throttle region is deliberately opened only on the blocking path, so a
	// zero throttle would legitimately produce no region at all.
	c := NewClient(WithThrottle(time.Millisecond), WithConcurrency(4))
	ctx, endTask := startTask(context.Background(), traceTaskAvailability)
	logTrace(ctx, "app.id", "com.example.app")

	const requests = 8
	err = parallelIndexed(ctx, requests, 4, func(ctx context.Context, i int) {
		if _, gerr := c.get(ctx, srv.URL); gerr != nil {
			t.Errorf("request %d: %v", i, gerr)
		}
	})
	endTask()
	trace.Stop()
	// Checked rather than deferred: an unflushed trace file parses as
	// truncated, and the failure would surface as a confusing absence of
	// regions rather than as the write error it actually is.
	if cerr := f.Close(); cerr != nil {
		t.Fatalf("close trace file: %v", cerr)
	}

	if err != nil {
		t.Fatalf("parallelIndexed: %v", err)
	}

	dump := parseTrace(t, path)

	for _, want := range []string{
		`Type="` + traceTaskAvailability + `"`,
		`Type="` + traceRegionHTTP + `"`,
		`Type="` + traceRegionThrottle + `"`,
		`Category="app.id"`,
		`Category="http.url"`,
		`Category="http.status"`,
	} {
		if !strings.Contains(dump, want) {
			t.Errorf("trace is missing %s", want)
		}
	}

	// Regions are per-goroutine, tasks are carried by the context. Every
	// http.get region must therefore name the enclosing task rather than
	// standing on its own, even though it runs on a pool worker.
	var orphaned int
	for _, line := range strings.Split(dump, "\n") {
		if !strings.Contains(line, `Type="`+traceRegionHTTP+`"`) {
			continue
		}
		if strings.Contains(line, "Task=0") || !strings.Contains(line, "Task=") {
			orphaned++
		}
	}
	if orphaned > 0 {
		t.Errorf("%d %s regions are not attributed to a task: the task id is not "+
			"reaching pool workers through the context", orphaned, traceRegionHTTP)
	}
}

// parseTrace renders a trace file as text using the toolchain's own reader.
// The binary format has no supported public parser, and pulling in
// golang.org/x/exp/trace would break the zero-dependency invariant that
// TestRootIsZeroDependency enforces, so this shells out instead.
func parseTrace(t *testing.T, path string) string {
	t.Helper()
	cmd := exec.Command("go", "tool", "trace", "-d=parsed", path)
	cmd.Env = append(os.Environ(), "GOWORK=off")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go tool trace: %v\n%s", err, out)
	}
	return string(out)
}
