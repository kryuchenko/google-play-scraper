package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	googleplayscraper "github.com/kryuchenko/google-play-scraper"
)

// The whole-CLI harness: the test binary re-executes itself with
// GPSCRAPE_TEST_MAIN set, and TestMain hands that process to main() instead
// of the tests. That is the only way to observe what a user observes -- the
// exit status, both streams, a panic -- since main ends in os.Exit. The child
// builds its client over offlineStore, so nothing leaves the process.

const (
	envTestMain = "GPSCRAPE_TEST_MAIN"
	envFixtures = "GPSCRAPE_TEST_FIXTURES"

	// The ids the offline store knows: one it answers, one it has never
	// heard of, and one that is not a package name at all.
	cliValidID     = "com.example.app"
	cliMissingID   = "com.example.missing"
	cliMalformedID = "not a package&id=x"

	// Ids every request for fails: with 500, with 429, and with 429 in de
	// and 500 elsewhere.
	cliBrokenID  = "com.example.broken"
	cliLimitedID = "com.example.limited"
	cliFlakyID   = "com.example.flaky"
)

// runMainForTest is the child side. It never returns.
func runMainForTest() {
	fixtures := os.Getenv(envFixtures)
	newClientHook = func(c *common) *googleplayscraper.Client {
		opts := []googleplayscraper.ClientOption{
			googleplayscraper.WithHTTPClient(&http.Client{Transport: offlineStore(fixtures)}),
			googleplayscraper.WithConcurrency(c.concurrency),
		}
		// The throttle is left out: it was validated by the time a client is
		// built, and an accepted -throttle 1h would otherwise make the run
		// wait an hour. -adaptive keeps its breaker wiring, on a floor short
		// enough to cost nothing.
		if c.adaptive {
			opts = append(opts,
				googleplayscraper.WithAdaptiveThrottle(googleplayscraper.AdaptivePolicy{
					Min: time.Microsecond, Max: time.Millisecond, Cooldown: time.Millisecond,
				}),
				googleplayscraper.WithHooks(googleplayscraper.Hooks{OnBreaker: c.onBreaker}))
		}
		return googleplayscraper.NewClient(opts...)
	}
	os.Args = append([]string{"gpscrape"}, os.Args[1:]...)
	main()
	os.Exit(0)
}

// cliResult is one run of the binary.
type cliResult struct {
	args   []string
	stdout string
	stderr string
	exit   int
}

func (r cliResult) String() string {
	return fmt.Sprintf("gpscrape %s\nexit %d\nstdout:\n%s\nstderr:\n%s",
		quoteArgs(r.args), r.exit, clip(r.stdout), clip(r.stderr))
}

func quoteArgs(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		if a == "" || strings.ContainsAny(a, " &!,'\"") {
			a = fmt.Sprintf("%q", a)
		}
		q[i] = a
	}
	return strings.Join(q, " ")
}

func clip(s string) string {
	if len(s) > 2000 {
		return s[:2000] + "...[clipped]"
	}
	return s
}

// runCLI runs `gpscrape args...` in a fresh process, in an empty directory
// and with an empty HOME, so verbs whose -dir defaults to ./catalog write
// nowhere that matters.
func runCLI(t *testing.T, args ...string) cliResult {
	t.Helper()
	fixtures, err := filepath.Abs(filepath.Join("..", "..", "testdata"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], args...)
	dir := t.TempDir()
	cmd.Dir = dir
	env := []string{envTestMain + "=1", envFixtures + "=" + fixtures, "HOME=" + dir}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "HOME", "GPSCRAPE_ERRORS", "GPSCRAPE_DEBUG", envTestMain, envFixtures:
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	res := cliResult{args: args, stdout: stdout.String(), stderr: stderr.String()}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		res.exit = ee.ExitCode()
	default:
		t.Fatalf("gpscrape %s: %v", quoteArgs(args), err)
	}
	if ctx.Err() != nil {
		t.Fatalf("gpscrape %s did not finish in time\n%s", quoteArgs(args), res)
	}
	return res
}

// cliError is the error object main writes to stderr.
type cliError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// stderrErrors returns the {"error":{...}} objects on stderr, in order.
func stderrErrors(stderr string) []cliError {
	var out []cliError
	for line := range strings.SplitSeq(stderr, "\n") {
		if !strings.HasPrefix(line, `{"error":`) {
			continue
		}
		var e struct {
			Error cliError `json:"error"`
		}
		if json.Unmarshal([]byte(line), &e) == nil {
			out = append(out, e.Error)
		}
	}
	return out
}

// offlineStore is the Play Store the child talks to, built from the root
// package's recorded pages. cliMissingID and cliMalformedID are unknown to it
// wherever they appear: a 404 for a page, a missing answer inside a batch.
// A request it has no route for is reported on stderr, where the parent
// checks for it: a fixture that has stopped covering what a command fetches
// should be heard about.
func offlineStore(fixtures string) http.RoundTripper {
	var mu sync.Mutex
	calls := map[string]int{}
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join(fixtures, name))
		if err != nil {
			fmt.Fprintf(os.Stderr, "offlinestore: %v\n", err)
		}
		return b
	}
	unknown := func(s string) bool {
		return strings.Contains(s, cliMissingID) || strings.Contains(s, cliMalformedID) ||
			strings.Contains(s, "NO_SUCH_CATEGORY")
	}
	return roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var body []byte
		if req.Body != nil {
			body, _ = io.ReadAll(req.Body)
		}
		form, _ := url.QueryUnescape(string(body))
		q := req.URL.Query()
		var qv []string
		for _, vs := range q {
			qv = append(qv, vs...)
		}
		hay := strings.Join(qv, "\n")

		status, out := http.StatusOK, []byte(nil)
		switch p := req.URL.Path; {
		case unknown(hay):
			status = http.StatusNotFound
		case strings.Contains(hay+form, cliBrokenID):
			status = http.StatusInternalServerError
		case strings.Contains(hay+form, cliLimitedID):
			status = http.StatusTooManyRequests
		case strings.Contains(hay+form, cliFlakyID):
			// Refused in Germany, broken everywhere else: failures that do
			// not agree on a code.
			status = http.StatusInternalServerError
			if q.Get("gl") == "de" {
				status = http.StatusTooManyRequests
			}
		case p == "/store/apps/details":
			out = read("app_page.html")
		case p == "/store/search":
			out = read("search_page.html")
		case p == "/store/apps/datasafety":
			out = read("datasafety_page.html")
		case p == "/store/apps/dev", p == "/store/apps/developer":
			out = read("developer_page.html")
		case p == "/store/apps/collection/cluster":
			out = read("similar_page.html")
		case p == "/store/apps/top", p == "/store/apps", strings.HasPrefix(p, "/store/apps/category/"):
			out = read("category_page.html")
		case p == "/pc-store/games/details":
			out = read("pc_page_game.html")
		case p == pathBatch:
			out = offlineBatch(q.Get("rpcids"), form, unknown, read, &mu, calls)
			if out == nil {
				status = http.StatusNotFound
			}
		default:
			fmt.Fprintf(os.Stderr, "offlinestore: unrouted %s %s\n", req.Method, req.URL)
			status = http.StatusNotFound
		}
		return &http.Response{
			StatusCode: status, Header: http.Header{}, Request: req,
			Body: io.NopCloser(bytes.NewReader(out)),
		}, nil
	})
}

// offlineBatch answers one batchexecute request.
func offlineBatch(rpc, form string, unknown func(string) bool, read func(string) []byte,
	mu *sync.Mutex, calls map[string]int,
) []byte {
	mu.Lock()
	calls[rpc]++
	n := calls[rpc]
	mu.Unlock()

	// Per-id RPCs: one frame per id, and an empty one for an id the store
	// does not know -- what Google sends for a missing app in a batch.
	perID := func(payload string) []byte {
		byIndex := map[string]string{}
		var order []string
		for i, m := range appRe.FindAllStringSubmatch(form, -1) {
			idx := fmt.Sprint(i)
			byIndex[idx] = payload
			if unknown(m[1]) {
				byIndex[idx] = ""
			}
			order = append(order, idx)
		}
		return framesEnvelope(rpc, byIndex, order)
	}
	switch rpc {
	case "Ws7gDc":
		return perID(`[null,[null,null,[["ok"]]]]`)
	case "xdSrCf":
		return perID(`[[["Location",null,[[1,"approximate location"]]]]]`)
	}
	if unknown(form) {
		return nil
	}
	switch rpc {
	case "vyAe2":
		return read("list_vyae2.bin")
	case "IJ4APc":
		return read("suggest_batch.bin")
	case "lGYRle":
		return []byte(")]}'\n\n[[\"wrb.fr\",\"lGYRle\",null,null,null,null,\"generic\"]]")
	case "qnKhOb":
		return read("qnkhob_page.bin")
	case "oCPfdb":
		// One page, then the end: the recorded page carries a token, and
		// answering it forever would make -limit 0 a run that never ends.
		if n == 1 {
			return read("reviews_batch.bin")
		}
		return []byte(")]}'\n\n[[\"wrb.fr\",\"oCPfdb\",\"[[]]\",null,null,null,\"generic\"]]")
	}
	fmt.Fprintf(os.Stderr, "offlinestore: unrouted batchexecute rpcids=%s\n", rpc)
	return nil
}
