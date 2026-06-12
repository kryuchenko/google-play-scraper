package lightfeed

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	gps "github.com/kryuchenko/google-play-scraper"
)

// TestNewValidation covers the offline configuration checks: New must reject
// zero and both endpoints, and a missing binary, without touching the network or
// starting a process.
func TestNewValidation(t *testing.T) {
	if _, err := New(); !errors.Is(err, errBadConfig) {
		t.Errorf("New() with no options: err = %v, want errBadConfig", err)
	}

	if _, err := New(WithCDPEndpoint("ws://x"), WithLightpandaPath("/x")); !errors.Is(err, errBadConfig) {
		t.Errorf("New() with both endpoints: err = %v, want errBadConfig", err)
	}

	if _, err := New(WithLightpandaPath("/no/such/lightpanda")); !errors.Is(err, ErrLightpandaNotFound) {
		t.Errorf("New() with missing binary: err = %v, want ErrLightpandaNotFound", err)
	}

	if _, err := New(WithCDPEndpoint("ws://127.0.0.1:9222")); err != nil {
		t.Errorf("New() with a valid endpoint: unexpected err = %v", err)
	}
}

// TestImplementsFeedPaginator is a compile-time assertion that *Paginator
// satisfies the root package's interface — the whole point of the module.
func TestImplementsFeedPaginator(t *testing.T) {
	var _ gps.FeedPaginator = (*Paginator)(nil)
}

// TestLinkSetDedupAndOrder unit-tests the harvesting accumulator offline: dedup
// across rounds, first-seen ordering, thin-field extraction, and the limit cap.
func TestLinkSetDedupAndOrder(t *testing.T) {
	set := newLinkSet()
	set.addRaw("com.a\thttps://x/?id=com.a\tApp A\ticonA\ncom.b\thttps://x/?id=com.b\tApp B\t")
	set.addRaw("com.a\thttps://x/?id=com.a\tApp A dup\t\ncom.c\thttps://x/?id=com.c\t\t")

	got := set.results(0)
	if len(got) != 3 {
		t.Fatalf("got %d apps, want 3 (deduped)", len(got))
	}
	if got[0].AppID != "com.a" || got[1].AppID != "com.b" || got[2].AppID != "com.c" {
		t.Errorf("order = %v, want [com.a com.b com.c]", []string{got[0].AppID, got[1].AppID, got[2].AppID})
	}
	if got[0].Title != "App A" || got[0].Icon != "iconA" {
		t.Errorf("thin fields = %+v, want Title=App A Icon=iconA", got[0])
	}

	if capped := set.results(2); len(capped) != 2 {
		t.Errorf("results(2) len = %d, want 2", len(capped))
	}
}

// TestLiveDeepPagination is the canary: it runs a real GAME_ACTION deep scroll
// when both LIGHTPANDA_PATH and LIGHTPANDA_CDP are set, and skips otherwise.
//
//   - LIGHTPANDA_PATH: autostart a managed lightpanda from this binary.
//   - LIGHTPANDA_CDP:  connect to an already-running browser at this ws endpoint.
//
// Set both to satisfy the canary contract; the test prefers the external
// endpoint when present (it is the cheaper, more deterministic path). The
// threshold is soft: a browser scroll should beat the stateless FeedLightweight
// mode, but exact counts drift with Google's feed.
func TestLiveDeepPagination(t *testing.T) {
	path := os.Getenv("LIGHTPANDA_PATH")
	endpoint := os.Getenv("LIGHTPANDA_CDP")
	if path == "" || endpoint == "" {
		t.Skip("set LIGHTPANDA_PATH and LIGHTPANDA_CDP to run the live deep-pagination canary")
	}

	var opt Option
	if endpoint != "" {
		opt = WithCDPEndpoint(endpoint)
	} else {
		opt = WithLightpandaPath(path)
	}

	p, err := New(opt, WithTimeout(120*time.Second))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 130*time.Second)
	defer cancel()

	client := gps.NewClient()

	lightweight, err := client.Cluster(ctx, gps.ClusterOptions{
		Path:     "/store/apps/category/GAME_ACTION",
		FeedMode: gps.FeedLightweight,
	})
	if err != nil {
		t.Fatalf("Cluster(FeedLightweight): %v", err)
	}

	browser, err := client.Cluster(ctx, gps.ClusterOptions{
		Path:          "/store/apps/category/GAME_ACTION",
		FeedMode:      gps.FeedBrowser,
		FeedPaginator: p,
	})
	if err != nil {
		t.Fatalf("Cluster(FeedBrowser): %v", err)
	}

	t.Logf("GAME_ACTION: FeedLightweight=%d apps, FeedBrowser=%d apps", len(lightweight), len(browser))

	if dups := countDuplicateIDs(browser); dups > 0 {
		t.Errorf("FeedBrowser returned %d duplicate AppIDs", dups)
	}
	if len(browser) <= len(lightweight) {
		t.Errorf("FeedBrowser=%d did not exceed FeedLightweight=%d; the deep scroll added nothing",
			len(browser), len(lightweight))
	}
}

func countDuplicateIDs(rs []gps.SearchResult) int {
	seen := make(map[string]bool, len(rs))
	dups := 0
	for _, r := range rs {
		if seen[r.AppID] {
			dups++
			continue
		}
		seen[r.AppID] = true
	}
	return dups
}
