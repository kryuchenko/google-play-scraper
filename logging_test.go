package googleplayscraper

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func debugLogger(buf *bytes.Buffer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: level}))
}

func TestLoggerRecordsThePipeline(t *testing.T) {
	var calls atomic.Int64
	route := func(req *http.Request) (mockResponse, bool) {
		if calls.Add(1) == 1 {
			return mockResponse{Status: 500}, true
		}
		return mockResponse{Body: []byte("body-that-must-not-be-logged")}, true
	}
	c := newMockClient(t, route)
	var buf bytes.Buffer
	WithLogger(debugLogger(&buf, LevelTrace))(c)
	WithRetry(RetryPolicy{MaxAttempts: 2, BaseDelay: time.Microsecond, MaxDelay: time.Millisecond})(c)

	if _, err := c.get(context.Background(), BaseURL+"/x"); err != nil {
		t.Fatalf("get: %v", err)
	}

	log := buf.String()
	for _, want := range []string{
		"msg=request", "attempt=1", "msg=response", "status=500",
		"msg=retry", "delay=", "attempt=2", "status=200", "bytes=28",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, "must-not-be-logged") {
		t.Errorf("the response body was logged:\n%s", log)
	}
	// The mock transport never dials, so nothing fires and nothing is said:
	// a connection record with no timings in it would be noise.
	if strings.Contains(log, "msg=connection") {
		t.Errorf("connection timings reported for a transport that has none:\n%s", log)
	}
}

func TestLoggerRecordsConnectionTimingsAtTraceLevel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	var buf bytes.Buffer
	c := NewClient(WithHTTPClient(srv.Client()), WithLogger(debugLogger(&buf, LevelTrace)))
	for range 2 {
		if _, err := c.get(context.Background(), srv.URL+"/x"); err != nil {
			t.Fatalf("get: %v", err)
		}
	}

	log := buf.String()
	for _, want := range []string{"msg=connection", "connect=", "reused=false", "reused=true", "ttfb=", "addr="} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}

	// At Debug the timings are not collected, let alone written.
	buf.Reset()
	WithLogger(debugLogger(&buf, slog.LevelDebug))(c)
	if _, err := c.get(context.Background(), srv.URL+"/x"); err != nil {
		t.Fatalf("get: %v", err)
	}
	if strings.Contains(buf.String(), "msg=connection") {
		t.Errorf("connection timings written at Debug:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "msg=response") {
		t.Errorf("Debug lost the response record:\n%s", buf.String())
	}
}

func TestLoggerReportsThrottleChanges(t *testing.T) {
	var buf bytes.Buffer
	c := NewClient(
		WithThrottle(100*time.Millisecond),
		WithAdaptiveThrottle(AdaptivePolicy{Min: 100 * time.Millisecond, Max: 200 * time.Millisecond}),
		WithLogger(debugLogger(&buf, slog.LevelDebug)),
	)

	c.observe(context.Background(), http.StatusTooManyRequests, time.Millisecond, nil)
	log := buf.String()
	for _, want := range []string{"msg=throttle", "from=100ms", "to=200ms", "reason=refused"} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}

	// Already at the ceiling: the controller decides to slow down and the
	// interval does not move, so there is nothing to report.
	buf.Reset()
	c.observe(context.Background(), http.StatusTooManyRequests, time.Millisecond, nil)
	if buf.Len() != 0 {
		t.Errorf("a decision that changed nothing was logged:\n%s", buf.String())
	}

	buf.Reset()
	c.retryAfterHint(context.Background(), &StatusError{Code: 429, RetryAfter: time.Second})
	if buf.Len() != 0 {
		t.Errorf("Retry-After that could not raise the interval was logged:\n%s", buf.String())
	}
}

func TestLoggerIsNilSafeAndSilentByDefault(t *testing.T) {
	var c Client // a zero Client, as tests build them
	c.logResponse(context.Background(), http.MethodGet, "u", 1, 200, time.Millisecond, 0, nil)
	c.logThrottle(context.Background(), time.Second, 2*time.Second, "x")
	if c.log() != discardLogger {
		t.Error("a zero Client should log to the discard logger")
	}
	if NewClient().log() != discardLogger {
		t.Error("a built Client without WithLogger should be silent")
	}
	if NewClient(WithLogger(nil)).log() != discardLogger {
		t.Error("WithLogger(nil) should leave the client silent, not panic later")
	}
}
