package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	googleplayscraper "github.com/kryuchenko/google-play-scraper"
	"github.com/kryuchenko/google-play-scraper/records"
)

// The error-code traceability matrix for the CLI: every records.Code, the
// scenario that makes a gpscrape process report it, the exit status that
// process ended with, and whether README.md documents both.
//
// Codes are recorded from behaviour, not from source: each scenario runs the
// test binary as gpscrape (see TestMain) against a store answering one way,
// and the code is read back from the error object the process wrote to
// stderr, the exit status from the process. The matrix is complete when every
// code in records/errors.go was observed here or is one this surface cannot
// produce, named in cliElsewhere and produced by the MCP server's matrix
// (mcp/codes_test.go), which is a separate module and runs there.
//
//	go test -run TestErrorCodeMatrix -v ./cmd/gpscrape
//
// prints the matrix.

// cliElsewhere are the codes gpscrape cannot report, and why.
var cliElsewhere = map[records.Code]string{
	records.CodeBusy: "the CLI runs one command per process and has no queue to be full; gpscrape-mcp only",
}

// codesFromSource enumerates the Code constants declared in records/errors.go,
// so a code added there is a row here without anyone remembering to add it.
func codesFromSource(t *testing.T, path string) (codes []records.Code, names map[records.Code]string) {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	names = map[records.Code]string{}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, s := range gd.Specs {
			vs := s.(*ast.ValueSpec)
			if id, ok := vs.Type.(*ast.Ident); !ok || id.Name != "Code" {
				continue
			}
			for i, v := range vs.Values {
				lit, ok := v.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					t.Fatalf("a Code constant that is not a string literal: %#v", v)
				}
				s, _ := strconv.Unquote(lit.Value)
				codes = append(codes, records.Code(s))
				names[records.Code(s)] = vs.Names[i].Name
			}
		}
	}
	if len(codes) == 0 {
		t.Fatalf("no Code constants in %s", path)
	}
	return codes, names
}

// cliRun is what one gpscrape process did.
type cliRun struct {
	exit   int
	code   records.Code // from the stderr error object; "cancelled" for an interrupt
	stderr string
}

// runCLI runs the test binary as `gpscrape args...` against handler. With
// interrupt set it sends SIGINT once the store has a request in hand.
func runCLI(t *testing.T, handler http.HandlerFunc, interrupt bool, args ...string) cliRun {
	t.Helper()
	inFlight := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case inFlight <- struct{}{}:
		default:
		}
		if handler == nil {
			t.Errorf("gpscrape %v made a request (%s %s) it should not have", args, r.Method, r.URL.Path)
			w.WriteHeader(http.StatusTeapot)
			return
		}
		handler(w, r)
	}))
	defer srv.Close()

	cmd := exec.CommandContext(t.Context(), os.Args[0], args...)
	cmd.Env = append(os.Environ(), asMainEnv+"=1", storeEnv+"="+srv.URL, "GPSCRAPE_ERRORS=json")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if interrupt {
		select {
		case <-inFlight:
		case <-time.After(10 * time.Second):
			t.Fatal("no request reached the store to interrupt")
		}
		if err := cmd.Process.Signal(os.Interrupt); err != nil {
			t.Fatal(err)
		}
	}
	_ = cmd.Wait()
	run := cliRun{exit: cmd.ProcessState.ExitCode(), stderr: stderr.String()}
	for _, line := range strings.Split(run.stderr, "\n") {
		var obj struct {
			Error struct {
				Code records.Code `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(line), &obj) == nil && obj.Error.Code != "" {
			run.code = obj.Error.Code
		}
	}
	// An interrupt is not an error object: the CLI spells cancelled as
	// "interrupted" and exit 130, and that pair is what is recorded.
	if run.code == "" && run.exit == 130 && strings.Contains(run.stderr, "gpscrape: interrupted") {
		run.code = records.CodeCancelled
	}
	return run
}

func status(code int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) }
}

// hang answers nothing until the client gives up.
func hang(w http.ResponseWriter, r *http.Request) {
	select {
	case <-r.Context().Done():
	case <-time.After(10 * time.Second):
	}
	w.WriteHeader(http.StatusServiceUnavailable)
}

// emptyPermissions answers xdSrCf for one id with a present frame and an
// empty payload: the store's way of saying it has nothing for that id here.
func emptyPermissions(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write(framesEnvelope("xdSrCf", map[string]string{"0": ""}, []string{"0"}))
}

type cliScenario struct {
	code      records.Code
	how       string
	args      []string
	store     http.HandlerFunc // nil: the command must not reach the store
	interrupt bool
}

var cliScenarios = []cliScenario{
	{code: records.CodeInvalid, how: "reviews -score 9", args: []string{"reviews", "-score", "9", "com.qa.x"}},
	{code: records.CodeInvalid, how: "app com.a \"  \", no request", args: []string{"app", "com.qa.a", "  "}},
	{code: records.CodeInvalid, how: "permissions com.a \"\", no request", args: []string{"permissions", "com.qa.a", ""}},
	{code: records.CodeInvalid, how: "pcapp \" \", no request", args: []string{"pcapp", " "}},
	{code: records.CodeNotFound, how: "app, details page 404", args: []string{"app", "com.qa.x"}, store: status(404)},
	{code: records.CodeNotListed, how: "permissions, an empty frame for the id", args: []string{"permissions", "com.qa.x"}, store: emptyPermissions},
	{code: records.CodeRateLimited, how: "app, store answers 429", args: []string{"app", "com.qa.x"}, store: status(429)},
	{code: records.CodeRateLimited, how: "app com.a com.b, batch answers 429", args: []string{"app", "com.qa.a", "com.qa.b"}, store: status(429)},
	{code: records.CodeTimeout, how: "app -timeout 200ms, store hangs", args: []string{"app", "-timeout", "200ms", "com.qa.x"}, store: hang},
	{code: records.CodeCancelled, how: "app, SIGINT while the request is out", args: []string{"app", "com.qa.x"}, store: hang, interrupt: true},
	{code: records.CodeConfirmation, how: "catalog ids, no -yes, stdin not a terminal", args: []string{"catalog", "ids"}},
	{code: records.CodeStore, how: "app, store answers 500", args: []string{"app", "com.qa.x"}, store: status(500)},
	{code: records.CodeInternal, how: "a command that panics", args: []string{"test-panic"}},
}

func TestErrorCodeMatrix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the interrupt scenario sends SIGINT")
	}
	codes, names := codesFromSource(t, "../../records/errors.go")
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}

	type row struct{ how, exit string }
	seen := map[records.Code][]row{}
	exits := map[int]bool{}
	for _, s := range cliScenarios {
		t.Run(string(s.code), func(t *testing.T) {
			got := runCLI(t, s.store, s.interrupt, s.args...)
			exits[got.exit] = true
			if got.code != s.code {
				t.Errorf("gpscrape %s: reported %q, want %q\nstderr: %s", strings.Join(s.args, " "), got.code, s.code, got.stderr)
				return
			}
			if want := s.code.ExitCode(); got.exit != want {
				t.Errorf("gpscrape %s: exit %d, want %d for %s", strings.Join(s.args, " "), got.exit, want, s.code)
			}
			seen[s.code] = append(seen[s.code], row{s.how, strconv.Itoa(got.exit)})
		})
	}
	t.Run("ok", func(t *testing.T) {
		if got := runCLI(t, nil, false, "version"); got.exit != 0 {
			t.Errorf("version: exit %d", got.exit)
		} else {
			exits[0] = true
		}
	})

	t.Logf("%-22s %-5s %-4s %s", "code", "exit", "doc", "produced by")
	for _, c := range codes {
		doc := documentedCLI(readme, c)
		rows := seen[c]
		switch why, elsewhere := cliElsewhere[c]; {
		case elsewhere && len(rows) > 0:
			t.Errorf("%s is listed as not reachable from the CLI, yet a scenario produced it", c)
		case elsewhere:
			t.Logf("%-22s %-5s %-4s (not here: %s)", c, "-", yesNo(doc), why)
		case len(rows) == 0:
			t.Errorf("%s: no test makes gpscrape report it; add a scenario to cliScenarios", c)
		}
		for _, r := range rows {
			t.Logf("%-22s %-5s %-4s %s", c, r.exit, yesNo(doc), r.how)
		}
		if !doc {
			t.Errorf("%s is not in README.md's table of codes (a row starting \"| `%s` | %s\")", c, c, exitColumn(c))
		}
	}
	// gpscrape-mcp is another module and its matrix runs there; what can be
	// checked from here is that it has a scenario for each code this one
	// hands to it.
	mcpMatrix, err := os.ReadFile("../../mcp/codes_test.go")
	if err != nil {
		t.Fatal(err)
	}
	for c := range cliElsewhere {
		if !slices.Contains(codes, c) {
			t.Errorf("cliElsewhere names %s, which records/errors.go does not declare", c)
		}
		if !bytes.Contains(mcpMatrix, []byte("{records."+names[c]+",")) {
			t.Errorf("%s is left to gpscrape-mcp, but mcp/codes_test.go has no scenario for it", c)
		}
	}

	// Every exit status a code maps to, and 0, is documented and was seen.
	want := map[int]bool{0: true}
	for _, c := range codes {
		if _, elsewhere := cliElsewhere[c]; !elsewhere {
			want[c.ExitCode()] = true
		}
	}
	usageExits := usage[strings.Index(usage, "Exit status:"):]
	for e := range want {
		if !exits[e] {
			t.Errorf("exit status %d: no scenario ended with it", e)
		}
		if !strings.Contains(usageExits, " "+strconv.Itoa(e)+" ") {
			t.Errorf("exit status %d is not in the usage text's Exit status line", e)
		}
	}
}

// documentedCLI reports whether README.md's code table has c with the exit
// status gpscrape ends with for it and the retryable flag the error object
// carries.
func documentedCLI(readme []byte, c records.Code) bool {
	prefix := fmt.Sprintf("| `%s` | %s |", c, exitColumn(c))
	for _, line := range strings.Split(string(readme), "\n") {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		cells := strings.Split(strings.Trim(line, "| "), "|")
		return strings.TrimSpace(cells[len(cells)-1]) == map[bool]string{true: "yes", false: "no"}[c.Retryable()]
	}
	return false
}

func exitColumn(c records.Code) string {
	if _, elsewhere := cliElsewhere[c]; elsewhere {
		return "-"
	}
	return strconv.Itoa(c.ExitCode())
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "NO"
}

// A run whose apps all failed the same way carries that code; mixed failures
// are a store_error; an interrupt is still an interrupt.
func TestAllFailedKeepsTheSharedCode(t *testing.T) {
	gone := fmt.Errorf("request failed: %w", googleplayscraper.ErrNotFound)
	for _, c := range []struct {
		errs []error
		want records.Code
	}{
		{[]error{gone, gone}, records.CodeNotFound},
		{[]error{googleplayscraper.ErrBlocked}, records.CodeRateLimited},
		{[]error{gone, googleplayscraper.ErrBlocked}, records.CodeStore},
		{[]error{context.Canceled, context.Canceled}, records.CodeCancelled},
	} {
		err := batchFailed(c.errs)
		if got := records.Classify(err); got != c.want {
			t.Errorf("%v: %s, want %s", c.errs, got, c.want)
		}
		if !strings.Contains(err.Error(), fmt.Sprintf("all %d apps failed", len(c.errs))) {
			t.Errorf("%v: %q does not give the count", c.errs, err)
		}
	}
	if !errors.Is(batchFailed([]error{context.Canceled}), context.Canceled) {
		t.Error("an interrupted run no longer reads as one, so main would not exit 130")
	}
}
