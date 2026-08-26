// Command category-coverage drives CategoryApps to collect the most complete
// unique set of apps for a Google Play category and writes them to CSV.
//
// It unions many independent listing slices (collections, locales, search
// terms, and an optional similar/developer graph walk) to beat the ~200-app
// ceiling of a single anonymous request.
//
// Usage:
//
//	go run ./examples/category-coverage -category GAME_ACTION -locales 3 -max 5000 -out apps.csv
//	go run ./examples/category-coverage -category GAME_PUZZLE -locales 5 -graph-depth 2 -suggest -out puzzle.csv
package main

import (
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	googleplayscraper "github.com/kryuchenko/google-play-scraper/v2"
)

func main() {
	category := flag.String("category", "GAME_ACTION", "category ID (e.g. GAME_ACTION)")
	locales := flag.Int("locales", 3, "number of locales from the CoverageLocales preset")
	graphDepth := flag.Int("graph-depth", 0, "similar/developer BFS depth (0 disables)")
	suggest := flag.Bool("suggest", false, "expand search terms via Suggest")
	noSearch := flag.Bool("no-search", false, "skip the search-term phase (only collections, clusters, graph)")
	max := flag.Int("max", 5000, "hard ceiling on unique apps")
	throttle := flag.Duration("throttle", 500*time.Millisecond, "minimum delay between requests")
	out := flag.String("out", "coverage.csv", "output CSV path")
	flag.Parse()

	locs := googleplayscraper.CoverageLocales
	if *locales > 0 && *locales < len(locs) {
		locs = locs[:*locales]
	}

	// An empty (non-nil) slice disables the search phase; nil falls back to the
	// built-in dictionary for the category.
	var searchTerms []string
	if *noSearch {
		searchTerms = []string{}
	}

	client := googleplayscraper.NewClient(googleplayscraper.WithThrottle(*throttle))
	ctx := context.Background()

	start := time.Now()
	fmt.Printf("Coverage run: category=%s locales=%d graph-depth=%d suggest=%v max=%d throttle=%s\n",
		*category, len(locs), *graphDepth, *suggest, *max, *throttle)

	result, err := client.CategoryApps(ctx, googleplayscraper.CoverageOptions{
		Category:      googleplayscraper.Category(*category),
		Locales:       locs,
		SearchTerms:   searchTerms,
		GraphDepth:    *graphDepth,
		ExpandSuggest: *suggest,
		MaxApps:       *max,
		Progress: func(p googleplayscraper.CoverageProgress) {
			if p.NewCount > 0 {
				fmt.Printf("  %-40s +%-4d total=%d\n", p.Source, p.NewCount, p.TotalUnique)
			}
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "CategoryApps error: %v\n", err)
		os.Exit(1)
	}

	if err := writeCSV(*out, result.Apps); err != nil {
		fmt.Fprintf(os.Stderr, "write CSV: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\nDone in %s\n", time.Since(start).Round(time.Second))
	fmt.Printf("  Unique apps:   %d\n", len(result.Apps))
	fmt.Printf("  Sources run:   %d\n", result.SourcesRun)
	fmt.Printf("  Requests made: %d\n", result.RequestsMade)
	fmt.Printf("  Saturated:     %v\n", result.Saturated)
	fmt.Printf("  CSV:           %s\n", *out)

	fmt.Println("\nTop sources by new apps contributed:")
	for _, s := range topSources(result.PerSourceNew, 15) {
		fmt.Printf("  %-44s %d\n", s.source, s.count)
	}
}

func writeCSV(path string, apps []googleplayscraper.SearchResult) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	w := csv.NewWriter(f)
	defer w.Flush()

	if err := w.Write([]string{"appId", "title", "developer", "score", "price", "free", "url"}); err != nil {
		return err
	}
	for _, a := range apps {
		record := []string{
			a.AppID,
			a.Title,
			a.Developer,
			strconv.FormatFloat(a.Score, 'f', 2, 64),
			strconv.FormatFloat(a.Price, 'f', 2, 64),
			strconv.FormatBool(a.Free),
			a.URL,
		}
		if err := w.Write(record); err != nil {
			return err
		}
	}
	return w.Error()
}

type sourceCount struct {
	source string
	count  int
}

// topSources returns the n sources that contributed the most new apps, sorted
// descending, for a readable run summary.
func topSources(perSource map[string]int, n int) []sourceCount {
	all := make([]sourceCount, 0, len(perSource))
	for s, c := range perSource {
		all = append(all, sourceCount{s, c})
	}
	// Simple insertion-style selection: the list is small (hundreds at most).
	for i := 0; i < len(all); i++ {
		for j := i + 1; j < len(all); j++ {
			if all[j].count > all[i].count {
				all[i], all[j] = all[j], all[i]
			}
		}
	}
	if n < len(all) {
		all = all[:n]
	}
	return all
}
