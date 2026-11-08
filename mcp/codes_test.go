package gpsmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	gps "github.com/kryuchenko/google-play-scraper"
	"github.com/kryuchenko/google-play-scraper/records"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The error-code traceability matrix for the MCP server: every records.Code,
// the scenario that makes a tool call fail with it, and whether README.md
// lists it. The CLI's matrix is cmd/gpscrape/codes_test.go; between them every
// code is produced by a test.
//
// Codes are recorded from behaviour: each scenario calls a tool through a
// client session and reads the code off the front of the error the client got
// ("code: message"), or for a cancelled call, off the response the server
// wrote to the wire. The list of codes is read from records/errors.go in this
// repository, so a code added there is a row here at once.
//
//	go test -run TestErrorCodeMatrix -v
//
// prints the matrix.

// mcpElsewhere are the codes no tool can return, and why.
var mcpElsewhere = map[records.Code]string{
	records.CodeConfirmation: "no tool starts an unbounded run; the CLI's catalog sweep and catalog ids only",
}

// codesFromSource enumerates the Code constants declared in records/errors.go.
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

// codeOf is the code a tool error starts with, or "".
func codeOf(msg string) records.Code {
	c, _, ok := strings.Cut(msg, ": ")
	if !ok {
		return ""
	}
	return records.Code(c)
}

type mcpScenario struct {
	code records.Code
	how  string
	run  func(t *testing.T) string // the error text the client saw
}

var appArgs = map[string]any{"app_id": "com.qa.x"}

func failing(s Scraper, opts Options, tool string, args map[string]any) func(t *testing.T) string {
	return func(t *testing.T) string { return callErr(t, connect(t, s, opts), tool, args) }
}

var mcpScenarios = []mcpScenario{
	{records.CodeInvalid, "play_get_app with a name for an id", failing(&fake{}, Options{}, "play_get_app", map[string]any{"app_id": "not an id"})},
	{records.CodeInvalid, "play_get_apps with a blank id among good ones, no request", blankInBatch},
	{records.CodeNotFound, "play_get_app, the store says 404", failing(&fake{appErr: &gps.StatusError{Code: 404}}, Options{}, "play_get_app", appArgs)},
	{records.CodeNotListed, "play_get_permissions, an empty frame for a listed app", failing(&fake{permErr: errors.New("no data returned for com.qa.x")}, Options{}, "play_get_permissions", appArgs)},
	{records.CodeRateLimited, "play_get_app, the store blocks", failing(&fake{appErr: gps.ErrBlocked}, Options{}, "play_get_app", appArgs)},
	{records.CodeTimeout, "play_get_app past -timeout", failing(&fake{block: make(chan struct{})}, Options{CallTimeout: 50 * time.Millisecond}, "play_get_app", appArgs)},
	{records.CodeBusy, "a second call while the only slot is held", busyCall},
	{records.CodeCancelled, "a queued call the client cancels", cancelledCall},
	{records.CodeStore, "play_get_app, an unclassified store failure", failing(&fake{appErr: errors.New("boom")}, Options{}, "play_get_app", appArgs)},
	{records.CodeInternal, "play_get_app panics", failing(&fake{panicApp: true}, Options{}, "play_get_app", appArgs)},
}

// noBatch is a scraper whose batched lookup must not be reached.
type noBatch struct {
	*fake
	t *testing.T
}

func (n noBatch) AppsMany(ctx context.Context, ids []string, o gps.AppOptions) []gps.AppResult {
	n.t.Errorf("AppsMany(%q) was called; a blank id should be refused first", ids)
	return n.fake.AppsMany(ctx, ids, o)
}

// blankInBatch sends a whitespace-only id between two good ones.
func blankInBatch(t *testing.T) string {
	return callErr(t, connect(t, noBatch{&fake{}, t}, Options{}), "play_get_apps",
		map[string]any{"app_ids": []string{"com.qa.a", "  ", "com.qa.b"}})
}

// busyCall holds the one slot and calls again: the second call waits out the
// call timeout and is refused.
func busyCall(t *testing.T) string {
	block := make(chan struct{})
	f := &fake{block: block, started: make(chan struct{}, 1), ignoreCtx: true}
	cs := connect(t, f, Options{MaxConcurrent: 1, CallTimeout: 200 * time.Millisecond})
	done := make(chan struct{})
	go func() {
		_, _ = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "play_get_app", Arguments: appArgs})
		close(done)
	}()
	<-f.started
	defer func() { close(block); <-done }()
	return callErr(t, cs, "play_list_categories", map[string]any{})
}

// wireLog is the server side of a LoggingTransport: every message the server
// wrote, one per line.
type wireLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *wireLog) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *wireLog) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// cancelledCall cancels a call while it waits for a slot. The client's
// CallTool returns its own context error, so the code is read off the wire:
// what the server answered the cancelled request with, if anything.
func cancelledCall(t *testing.T) string {
	block := make(chan struct{})
	f := &fake{block: block, started: make(chan struct{}, 1), ignoreCtx: true}
	srv := NewServer(f, Options{MaxConcurrent: 1, CallTimeout: 10 * time.Second})
	st, ct := mcp.NewInMemoryTransports()
	wire := &wireLog{}
	ctx := context.Background()
	if _, err := srv.Connect(ctx, &mcp.LoggingTransport{Transport: st, Writer: wire}, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()

	first := make(chan struct{})
	go func() {
		_, _ = cs.CallTool(ctx, &mcp.CallToolParams{Name: "play_get_app", Arguments: appArgs})
		close(first)
	}()
	<-f.started
	cctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		_, _ = cs.CallTool(cctx, &mcp.CallToolParams{Name: "play_list_categories", Arguments: map[string]any{}})
		close(done)
	}()
	time.Sleep(100 * time.Millisecond) // let the call reach the slot wait
	cancel()
	<-done
	var text string
	for i := 0; i < 100 && text == ""; i++ {
		text = toolErrorOnWire(wire.String(), records.CodeCancelled)
		time.Sleep(20 * time.Millisecond)
	}
	close(block)
	<-first
	if text == "" {
		t.Errorf("the server answered the cancelled call with no tool error; it wrote:\n%s", wire.String())
	}
	return text
}

// toolErrorOnWire finds a tool error starting with code among the messages
// the server wrote.
func toolErrorOnWire(wire string, code records.Code) string {
	for _, line := range strings.Split(wire, "\n") {
		_, msg, ok := strings.Cut(line, "{")
		if !ok {
			continue
		}
		var m struct {
			Result struct {
				IsError bool `json:"isError"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"result"`
		}
		if json.Unmarshal([]byte("{"+msg), &m) != nil || !m.Result.IsError || len(m.Result.Content) == 0 {
			continue
		}
		if txt := m.Result.Content[0].Text; codeOf(txt) == code {
			return txt
		}
	}
	return ""
}

func TestErrorCodeMatrix(t *testing.T) {
	codes, names := codesFromSource(t, "../records/errors.go")
	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}

	seen := map[records.Code][]string{}
	for _, s := range mcpScenarios {
		t.Run(string(s.code), func(t *testing.T) {
			msg := s.run(t)
			if got := codeOf(msg); got != s.code {
				t.Errorf("%s: %q, want a %s error", s.how, msg, s.code)
				return
			}
			seen[s.code] = append(seen[s.code], s.how)
		})
	}

	t.Logf("%-22s %-4s %s", "code", "doc", "produced by")
	for _, c := range codes {
		doc := bytes.Contains(readme, fmt.Appendf(nil, "| `%s` |", c))
		mark := map[bool]string{true: "yes", false: "NO"}[doc]
		switch why, elsewhere := mcpElsewhere[c]; {
		case elsewhere && len(seen[c]) > 0:
			t.Errorf("%s is listed as not reachable from a tool, yet a scenario produced it", c)
		case elsewhere:
			t.Logf("%-22s %-4s (not here: %s)", c, "-", why)
			continue
		case len(seen[c]) == 0:
			t.Errorf("%s: no test makes a tool return it; add a scenario to mcpScenarios", c)
		}
		for _, how := range seen[c] {
			t.Logf("%-22s %-4s %s", c, mark, how)
		}
		if !doc {
			t.Errorf("%s is not in README.md's table of tool error codes", c)
		}
	}
	// The CLI is another module and its matrix runs there; what can be
	// checked from here is that it has a scenario for each code this one
	// hands to it.
	cli, err := os.ReadFile("../cmd/gpscrape/codes_test.go")
	if err != nil {
		t.Fatal(err)
	}
	for c := range mcpElsewhere {
		if !slices.Contains(codes, c) {
			t.Errorf("mcpElsewhere names %s, which records/errors.go does not declare", c)
		}
		if !bytes.Contains(cli, []byte("{code: records."+names[c]+",")) {
			t.Errorf("%s is left to the CLI, but cmd/gpscrape/codes_test.go has no scenario for it", c)
		}
	}
}
