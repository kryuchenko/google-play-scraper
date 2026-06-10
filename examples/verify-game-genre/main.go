// Command verify-game-genre samples random app IDs from a file and checks
// against live Google Play that each app's GenreID actually starts with "GAME".
//
// Input: a text/CSV file with one app ID per line (for CSV, the app ID must
// be in the first column; a header line is skipped automatically).
//
// Usage:
//
//	go run ./examples/verify-game-genre -input ids.txt -n 1000
package main

import (
	"bufio"
	"context"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/kryuchenko/google-play-scraper"
)

func main() {
	input := flag.String("input", "", "path to file with app IDs (txt or csv, ID in first column)")
	n := flag.Int("n", 1000, "number of random apps to verify")
	seed := flag.Int64("seed", time.Now().UnixNano(), "random seed for sampling (set for reproducibility)")
	lang := flag.String("lang", "en", "language")
	country := flag.String("country", "us", "country")
	throttle := flag.Duration("throttle", 600*time.Millisecond, "minimum delay between requests")
	out := flag.String("out", "verify_report.csv", "output CSV report")
	flag.Parse()

	if *input == "" {
		fmt.Fprintln(os.Stderr, "Usage: verify-game-genre -input <file-with-app-ids> [-n 1000]")
		os.Exit(1)
	}

	ids, err := readAppIDs(*input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading input: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Loaded %d unique app IDs from %s\n", len(ids), *input)

	// Sample without replacement
	rng := rand.New(rand.NewSource(*seed))
	rng.Shuffle(len(ids), func(i, j int) { ids[i], ids[j] = ids[j], ids[i] })
	if *n < len(ids) {
		ids = ids[:*n]
	}
	fmt.Printf("Verifying %d apps (seed %d, throttle %s) — ETA ~%s\n",
		len(ids), *seed, *throttle, (*throttle * time.Duration(len(ids))).Round(time.Minute))

	client := googleplayscraper.NewClient(googleplayscraper.WithThrottle(*throttle))
	ctx := context.Background()

	f, err := os.Create(*out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating report: %v\n", err)
		os.Exit(1)
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	w.Write([]string{"appId", "verdict", "genreId", "genre", "title", "error"})

	var game, notGame, gone, failed int
	start := time.Now()

	for i, id := range ids {
		app, err := client.App(ctx, id, googleplayscraper.AppOptions{Lang: *lang, Country: *country})
		var statusErr *googleplayscraper.StatusError
		switch {
		case errors.As(err, &statusErr) && statusErr.Code == http.StatusNotFound:
			gone++
			w.Write([]string{id, "gone", "", "", "", err.Error()})
		case err != nil:
			failed++
			w.Write([]string{id, "error", "", "", "", err.Error()})
		case strings.HasPrefix(app.GenreID, "GAME"):
			game++
			w.Write([]string{id, "game", app.GenreID, app.Genre, app.Title, ""})
		default:
			notGame++
			w.Write([]string{id, "not_game", app.GenreID, app.Genre, app.Title, ""})
			fmt.Printf("  MISMATCH: %s → %q (%s)\n", id, app.GenreID, app.Title)
		}

		if (i+1)%50 == 0 {
			elapsed := time.Since(start).Round(time.Second)
			fmt.Printf("[%d/%d] game=%d not_game=%d gone=%d error=%d (%s elapsed)\n",
				i+1, len(ids), game, notGame, gone, failed, elapsed)
			w.Flush()
		}
	}

	checked := game + notGame
	fmt.Printf("\nDone in %s. Report: %s\n", time.Since(start).Round(time.Second), *out)
	fmt.Printf("  Confirmed GAME_*:  %d\n", game)
	fmt.Printf("  NOT a game:        %d\n", notGame)
	fmt.Printf("  Gone (404):        %d\n", gone)
	fmt.Printf("  Errors:            %d\n", failed)
	if checked > 0 {
		fmt.Printf("  Accuracy (of reachable apps): %.2f%%\n", 100*float64(game)/float64(checked))
	}
}

// readAppIDs reads app IDs from a text or CSV file (first column), skipping
// header lines and deduplicating.
func readAppIDs(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	seen := make(map[string]bool)
	var ids []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		// CSV/TSV: take the first field
		if i := strings.IndexAny(line, ",;\t"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		line = strings.Trim(line, `"`)
		// App IDs are reverse-domain package names; skip headers and junk
		if !strings.Contains(line, ".") || strings.ContainsAny(line, " /") {
			continue
		}
		if !seen[line] {
			seen[line] = true
			ids = append(ids, line)
		}
	}
	return ids, scanner.Err()
}
