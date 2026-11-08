package googleplayscraper

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// A response over maxBodyBytes fails with ErrBodyTooLarge instead of being
// read to the end, and is not asked for again.
func TestBodyTooLarge(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		chunk := bytes.Repeat([]byte("x"), 1<<20)
		for range maxBodyBytes>>20 + 2 {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	c := NewClient(WithThrottle(0))
	_, err := c.get(context.Background(), srv.URL)
	if !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("err = %v, want ErrBodyTooLarge", err)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("%d requests, want 1: an oversized body must not be retried", n)
	}
}

// A gzip stream that inflates past maxInflatedBytes stops with
// ErrInflatedTooLarge; one under the cap reads whole.
func TestInflateLimit(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zero := make([]byte, 1<<20)
	for range maxInflatedBytes>>20 + 1 {
		_, _ = zw.Write(zero)
	}
	_ = zw.Close()
	if buf.Len() > 2<<20 {
		t.Fatalf("bomb is %d bytes compressed; the test assumes it is small", buf.Len())
	}
	zr, err := gzip.NewReader(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, limitInflate(zr)); !errors.Is(err, ErrInflatedTooLarge) {
		t.Fatalf("err = %v, want ErrInflatedTooLarge", err)
	}

	got, err := io.ReadAll(limitInflate(strings.NewReader("small")))
	if err != nil || string(got) != "small" {
		t.Fatalf("under the cap: %q, %v", got, err)
	}
}

// The caps are inclusive: a body of exactly maxBodyBytes, and a stream that
// inflates to exactly maxInflatedBytes, are read whole.
func TestLimitsAreInclusive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(make([]byte, maxBodyBytes))
	}))
	defer srv.Close()
	body, err := NewClient(WithThrottle(0)).get(context.Background(), srv.URL)
	if err != nil || len(body) != maxBodyBytes {
		t.Errorf("a body of exactly maxBodyBytes: %d bytes, %v", len(body), err)
	}

	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zero := make([]byte, 1<<20)
	for range maxInflatedBytes >> 20 {
		_, _ = zw.Write(zero)
	}
	_ = zw.Close()
	zr, err := gzip.NewReader(&buf)
	if err != nil {
		t.Fatal(err)
	}
	n, err := io.Copy(io.Discard, limitInflate(zr))
	if err != nil || n != maxInflatedBytes {
		t.Errorf("a stream of exactly maxInflatedBytes: %d bytes, %v", n, err)
	}
}
