package googleplayscraper

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestClassifyFinalURL(t *testing.T) {
	req, _ := url.Parse("https://play.google.com/store/search?q=x")
	tests := []struct {
		final   string
		verdict Verdict
		reason  string
	}{
		{"https://play.google.com/store/search?q=x&hl=de", VerdictOK, ""},
		{"https://PLAY.google.com/store/apps", VerdictOK, ""},
		{"https://www.google.com/sorry/index?continue=x", VerdictBlocked, "sorry-redirect"},
		{"https://www.google.com/sorry", VerdictBlocked, "sorry-redirect"},
		{"https://consent.google.com/m?continue=x", VerdictInterstitial, "consent"},
		{"https://accounts.google.com/ServiceLogin", VerdictInterstitial, "sign-in"},
		// Path boundaries, not substrings.
		{"https://www.google.com/sorryish", VerdictOK, ""},
		{"https://example.com/sorry/index", VerdictOK, ""},
		// A transport that rewrites the host (a proxy, a test server) is not
		// a redirect anyone should judge.
		{"http://127.0.0.1:8080/store/search?q=x", VerdictOK, ""},
	}
	for _, tt := range tests {
		final, _ := url.Parse(tt.final)
		v, reason := classifyFinalURL(req, final)
		if v != tt.verdict || reason != tt.reason {
			t.Errorf("classifyFinalURL(%s) = %s %q, want %s %q", tt.final, v, reason, tt.verdict, tt.reason)
		}
	}
}

func TestValidators(t *testing.T) {
	page := `<script>AF_initDataCallback({key: 'ds:3', hash: '1', data:[1], sideChannel: {}});</script>`
	tests := []struct {
		name    string
		v       validator
		body    string
		verdict Verdict
	}{
		{"html ok", validateHTMLPage, page, VerdictOK},
		{"html challenge", validateHTMLPage, "<html><form id=captcha-form></form></html>", VerdictBlocked},
		{"block present", requireBlock("ds:3"), page, VerdictOK},
		{"block missing", requireBlock("ds:5"), page, VerdictMalformed},
		{"block page", requireBlock("ds:5"), "<html>sorry</html>", VerdictBlocked},
		{"rpc envelope", validateRPC, ")]}'\n[[\"wrb.fr\",\"x\",null]]", VerdictOK},
		{"rpc bare json", validateRPC, `[["wrb.fr","x",null]]`, VerdictOK},
		{"rpc html", validateRPC, "\n  <!doctype html><html></html>", VerdictBlocked},
	}
	for _, tt := range tests {
		if v, _ := tt.v([]byte(tt.body)); v != tt.verdict {
			t.Errorf("%s: got %s, want %s", tt.name, v, tt.verdict)
		}
	}
}

// The page Google serves for a search that matches nothing names its section
// zero_results_cluster inside a base64 token. That is what keeps a real empty
// search an empty result rather than an error.
func TestClassifySearchPageFixtures(t *testing.T) {
	zero := readFixture(t, "search_zero_results.html")
	results, _, _ := parseSearchPage(zero, 20)
	if len(results) != 0 {
		t.Fatalf("zero-results fixture parsed to %d results", len(results))
	}
	if v, reason := classifySearchPage(zero, results); v != VerdictEmpty {
		t.Errorf("zero-results search = %s (%s), want empty", v, reason)
	}

	full := readFixture(t, "search_page.html")
	results, _, _ = parseSearchPage(full, 20)
	if v, _ := classifySearchPage(full, results); v != VerdictOK {
		t.Errorf("populated search = %s, want ok", v)
	}

	// A store page that is not a search: data blocks, no results, no marker.
	app := readFixture(t, "app_page.html")
	if v, _ := classifySearchPage(app, nil); v != VerdictMalformed {
		t.Errorf("app page served as search = %s, want malformed", v)
	}
	if v, _ := classifySearchPage([]byte("<html>unusual traffic</html>"), nil); v != VerdictBlocked {
		t.Errorf("challenge page served as search = %s, want blocked", v)
	}
}

// Every fixture an endpoint check is attached to must pass that check, or the
// check would turn a healthy page into an error.
func TestEndpointChecksAcceptFixtures(t *testing.T) {
	cases := []struct {
		fixture string
		v       validator
	}{
		{"app_page.html", requireBlock("ds:5")},
		{"app_page_game.html", requireBlock("ds:5")},
		{"app_unavailable_region.html", requireBlock("ds:5")},
		{"developer_page.html", requireBlock("ds:3")},
		{"datasafety_page.html", requireBlock("ds:3")},
		{"pc_page_game.html", requireBlock("ds:4")},
		{"similar_page.html", validateHTMLPage},
		{"cluster_page.html", validateHTMLPage},
		{"category_page.html", validateHTMLPage},
		{"top_charts_page.html", validateHTMLPage},
	}
	for _, tc := range cases {
		if v, reason := tc.v(readFixture(t, tc.fixture)); v != VerdictOK {
			t.Errorf("%s: %s (%s), want ok", tc.fixture, v, reason)
		}
	}
}

// redirectTo answers every request as if the redirect chain had ended at
// final, with the given status and body.
func redirectTo(final string, status int, body string, calls *atomic.Int32) *http.Client {
	return &http.Client{Transport: &MockTransport{RoundTripFunc: func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		end, _ := http.NewRequest(http.MethodGet, final, nil)
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
			Request:    end,
		}, nil
	}}}
}

// A 200 that ended on google.com/sorry used to be a clean response: it fed the
// next speed-up and reached the parser as an empty page. It is a refusal now,
// before the controller or a hook sees it.
func TestSorryRedirectIsARefusal(t *testing.T) {
	var calls atomic.Int32
	var hookErr error
	var hookStatus int
	c := NewClient(
		WithHTTPClient(redirectTo("https://www.google.com/sorry/index", 200, "<html>captcha</html>", &calls)),
		WithAdaptiveThrottle(AdaptivePolicy{Min: time.Millisecond, Max: time.Second, Start: 10 * time.Millisecond}),
		WithHooks(Hooks{OnResponse: func(_, _ string, _, status int, _ time.Duration, err error) {
			hookStatus, hookErr = status, err
		}}),
	)

	_, err := c.Search(context.Background(), SearchOptions{Term: "x"})
	var sb *SoftBlockError
	if !errors.As(err, &sb) || sb.Verdict != VerdictBlocked || sb.Reason != "sorry-redirect" {
		t.Fatalf("err = %v, want a blocked SoftBlockError", err)
	}
	if !errors.As(hookErr, &sb) || hookStatus != 200 {
		t.Errorf("OnResponse saw status %d err %v, want the soft block", hookStatus, hookErr)
	}
	if got := c.CurrentThrottle(); got != 20*time.Millisecond {
		t.Errorf("interval = %v, want the full slowdown to 20ms", got)
	}
}

// A consent page is the same page next time: not retried, not a reason to
// slow down.
func TestConsentIsNotRetriedOrThrottled(t *testing.T) {
	var calls atomic.Int32
	c := NewClient(
		WithHTTPClient(redirectTo("https://consent.google.com/m?continue=x", 200, "<html></html>", &calls)),
		WithAdaptiveThrottle(AdaptivePolicy{Min: time.Millisecond, Max: time.Second, Start: 10 * time.Millisecond}),
		WithRetry(RetryPolicy{MaxAttempts: 3, BaseDelay: time.Millisecond}),
	)
	_, err := c.App(context.Background(), "com.example", AppOptions{})
	var sb *SoftBlockError
	if !errors.As(err, &sb) || sb.Verdict != VerdictInterstitial {
		t.Fatalf("err = %v, want an interstitial SoftBlockError", err)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("made %d requests, want 1", n)
	}
	if got := c.CurrentThrottle(); got != 10*time.Millisecond {
		t.Errorf("interval = %v, want it unchanged", got)
	}
}

// A store page missing the block the parser reads is more likely a markup
// change than a refusal: gentle slowdown, and the breaker stays closed.
func TestMalformedGetsSoftSlowdownOnly(t *testing.T) {
	var calls atomic.Int32
	page := `<script>AF_initDataCallback({key: 'ds:1', hash: '1', data:[1], sideChannel: {}});</script>`
	c := NewClient(
		WithHTTPClient(redirectTo("https://play.google.com/store/apps/details?id=x", 200, page, &calls)),
		WithAdaptiveThrottle(AdaptivePolicy{Min: time.Millisecond, Max: time.Second, Start: 10 * time.Millisecond,
			Cooldown: time.Hour}),
	)
	_, err := c.App(context.Background(), "x", AppOptions{})
	var sb *SoftBlockError
	if !errors.As(err, &sb) || sb.Verdict != VerdictMalformed || sb.Reason != "no-ds:5" {
		t.Fatalf("err = %v, want malformed no-ds:5", err)
	}
	if got := c.CurrentThrottle(); got != 13*time.Millisecond {
		t.Errorf("interval = %v, want the soft slowdown to 13ms", got)
	}
	if s, _ := c.Breaker(); s != BreakerClosed {
		t.Errorf("breaker = %s, want closed", s)
	}
}

// FullDetail used to skip lookups that failed and return un-enriched results
// with a nil error. A refusal now reaches the caller.
func TestSearchFullDetailPropagatesRefusal(t *testing.T) {
	page := readFixture(t, "search_page.html")
	c := newMockClient(t,
		routePath("/store/search", page),
		routePathStatus("/_/PlayStoreUi/data/batchexecute", http.StatusTooManyRequests),
	)
	_, err := c.Search(context.Background(), SearchOptions{Term: "minecraft", FullDetail: true})
	var se *StatusError
	if !errors.As(err, &se) || se.Code != http.StatusTooManyRequests {
		t.Fatalf("err = %v, want the 429 from the detail lookups", err)
	}
}

func TestSearchZeroResultsIsEmptyNotError(t *testing.T) {
	c := newMockClient(t, routePath("/store/search", readFixture(t, "search_zero_results.html")))
	results, err := c.Search(context.Background(), SearchOptions{Term: "zqxjvkwplmqzzx9183"})
	if err != nil || len(results) != 0 {
		t.Fatalf("got %d results, err %v; want none and no error", len(results), err)
	}
}
