package googleplayscraper

import (
	"context"
	"errors"
	"testing"
)

// TestEffectiveFeedMode covers the FollowFeed → FeedMode bridge and the
// precedence rule (an explicit FeedMode wins over the deprecated flag).
func TestEffectiveFeedMode(t *testing.T) {
	tests := []struct {
		name string
		opts ClusterOptions
		want FeedMode
	}{
		{"default is none", ClusterOptions{}, FeedNone},
		{"followFeed maps to lightweight", ClusterOptions{FollowFeed: true}, FeedLightweight},
		{"explicit lightweight", ClusterOptions{FeedMode: FeedLightweight}, FeedLightweight},
		{"explicit browser", ClusterOptions{FeedMode: FeedBrowser}, FeedBrowser},
		{"FeedMode wins over FollowFeed", ClusterOptions{FeedMode: FeedBrowser, FollowFeed: true}, FeedBrowser},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.opts.effectiveFeedMode(); got != tt.want {
				t.Errorf("effectiveFeedMode() = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestClusterBrowserRequiresPaginator asserts FeedBrowser without a paginator
// fails fast with the sentinel, before any browser work is attempted.
func TestClusterBrowserRequiresPaginator(t *testing.T) {
	c := newMockClient(t, routePath(pathCluster, clusterHTMLPage(t, []string{"com.a"}, "GAME")))

	_, err := c.Cluster(context.Background(), ClusterOptions{
		Path:     "/store/apps/collection/cluster?x=1",
		FeedMode: FeedBrowser,
	})
	if !errors.Is(err, ErrFeedPaginatorRequired) {
		t.Fatalf("Cluster(FeedBrowser, nil paginator) error = %v, want ErrFeedPaginatorRequired", err)
	}
}

// stubPaginator is an offline FeedPaginator that returns a fixed batch and
// records the request it was handed, so tests can assert the adapter's wiring.
type stubPaginator struct {
	out     []SearchResult
	err     error
	gotReq  FeedRequest
	callCnt int
}

func (s *stubPaginator) PaginateFeed(_ context.Context, req FeedRequest) ([]SearchResult, error) {
	s.callCnt++
	s.gotReq = req
	return s.out, s.err
}

// TestClusterBrowserMergesAndDedups drives a full FeedBrowser Cluster call
// offline: the initial grid carries one rich app, the stub paginator returns a
// duplicate of it plus two new thin apps. The result must keep the rich initial
// record, append the new apps once, and pass the page through to the paginator.
func TestClusterBrowserMergesAndDedups(t *testing.T) {
	c := newMockClient(t, routePath(pathCluster, clusterHTMLPage(t, []string{"com.rich"}, "GAME")))

	stub := &stubPaginator{out: []SearchResult{
		{AppID: "com.rich", Title: "thin duplicate"}, // dup of initial grid
		{AppID: "com.new1", Title: "New One", URL: "u1"},
		{AppID: "com.new2", Title: "New Two", URL: "u2"},
		{AppID: ""}, // blank, must be skipped
		{AppID: "com.new1", Title: "New One again"}, // intra-batch dup
	}}

	got, err := c.Cluster(context.Background(), ClusterOptions{
		Path:          "/store/apps/collection/cluster?x=1",
		FeedMode:      FeedBrowser,
		FeedPaginator: stub,
	})
	if err != nil {
		t.Fatalf("Cluster(FeedBrowser): %v", err)
	}

	if stub.callCnt != 1 {
		t.Fatalf("paginator called %d times, want 1", stub.callCnt)
	}
	if len(stub.gotReq.PageHTML) == 0 {
		t.Error("paginator received empty PageHTML; the fetched page should be forwarded")
	}

	ids := collectAppIDs(got)
	if len(ids) != 3 {
		t.Fatalf("got %d apps %v, want 3 (rich + 2 new, deduped)", len(ids), ids)
	}
	// The rich initial record must survive, not the thin duplicate.
	for _, r := range got {
		if r.AppID == "com.rich" && r.Title == "thin duplicate" {
			t.Error("thin browser duplicate overwrote the rich initial-grid record")
		}
	}
}

// TestClusterBrowserRespectsLimit caps Num below the harvested count and
// confirms the merged slice is truncated.
func TestClusterBrowserRespectsLimit(t *testing.T) {
	c := newMockClient(t, routePath(pathCluster, clusterHTMLPage(t, []string{"com.a"}, "GAME")))

	stub := &stubPaginator{out: []SearchResult{
		{AppID: "com.b"}, {AppID: "com.c"}, {AppID: "com.d"},
	}}

	got, err := c.Cluster(context.Background(), ClusterOptions{
		Path:          "/store/apps/collection/cluster?x=1",
		FeedMode:      FeedBrowser,
		FeedPaginator: stub,
		Num:           2,
	})
	if err != nil {
		t.Fatalf("Cluster(FeedBrowser, Num=2): %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d apps, want 2 (Num cap)", len(got))
	}
}

// TestClusterBrowserPropagatesError wraps a paginator failure rather than
// swallowing it.
func TestClusterBrowserPropagatesError(t *testing.T) {
	c := newMockClient(t, routePath(pathCluster, clusterHTMLPage(t, []string{"com.a"}, "GAME")))

	sentinel := errors.New("boom")
	stub := &stubPaginator{err: sentinel}

	_, err := c.Cluster(context.Background(), ClusterOptions{
		Path:          "/store/apps/collection/cluster?x=1",
		FeedMode:      FeedBrowser,
		FeedPaginator: stub,
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Cluster error = %v, want it to wrap %v", err, sentinel)
	}
}

func collectAppIDs(rs []SearchResult) []string {
	ids := make([]string, 0, len(rs))
	for _, r := range rs {
		ids = append(ids, r.AppID)
	}
	return ids
}
