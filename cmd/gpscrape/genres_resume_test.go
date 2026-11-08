package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	googleplayscraper "github.com/kryuchenko/google-play-scraper"
)

// The resume tests walk a snapshot of resumeN ids over several packs, with a
// table that knows half of them and holds two rows the snapshot no longer
// lists. In us every tenth id is absent and every seventh changed genre; in
// the confirmation storefront every twentieth id is alive.
const resumeN = 3500

var resumeGen = googleplayscraper.Generation{Date: "2026-11-06", Run: "1793973725"}

func resumeID(i int) string { return fmt.Sprintf("com.app%05d", i) }

func resumeIndex(id string) int {
	var i int
	_, _ = fmt.Sscanf(id, "com.app%05d", &i)
	return i
}

// writeResumeFixture writes the snapshot, its manifest and the table into dir.
func writeResumeFixture(t *testing.T, dir string) {
	t.Helper()
	ids := make([]string, resumeN)
	for i := range ids {
		ids[i] = resumeID(i)
	}
	snap := filepath.Join(dir, "snapshot-"+resumeGen.ID()+".txt.gz")
	sum, err := writeSnapshot(snap, ids)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(dir, "manifest-"+resumeGen.ID()+".json"),
		manifest{Generation: resumeGen, File: filepath.Base(snap), IDs: len(ids), SHA256: sum}); err != nil {
		t.Fatal(err)
	}
	var known []string
	for i := 0; i < resumeN; i += 2 {
		known = append(known, resumeID(i))
	}
	known = append(known, "com.zz.old1", "com.zz.old2") // unmatched: kept, or pruned
	slices.Sort(known)
	if err := writeGenreLines(genresPath(dir), known, func(string) string { return "TOOLS" }); err != nil {
		t.Fatal(err)
	}
}

// resumeStore answers digest requests for the fixture. block, when set, is
// asked before each request and makes it hang until the client gives up --
// which is how a test interrupts a run at a chosen request.
type resumeStore struct {
	mu    sync.Mutex
	n     map[string]int // gl -> requests so far
	block func(gl string, n int) bool
}

func (s *resumeStore) install(t *testing.T, store *fakeStore) {
	store.setFunc(pathBatch, func(r *http.Request) (int, []byte) {
		gl := r.URL.Query().Get("gl")
		// A run stopped mid-request leaves a cut body; that is the client
		// leaving, not a fixture failure, so the request just goes unanswered.
		if r.Context().Err() != nil {
			return http.StatusServiceUnavailable, nil
		}
		ids := requestedIDsNoT(r)
		if len(ids) == 0 {
			return http.StatusServiceUnavailable, nil
		}
		s.mu.Lock()
		s.n[gl]++
		n, block := s.n[gl], s.block
		s.mu.Unlock()
		if block != nil && block(gl, n) {
			<-r.Context().Done()
			return http.StatusServiceUnavailable, nil
		}
		byIndex := map[string]string{}
		var order []string
		for i, id := range ids {
			idx := resumeIndex(id)
			var payload string
			switch {
			case gl == "us" && idx%10 == 0:
				payload = "" // absent here
			case gl == "us" && idx%7 == 0:
				payload = genrePayload("GAME_PUZZLE", "Puzzle")
			case gl == "us":
				payload = genrePayload("TOOLS", "Tools")
			case idx%20 == 0:
				payload = genrePayload("GAME_BOARD", "Board")
			}
			byIndex[fmt.Sprint(i)] = payload
			order = append(order, fmt.Sprint(i))
		}
		return http.StatusOK, framesEnvelope("Ws7gDc", byIndex, order)
	})
}

func (s *resumeStore) requests(gl string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n[gl]
}

func newResumeStore(t *testing.T) *resumeStore {
	t.Helper()
	store := newFakeStore(t)
	useFakeClient(t, store.transport())
	rs := &resumeStore{n: map[string]int{}}
	rs.install(t, store)
	return rs
}

func runResumeGenres(t *testing.T, dir string, extra ...string) (stdout, stderr string, err error) {
	t.Helper()
	args := append([]string{"-dir", dir, "-all", "-country", "us", "-confirm-gone", "de",
		"-concurrency", "1"}, extra...)
	return runVerb(t, catalogGenres, args...)
}

// reference runs the whole pass uninterrupted in a fresh directory and
// returns the table it publishes and the ids it reports.
func reference(t *testing.T, extra ...string) ([]byte, []string) {
	t.Helper()
	newResumeStore(t)
	dir := t.TempDir()
	writeResumeFixture(t, dir)
	out, _, err := runResumeGenres(t, dir, extra...)
	if err != nil {
		t.Fatalf("reference run: %v", err)
	}
	table, err := os.ReadFile(genresPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	return table, reportedIDs(t, out)
}

func reportedIDs(t *testing.T, out string) []string {
	t.Helper()
	var ids []string
	for id := range genreRecords(t, out) {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func smallCheckpoints(t *testing.T) {
	t.Helper()
	saved := genresCheckpointEvery
	genresCheckpointEvery = 300
	t.Cleanup(func() { genresCheckpointEvery = saved })
}

// Interrupted at several points -- mid-walk, and in the confirmation pass --
// then resumed, the pass publishes a table byte-for-byte the same as an
// uninterrupted run, every id is reported, and the resumed run does not ask
// again about ids the checkpoint holds.
func TestGenresResumeMatchesAnUninterruptedRun(t *testing.T) {
	smallCheckpoints(t)
	want, wantIDs := reference(t, "-prune")

	for _, c := range []struct {
		name  string
		block func(gl string, n int) bool
	}{
		{"after one pack", func(gl string, n int) bool { return gl == "us" && n == 2 }},
		{"after three packs", func(gl string, n int) bool { return gl == "us" && n == 4 }},
		{"in the confirmation pass", func(gl string, n int) bool { return gl == "de" }},
	} {
		t.Run(c.name, func(t *testing.T) {
			rs := newResumeStore(t)
			dir := t.TempDir()
			writeResumeFixture(t, dir)

			rs.block = c.block
			out1, _, err := runResumeGenres(t, dir, "-prune", "-timeout", "500ms")
			if err == nil {
				t.Fatal("the interrupted run succeeded")
			}
			if _, serr := os.Stat(genresResumePath(dir)); serr != nil {
				t.Fatalf("no resume state after the interruption: %v", serr)
			}
			usBefore := rs.requests("us")

			rs.block = nil
			out2, stderr, err := runResumeGenres(t, dir, "-prune")
			if err != nil {
				t.Fatalf("resumed run: %v\n%s", err, stderr)
			}
			if !strings.Contains(stderr, "resuming the genre pass") {
				t.Errorf("stderr does not say it resumed:\n%s", stderr)
			}
			got, err := os.ReadFile(genresPath(dir))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(want) {
				t.Errorf("resumed table differs from an uninterrupted run (%d vs %d bytes)", len(got), len(want))
			}
			ids := reportedIDs(t, out1+"\n"+out2)
			if !slices.Equal(ids, wantIDs) {
				t.Errorf("reported %d ids across the two runs, want %d", len(ids), len(wantIDs))
			}
			// 3500 ids are four packs. A resume asks only for what the
			// checkpoint does not hold: never all four again.
			if again := rs.requests("us") - usBefore; again >= 4 {
				t.Errorf("the resumed run sent %d us requests; it started over", again)
			}
			for _, leftover := range []string{genresResumePath(dir), genresPath(dir) + ".merge"} {
				if _, err := os.Stat(leftover); err == nil {
					t.Errorf("%s survived a published run", filepath.Base(leftover))
				}
			}
		})
	}
}

// A hard kill leaves rows past the checkpoint, the last one torn. The resume
// cuts the merge file back to the checkpoint and the result is unchanged.
func TestGenresResumeCutsBackPastTheCheckpoint(t *testing.T) {
	smallCheckpoints(t)
	want, _ := reference(t)

	rs := newResumeStore(t)
	dir := t.TempDir()
	writeResumeFixture(t, dir)
	rs.block = func(gl string, n int) bool { return gl == "us" && n == 3 }
	if _, _, err := runResumeGenres(t, dir, "-timeout", "500ms"); err == nil {
		t.Fatal("the interrupted run succeeded")
	}
	f, err := os.OpenFile(genresPath(dir)+".merge", os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("-\tcom.app03498\tBOGUS\nA\tcom.app034")
	_ = f.Close()

	rs.block = nil
	if _, stderr, err := runResumeGenres(t, dir); err != nil {
		t.Fatalf("resumed run: %v\n%s", err, stderr)
	}
	got, _ := os.ReadFile(genresPath(dir))
	if string(got) != string(want) {
		t.Error("rows written after the checkpoint leaked into the published table")
	}
}

// State that belongs to another walk -- a different table, snapshot or
// storefront -- is discarded, and the run starts clean and still gets the
// right answer.
func TestGenresResumeStartsCleanOnAnotherRun(t *testing.T) {
	smallCheckpoints(t)
	for _, c := range []struct {
		name   string
		change func(t *testing.T, dir string)
		extra  []string
	}{
		{"table rewritten", func(t *testing.T, dir string) {
			b, _ := os.ReadFile(genresPath(dir))
			if err := os.WriteFile(genresPath(dir), append(b, "com.zz.zz\tTOOLS\n"...), 0o644); err != nil {
				t.Fatal(err)
			}
		}, nil},
		{"new snapshot", func(t *testing.T, dir string) {
			m := filepath.Join(dir, "manifest-"+resumeGen.ID()+".json")
			b, _ := os.ReadFile(m)
			if err := os.WriteFile(m, []byte(strings.Replace(string(b), `"sha256": "`, `"sha256": "0`, 1)), 0o644); err != nil {
				t.Fatal(err)
			}
		}, nil},
		{"another country", func(*testing.T, string) {}, []string{"-country", "gb"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			rs := newResumeStore(t)
			dir := t.TempDir()
			writeResumeFixture(t, dir)
			rs.block = func(gl string, n int) bool { return gl == "us" && n == 2 }
			if _, _, err := runResumeGenres(t, dir, "-timeout", "500ms"); err == nil {
				t.Fatal("the interrupted run succeeded")
			}
			c.change(t, dir)
			rs.block = nil
			_, stderr, err := runResumeGenres(t, dir, c.extra...)
			if c.name == "another country" {
				// gb answers nothing in this fixture; what matters is that the
				// us state was not resumed under it.
				_ = err
			} else if err != nil {
				t.Fatalf("clean run: %v\n%s", err, stderr)
			}
			if strings.Contains(stderr, "resuming") {
				t.Errorf("resumed state from another run:\n%s", stderr)
			}
		})
	}
}

// An -ids run never resumes and clears what a snapshot run left: its merge
// file is about to be reused, and a subset is not the walk the state
// describes.
func TestGenresIDsRunClearsResumeState(t *testing.T) {
	smallCheckpoints(t)
	rs := newResumeStore(t)
	dir := t.TempDir()
	writeResumeFixture(t, dir)
	rs.block = func(gl string, n int) bool { return gl == "us" && n == 2 }
	if _, _, err := runResumeGenres(t, dir, "-timeout", "500ms"); err == nil {
		t.Fatal("the interrupted run succeeded")
	}
	ids := filepath.Join(dir, "ids.txt")
	if err := os.WriteFile(ids, []byte(resumeID(1)+"\n"+resumeID(3)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rs.block = nil
	_, stderr, err := runResumeGenres(t, dir, "-ids", ids)
	if err != nil {
		t.Fatalf("-ids run: %v\n%s", err, stderr)
	}
	if strings.Contains(stderr, "resuming") {
		t.Errorf("an -ids run resumed a snapshot walk:\n%s", stderr)
	}
	if _, err := os.Stat(genresResumePath(dir)); err == nil {
		t.Error("the snapshot run's resume state survived an -ids run")
	}
}

// A run in which every lookup failed leaves no state: a rerun should ask
// again, not resume a pass that learned nothing.
func TestGenresAllFailedLeavesNoResumeState(t *testing.T) {
	smallCheckpoints(t)
	store := newFakeStore(t)
	useFakeClient(t, store.transport())
	store.set(pathBatch, http.StatusInternalServerError, nil)
	dir := t.TempDir()
	writeResumeFixture(t, dir)
	if _, _, err := runResumeGenres(t, dir); err == nil {
		t.Fatal("a run where every request failed succeeded")
	}
	for _, leftover := range []string{genresResumePath(dir), genresPath(dir) + ".merge"} {
		if _, err := os.Stat(leftover); err == nil {
			t.Errorf("%s left behind by a run that resolved nothing", filepath.Base(leftover))
		}
	}
}

// Stopped at an arbitrary moment -- a deadline from 1ms to ~120ms lands
// mid-pack, mid-checkpoint, between packs -- the resumed run still publishes
// the uninterrupted table byte for byte. Packs in flight when the run stops
// come back with the context's error on every id; counting those as answers
// skipped them on resume and lost the rows of apps the table did not know.
func TestGenresResumeAfterAStopAtAnyMoment(t *testing.T) {
	smallCheckpoints(t)
	want, _ := reference(t, "-prune")
	for d := 1; d <= 121; d += 6 {
		t.Run(fmt.Sprintf("%dms", d), func(t *testing.T) {
			newResumeStore(t)
			dir := t.TempDir()
			writeResumeFixture(t, dir)
			_, _, first := runResumeGenres(t, dir, "-prune", "-timeout", (time.Duration(d) * time.Millisecond).String())
			_, stderr, err := runResumeGenres(t, dir, "-prune")
			if err != nil {
				t.Fatalf("resume: %v\n%s", err, stderr)
			}
			got, err := os.ReadFile(genresPath(dir))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(want) {
				t.Errorf("stopped with %v; resumed table %d bytes, want %d", first, len(got), len(want))
			}
		})
	}
}
