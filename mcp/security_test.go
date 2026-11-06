package gpsmcp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	gps "github.com/kryuchenko/google-play-scraper"
)

func TestHTTPOriginValidation(t *testing.T) {
	for _, token := range []string{"", "secret"} {
		handler := HTTPHandler(NewServer(&fake{}, Options{}), token)
		for _, method := range []string{"GET", "HEAD", "OPTIONS", "POST", "DELETE"} {
			for _, origin := range []string{"https://evil.example", "null", "", "http://localhost/path", "http://localhost?x", "http://user@localhost", "http://localhost#x", "ftp://localhost", "http://localhost https://evil.example"} {
				req := httptest.NewRequest(method, "http://localhost/mcp", strings.NewReader(`{}`))
				req.Header.Set("Origin", origin)
				// A Fetch Metadata header must not override an invalid Origin.
				req.Header.Set("Sec-Fetch-Site", "same-origin")
				req.Header.Set("Authorization", "Bearer secret")
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, req)
				if w.Code != http.StatusForbidden {
					t.Errorf("token=%t %s Origin=%q: got %d, want 403", token != "", method, origin, w.Code)
				}
			}
		}
		req := httptest.NewRequest("POST", "http://localhost/mcp/secret", nil)
		req.Header["Origin"] = []string{"http://localhost", "https://evil.example"}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Errorf("multiple origins: %d", w.Code)
		}
	}
	// Native clients omit Origin; reverse proxies may terminate HTTPS while
	// forwarding the public Host. Both remain usable.
	ts := httptest.NewServer(HTTPHandler(NewServer(&fake{}, Options{}), "secret"))
	defer ts.Close()
	for _, origin := range []string{"", "http://play.example", "https://play.example"} {
		hdr := map[string]string{"Host": "play.example", "Authorization": "Bearer secret"}
		if origin != "" {
			hdr["Origin"] = origin
		}
		if got := post(t, ts.URL+"/mcp", hdr); got != 200 {
			t.Errorf("valid Origin %q: %d", origin, got)
		}
	}
}

// The old sessionless HTTP binding cannot route cancellation notifications
// across requests. The new binding must cancel upstream work on disconnect.
type cancellationScraper struct {
	*fake
	started   chan struct{}
	cancelled chan struct{}
}

func (s *cancellationScraper) App(ctx context.Context, _ string, _ gps.AppOptions) (*gps.App, error) {
	close(s.started)
	<-ctx.Done()
	close(s.cancelled)
	return nil, ctx.Err()
}

func TestHTTPNewProtocolCancellation(t *testing.T) {
	s := &cancellationScraper{fake: &fake{}, started: make(chan struct{}), cancelled: make(chan struct{})}
	ts := httptest.NewServer(HTTPHandler(NewServer(s, Options{CallTimeout: 10 * time.Second}), ""))
	defer ts.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"app","arguments":{"app_id":"com.example.app"},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`
	req, err := http.NewRequestWithContext(ctx, "POST", ts.URL+"/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", "2026-07-28")
	req.Header.Set("Mcp-Method", "tools/call")
	req.Header.Set("Mcp-Name", "app")
	done := make(chan struct{})
	go func() {
		defer close(done)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()
	select {
	case <-s.started:
	case <-time.After(3 * time.Second):
		t.Fatal("tool did not start")
	}
	cancel()
	select {
	case <-s.cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("disconnect did not cancel upstream work")
	}
	<-done
}

func TestWorkflowActionsAreImmutable(t *testing.T) {
	files, err := filepath.Glob("../.github/workflows/*.yml")
	if err != nil || len(files) == 0 {
		t.Fatalf("workflows: %v", err)
	}
	uses := regexp.MustCompile(`uses:\s+([^\s#]+)`)
	pinned := regexp.MustCompile(`^[^@]+@[0-9a-f]{40}$`)
	for _, file := range files {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range uses.FindAllStringSubmatch(string(b), -1) {
			if !pinned.MatchString(match[1]) {
				t.Errorf("%s: action %s must use a full commit SHA", file, match[1])
			}
		}
	}
}
