package googleplayscraper

import (
	"context"
	"errors"
)

// FeedMode selects how Cluster extends a category/cluster page's initial app
// grid with the "recommended for you" feed below it.
//
// The three modes trade reach against cost and dependencies:
//
//   - FeedNone returns only the initial grid (a single GET). Cheapest, no feed.
//   - FeedLightweight replays the page's stateless qnKhOb recommendation tokens,
//     one extra request per topic, all over the zero-dependency HTTP client. This
//     is the historical FollowFeed behaviour (verified live: GAME_ACTION grows
//     from ~18 to ~77 apps).
//   - FeedBrowser drives a real headless browser (Lightpanda via CDP) to scroll
//     the page and harvest every lazily-loaded app link. It reaches deepest
//     (~149 apps on GAME_ACTION) but requires a FeedPaginator, which lives in the
//     separate lightfeed module so the root package stays dependency-free.
type FeedMode int

const (
	// FeedNone returns only the initial grid parsed from the page HTML.
	FeedNone FeedMode = iota
	// FeedLightweight extends the grid via the stateless qnKhOb feed tokens
	// (paginateQnKhOb). Equivalent to the deprecated FollowFeed flag.
	FeedLightweight
	// FeedBrowser extends the grid via a browser-driven deep scroll. It requires
	// ClusterOptions.FeedPaginator; without one Cluster returns
	// ErrFeedPaginatorRequired.
	FeedBrowser
)

// ErrFeedPaginatorRequired is returned by Cluster when FeedBrowser is requested
// but no FeedPaginator was supplied. The paginator implementation lives in the
// github.com/kryuchenko/google-play-scraper/lightfeed submodule.
var ErrFeedPaginatorRequired = errors.New("googleplayscraper: FeedBrowser requires a FeedPaginator (see the lightfeed submodule)")

// FeedRequest is the input a FeedPaginator receives for one cluster/category
// page. It is intentionally free of any CDP/browser types so the root package
// never has to import a browser driver.
type FeedRequest struct {
	// URL is the absolute, lang/country-qualified page URL to deep-scroll.
	URL string
	// Lang and Country are the resolved locale (e.g. "en", "us").
	Lang    string
	Country string
	// Limit caps how many apps the paginator should aim to return. Zero means
	// "as many as available", bounded by the paginator's own scroll guards.
	Limit int
	// PageHTML is the already-downloaded initial page, so a paginator can reuse
	// it (e.g. to seed cookies or read metadata) instead of re-fetching.
	PageHTML []byte
}

// FeedPaginator deep-paginates a cluster/category feed. It is the extension
// point for FeedBrowser: the root package defines the contract, and an external
// module (lightfeed) supplies a browser-backed implementation.
//
// Implementations should return thin SearchResults — at minimum AppID and URL —
// since a deep scroll harvests app links, not the rich grid payload. Cluster
// merges these against the rich initial grid, preferring the grid's fields.
type FeedPaginator interface {
	PaginateFeed(ctx context.Context, req FeedRequest) ([]SearchResult, error)
}

// effectiveFeedMode resolves the feed mode for a Cluster call, bridging the
// deprecated FollowFeed flag: an explicit FeedMode wins; otherwise FollowFeed
// maps to FeedLightweight; otherwise FeedNone.
func (o ClusterOptions) effectiveFeedMode() FeedMode {
	if o.FeedMode != FeedNone {
		return o.FeedMode
	}
	if o.FollowFeed {
		return FeedLightweight
	}
	return FeedNone
}

// paginateBrowser adapts a FeedPaginator into Cluster's result flow: it builds a
// FeedRequest from the already-fetched page, invokes the paginator, then merges
// the harvested apps into the initial grid.
//
// Merge semantics mirror paginateQnKhOb: dedup by AppID, and keep the initial
// grid's (richer) record when both sources carry the same app, since the browser
// scroll yields only thin AppID/URL entries. New apps are appended in discovery
// order, and the combined slice is capped at limit.
func (c *Client) paginateBrowser(ctx context.Context, results []SearchResult, pageHTML []byte, opts ClusterOptions, limit int) ([]SearchResult, error) {
	req := FeedRequest{
		URL:      withLangCountry(absoluteURL(opts.Path), opts.Lang, opts.Country),
		Lang:     opts.Lang,
		Country:  opts.Country,
		Limit:    limit,
		PageHTML: pageHTML,
	}

	harvested, err := opts.FeedPaginator.PaginateFeed(ctx, req)
	if err != nil {
		return results, err
	}

	seen := make(map[string]bool, len(results))
	for _, r := range results {
		seen[r.AppID] = true
	}

	for _, r := range harvested {
		if len(results) >= limit {
			break
		}
		if r.AppID == "" || seen[r.AppID] {
			continue // skip blanks and apps the rich initial grid already has
		}
		seen[r.AppID] = true
		results = append(results, r)
	}
	return results, nil
}
