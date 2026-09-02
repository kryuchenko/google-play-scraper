// Command catalog-crawler enumerates Google Play's full app catalog via the
// public sitemaps and writes every app package id to a file.
//
// Unlike category-coverage (which maximizes the commercially-visible layer of
// ONE category), this walks Google's own sitemap of the entire store — tens of
// thousands of gzipped shards, ~3 million app ids — and is the only anonymous
// channel that enumerates the catalog rather than the top of it.
//
// A full sweep is large: ~83k shards. Use -shards to cap it for a demo or a
// sample, and -throttle/-concurrency to stay polite. The optional -resolve step
// fetches full App() details for the first N collected ids to confirm they are
// real listings.
//
// Usage:
//
//	# Sample: first 50 shards, 4 in parallel, write ids to catalog.txt
//	go run ./examples/catalog-crawler -shards 50 -concurrency 4 -out catalog.txt
//
//	# Full catalog (long-running): every shard
//	go run ./examples/catalog-crawler -shards 0 -concurrency 8 -throttle 200ms -out catalog.txt
//
//	# Sample, then resolve the first 5 ids to titles as a sanity check
//	go run ./examples/catalog-crawler -shards 10 -resolve 5
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	googleplayscraper "github.com/kryuchenko/google-play-scraper"
)

func main() {
	shards := flag.Int("shards", 50, "crawl only the first N shards (0 = all, currently ~83k)")
	concurrency := flag.Int("concurrency", 4, "parallel shard fetches")
	throttle := flag.Duration("throttle", 200*time.Millisecond, "minimum delay between requests")
	dedup := flag.Bool("dedup", true, "deduplicate package ids across shards")
	out := flag.String("out", "catalog.txt", "output file for newline-delimited package ids ('-' for stdout)")
	resolve := flag.Int("resolve", 0, "after enumeration, fetch App() detail for the first N ids as a sanity check")
	flag.Parse()

	client := googleplayscraper.NewClient(
		googleplayscraper.WithThrottle(*throttle),
	)

	// Ctrl-C cancels the sweep and still flushes whatever was collected — the
	// enumeration is designed to return a partial catalog on cancellation.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Build the shard subset (the first N indices) when -shards > 0.
	var subset []int
	if *shards > 0 {
		subset = make([]int, *shards)
		for i := range subset {
			subset[i] = i
		}
	}

	// emit and the OnShard* callbacks are invoked serially by EnumerateCatalog,
	// so the counters and maps below need no locking of their own.
	var (
		w       *bufio.Writer
		file    *os.File
		total   int
		unique  int
		seen    = make(map[string]struct{})
		started = time.Now()
	)

	if *out == "-" {
		w = bufio.NewWriter(os.Stdout)
	} else {
		f, err := os.Create(*out)
		if err != nil {
			fmt.Fprintf(os.Stderr, "create %s: %v\n", *out, err)
			os.Exit(1)
		}
		file = f
		w = bufio.NewWriter(f)
	}

	ids := make([]string, 0, 1024) // kept for the optional resolve step

	fmt.Fprintf(os.Stderr, "enumerating catalog (shards=%s, concurrency=%d)...\n", shardLabel(*shards), *concurrency)

	var err error
	for pkg, seqErr := range client.CatalogSeq(ctx, googleplayscraper.CatalogOptions{
		Concurrency: *concurrency,
		Shards:      subset,
		OnShardError: func(idx int, url string, err error) {
			fmt.Fprintf(os.Stderr, "  shard %d failed: %v\n", idx, err)
		},
		OnShardDone: func(idx int, _ string, n int) {
			if idx%25 == 0 {
				fmt.Fprintf(os.Stderr, "  shard %d done (+%d apps, %d unique so far, %s elapsed)\n",
					idx, n, unique, time.Since(started).Round(time.Second))
			}
		},
	}) {
		// Terminal errors only -- a shard that fails went to OnShardError
		// above and the sweep carried on past it.
		if seqErr != nil {
			err = seqErr
			break
		}
		total++
		if *dedup {
			if _, dup := seen[pkg]; dup {
				continue
			}
			seen[pkg] = struct{}{}
		}
		unique++
		// A write error here is latched by the writer and reported at Flush.
		_, _ = fmt.Fprintln(w, pkg)
		if *resolve > 0 && len(ids) < *resolve {
			ids = append(ids, pkg)
		}
	}

	// A sweep costs hours and tens of gigabytes of traffic. Losing its output
	// to an unreported flush or close failure -- a full disk is the ordinary
	// way this happens -- would be the worst possible ending, and it would
	// look exactly like a successful run with fewer apps than expected.
	if ferr := w.Flush(); ferr != nil {
		fmt.Fprintf(os.Stderr, "flush output: %v\n", ferr)
		os.Exit(1)
	}
	if file != nil {
		if cerr := file.Close(); cerr != nil {
			fmt.Fprintf(os.Stderr, "close output: %v\n", cerr)
			os.Exit(1)
		}
	}

	// A cancelled sweep is not a failure: report it and keep the partial output.
	if err != nil {
		fmt.Fprintf(os.Stderr, "sweep stopped: %v (partial result kept)\n", err)
	}

	fmt.Fprintf(os.Stderr, "\ncollected %d ids (%d unique) in %s\n",
		total, unique, time.Since(started).Round(time.Second))
	if *out != "-" {
		fmt.Fprintf(os.Stderr, "written to %s\n", *out)
	}

	if *resolve > 0 && len(ids) > 0 {
		fmt.Fprintf(os.Stderr, "\nresolving first %d ids:\n", len(ids))
		for _, id := range ids {
			rctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			app, err := client.App(rctx, id, googleplayscraper.AppOptions{})
			cancel()
			if err != nil {
				fmt.Fprintf(os.Stderr, "  %-45s  ERROR: %v\n", id, err)
				continue
			}
			fmt.Fprintf(os.Stderr, "  %-45s  %s (score %.2f)\n", id, app.Title, app.Score)
		}
	}
}

func shardLabel(n int) string {
	if n <= 0 {
		return "all"
	}
	return fmt.Sprintf("first %d", n)
}
