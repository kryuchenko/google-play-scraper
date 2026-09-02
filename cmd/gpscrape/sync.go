package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	googleplayscraper "github.com/kryuchenko/google-play-scraper"
)

// The catalog is not an operation, it is a dataset.
//
// A full sweep is ~83k requests and around twenty gigabytes of transfer, which
// makes it a scheduled job rather than something a caller invokes. Two facts
// about Google's sitemaps shape everything here:
//
// Shard filenames carry the id of the generation that produced them, e.g.
// play_sitemaps_2026-08-23_1787500934-00000-of-83445.xml.gz. Every shard in a
// generation shares that id, so noticing a new one costs three requests
// instead of eighty-three thousand. Google does not regenerate daily, so most
// checks find nothing to do and should cost nothing.
//
// Within one generation shards are immutable -- a new generation produces
// entirely new filenames. That is what makes a sweep resumable: a run
// interrupted at shard 60,000 continues from there rather than starting over,
// and a 404 mid-sweep means the generation rolled and the run must restart.
//
// Resuming deliberately does not go through CatalogSeq. Its Shards option
// takes indices into a freshly fetched shard list, and after a generation
// rolls the same index points at a different file -- the sweep would carry on
// silently against the wrong work-list. State records the shard URLs
// themselves.

// The generation type, its parsing and its ordering live in the library now.
// They are knowledge about Google's URL format, not about how snapshots are
// kept here -- and the consumer service that reads these directories needs the
// same parsing without needing this tool's opinions about directories.
//
// What stays below is exactly those opinions: the done log, the manifest, the
// file layout, the resume state.

// syncState is what makes a sweep resumable. It is rewritten periodically
// rather than on every shard: at 83k shards the write amplification would
// dominate, and losing a few hundred shards' progress to a crash is cheap next
// to losing the run.
type syncState struct {
	Generation googleplayscraper.Generation `json:"generation"`
	// Failed records shards that could not be fetched, so the retry pass at
	// the end of a sweep knows which ones to repeat. It stays in the state
	// file because it is small -- a sweep with tens of thousands of failures
	// has bigger problems than its bookkeeping.
	Failed []string `json:"failed,omitempty"`
	IDs    int      `json:"ids"`

	// SamplePct and SampleSeed record which shards the interrupted run was
	// working through, and they are part of what makes resuming safe.
	//
	// The state used to be keyed to the generation alone, which is not enough:
	// a shard list is a function of the generation AND the sampling. Resuming
	// a 0.001% run onto a full one, or the reverse, mixes two different work
	// lists. It produced a panic -- the pending slice was sized
	// len(shards)-len(finished), and a done log from a bigger run makes that
	// negative -- and, when it did not panic, a manifest claiming a coverage
	// its snapshot did not have, which is the worse of the two.
	SamplePct  float64 `json:"samplePct,omitempty"`
	SampleSeed int64   `json:"sampleSeed,omitempty"`
}

// Which shards are finished is kept in an append-only log beside the state
// rather than as a list inside it.
//
// The list version rewrote every remaining URL on each checkpoint: at 83,445
// shards and a checkpoint every 500, that is about 650MB written to produce a
// 30MB snapshot, and each completed shard also cost a linear search and a
// slice delete under the lock the workers share. One line appended per shard
// is ~9MB over the whole sweep, O(1) per shard, and needs no lock beyond the
// writer's own.
//
// Ordering matters between the two files. The ids are flushed before the done
// log, so a crash in between re-fetches a shard whose ids were already
// written; the sweep deduplicates at the end, so a duplicate costs nothing.
// The other order would lose ids permanently.
func doneLogPath(dir string, gen googleplayscraper.Generation) string {
	return filepath.Join(dir, "done-"+gen.ID()+".log")
}

// readDoneLog returns the shard URLs a previous run finished. A truncated
// final line -- the ordinary result of a kill -- is discarded rather than
// treated as a completed shard.
func readDoneLog(path string) (map[string]struct{}, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]struct{}{}, nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()

	done := make(map[string]struct{}, 1<<16)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			done[line] = struct{}{}
		}
	}
	return done, sc.Err()
}

// manifest describes a finished snapshot.
type manifest struct {
	Generation  googleplayscraper.Generation `json:"generation"`
	CompletedAt string                       `json:"completedAt"`
	IDs         int                          `json:"ids"`
	FailedShard int                          `json:"failedShards,omitempty"`
	File        string                       `json:"file"`
	SHA256      string                       `json:"sha256"`

	// SamplePct and SampleSeed are set when the sweep covered only part of the
	// catalog.
	//
	// They are recorded because a partial snapshot looks exactly like a
	// complete one, and diffing a 1% sample against a full snapshot reports
	// 99% of the store as removed. The seed makes a sampled run reproducible,
	// which is what turns a sample into a measurement rather than an anecdote.
	SamplePct  float64 `json:"samplePct,omitempty"`
	SampleSeed int64   `json:"sampleSeed,omitempty"`
}

// complete reports whether the manifest describes a sweep of the whole catalog
// rather than a sample of it.
//
// Every question of the form "do we already have this generation?" has to ask
// this too, and for a while only one of the four places that asked did. A
// sampled snapshot is a measurement, not a copy: treating it as one means the
// full sweep of that generation never runs, and nothing says so, because from
// the outside a 0.001% snapshot and a complete one are the same sorted list of
// ids under the same name.
func (m manifest) complete() bool { return m.SamplePct == 0 }

// sampleShards picks a deterministic subset of shard indices.
//
// A uniform subset is an unbiased sample of the catalog because the shards are
// hash-partitioned: any shard has the same mix of apps, books and films, and
// the same spread of release dates, as any other -- verified by comparing the
// first and last. So a 1% sweep estimates catalog size, the share of games,
// the share of ids that no longer resolve, and how fast the store grows, for
// 834 requests instead of 83,445.
//
// Seeded rather than random so the same seed sweeps the same shards. An
// unreproducible sample cannot be compared with anything, including itself.
func sampleShards(total int, pct float64, seed int64) []int {
	if pct <= 0 || pct >= 100 {
		return nil
	}
	want := int(float64(total) * pct / 100)
	if want < 1 {
		want = 1
	}
	rng := rand.New(rand.NewPCG(uint64(seed), 0x9E3779B97F4A7C15))
	perm := rng.Perm(total)[:want]
	sort.Ints(perm)
	return perm
}

// delta is what periodic sweeping is actually for. A full snapshot is tens of
// megabytes; the answer to "what appeared in the store this week" is a few
// hundred kilobytes, and nothing else publishes it.
type delta struct {
	From    googleplayscraper.Generation `json:"from"`
	To      googleplayscraper.Generation `json:"to"`
	Added   []string                     `json:"added"`
	Removed []string                     `json:"removed"`
}

func cmdSync(args []string) error {
	c := newCommon("sync")
	dir := c.fs.String("dir", "catalog", "directory for snapshots, deltas and resume state")
	check := c.fs.Bool("check", false, "report whether a new generation exists and exit (3 requests)")
	force := c.fs.Bool("force", false, "sweep even if the current generation is already snapshotted")
	sample := c.fs.Float64("sample", 0, "sweep only this percent of shards (0 = all)")
	seed := c.fs.Int64("seed", 0, "seed for -sample (0 derives one from the generation)")
	if err := c.parse(args); err != nil {
		return err
	}
	if err := c.noArgs("catalog sweep"); err != nil {
		return err
	}
	// A fraction outside (0,100] is not a smaller sweep, it is a different
	// command by accident: -sample 100 produced an empty shard list and wrote
	// a manifest saying the catalog held nothing, and -sample -5 was ignored
	// and swept all 83,445 shards. Both exited 0.
	if *sample != 0 && (*sample < 0 || *sample > 100 || math.IsNaN(*sample)) {
		return fmt.Errorf("-sample %v: a share of the catalog is above 0 and at most 100", *sample)
	}
	// 100% is the whole catalog, which is what no sampling means. Left as a
	// sample it produced an empty shard list -- sampleShards declines to
	// sample everything -- and the run wrote a manifest saying the store held
	// nothing.
	if *sample == 100 {
		*sample = 0
	}

	ctx, stop := c.context()
	defer stop()

	client := c.client()

	shards, err := client.AllSitemapShards(ctx)
	if err != nil {
		return fmt.Errorf("list shards: %w", err)
	}
	if len(shards) == 0 {
		return fmt.Errorf("no sitemap shards advertised: robots.txt lists indexes, but they are empty")
	}
	// Every shard must agree on the generation. Parsing only the first would
	// accept a list read while Google was republishing -- half one build and
	// half the next -- and sweeping that produces a catalog that existed at no
	// moment.
	gen, err := googleplayscraper.GenerationOf(shards)
	if err != nil {
		return err
	}
	if gen.Shards != 0 && gen.Shards != len(shards) {
		fmt.Fprintf(os.Stderr, "warning: filenames say %d shards, the indexes list %d\n",
			gen.Shards, len(shards))
	}

	// latestManifest globs, so a missing directory simply reports "nothing
	// here" -- which is why the directory is created only once a sweep is
	// actually going to happen. -check is a query and used to create the
	// snapshot directory as a side effect of asking a question.
	prev, havePrev := latestManifest(*dir)
	// A sample of this generation is not this generation: the sweep still has
	// to happen, so this must not report the work as done.
	upToDate := havePrev && prev.Generation.ID() == gen.ID() && prev.complete()

	if *check {
		// A record rather than a sentence: the answer to -check is the whole
		// point of running it, and everything else this tool writes to stdout
		// is newline-delimited JSON.
		rec := struct {
			Generation string  `json:"generation"`
			Have       string  `json:"have,omitempty"`
			HaveSample float64 `json:"haveSamplePct,omitempty"`
			UpToDate   bool    `json:"upToDate"`
		}{Generation: gen.ID(), UpToDate: upToDate}
		if havePrev {
			// ID() rather than String(): `catalog check` reports the same
			// field and a consumer that stores one and compares it with the
			// other would never match.
			rec.Have = prev.Generation.ID()
			rec.HaveSample = prev.SamplePct
		}
		return emitOne(rec)
	}

	if err := os.MkdirAll(*dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", *dir, err)
	}

	if upToDate && !*force {
		fmt.Fprintf(os.Stderr, "already have %s; nothing to do (use -force to sweep anyway)\n", gen)
		return nil
	}
	if havePrev && prev.Generation.ID() == gen.ID() && !prev.complete() {
		fmt.Fprintf(os.Stderr, "the snapshot for %s covers %g%% of the catalog; sweeping it in full\n",
			gen.ID(), prev.SamplePct)
	}

	// A sampled sweep is a measurement, not a catalog. The shards are
	// hash-partitioned, so a uniform subset is an unbiased estimate of the
	// whole -- and 1% of them answers "how big is it, what share are games,
	// how many ids are already dead, how fast is it growing" for 834 requests
	// instead of 83,445.
	samp := sampling{Pct: *sample}
	if samp.Pct > 0 {
		samp.Seed = *seed
		if samp.Seed == 0 {
			// Derived from the generation so a re-run of the same build picks
			// the same shards without the caller having to remember a number,
			// while a new build gets a fresh sample.
			samp.Seed = seedFromGeneration(gen)
		}
		picked := sampleShards(len(shards), samp.Pct, samp.Seed)
		sampled := make([]string, 0, len(picked))
		for _, i := range picked {
			sampled = append(sampled, shards[i])
		}
		fmt.Fprintf(os.Stderr, "sampling %.2f%% of %d shards: %d shards, seed %d\n",
			samp.Pct, len(shards), len(sampled), samp.Seed)
		shards = sampled
	}

	return sweep(ctx, client, *dir, gen, shards, prev, havePrev, c.concurrency, samp)
}

// sampling describes a partial sweep, or is zero for a complete one.
type sampling struct {
	Pct  float64
	Seed int64
}

func sweep(
	ctx context.Context,
	client *googleplayscraper.Client,
	dir string,
	gen googleplayscraper.Generation,
	shards []string,
	prev manifest,
	havePrev bool,
	concurrency int,
	samp sampling,
) error {
	statePath := filepath.Join(dir, "state.json")
	partialPath := filepath.Join(dir, "partial-"+gen.ID()+".txt")

	donePath := doneLogPath(dir, gen)

	state, resumed := loadState(statePath, gen, samp)
	if !resumed {
		state = syncState{Generation: gen, SamplePct: samp.Pct, SampleSeed: samp.Seed}
		// Files from an older generation are not a base to append to.
		_ = os.Remove(partialPath)
		_ = os.Remove(donePath)
	}

	finished, err := readDoneLog(donePath)
	if err != nil {
		return fmt.Errorf("read %s: %w", donePath, err)
	}
	// max rather than the bare difference: the two are only guaranteed to be
	// consistent because loadState now refuses a mismatched sample, and a
	// capacity hint is not the place to find out that they are not.
	pending := make([]string, 0, max(0, len(shards)-len(finished)))
	for _, u := range shards {
		if _, ok := finished[u]; !ok {
			pending = append(pending, u)
		}
	}

	partial, err := os.OpenFile(partialPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", partialPath, err)
	}
	w := bufio.NewWriterSize(partial, 1<<20)

	doneLog, err := os.OpenFile(donePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", donePath, err)
	}
	dw := bufio.NewWriterSize(doneLog, 1<<16)

	// checkpoint makes progress durable in the only order that cannot lose
	// work: the ids first, then the record saying the shard is finished. A
	// crash in between re-fetches a shard whose ids are already on disk, and
	// the sweep deduplicates at the end, so the cost is one wasted request.
	// The other order would mark a shard done whose ids never reached the
	// file, and nothing downstream could tell.
	checkpoint := func() error {
		if err := w.Flush(); err != nil {
			return fmt.Errorf("flush %s: %w", partialPath, err)
		}
		if err := partial.Sync(); err != nil {
			return fmt.Errorf("sync %s: %w", partialPath, err)
		}
		if err := dw.Flush(); err != nil {
			return fmt.Errorf("flush %s: %w", donePath, err)
		}
		if err := doneLog.Sync(); err != nil {
			return fmt.Errorf("sync %s: %w", donePath, err)
		}
		return saveState(statePath, state)
	}

	started := time.Now()
	if resumed {
		fmt.Fprintf(os.Stderr, "resuming %s: %d of %d shards left\n",
			gen, len(pending), len(shards))
	} else {
		fmt.Fprintf(os.Stderr, "sweeping %s: %d shards\n", gen, len(shards))
	}

	var (
		mu     sync.Mutex
		done   int
		rolled bool
	)

	// Each shard is fetched directly rather than through CatalogSeq: this loop
	// needs to know which URL produced which ids so an interrupted run can
	// resume from the URLs it has not reached.
	sweepErr := parallelShards(ctx, pending, concurrency, func(ctx context.Context, url string) {
		pkgs, err := client.SitemapShardPackages(ctx, url)

		mu.Lock()
		defer mu.Unlock()

		if err != nil {
			if ctx.Err() != nil {
				return // interrupted; the shard is simply not marked done
			}
			if isGone(err) {
				rolled = true
			}
			state.Failed = append(state.Failed, url)
			fmt.Fprintf(os.Stderr, "shard failed: %v\n", err)
			return
		}

		for _, pkg := range pkgs {
			if _, werr := fmt.Fprintln(w, pkg); werr != nil {
				fmt.Fprintf(os.Stderr, "write: %v\n", werr)
				return
			}
		}
		state.IDs += len(pkgs)
		if _, werr := fmt.Fprintln(dw, url); werr != nil {
			fmt.Fprintf(os.Stderr, "write done log: %v\n", werr)
			return
		}

		done++
		if done%500 == 0 {
			if cerr := checkpoint(); cerr != nil {
				fmt.Fprintf(os.Stderr, "checkpoint: %v\n", cerr)
			}
			fmt.Fprintf(os.Stderr, "%d/%d shards, %d ids, %s elapsed\n",
				len(shards)-len(pending)+done, len(shards), state.IDs,
				time.Since(started).Round(time.Second))
		}
	})

	if cerr := checkpoint(); cerr != nil {
		return cerr
	}
	if err := partial.Close(); err != nil {
		return fmt.Errorf("close %s: %w", partialPath, err)
	}
	if err := doneLog.Close(); err != nil {
		return fmt.Errorf("close %s: %w", donePath, err)
	}

	// Shards that failed get one more pass before the sweep is called done.
	// Without it a single transient 503 silently costs ~44 app ids, and those
	// ids surface as a phantom pair in the deltas: removed next generation,
	// added the one after. The delta is the product here, so quietly wrong
	// deltas are worse than a sweep that admits it is incomplete.
	if len(state.Failed) > 0 && ctx.Err() == nil && !rolled {
		retryURLs := state.Failed
		state.Failed = nil
		fmt.Fprintf(os.Stderr, "retrying %d failed shards\n", len(retryURLs))

		partial, err = os.OpenFile(partialPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return fmt.Errorf("reopen %s: %w", partialPath, err)
		}
		w = bufio.NewWriterSize(partial, 1<<20)
		doneLog, err = os.OpenFile(donePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return fmt.Errorf("reopen %s: %w", donePath, err)
		}
		dw = bufio.NewWriterSize(doneLog, 1<<16)

		retryErr := parallelShards(ctx, retryURLs, concurrency, func(ctx context.Context, url string) {
			pkgs, ferr := client.SitemapShardPackages(ctx, url)

			mu.Lock()
			defer mu.Unlock()

			if ferr != nil {
				if ctx.Err() != nil {
					return
				}
				if isGone(ferr) {
					rolled = true
				}
				state.Failed = append(state.Failed, url)
				return
			}
			for _, pkg := range pkgs {
				if _, werr := fmt.Fprintln(w, pkg); werr != nil {
					fmt.Fprintf(os.Stderr, "write: %v\n", werr)
					return
				}
			}
			state.IDs += len(pkgs)
			if _, werr := fmt.Fprintln(dw, url); werr != nil {
				fmt.Fprintf(os.Stderr, "write done log: %v\n", werr)
				return
			}
			done++
		})

		if cerr := checkpoint(); cerr != nil {
			return cerr
		}
		if err := partial.Close(); err != nil {
			return fmt.Errorf("close %s: %w", partialPath, err)
		}
		if err := doneLog.Close(); err != nil {
			return fmt.Errorf("close %s: %w", donePath, err)
		}
		if retryErr != nil {
			return fmt.Errorf("%w (progress kept in %s)", retryErr, donePath)
		}
	}

	remaining := len(pending) - done

	if len(state.Failed) > 0 && !rolled {
		// Refusing to write a manifest is deliberate. A snapshot that is
		// quietly missing shards looks exactly like a catalog that shrank, and
		// nothing downstream can tell the difference.
		return fmt.Errorf("%d shards could not be fetched after a retry pass; "+
			"the snapshot would be incomplete. Rerun to try them again "+
			"(progress is kept in %s)", len(state.Failed), donePath)
	}

	if rolled {
		return fmt.Errorf("the sitemap generation rolled mid-sweep (a shard is gone): "+
			"rerun to start on the new generation; %d shards of %s were collected",
			done, gen)
	}
	if sweepErr != nil {
		return fmt.Errorf("%w (progress kept: %d shards left)", sweepErr, remaining)
	}
	if remaining > 0 {
		return fmt.Errorf("%d shards still pending; rerun to continue", remaining)
	}

	return finish(dir, gen, partialPath, state, prev, havePrev, started, samp)
}

// finish turns the append-ordered partial file into a sorted, deduplicated
// snapshot, records a manifest, and diffs against the previous snapshot.
func finish(
	dir string,
	gen googleplayscraper.Generation,
	partialPath string,
	state syncState,
	prev manifest,
	havePrev bool,
	started time.Time,
	samp sampling,
) error {
	ids, err := readLines(partialPath)
	if err != nil {
		return err
	}
	// A sweep that collected nothing must not publish a manifest. The same
	// reasoning already refuses one when shards merely failed -- a snapshot
	// quietly missing shards looks exactly like a catalog that shrank -- and
	// zero shards is that hazard at its limit: `-sample 100` wrote a manifest
	// saying the store held no apps, with exit 0.
	if len(ids) == 0 {
		return fmt.Errorf("the sweep collected no ids, so no snapshot was written; "+
			"this is not an empty catalog, it is a run that did no work (%d shards attempted)",
			state.IDs)
	}
	ids = sortStrings(ids)
	ids = slices.Compact(ids)

	snapPath := filepath.Join(dir, "snapshot-"+gen.ID()+".txt.gz")
	sum, err := writeSnapshot(snapPath, ids)
	if err != nil {
		return err
	}

	m := manifest{
		Generation:  gen,
		CompletedAt: time.Now().UTC().Format(time.RFC3339),
		IDs:         len(ids),
		FailedShard: len(state.Failed),
		File:        filepath.Base(snapPath),
		SHA256:      sum,
		SamplePct:   samp.Pct,
		SampleSeed:  samp.Seed,
	}
	if err := writeJSON(filepath.Join(dir, "manifest-"+gen.ID()+".json"), m); err != nil {
		return err
	}

	// A -force rerun of the generation already on disk would diff it against
	// itself: an empty delta claiming a week of no change that never happened.
	if havePrev && prev.Generation.ID() != gen.ID() {
		// Streamed rather than loaded: holding both snapshots costs another
		// ~160MB at catalog scale, and the merge only ever needs one id from
		// each side at a time.
		d, rerr := diffAgainstSnapshot(filepath.Join(dir, prev.File), prev.Generation, gen, ids)
		if rerr != nil {
			fmt.Fprintf(os.Stderr, "warning: cannot diff against %s: %v\n", prev.File, rerr)
		} else {
			name := fmt.Sprintf("delta-%s-to-%s.json", prev.Generation.ID(), gen.ID())
			if err := writeJSON(filepath.Join(dir, name), d); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "delta: +%d -%d\n", len(d.Added), len(d.Removed))
		}
	}

	// A finished sweep leaves nothing to resume from.
	_ = os.Remove(partialPath)
	_ = os.Remove(doneLogPath(dir, gen))
	_ = os.Remove(filepath.Join(dir, "state.json"))

	fmt.Fprintf(os.Stderr, "%d ids in %s -> %s\n",
		len(ids), time.Since(started).Round(time.Second), snapPath)
	return nil
}

// diffAgainstSnapshot merges a sorted id list against a snapshot read from
// disk, without materialising the snapshot. Both sides are sorted, so one
// scanner and one index are enough.
func diffAgainstSnapshot(path string, from, to googleplayscraper.Generation, newIDs []string) (delta, error) {
	f, err := os.Open(path)
	if err != nil {
		return delta{}, err
	}
	defer func() { _ = f.Close() }()

	var r io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		zr, zerr := gzip.NewReader(f)
		if zerr != nil {
			return delta{}, fmt.Errorf("%s: %w", path, zerr)
		}
		defer func() { _ = zr.Close() }()
		r = zr
	}

	d := delta{From: from, To: to, Added: []string{}, Removed: []string{}}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)

	j := 0
	for sc.Scan() {
		old := strings.TrimSpace(sc.Text())
		if old == "" {
			continue
		}
		for j < len(newIDs) && newIDs[j] < old {
			d.Added = append(d.Added, newIDs[j])
			j++
		}
		if j < len(newIDs) && newIDs[j] == old {
			j++
			continue
		}
		d.Removed = append(d.Removed, old)
	}
	if err := sc.Err(); err != nil {
		return delta{}, err
	}
	d.Added = append(d.Added, newIDs[j:]...)
	return d, nil
}

// diff walks two sorted lists once. At a few million ids on each side this is
// the whole reason snapshots are kept sorted.
func diff(from, to googleplayscraper.Generation, oldIDs, newIDs []string) delta {
	d := delta{From: from, To: to, Added: []string{}, Removed: []string{}}
	i, j := 0, 0
	for i < len(oldIDs) && j < len(newIDs) {
		switch {
		case oldIDs[i] == newIDs[j]:
			i++
			j++
		case oldIDs[i] < newIDs[j]:
			d.Removed = append(d.Removed, oldIDs[i])
			i++
		default:
			d.Added = append(d.Added, newIDs[j])
			j++
		}
	}
	d.Removed = append(d.Removed, oldIDs[i:]...)
	d.Added = append(d.Added, newIDs[j:]...)
	return d
}

// ---- plumbing ----

// parallelShards runs fn over urls with the given concurrency, stopping
// dispatch when ctx is cancelled. The library's own pool takes an index-based
// callback; this one hands over the URL, which is what the resume state is
// keyed on.
func parallelShards(ctx context.Context, urls []string, workers int, fn func(context.Context, string)) error {
	if workers < 1 {
		workers = 1
	}
	if workers > len(urls) {
		workers = len(urls)
	}
	if len(urls) == 0 {
		return nil
	}

	ch := make(chan string)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for u := range ch {
				if ctx.Err() != nil {
					continue // drain without working, so the sender never blocks
				}
				fn(ctx, u)
			}
		}()
	}
	for _, u := range urls {
		if ctx.Err() != nil {
			break
		}
		ch <- u
	}
	close(ch)
	wg.Wait()
	return ctx.Err()
}

func isGone(err error) bool {
	var se *googleplayscraper.StatusError
	return errors.As(err, &se) && se.Code == 404
}

func loadState(path string, gen googleplayscraper.Generation, samp sampling) (syncState, bool) {
	f, err := os.Open(path)
	if err != nil {
		return syncState{}, false
	}
	defer func() { _ = f.Close() }()

	var s syncState
	if json.NewDecoder(f).Decode(&s) != nil {
		return syncState{}, false
	}
	// State from a previous generation describes shard URLs that no longer
	// exist. Resuming onto it would sweep files Google has already replaced.
	if s.Generation.ID() != gen.ID() {
		return syncState{}, false
	}
	// Nor is state from a different sample: the shard list is a function of
	// the generation and the sampling together, and appending one run's
	// results to another's produces a snapshot whose coverage nothing on disk
	// describes. Refusing here is what makes the caller delete the partial
	// files and start clean, which it already knows how to do.
	if s.SamplePct != samp.Pct || s.SampleSeed != samp.Seed {
		return syncState{}, false
	}
	return s, true
}

func saveState(path string, s syncState) error {
	return writeJSON(path, s)
}

// writeJSON replaces a file atomically and durably.
//
// Write to a temporary file, make its contents durable, rename over the
// target, then make the rename itself durable. Each step matters and the last
// is the one usually left out: after a rename the directory entry lives in the
// page cache like anything else, so a power loss can leave the old name intact
// with the new contents unreachable. The write modes measured for checkpointing
// on APFS separate exactly these three levels, and only the one that syncs the
// directory survives.
//
// Skipping the temporary file entirely would be worse than any of this: a
// crash partway through an in-place write leaves a file that parses as garbage,
// and what it would discard here is an hours-long sweep.
func writeJSON(path string, v any) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmp, err)
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename %s: %w", tmp, err)
	}
	return syncDir(filepath.Dir(path))
}

// syncDir makes a directory entry durable, which is what a rename actually
// changes. A failure here is reported rather than ignored: it means the file
// is present but its name may not survive a power loss, and a caller that
// believes a checkpoint landed when it did not is worse off than one told the
// truth.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open %s: %w", dir, err)
	}
	defer func() { _ = d.Close() }()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", dir, err)
	}
	return nil
}

// writeSnapshot compresses the ids and returns the sha256 of the file.
//
// The compression is split across cores. A gzip file is a sequence of
// independent members and a decompressor must read them as one continuous
// stream (RFC 1952; stdlib's Reader does this by default), so blocks can be
// compressed separately and concatenated. Measured on 3.7 million ids: 854ms
// to 158ms, with the output the same 18.0MB -- carrying no dictionary between
// blocks costs nothing at this size. Verified readable by both stdlib and the
// system gunzip.
//
// Lowering the compression level was the obvious alternative and a worse one:
// BestSpeed saved 286ms and cost 2.1MB on a file that is published once and
// downloaded repeatedly.
func writeSnapshot(path string, ids []string) (string, error) {
	workers := runtime.GOMAXPROCS(0)
	if workers < 2 || len(ids) < 1<<16 {
		return writeSnapshotSerial(path, ids)
	}

	chunk := (len(ids) + workers - 1) / workers
	nparts := (len(ids) + chunk - 1) / chunk
	parts := make([][]byte, nparts)

	var wg sync.WaitGroup
	var firstErr error
	var errOnce sync.Once
	for k := range nparts {
		lo := k * chunk
		hi := min(lo+chunk, len(ids))
		wg.Add(1)
		go func(k int, slice []string) {
			defer wg.Done()
			var buf bytes.Buffer
			buf.Grow(len(slice) * 8)
			zw := gzip.NewWriter(&buf)
			w := bufio.NewWriterSize(zw, 1<<20)
			for _, id := range slice {
				if _, err := w.WriteString(id); err != nil {
					errOnce.Do(func() { firstErr = err })
					return
				}
				if err := w.WriteByte('\n'); err != nil {
					errOnce.Do(func() { firstErr = err })
					return
				}
			}
			if err := w.Flush(); err != nil {
				errOnce.Do(func() { firstErr = err })
				return
			}
			if err := zw.Close(); err != nil {
				errOnce.Do(func() { firstErr = err })
				return
			}
			parts[k] = buf.Bytes()
		}(k, ids[lo:hi])
	}
	wg.Wait()
	if firstErr != nil {
		return "", fmt.Errorf("compress %s: %w", path, firstErr)
	}

	f, err := os.Create(path)
	if err != nil {
		return "", fmt.Errorf("create %s: %w", path, err)
	}
	h := sha256.New()
	out := io.MultiWriter(f, h)
	for _, part := range parts {
		if _, werr := out.Write(part); werr != nil {
			_ = f.Close()
			return "", fmt.Errorf("write %s: %w", path, werr)
		}
	}
	if cerr := f.Close(); cerr != nil {
		return "", fmt.Errorf("close %s: %w", path, cerr)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// writeSnapshotSerial is the single-member path, used below the size where
// splitting pays for itself and whenever there is one core to split across.
func writeSnapshotSerial(path string, ids []string) (string, error) {
	f, err := os.Create(path)
	if err != nil {
		return "", fmt.Errorf("create %s: %w", path, err)
	}
	h := sha256.New()
	zw := gzip.NewWriter(io.MultiWriter(f, h))
	w := bufio.NewWriterSize(zw, 1<<20)
	for _, id := range ids {
		if _, werr := fmt.Fprintln(w, id); werr != nil {
			_ = f.Close()
			return "", fmt.Errorf("write %s: %w", path, werr)
		}
	}
	for _, closeErr := range []error{w.Flush(), zw.Close(), f.Close()} {
		if closeErr != nil {
			return "", fmt.Errorf("write %s: %w", path, closeErr)
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func readSnapshot(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var r io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		zr, zerr := gzip.NewReader(f)
		if zerr != nil {
			return nil, fmt.Errorf("%s: %w", path, zerr)
		}
		defer func() { _ = zr.Close() }()
		r = zr
	}
	return scanLines(r)
}

// readLines reads a whole file and slices it into lines.
//
// The obvious bufio.Scanner loop allocates a string per line, which at 3.7
// million ids is 3.7 million allocations and about 250MB of small objects.
// strings.Split returns substrings that share the backing array of the string
// they came from -- a language guarantee, not a trick -- so this pays for one
// copy of the file and nothing else. Measured 1.5x faster on a real-sized
// partial file, and it leaves the garbage collector alone.
func readLines(path string) ([]string, error) {
	buf, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	all := string(buf)
	lines := strings.Split(all, "\n")
	out := lines[:0] // filter in place; the header array is already allocated
	for _, l := range lines {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out, nil
}

func scanLines(r io.Reader) ([]string, error) {
	var out []string
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			out = append(out, line)
		}
	}
	return out, sc.Err()
}

// newerThan orders generations by date, then by run id numerically.
//
// Numerically rather than lexicographically: the run id looks like a unix
// timestamp, and a lexicographic compare would silently invert the order the
// day it gains a digit. Non-numeric ids fall back to a string compare rather
// than being treated as equal.

// latestManifest returns the newest manifest in dir, if any. Newest by
// generation date then run id, which is the order Google produces them in --
// not by file mtime, which a copy or a checkout would scramble.
func latestManifest(dir string) (manifest, bool) {
	matches, err := filepath.Glob(filepath.Join(dir, "manifest-*.json"))
	if err != nil || len(matches) == 0 {
		return manifest{}, false
	}
	slices.Sort(matches)

	var best manifest
	var found bool
	for _, path := range matches {
		f, oerr := os.Open(path)
		if oerr != nil {
			continue
		}
		var m manifest
		derr := json.NewDecoder(f).Decode(&m)
		_ = f.Close()
		if derr != nil {
			continue
		}
		if !found || m.Generation.Compare(best.Generation) > 0 {
			best, found = m, true
		}
	}
	return best, found
}

// sortStrings sorts in place, in parallel, without allocating.
//
// This runs once at the end of a sweep that has already taken hours -- and it
// is also the moment someone is watching a terminal wondering whether the tool
// has hung, so it is worth doing properly. Measured on 3.7 million ids:
//
//	slices.Sort          845ms   0MB allocated   227MB peak
//	parallel merge       290ms   178MB           404MB peak
//	this                 300ms   0MB             168MB peak
//
// American Flag Sort (Bentley & Sedgewick, "Fast Algorithms for Sorting and
// Searching Strings", 1997): count byte frequencies at depth d, permute into
// buckets in place, recurse per bucket at d+1. A shared prefix is examined
// once per group rather than re-examined on every comparison.
//
// Single-threaded it is actually slower than slices.Sort here (903ms) -- the
// package ids share only three or four leading bytes, which is not enough to
// repay the bucket setup. What it wins is the parallelism: the top-level
// buckets are disjoint ranges of the same array, so they sort concurrently
// with no merge step and therefore no merge buffers. Same wall clock as a
// parallel merge, less than half the peak memory, and nothing for the garbage
// collector to do.
func sortStrings(a []string) []string {
	workers := runtime.GOMAXPROCS(0)
	if workers < 2 || len(a) <= radixCutoff*workers {
		slices.Sort(a)
		return a
	}

	count, _ := radixPartition(a, 0)

	// Buckets are disjoint slices of a, so they need no synchronisation.
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	pos := count[0]
	for i := 1; i < radixBuckets; i++ {
		if count[i] > 1 {
			bucket := a[pos : pos+count[i]]
			wg.Add(1)
			sem <- struct{}{}
			go func(b []string) {
				defer wg.Done()
				defer func() { <-sem }()
				radixSort(b, 1)
			}(bucket)
		}
		pos += count[i]
	}
	wg.Wait()
	return a
}

// radixBuckets is 257: one for strings that end at this depth, plus 256 bytes.
// Ending early has to sort before any character, which is what byteAt encodes.
const radixBuckets = 257

// radixCutoff is where distributing costs more than comparing. Below it the
// comparison sort wins outright.
const radixCutoff = 64

func radixSort(a []string, depth int) {
	if len(a) <= radixCutoff {
		slices.Sort(a)
		return
	}
	count, _ := radixPartition(a, depth)
	pos := count[0]
	for i := 1; i < radixBuckets; i++ {
		if count[i] > 1 {
			radixSort(a[pos:pos+count[i]], depth+1)
		}
		pos += count[i]
	}
}

// radixPartition rearranges a in place so that entries sharing a byte at the
// given depth are contiguous, and returns the bucket sizes.
func radixPartition(a []string, depth int) (count, offset [radixBuckets]int) {
	for _, s := range a {
		count[byteAt(s, depth)]++
	}

	var next [radixBuckets]int
	sum := 0
	for i := range count {
		offset[i] = sum
		next[i] = sum
		sum += count[i]
	}

	// The in-place permutation: take the element at the head of bucket i, walk
	// it to where it belongs, and keep whatever it displaced, until the cycle
	// closes back on bucket i.
	for i := range radixBuckets {
		for next[i] < offset[i]+count[i] {
			s := a[next[i]]
			b := byteAt(s, depth)
			for b != i {
				// Swap s into its own bucket and pick up whatever it displaced.
				a[next[i]] = a[next[b]]
				a[next[b]] = s
				next[b]++
				s = a[next[i]]
				b = byteAt(s, depth)
			}
			next[i]++
		}
	}
	return count, offset
}

// byteAt returns 0 for the end of a string and byte+1 otherwise, so that a
// string which ends sorts before any string that continues.
func byteAt(s string, depth int) int {
	if depth >= len(s) {
		return 0
	}
	return int(s[depth]) + 1
}

// seedFromGeneration derives a sample seed from the build id, so the same
// generation always samples the same shards and a new one samples afresh.
func seedFromGeneration(g googleplayscraper.Generation) int64 {
	return int64(g.SampleSeed() &^ (1 << 63))
}
