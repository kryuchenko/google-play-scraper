//go:build unix

package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	googleplayscraper "github.com/kryuchenko/google-play-scraper"
)

// Kill-and-resume against real processes.
//
// The in-process resume tests drive cmdSync and simulate a crash by writing
// the files a crashed run would leave. That proves the resume logic reads
// such files correctly, not that a real kill produces them: a buffer the
// kernel never saw, a done line written before its ids, a lock left behind
// by a dead process. This test builds gpscrape with -tags killtest (see
// killtest_hook.go), runs catalog sweep as a child process against a fake
// Play, kills it at chosen points -- by request count, during the retry
// pass, and between finish's writes -- reruns it with the same flags until
// it completes, and compares the result to an uninterrupted control run.
//
// After every kill it checks the invariant the resume depends on: every
// shard in the done log has all its ids in the partial file, and every
// complete line of the partial file is a whole id. At the end the snapshot,
// manifest and delta must equal the control's.
//
// A few kill points run by default. GPSCRAPE_KILLTEST=1 runs a seeded set of
// several dozen over more shards, including chains of kills on reruns.

// killIDRe is the grammar of every id the fake serves: a glued or torn id
// cannot match it.
var killIDRe = regexp.MustCompile(`^com\.k\d{5}\.a\d$`)

// killTornRe matches every prefix of such an id: what a kill mid-write can
// leave after the partial file's last newline.
var killTornRe = regexp.MustCompile(`^c(o(m(\.(k(\d{1,5}(\.(a\d?)?)?)?)?)?)?)?$`)

var (
	killGenA = googleplayscraper.Generation{Date: "2026-09-01", Run: "1790000000"}
	killGenB = googleplayscraper.Generation{Date: "2026-09-05", Run: "1790400000"}
)

// killStore is a fake Play serving one generation of n shards, shard i
// holding 3 + i%7 ids, with per-run hooks to kill the client mid-request.
type killStore struct {
	srv *httptest.Server

	mu     sync.Mutex
	gen    googleplayscraper.Generation
	n      int
	bodies map[string][]byte // shard path -> gzip body
	paths  map[string]int    // shard index by path
	// failFirst makes the first hit on these shard indexes, per run, a 500:
	// the shard goes to the retry pass.
	failFirst map[int]bool
	// onShard, if set, is called for every shard request with the shard,
	// how many times this run has asked for it, and how many shard
	// requests this run has made; returning true blocks the response until
	// the client goes away.
	onShard func(i, hitOfShard int, total int64) bool
	hits    map[int]int
	total   atomic.Int64
}

func killShardIDs(gen googleplayscraper.Generation, i int) []string {
	ids := make([]string, 3+i%7)
	for j := range ids {
		ids[j] = fmt.Sprintf("com.k%05d.a%d", i, j)
	}
	if gen.ID() == killGenA.ID() && i%10 == 0 {
		// A has an id B dropped, and B adds one A lacks, so there is a delta.
		ids = append(ids[:len(ids)-1], fmt.Sprintf("com.k%05d.a9", i))
	}
	return ids
}

func (s *killStore) shardPath(i int) string {
	return fmt.Sprintf("/sitemaps/play_sitemaps_%s_%s-%05d-of-%05d.xml.gz", s.gen.Date, s.gen.Run, i, s.n)
}

// publish switches the store to gen with n shards.
func (s *killStore) publish(t *testing.T, gen googleplayscraper.Generation, n int) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gen, s.n = gen, n
	s.bodies, s.paths = map[string][]byte{}, map[string]int{}
	for i := range n {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		_, _ = zw.Write([]byte(`<?xml version='1.0' encoding='UTF-8'?><urlset>`))
		for _, id := range killShardIDs(gen, i) {
			_, _ = fmt.Fprintf(zw, "<url><loc>%s/store/apps/details?id=%s</loc></url>", googleplayscraper.BaseURL, id)
		}
		_, _ = zw.Write([]byte(`</urlset>`))
		_ = zw.Close()
		p := s.shardPath(i)
		s.bodies[p], s.paths[p] = buf.Bytes(), i
	}
}

// reset clears the per-run counters and hooks.
func (s *killStore) reset(onShard func(i, hitOfShard int, total int64) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onShard, s.hits = onShard, map[int]int{}
	s.total.Store(0)
}

func (s *killStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	switch r.URL.Path {
	case "/robots.txt":
		s.mu.Unlock()
		_, _ = w.Write(robotsTestBody(googleplayscraper.BaseURL+indexPath0, googleplayscraper.BaseURL+indexPath1))
		return
	case indexPath0, indexPath1:
		half := (s.n + 1) / 2
		lo, hi := 0, half
		if r.URL.Path == indexPath1 {
			lo, hi = half, s.n
		}
		urls := make([]string, 0, hi-lo)
		for i := lo; i < hi; i++ {
			urls = append(urls, googleplayscraper.BaseURL+s.shardPath(i))
		}
		s.mu.Unlock()
		_, _ = w.Write(indexTestXML(urls...))
		return
	}
	i, ok := s.paths[r.URL.Path]
	if !ok {
		s.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	s.hits[i]++
	hit := s.hits[i]
	total := s.total.Add(1)
	body := s.bodies[r.URL.Path]
	fail := s.failFirst[i] && hit == 1
	hook := s.onShard
	s.mu.Unlock()

	if hook != nil && hook(i, hit, total) {
		<-r.Context().Done()
		return
	}
	if fail {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(body)
}

// killBinary builds gpscrape with the killtest seams once per test binary.
var (
	killBinOnce sync.Once
	killBinPath string
	killBinErr  error
)

func killBinary(t *testing.T) string {
	t.Helper()
	killBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "gpscrape-killtest-")
		if err != nil {
			killBinErr = err
			return
		}
		killBinPath = filepath.Join(dir, "gpscrape")
		// Built once for every test that needs it, so not tied to the
		// first caller's context.
		out, err := exec.CommandContext(context.Background(), "go", "build", "-tags", "killtest", "-o", killBinPath, ".").CombinedOutput()
		if err != nil {
			killBinErr = fmt.Errorf("build: %w\n%s", err, out)
		}
	})
	if killBinErr != nil {
		t.Fatal(killBinErr)
	}
	return killBinPath
}

// killPoint says where a run is stopped.
type killPoint struct {
	name   string
	sig    syscall.Signal
	atReq  int64  // stop when the run's shard request count reaches this
	retry  bool   // stop on the first retry-pass request
	finish string // stop at this finish step (killtest_hook.go)
}

type killRun struct {
	exit   int
	stderr string
}

// runSweep runs one catalog sweep over dir, stopping it at kp if kp is not nil.
func runSweep(t *testing.T, bin string, store *killStore, dir string, kp *killPoint) killRun {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), bin, "catalog", "sweep", "-dir", dir, "-concurrency", "4", "-yes")
	cmd.Env = append(os.Environ(), "GPSCRAPE_TEST_ORIGIN="+store.srv.URL, "GPSCRAPE_ERRORS=text")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	var proc atomic.Pointer[os.Process]
	var once sync.Once
	signal := func() {
		once.Do(func() {
			if p := proc.Load(); p != nil {
				_ = p.Signal(kp.sig)
			}
		})
	}
	stopFile := filepath.Join(t.TempDir(), "stopped")
	var hook func(i, hit int, total int64) bool
	if kp != nil {
		switch {
		case kp.finish != "":
			cmd.Env = append(cmd.Env, "GPSCRAPE_TEST_STOP_AT="+kp.finish, "GPSCRAPE_TEST_STOP_FILE="+stopFile)
		case kp.retry:
			hook = func(i, hit int, _ int64) bool {
				if store.failFirst[i] && hit == 2 {
					signal()
					return true
				}
				return false
			}
		default:
			hook = func(_, _ int, total int64) bool {
				if total == kp.atReq {
					signal()
					return true
				}
				return false
			}
		}
	}
	store.reset(hook)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	proc.Store(cmd.Process)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	if kp != nil && kp.finish != "" {
		for {
			if _, err := os.Stat(stopFile); err == nil {
				signal()
				break
			}
			select {
			case err := <-done:
				t.Fatalf("%s: the run ended before reaching finish step %q: %v\n%s", kp.name, kp.finish, err, stderr.String())
			case <-time.After(5 * time.Millisecond):
			}
		}
	}
	var err error
	select {
	case err = <-done:
	case <-time.After(2 * time.Minute):
		_ = cmd.Process.Kill()
		t.Fatalf("sweep did not finish\n%s", stderr.String())
	}
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return killRun{exit: code, stderr: stderr.String()}
}

// checkPartialState checks what an interrupted run left: every complete
// line of the partial file is a whole id, and every shard the done log names
// has all its ids in the partial file.
func checkPartialState(t *testing.T, label string, store *killStore, dir string, gen googleplayscraper.Generation) {
	t.Helper()
	have := map[string]bool{}
	if b, err := os.ReadFile(filepath.Join(dir, "partial-"+gen.ID()+".txt")); err == nil {
		lines := strings.Split(string(b), "\n")
		for k, l := range lines {
			if k == len(lines)-1 {
				// After the last newline: empty, or the torn tail of an id
				// the next run cuts off.
				if l != "" && !killTornRe.MatchString(l) {
					t.Errorf("%s: torn tail %q is not part of an id", label, l)
				}
				continue
			}
			if !killIDRe.MatchString(l) {
				t.Errorf("%s: partial line %d is %q, not an id", label, k+1, l)
			}
			have[l] = true
		}
	}
	b, err := os.ReadFile(doneLogPath(dir, gen))
	if err != nil {
		return // no done log yet
	}
	lines := strings.Split(string(b), "\n")
	for _, u := range lines[:len(lines)-1] { // the last element is after the final newline
		path := strings.TrimPrefix(u, googleplayscraper.BaseURL)
		store.mu.Lock()
		i, ok := store.paths[path]
		store.mu.Unlock()
		if !ok {
			t.Errorf("%s: done log names %q, not a shard", label, u)
			continue
		}
		for _, id := range killShardIDs(gen, i) {
			if !have[id] {
				t.Errorf("%s: shard %d is in the done log but its id %s is not in the partial file", label, i, id)
				return
			}
		}
	}
}

type sweepResult struct {
	ids      []string
	manifest manifest
	delta    []byte
}

func readSweepResult(t *testing.T, dir string) sweepResult {
	t.Helper()
	var r sweepResult
	readJSON(t, filepath.Join(dir, "manifest-"+killGenB.ID()+".json"), &r.manifest)
	f, err := os.Open(filepath.Join(dir, r.manifest.File))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(zr)
	for sc.Scan() {
		r.ids = append(r.ids, sc.Text())
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	r.delta, err = os.ReadFile(filepath.Join(dir, deltaName(killGenA, killGenB)))
	if err != nil {
		t.Fatalf("no delta: %v", err)
	}
	return r
}

func copyDir(t *testing.T, from string) string {
	t.Helper()
	to := t.TempDir()
	entries, err := os.ReadDir(from)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(from, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(to, e.Name()), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return to
}

func TestSweepSurvivesRealKills(t *testing.T) {
	long := os.Getenv("GPSCRAPE_KILLTEST") != ""
	shardsB := 1200
	if long {
		shardsB = 4000
	}
	bin := killBinary(t)
	store := &killStore{failFirst: map[int]bool{}}
	store.srv = httptest.NewServer(store)
	defer store.srv.Close()

	// The template: generation A swept to completion, the base every run
	// starts from, so finishing B writes a delta.
	template := t.TempDir()
	store.publish(t, killGenA, 300)
	if r := runSweep(t, bin, store, template, nil); r.exit != 0 {
		t.Fatalf("sweep of A: exit %d\n%s", r.exit, r.stderr)
	}
	store.publish(t, killGenB, shardsB)
	// Two shards fail once per run, so every run has a retry pass.
	store.failFirst = map[int]bool{17: true, shardsB - 3: true}

	control := copyDir(t, template)
	if r := runSweep(t, bin, store, control, nil); r.exit != 0 {
		t.Fatalf("control sweep: exit %d\n%s", r.exit, r.stderr)
	}
	want := readSweepResult(t, control)
	if len(want.ids) == 0 || want.manifest.IDs != len(want.ids) {
		t.Fatalf("control: %d ids, manifest %+v", len(want.ids), want.manifest)
	}

	points := []killPoint{
		{name: "kill before the first checkpoint", sig: syscall.SIGKILL, atReq: 100},
		// 530, not 501: the store counts a request when it arrives, the
		// checkpoint fires on the 500th shard processed, and in between are
		// four requests in flight and the shard that failed once.
		{name: "kill just after the first checkpoint", sig: syscall.SIGKILL, atReq: 530},
		{name: "kill in the retry pass", sig: syscall.SIGKILL, retry: true},
		{name: "kill between snapshot and delta", sig: syscall.SIGKILL, finish: "delta"},
		{name: "kill between delta and manifest", sig: syscall.SIGKILL, finish: "manifest"},
		{name: "interrupt mid-sweep", sig: syscall.SIGINT, atReq: 900},
	}
	chains := [][]killPoint(nil)
	if long {
		points = append(points,
			killPoint{name: "kill before the snapshot", sig: syscall.SIGKILL, finish: "snapshot"},
			killPoint{name: "kill before cleanup", sig: syscall.SIGKILL, finish: "cleanup"},
			killPoint{name: "terminate in the retry pass", sig: syscall.SIGTERM, retry: true},
			killPoint{name: "terminate mid-sweep", sig: syscall.SIGTERM, atReq: 2100},
		)
		rng := rand.New(rand.NewPCG(20261108, 2))
		for k := range 24 {
			sig := []syscall.Signal{syscall.SIGKILL, syscall.SIGKILL, syscall.SIGINT, syscall.SIGTERM}[rng.IntN(4)]
			points = append(points, killPoint{
				name: fmt.Sprintf("seeded %d: %v at request %d", k, sig, 0), sig: sig,
				atReq: 1 + rng.Int64N(int64(shardsB)),
			})
		}
		for k := range 6 {
			var chain []killPoint
			for range 2 + rng.IntN(3) {
				chain = append(chain, killPoint{name: fmt.Sprintf("chain %d", k), sig: syscall.SIGKILL,
					atReq: 1 + rng.Int64N(int64(shardsB)/3)})
			}
			chains = append(chains, chain)
		}
	}
	for _, kp := range points {
		chains = append(chains, []killPoint{kp})
	}

	for _, chain := range chains {
		name := chain[0].name
		if len(chain) > 1 {
			name = fmt.Sprintf("%s (%d kills)", chain[0].name, len(chain))
		} else if chain[0].atReq > 0 && strings.HasPrefix(name, "seeded") {
			name = strings.Replace(name, "request 0", "request "+strconv.FormatInt(chain[0].atReq, 10), 1)
		}
		t.Run(name, func(t *testing.T) {
			dir := copyDir(t, template)
			for k, kp := range chain {
				r := runSweep(t, bin, store, dir, &kp)
				label := fmt.Sprintf("after kill %d (%v)", k+1, kp.sig)
				switch kp.sig {
				case syscall.SIGKILL:
					if r.exit == 0 {
						t.Fatalf("%s: the run exited 0", label)
					}
					if _, err := os.Stat(lockPath(dir)); err != nil {
						t.Errorf("%s: no lock left behind (%v); the reclaim is not being exercised", label, err)
					}
				default:
					if r.exit != 130 {
						t.Errorf("%s: exit %d, want 130 for an interrupt\n%s", label, r.exit, r.stderr)
					}
				}
				checkPartialState(t, label, store, dir, killGenB)
			}
			// Rerun with the same flags until it completes. A kill by request
			// count lands after kp.atReq shard requests; the rerun must pick
			// up from there, not start over -- the result would be the same
			// either way, so only the request count shows a resume that
			// quietly threw its work away.
			for attempt := 1; ; attempt++ {
				r := runSweep(t, bin, store, dir, nil)
				if attempt == 1 && len(chain) == 1 && chain[0].atReq > 1 {
					// What is durable when the run stops: everything for an
					// interrupt, which checkpoints on the way out; for a kill,
					// only what the last checkpoint (every 500 shards) wrote.
					// Plus the requests in flight (concurrency 4) and the two
					// retried shards.
					// The 10 is the slack between a request arriving and its
					// shard being processed (see the 530 above).
					kept := max(chain[0].atReq-10, 0)
					if chain[0].sig == syscall.SIGKILL {
						kept = kept / 500 * 500
					}
					if fetched, most := store.total.Load(), int64(shardsB)-kept+4+2; fetched > most {
						t.Errorf("the rerun fetched %d shards, more than the %d left: it did not resume", fetched, most)
					}
				}
				if r.exit == 0 {
					break
				}
				if attempt == 3 {
					t.Fatalf("still failing after %d reruns: exit %d\n%s", attempt, r.exit, r.stderr)
				}
			}
			got := readSweepResult(t, dir)
			if !slices.Equal(got.ids, want.ids) {
				t.Errorf("snapshot differs from the control: %d ids vs %d", len(got.ids), len(want.ids))
			}
			for _, id := range got.ids {
				if !killIDRe.MatchString(id) {
					t.Errorf("snapshot holds %q, not an id", id)
					break
				}
			}
			if got.manifest.IDs != want.manifest.IDs || got.manifest.SHA256 != want.manifest.SHA256 {
				t.Errorf("manifest %d ids %s, control %d ids %s",
					got.manifest.IDs, got.manifest.SHA256, want.manifest.IDs, want.manifest.SHA256)
			}
			if !bytes.Equal(got.delta, want.delta) {
				t.Errorf("delta differs from the control's")
			}
			for _, leftover := range []string{"state.json", "partial-" + killGenB.ID() + ".txt", "done-" + killGenB.ID() + ".log", lockName} {
				if _, err := os.Stat(filepath.Join(dir, leftover)); err == nil {
					t.Errorf("%s left behind after a completed sweep", leftover)
				}
			}
		})
	}
}
