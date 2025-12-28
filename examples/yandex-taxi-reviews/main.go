package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/kryuchenko/google-play-scraper"
)

func main() {
	client := googleplayscraper.NewClient(
		googleplayscraper.WithThrottle(500 * time.Millisecond), // Rate limiting
	)

	ctx := context.Background()
	appID := "ru.yandex.taxi"

	fmt.Printf("Fetching reviews for %s (Russia, Russian)...\n", appID)

	// Fetch reviews - up to 5000 (Google Play limit in practice)
	reviews, err := client.ReviewsAll(ctx, appID, googleplayscraper.ReviewOptions{
		Lang:    "ru",
		Country: "ru",
		Sort:    googleplayscraper.SortNewest,
		Count:   5000,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error fetching reviews: %v\n", err)
		os.Exit(1)
	}

	// Filter reviews from the last year
	oneYearAgo := time.Now().AddDate(-1, 0, 0)
	var filtered []googleplayscraper.Review

	for _, r := range reviews {
		if r.Date.After(oneYearAgo) {
			filtered = append(filtered, r)
		}
	}

	fmt.Printf("Total reviews fetched: %d\n", len(reviews))
	fmt.Printf("Reviews from last year: %d\n", len(filtered))

	// Save to JSON file
	output, err := json.MarshalIndent(filtered, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error marshaling JSON: %v\n", err)
		os.Exit(1)
	}

	filename := fmt.Sprintf("yandex_taxi_reviews_%s.json", time.Now().Format("2006-01-02"))
	if err := os.WriteFile(filename, output, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing file: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Saved to %s\n", filename)

	// Print stats
	if len(filtered) > 0 {
		var totalScore int
		scoreCounts := make(map[int]int)
		for _, r := range filtered {
			totalScore += r.Score
			scoreCounts[r.Score]++
		}

		fmt.Printf("\nStats:\n")
		fmt.Printf("  Average score: %.2f\n", float64(totalScore)/float64(len(filtered)))
		fmt.Printf("  Score distribution:\n")
		for i := 5; i >= 1; i-- {
			count := scoreCounts[i]
			pct := float64(count) / float64(len(filtered)) * 100
			fmt.Printf("    %d star: %d (%.1f%%)\n", i, count, pct)
		}

		fmt.Printf("\nLatest review:\n")
		fmt.Printf("  Date: %s\n", filtered[0].Date.Format("2006-01-02"))
		fmt.Printf("  Score: %d\n", filtered[0].Score)
		fmt.Printf("  User: %s\n", filtered[0].UserName)
		if len(filtered[0].Text) > 200 {
			fmt.Printf("  Text: %s...\n", filtered[0].Text[:200])
		} else {
			fmt.Printf("  Text: %s\n", filtered[0].Text)
		}
	}
}
