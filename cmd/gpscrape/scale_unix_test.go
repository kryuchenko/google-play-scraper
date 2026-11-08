//go:build unix

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	googleplayscraper "github.com/kryuchenko/google-play-scraper"
)

// TestCatalogPeakAtScale runs the catalog's local, after-the-network work at
// catalog size -- four million ids, synthetic, no requests -- and fails if
// its peak heap passes a ceiling:
//
//   - finish: what sweep does after the last shard (read the partial file,
//     sort, deduplicate, write the snapshot, diff against the previous one);
//   - diff: catalog diff over two snapshots;
//   - genres: the genre pass's walk of the table, with a tenth of the ids
//     coming back absent, and the table settled and published.
//
// Each runs in a child process, so its numbers are its own and not the
// fixture's. Opt-in, a few seconds and ~400MB of temp disk:
//
//	GPSCRAPE_SCALE=1 go test -run TestCatalogPeakAtScale -count=1 -v ./cmd/gpscrape
//
// GPSCRAPE_SCALE_N overrides the id count (default 4,000,000).
func TestCatalogPeakAtScale(t *testing.T) {
	if dir := os.Getenv("GPSCRAPE_SCALE_CHILD"); dir != "" {
		scaleChild(t, dir, os.Getenv("GPSCRAPE_SCALE_CASE"))
		return
	}
	if os.Getenv("GPSCRAPE_SCALE") == "" {
		t.Skip("set GPSCRAPE_SCALE=1 to run (catalog-sized, a few seconds, ~400MB disk)")
	}
	n := 4_000_000
	if v := os.Getenv("GPSCRAPE_SCALE_N"); v != "" {
		_, _ = fmt.Sscan(v, &n)
	}
	dir := t.TempDir()
	buildScaleFixture(t, dir, n)

	// Ceilings on peak HeapInuse, with room over what was measured at 4M ids
	// on an M-series laptop: finish 300-415MB (most of it the sort's one copy
	// of every id), diff and genres a few tens of MB once they stopped
	// holding the lists.
	for _, c := range []struct {
		name    string
		ceiling float64 // MB
	}{{"finish", 600}, {"diff", 64}, {"genres", 64}} {
		t.Run(c.name, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestCatalogPeakAtScale$", "-test.v", "-test.count=1")
			cmd.Env = append(os.Environ(), "GPSCRAPE_SCALE_CHILD="+dir, "GPSCRAPE_SCALE_CASE="+c.name)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			var peak float64
			for _, line := range splitLines(string(out)) {
				if _, err := fmt.Sscanf(line, "    scale_unix_test.go:%d: peak=%fMB", new(int), &peak); err == nil {
					break
				}
			}
			t.Logf("%s", out)
			if peak == 0 {
				t.Fatal("the child reported no peak")
			}
			if peak > c.ceiling {
				t.Errorf("%s peak HeapInuse %.0fMB, ceiling %.0fMB", c.name, peak, c.ceiling)
			}
		})
	}
}

func splitLines(s string) []string {
	var out []string
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out
}

var (
	scalePrevGen = googleplayscraper.Generation{Date: "2026-11-01", Run: "1793000000", Shards: 87619}
	scaleGen     = googleplayscraper.Generation{Date: "2026-11-06", Run: "1793973725", Shards: 87619}
)

// buildScaleFixture writes, into dir: the partial file finish reads (n ids
// plus one interrupted checkpoint's worth of repeats), the previous snapshot
// with 0.5% churn each way, the new snapshot, and a genre table for the
// previous snapshot.
func buildScaleFixture(t *testing.T, dir string, n int) {
	t.Helper()
	// Ids shaped like the real ones: a reversed-domain prefix and a mean
	// length of ~24 bytes (24.3 measured on a 0.1% sample). Random letters
	// compress worse than real names, so the snapshot buffers are, if
	// anything, larger than production's.
	rng := rand.New(rand.NewPCG(1, 2))
	prefixes := []string{"com.", "com.", "com.", "net.", "org.", "io.", "app.", "br.com.", "jp.co."}
	id := func() string {
		b := []byte(prefixes[rng.IntN(len(prefixes))])
		for k := 6 + rng.IntN(28); k > 0; k-- { // 6..33 random bytes
			if k%9 == 0 {
				b = append(b, '.')
			} else {
				b = append(b, byte('a'+rng.IntN(26)))
			}
		}
		return string(b)
	}

	ids := make([]string, n)
	for i := range ids {
		ids[i] = id()
	}
	partial := filepath.Join(dir, "partial-"+scaleGen.ID()+".txt")
	f, err := os.Create(partial)
	if err != nil {
		t.Fatal(err)
	}
	w := bufio.NewWriterSize(f, 1<<20)
	for _, s := range ids {
		_, _ = fmt.Fprintln(w, s)
	}
	for _, s := range ids[:min(len(ids), 500*46)] { // one interrupted checkpoint's worth re-fetched
		_, _ = fmt.Fprintln(w, s)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	prev := slices.Clone(ids)
	for i := 0; i < len(prev); i += 200 { // 0.5% churn each way
		prev[i] = id() + "x"
	}
	slices.Sort(prev)
	prev = slices.Compact(prev)
	prevFile := "snapshot-" + scalePrevGen.ID() + ".txt.gz"
	if _, err := writeSnapshot(filepath.Join(dir, prevFile), prev); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(dir, "prev.json"), manifest{Generation: scalePrevGen, File: prevFile, IDs: len(prev)}); err != nil {
		t.Fatal(err)
	}
	genres := []string{"GAME_PUZZLE", "GAME_CASUAL", "TOOLS", "EDUCATION", "SHOPPING", "SOCIAL"}
	if err := writeGenreLines(genresPath(dir), prev, func(s string) string { return genres[len(s)%len(genres)] }); err != nil {
		t.Fatal(err)
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if _, err := writeSnapshot(filepath.Join(dir, "snapshot-"+scaleGen.ID()+".txt.gz"), ids); err != nil {
		t.Fatal(err)
	}
}

func scaleChild(t *testing.T, dir, which string) {
	var peak atomic.Uint64
	stop := make(chan struct{})
	sampled := make(chan struct{})
	go func() {
		defer close(sampled)
		var ms runtime.MemStats
		tk := time.NewTicker(2 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tk.C:
				runtime.ReadMemStats(&ms)
				if ms.HeapInuse > peak.Load() {
					peak.Store(ms.HeapInuse)
				}
			}
		}
	}()
	start := time.Now()
	var err error
	switch which {
	case "finish":
		var pm manifest
		b, rerr := os.ReadFile(filepath.Join(dir, "prev.json"))
		if rerr != nil {
			t.Fatal(rerr)
		}
		if err := json.Unmarshal(b, &pm); err != nil {
			t.Fatal(err)
		}
		out := t.TempDir()
		// finish consumes the partial file; work on a copy so the other cases
		// still find the fixture.
		partial := filepath.Join(out, "partial-"+scaleGen.ID()+".txt")
		if err := copyFile(filepath.Join(dir, "partial-"+scaleGen.ID()+".txt"), partial); err != nil {
			t.Fatal(err)
		}
		if err := copyFile(filepath.Join(dir, pm.File), filepath.Join(out, pm.File)); err != nil {
			t.Fatal(err)
		}
		err = finish(out, scaleGen, partial, syncState{Generation: scaleGen}, pm, true, start, sampling{}, 0)
	case "diff":
		_, err = diffSnapshots(scalePrevGen, scaleGen,
			filepath.Join(dir, "snapshot-"+scalePrevGen.ID()+".txt.gz"),
			filepath.Join(dir, "snapshot-"+scaleGen.ID()+".txt.gz"))
	case "genres":
		err = scaleGenresMerge(dir)
	default:
		t.Fatalf("unknown case %q", which)
	}
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	close(stop)
	<-sampled
	// One last sample: a short run can finish between two ticks.
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	if ms.HeapInuse > peak.Load() {
		peak.Store(ms.HeapInuse)
	}

	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	rss := float64(ru.Maxrss) // bytes on darwin, kilobytes on linux
	if runtime.GOOS != "darwin" {
		rss *= 1024
	}
	t.Logf("peak=%.1fMB HeapInuse, maxrss %.0fMB, %s, %s", float64(peak.Load())/1e6, rss/1e6, which, elapsed.Round(time.Millisecond))
}

// scaleGenresMerge is the genre pass without the network: the new snapshot's
// ids walked against the table, every tenth one absent, the absent ones
// settled a third each alive, unconfirmed and gone.
func scaleGenresMerge(dir string) error {
	path := genresPath(dir)
	prev, err := openTable(path)
	if err != nil {
		return err
	}
	defer prev.close()
	m, err := newTableMerge(prev, path+".merge")
	if err != nil {
		return err
	}
	defer m.abandon()
	lines, closeSnap, err := openSnapshotLines(filepath.Join(dir, "snapshot-"+scaleGen.ID()+".txt.gz"))
	if err != nil {
		return err
	}
	defer closeSnap()
	var absent []string
	for i := 0; ; i++ {
		b, ok := lines.next()
		if !ok {
			break
		}
		id := string(b)
		was, known, err := m.lookup(id)
		if err != nil {
			return err
		}
		if i%10 == 0 {
			absent = append(absent, id)
			mark := markAbsentFirst
			if known {
				mark = markAbsent
			}
			err = m.row(id, was, mark)
		} else {
			err = m.row(id, "TOOLS", "")
		}
		if err != nil {
			return err
		}
	}
	if err := lines.sc.Err(); err != nil {
		return err
	}
	if err := m.finish(); err != nil {
		return err
	}
	_, err = settleTable(m.path, path, absent, func(i int) (settled, string) {
		return settled(i % 3), "GAME_BOARD"
	}, true)
	return err
}

func copyFile(from, to string) error {
	b, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	return os.WriteFile(to, b, 0o644)
}
