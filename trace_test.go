package googleplayscraper

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime/trace"
	"strconv"
	"strings"
	"sync/atomic"
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

// TestPublicMethodsOpenTheirOwnTask pins the claim trace.go opens with: every
// public method that issues a request opens a task of its own. The value of a
// trace is that it reads as a list of operations; a method that opens no task
// files its requests under whatever task the caller happened to be in, which
// is worse than no attribution because it looks like attribution.
//
// Nesting is allowed and expected -- CatalogSize's shard requests sit in a
// CatalogSeq task whose parent is CatalogSize -- so a request is attributed by
// walking the parent chain, not by reading the task it names directly.
func TestPublicMethodsOpenTheirOwnTask(t *testing.T) {
	if trace.IsEnabled() {
		t.Skip("a trace is already running (go test -trace); cannot start a second one")
	}
	if !traceToolAvailable() {
		t.Skip("go tool trace is unavailable; nothing can read the trace back")
	}

	const shard0 = BaseURL + "/sitemaps/shard-00.xml.gz"
	const index0 = BaseURL + "/sitemaps/sitemaps-index-0.xml"

	// The reviews route paginates: the first three requests carry a token, the
	// fourth ends the chain. ReviewsSeq therefore walks several pages, which is
	// what makes the task-count assertion below mean something.
	var reviewRequests atomic.Int64
	const reviewPage = `[[["r1",["U"],5,null,"text",[1704067200],1]],[null,"tok"]]`

	routes := append(catalogShards(t, 4),
		func(req *http.Request) (mockResponse, bool) {
			if req.URL.Path != pathBatch || req.URL.Query().Get("rpcids") != "oCPfdb" {
				return mockResponse{}, false
			}
			if reviewRequests.Add(1) >= 4 {
				return mockResponse{Body: framesEnvelope("oCPfdb",
					map[string]string{"0": "[[],null]"}, []string{"0"})}, true
			}
			return mockResponse{Body: framesEnvelope("oCPfdb",
				map[string]string{"0": reviewPage}, []string{"0"})}, true
		},
		routeQuery(pathBatch, map[string]string{"rpcids": "Ws7gDc"},
			framesEnvelope("Ws7gDc", map[string]string{"0": "null"}, []string{"0"})),
		routeQuery(pathBatch, map[string]string{"rpcids": "xdSrCf"},
			framesEnvelope("xdSrCf", map[string]string{"0": "[[]]"}, []string{"0"})),
		routeQuery(pathBatch, map[string]string{"rpcids": "IJ4APc"},
			framesEnvelope("IJ4APc", map[string]string{"0": "[[]]"}, []string{"0"})),
	)
	c := newMockClient(t, routes...)
	ctx := context.Background()

	path := filepath.Join(t.TempDir(), "tasks.trace")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create trace file: %v", err)
	}
	if err := trace.Start(f); err != nil {
		_ = f.Close()
		t.Fatalf("start trace: %v", err)
	}

	// Results are ignored throughout: these payloads are the smallest thing the
	// routes can serve, and whether they parse says nothing about tracing. What
	// matters is that each call reached the transport at least once.
	_, _ = c.Reviews(ctx, "com.x", ReviewOptions{})
	_ = c.AppsMany(ctx, []string{"com.a"}, AppOptions{})
	_ = c.PermissionsMany(ctx, []string{"com.a"}, PermissionsOptions{})
	_ = c.SuggestMany(ctx, []string{"maps"}, SuggestOptions{})
	_, _ = c.SitemapIndexURLs(ctx)
	_, _ = c.SitemapShards(ctx, index0)
	_, _ = c.AllSitemapShards(ctx)
	_, _ = c.SitemapShardPackages(ctx, shard0)
	_, _ = c.CatalogSize(ctx, SizeOptions{Exact: true})
	for range c.ReviewsSeq(ctx, "com.x", ReviewOptions{}) { //nolint:revive // drained for the requests it makes
	}

	trace.Stop()
	if cerr := f.Close(); cerr != nil {
		t.Fatalf("close trace file: %v", cerr)
	}

	dump := parseTrace(t, path)
	tasks := parseTraceTasks(dump)

	// The constants, not the strings they hold: this test tracks whatever names
	// the vocabulary settles on.
	want := []string{
		traceTaskReviews,
		traceTaskAppsMany,
		traceTaskPermissionsMany,
		traceTaskSuggestMany,
		traceTaskSitemapIndexURLs,
		traceTaskSitemapShards,
		traceTaskAllSitemapShards,
		traceTaskSitemapShardPackages,
		traceTaskCatalogSize,
	}

	opened := make(map[string]bool, len(tasks))
	for _, task := range tasks {
		opened[task.typ] = true
	}
	for _, name := range want {
		if !opened[name] {
			t.Errorf("no task named %q was opened; that method's requests are attributed to its caller", name)
		}
	}

	// Every request must reach one of them through its parent chain, and every
	// one of them must have caught a request -- a task that opens around no
	// request is a name in the trace and nothing else.
	attributed := make(map[string]bool, len(want))
	var orphaned int
	for _, id := range httpRegionTasks(dump) {
		chain := taskChain(tasks, id)
		if len(chain) == 0 {
			orphaned++
			continue
		}
		for _, typ := range chain {
			attributed[typ] = true
		}
	}
	if orphaned > 0 {
		t.Errorf("%d %s regions resolve to no task at all", orphaned, traceRegionHTTP)
	}
	for _, name := range want {
		if !attributed[name] {
			t.Errorf("task %q caught no %s region: its requests are filed somewhere else", name, traceRegionHTTP)
		}
	}

	// A task per public call, not per iteration. Both of these methods are also
	// called internally in a loop -- reviewsPages walks the token chain, the
	// catalog sweep walks the shards -- and both go through a twin that opens
	// nothing. Deleting either twin makes a deep sweep open thousands of tasks,
	// which no trace viewer survives and which this counts instead.
	counts := make(map[string]int, len(tasks))
	for _, task := range tasks {
		counts[task.typ]++
	}
	if n := int(reviewRequests.Load()); n < 3 {
		t.Fatalf("the reviews route served %d requests; the pagination loop did not run", n)
	}
	for _, once := range []string{traceTaskReviews, traceTaskSitemapShardPackages} {
		if counts[once] != 1 {
			t.Errorf("%d %q tasks were opened for one call to it: a loop is opening one per iteration",
				counts[once], once)
		}
	}
}

// traceTask is one TaskBegin record: what it is called and what it was opened
// inside of.
type traceTask struct {
	typ    string
	parent uint64
}

// traceRootParent is the Parent a top-level task reports -- ^uint64(0), the id
// of the background task every trace has.
const traceRootParent = ^uint64(0)

var (
	taskBeginRe   = regexp.MustCompile(`TaskBegin .*\bID=(\d+) .*\bParent=(\d+) .*\bType="([^"]*)"`)
	regionBeginRe = regexp.MustCompile(`RegionBegin .*\bTask=(\d+) .*\bType="([^"]*)"`)
)

// parseTraceTasks reads the task tree out of a `go tool trace -d=parsed` dump.
func parseTraceTasks(dump string) map[uint64]traceTask {
	tasks := make(map[uint64]traceTask)
	for _, m := range taskBeginRe.FindAllStringSubmatch(dump, -1) {
		id, err := strconv.ParseUint(m[1], 10, 64)
		if err != nil {
			continue
		}
		parent, err := strconv.ParseUint(m[2], 10, 64)
		if err != nil {
			continue
		}
		tasks[id] = traceTask{typ: m[3], parent: parent}
	}
	return tasks
}

// httpRegionTasks returns the task id each http.request region was opened in.
func httpRegionTasks(dump string) []uint64 {
	var ids []uint64
	for _, m := range regionBeginRe.FindAllStringSubmatch(dump, -1) {
		if m[2] != traceRegionHTTP {
			continue
		}
		id, err := strconv.ParseUint(m[1], 10, 64)
		if err != nil {
			continue
		}
		ids = append(ids, id)
	}
	return ids
}

// taskChain names every task enclosing id, innermost first. A request is
// attributed to all of them: CatalogSize calls CatalogSeq, and the shard fetch
// belongs to both.
func taskChain(tasks map[uint64]traceTask, id uint64) []string {
	var chain []string
	for range len(tasks) + 1 { // bounded: a malformed dump must not loop forever
		task, ok := tasks[id]
		if !ok {
			break
		}
		chain = append(chain, task.typ)
		if task.parent == traceRootParent {
			break
		}
		id = task.parent
	}
	return chain
}

// traceToolAvailable reports whether `go tool trace` can be run at all, so a
// toolchain without it skips rather than fails.
func traceToolAvailable() bool {
	cmd := exec.Command("go", "tool", "trace", "-h")
	cmd.Env = append(os.Environ(), "GOWORK=off")
	// -h exits non-zero by design; only the "cannot run it" case matters here.
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		return errors.As(err, &exit)
	}
	return true
}
