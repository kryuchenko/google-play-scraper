// Command app-availability probes an app's region availability across many
// Google Play countries and prints a country→status map.
//
// It actively fetches the listing in each country and reads only the
// availability node, so it is far cheaper than a full detail fetch per country.
// A full AllCountries sweep is 177 requests, one per country; at the default
// 600ms throttle used here that is a little under two minutes. Probing is
// active and anonymous, so keep the throttle gentle to avoid rate limiting.
//
// Usage:
//
//	go run ./examples/app-availability -app com.spotify.music -countries us,de,cn
//	go run ./examples/app-availability -app com.spotify.music -countries all -out spotify.csv
package main

import (
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	googleplayscraper "github.com/kryuchenko/google-play-scraper"
)

func main() {
	app := flag.String("app", "com.spotify.music", "app ID to probe")
	countriesArg := flag.String("countries", "all", `comma-separated country codes, or "all" for every Play country`)
	concurrency := flag.Int("concurrency", 1, "number of countries probed in parallel")
	throttle := flag.Duration("throttle", 600*time.Millisecond, "minimum delay between requests")
	out := flag.String("out", "", "optional CSV output path (country,status)")
	flag.Parse()

	countries := parseCountries(*countriesArg)

	client := googleplayscraper.NewClient(googleplayscraper.WithThrottle(*throttle))
	ctx := context.Background()

	start := time.Now()
	fmt.Printf("Probing %s across %d countries (concurrency=%d, throttle=%s)\n\n",
		*app, len(countries), *concurrency, *throttle)

	result, err := client.Availability(ctx, *app, googleplayscraper.AvailabilityOptions{
		Countries:   countries,
		Concurrency: *concurrency,
		Progress: func(p googleplayscraper.AvailabilityProgress) {
			fmt.Printf("  [%3d/%3d] %-3s %s\n", p.DoneCount, p.TotalCount, p.Country, p.Status)
		},
	})
	if err != nil {
		// A non-nil error means the sweep was cut short (e.g. context cancelled);
		// the partial result is still usable, so report and continue.
		fmt.Fprintf(os.Stderr, "\nAvailability interrupted: %v (showing partial result)\n", err)
	}

	printSummary(result, time.Since(start))

	if *out != "" {
		if err := writeCSV(*out, result); err != nil {
			fmt.Fprintf(os.Stderr, "write CSV: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("\nCSV written to %s\n", *out)
	}
}

// parseCountries turns the -countries flag into a slice: "all" (case-insensitive)
// expands to AllCountries, otherwise the CSV is split into codes.
func parseCountries(arg string) []string {
	if strings.EqualFold(strings.TrimSpace(arg), "all") {
		return googleplayscraper.AllCountries
	}
	var countries []string
	for c := range strings.SplitSeq(arg, ",") {
		if code := strings.TrimSpace(c); code != "" {
			countries = append(countries, code)
		}
	}
	return countries
}

func printSummary(result googleplayscraper.AvailabilityResult, elapsed time.Duration) {
	counts := map[googleplayscraper.Status]int{}
	var available []string
	for country, status := range result.Statuses {
		counts[status]++
		if status == googleplayscraper.StatusAvailable {
			available = append(available, country)
		}
	}
	sort.Strings(available)

	fmt.Printf("\nDone in %s\n", elapsed.Round(time.Second))
	fmt.Printf("  Available:     %d  %s\n", counts[googleplayscraper.StatusAvailable], strings.Join(available, " "))
	fmt.Printf("  Not in region: %d\n", counts[googleplayscraper.StatusNotInRegion])
	fmt.Printf("  Not found:     %d\n", counts[googleplayscraper.StatusNotFound])
	fmt.Printf("  Errors:        %d\n", counts[googleplayscraper.StatusFetchError])
	fmt.Printf("  Checked:       %d\n", result.Checked)
	fmt.Printf("  GloballyRemoved: %v\n", result.GloballyRemoved)
}

func writeCSV(path string, result googleplayscraper.AvailabilityResult) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	w := csv.NewWriter(f)
	defer w.Flush()

	if err := w.Write([]string{"country", "status"}); err != nil {
		return err
	}

	countries := make([]string, 0, len(result.Statuses))
	for country := range result.Statuses {
		countries = append(countries, country)
	}
	sort.Strings(countries)

	for _, country := range countries {
		if err := w.Write([]string{country, result.Statuses[country].String()}); err != nil {
			return err
		}
	}
	return w.Error()
}
