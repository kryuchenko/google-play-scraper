package googleplayscraper_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	googleplayscraper "github.com/kryuchenko/google-play-scraper/v2"
)

// ExampleStatusError shows how to distinguish HTTP status failures (e.g. a
// missing app vs. rate limiting) by unwrapping the error with errors.As.
func ExampleStatusError() {
	client := googleplayscraper.NewClient()

	_, err := client.App(context.Background(), "com.does.not.exist", googleplayscraper.AppOptions{})

	var statusErr *googleplayscraper.StatusError
	switch {
	case errors.As(err, &statusErr) && statusErr.Code == http.StatusNotFound:
		fmt.Println("app not found")
	case errors.As(err, &statusErr) && statusErr.Code == http.StatusTooManyRequests:
		fmt.Println("rate limited; back off and retry")
	case err != nil:
		fmt.Println("other error:", err)
	default:
		fmt.Println("ok")
	}
}

// ExampleClient_CatalogSeq sweeps a small subset of Google's sitemap
// shards and collects the app package ids they list. A full sweep (Shards: nil)
// walks ~83k shards for roughly 3 million ids; here we cap it to the first
// five shards for a quick sample. Ids arrive one at a time, so the loop can
// stop as soon as it has what it needs -- on the full catalog that is the
// difference between a few requests and 83k of them.
func ExampleClient_CatalogSeq() {
	client := googleplayscraper.NewClient(
		googleplayscraper.WithThrottle(200 * time.Millisecond),
	)

	var ids []string
	for pkg, err := range client.CatalogSeq(context.Background(), googleplayscraper.CatalogOptions{
		Concurrency: 4,
		Shards:      []int{0, 1, 2, 3, 4}, // omit (nil) to sweep the entire catalog
		OnShardError: func(idx int, url string, err error) {
			fmt.Printf("shard %d failed: %v\n", idx, err)
		},
	}) {
		// Terminal errors only: a shard that failed went to OnShardError and
		// the sweep carried on past it.
		if err != nil {
			fmt.Println("sweep error:", err)
			return
		}
		ids = append(ids, pkg)
	}

	fmt.Printf("collected %d package ids\n", len(ids))
	// Resolve the ones you care about with client.App(...).
}
