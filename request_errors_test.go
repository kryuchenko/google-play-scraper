package googleplayscraper

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// These tests exercise the error branches of get/post that the happy-path
// request tests do not reach: a 429 status, a POST non-200, a body that fails to
// read (server lies about Content-Length and hangs up), a malformed URL that
// fails request construction, and the WithConcurrency clamp.

func TestGetTooManyRequests(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	_, err := NewClient().get(context.Background(), server.URL)
	var se *StatusError
	if !errors.As(err, &se) || se.Code != http.StatusTooManyRequests {
		t.Fatalf("err = %v, want StatusError{429}", err)
	}
}

func TestPostStatusError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	_, err := NewClient().post(context.Background(), server.URL, "application/x-www-form-urlencoded", "f.req=x")
	var se *StatusError
	if !errors.As(err, &se) || se.Code != http.StatusServiceUnavailable {
		t.Fatalf("err = %v, want StatusError{503}", err)
	}
}

func TestPostSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Errorf("Content-Type = %q", r.Header.Get("Content-Type"))
		}
		_, _ = w.Write([]byte("ok-" + string(body)))
	}))
	defer server.Close()

	got, err := NewClient().post(context.Background(), server.URL, "application/x-www-form-urlencoded", "f.req=payload")
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if string(got) != "ok-f.req=payload" {
		t.Errorf("body = %q", string(got))
	}
}

func TestGetBodyReadError(t *testing.T) {
	// Promise more bytes than we send, then hijack and slam the connection so the
	// client's io.ReadAll fails mid-body.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1024")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("short"))
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("ResponseWriter is not a Hijacker")
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Fatalf("hijack: %v", err)
		}
		_ = conn.Close()
	}))
	defer server.Close()

	if _, err := NewClient().get(context.Background(), server.URL); err == nil {
		t.Fatal("expected a body read error, got nil")
	}
}

func TestGetBadURL(t *testing.T) {
	// A control character in the URL makes http.NewRequestWithContext fail before
	// any transport is touched.
	if _, err := NewClient().get(context.Background(), "http://example.com/\x7f"); err == nil {
		t.Fatal("expected request-construction error for control char in URL")
	}
}

func TestPostBadURL(t *testing.T) {
	if _, err := NewClient().post(context.Background(), "http://example.com/\x7f", "text/plain", "x"); err == nil {
		t.Fatal("expected request-construction error for control char in URL")
	}
}

func TestWithConcurrencyClamp(t *testing.T) {
	tests := []struct {
		in, want int
	}{
		{-5, 1},
		{0, 1},
		{1, 1},
		{8, 8},
	}
	for _, tt := range tests {
		c := NewClient(WithConcurrency(tt.in))
		if c.concurrency != tt.want {
			t.Errorf("WithConcurrency(%d) -> concurrency=%d, want %d", tt.in, c.concurrency, tt.want)
		}
	}
}
