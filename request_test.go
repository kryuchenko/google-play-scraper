package googleplayscraper

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewClient(t *testing.T) {
	c := NewClient()

	if c.httpClient == nil {
		t.Error("httpClient is nil")
	}
	if c.userAgent == "" {
		t.Error("userAgent is empty")
	}
}

func TestClientWithOptions(t *testing.T) {
	c := NewClient(
		WithTimeout(5*time.Second),
		WithUserAgent("TestAgent/1.0"),
	)

	if c.httpClient.Timeout != 5*time.Second {
		t.Errorf("Timeout: got %v, want %v", c.httpClient.Timeout, 5*time.Second)
	}
	if c.userAgent != "TestAgent/1.0" {
		t.Errorf("UserAgent: got %q, want %q", c.userAgent, "TestAgent/1.0")
	}
}

func TestClientGet(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("Method: got %q, want GET", r.Method)
		}
		if r.Header.Get("User-Agent") == "" {
			t.Error("User-Agent header is missing")
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status": "ok"}`))
	}))
	defer server.Close()

	c := NewClient()
	body, err := c.get(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}

	if string(body) != `{"status": "ok"}` {
		t.Errorf("Body: got %q, want %q", string(body), `{"status": "ok"}`)
	}
}

func TestClientGetError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := NewClient()
	_, err := c.get(context.Background(), server.URL)
	if err == nil {
		t.Error("expected error for 404 status")
	}
}

func TestClientPost(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("Method: got %q, want POST", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Errorf("Content-Type: got %q", r.Header.Get("Content-Type"))
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`response`))
	}))
	defer server.Close()

	c := NewClient()
	body, err := c.post(context.Background(), server.URL, "application/x-www-form-urlencoded", "key=value")
	if err != nil {
		t.Fatalf("post failed: %v", err)
	}

	if string(body) != "response" {
		t.Errorf("Body: got %q, want %q", string(body), "response")
	}
}

func TestBuildURL(t *testing.T) {
	tests := []struct {
		path   string
		params map[string]string
		want   string
	}{
		{
			path:   "/store/apps/details",
			params: nil,
			want:   "https://play.google.com/store/apps/details",
		},
		{
			path:   "/store/apps/details",
			params: map[string]string{"id": "com.example"},
			want:   "https://play.google.com/store/apps/details?id=com.example",
		},
	}

	for _, tt := range tests {
		got := buildURL(tt.path, tt.params)
		// For single param, exact match; for multiple, just check contains
		if tt.params == nil || len(tt.params) <= 1 {
			if got != tt.want {
				t.Errorf("buildURL(%q, %v) = %q, want %q", tt.path, tt.params, got, tt.want)
			}
		}
	}
}
