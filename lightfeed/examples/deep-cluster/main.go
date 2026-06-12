// Command deep-cluster demonstrates FeedBrowser deep pagination of a Google Play
// category feed using the lightfeed browser paginator.
//
// It fetches the same category two ways — the zero-dependency FeedLightweight
// mode and the browser-driven FeedBrowser mode — and prints how many apps each
// collected, so the deep-scroll gain is visible.
//
// It lives in the lightfeed submodule because FeedBrowser pulls in chromedp and
// a headless browser; the root scraper stays dependency-free.
//
// Run it one of two ways:
//
// Autostart a managed lightpanda from a downloaded binary:
//
//	go run ./examples/deep-cluster -lightpanda ./lightpanda -category GAME_ACTION
//
// Or connect to a browser you already run (lightpanda serve --host 127.0.0.1 --port 9222):
//
//	go run ./examples/deep-cluster -cdp ws://127.0.0.1:9222 -category GAME_ACTION
//
// Download a lightpanda binary first, e.g. for Apple Silicon macOS:
//
//	curl -sL -o lightpanda \
//	  https://github.com/lightpanda-io/browser/releases/download/nightly/lightpanda-aarch64-macos
//	chmod +x lightpanda
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	gps "github.com/kryuchenko/google-play-scraper"
	"github.com/kryuchenko/google-play-scraper/lightfeed"
)

func main() {
	category := flag.String("category", "GAME_ACTION", "category ID, e.g. GAME_ACTION")
	lightpanda := flag.String("lightpanda", "", "path to a lightpanda binary (autostart mode)")
	cdp := flag.String("cdp", "", "CDP WebSocket endpoint of a running browser, e.g. ws://127.0.0.1:9222")
	rounds := flag.Int("rounds", 40, "max scroll rounds")
	timeout := flag.Duration("timeout", 120*time.Second, "deep-pagination timeout")
	flag.Parse()

	if (*lightpanda == "") == (*cdp == "") {
		fmt.Fprintln(os.Stderr, "provide exactly one of -lightpanda or -cdp")
		os.Exit(2)
	}

	opt := lightfeed.WithCDPEndpoint(*cdp)
	if *lightpanda != "" {
		opt = lightfeed.WithLightpandaPath(*lightpanda)
	}

	paginator, err := lightfeed.New(opt,
		lightfeed.WithScrollRounds(*rounds),
		lightfeed.WithTimeout(*timeout),
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, "configure paginator:", err)
		os.Exit(1)
	}
	defer paginator.Close()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout+10*time.Second)
	defer cancel()

	client := gps.NewClient()
	path := "/store/apps/category/" + *category

	lightweight, err := client.Cluster(ctx, gps.ClusterOptions{
		Path:     path,
		FeedMode: gps.FeedLightweight,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "FeedLightweight:", err)
		os.Exit(1)
	}

	start := time.Now()
	browser, err := client.Cluster(ctx, gps.ClusterOptions{
		Path:          path,
		FeedMode:      gps.FeedBrowser,
		FeedPaginator: paginator,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "FeedBrowser:", err)
		os.Exit(1)
	}

	fmt.Printf("category %s\n", *category)
	fmt.Printf("  FeedLightweight (stateless): %d apps\n", len(lightweight))
	fmt.Printf("  FeedBrowser     (deep scroll): %d apps  (%.1fs, +%d over lightweight)\n",
		len(browser), time.Since(start).Seconds(), len(browser)-len(lightweight))
}
