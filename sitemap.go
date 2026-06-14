package googleplayscraper

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"
)

// This file implements full-catalog enumeration via Google Play's public
// sitemaps — the one channel that lists the *entire* store rather than the
// commercially-visible ~200-per-request top of a category.
//
// The structure (verified live, snapshot 2026-06):
//
//	robots.txt
//	  Sitemap: https://play.google.com/sitemaps/sitemaps-index-0.xml   (shards 00000..49999)
//	  Sitemap: https://play.google.com/sitemaps/sitemaps-index-1.xml   (shards 50000..80944)
//	index-N.xml         <sitemapindex> -> <sitemap><loc> .../play_sitemaps_<date>_<run>-NNNNN-of-80945.xml.gz
//	shard.xml.gz        gzip -> <urlset> -> <url><loc> ...
//
// A shard is NOT apps-only: it interleaves books, movies, music and app URLs
// for the whole store. App listings are exactly the `/store/apps/details?id=PKG`
// locs; everything else is skipped. There are ~80945 shards of ~400 URLs each,
// of which ~30–55 are apps, so a full sweep yields on the order of 3 million
// app package ids.
//
// Rate limiting reuses the client's WithThrottle setting (every fetch goes
// through c.get); crawl parallelism is a per-call option, not a Client field,
// because the sitemap sweep is a different rate regime from normal API calls.

// sitemapIndexDoc / sitemapURLSetDoc mirror the sitemaps.org 0.9 schema. Only
// <loc> is read; alternate <xhtml:link> hreflang entries inside a <url> are
// ignored.
type sitemapIndexDoc struct {
	XMLName  xml.Name     `xml:"sitemapindex"`
	Sitemaps []sitemapLoc `xml:"sitemap"`
}

type sitemapLoc struct {
	Loc string `xml:"loc"`
}

type sitemapURLSetDoc struct {
	XMLName xml.Name     `xml:"urlset"`
	URLs    []sitemapLoc `xml:"url"`
}

// SitemapIndexURLs returns the sitemap-index URLs Google advertises in
// robots.txt (the `Sitemap:` directives). At the time of writing there are two,
// together covering all 80945 shards, but the count is read from robots.txt
// rather than hardcoded so it tracks Google's own advertisement.
func (c *Client) SitemapIndexURLs(ctx context.Context) ([]string, error) {
	body, err := c.get(ctx, BaseURL+"/robots.txt")
	if err != nil {
		return nil, fmt.Errorf("fetch robots.txt: %w", err)
	}

	var indexes []string
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		const prefix = "sitemap:"
		if len(line) < len(prefix) || !strings.EqualFold(line[:len(prefix)], prefix) {
			continue
		}
		if u := strings.TrimSpace(line[len(prefix):]); u != "" {
			indexes = append(indexes, u)
		}
	}
	if len(indexes) == 0 {
		return nil, fmt.Errorf("no Sitemap directives in robots.txt")
	}
	return indexes, nil
}

// SitemapShards fetches one sitemap-index and returns the shard URLs it lists
// (the per-shard `<loc>` values, typically `…-NNNNN-of-80945.xml.gz`).
func (c *Client) SitemapShards(ctx context.Context, indexURL string) ([]string, error) {
	body, err := c.get(ctx, indexURL)
	if err != nil {
		return nil, fmt.Errorf("fetch sitemap index %s: %w", indexURL, err)
	}

	var doc sitemapIndexDoc
	if err := xml.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("parse sitemap index %s: %w", indexURL, err)
	}

	shards := make([]string, 0, len(doc.Sitemaps))
	for _, s := range doc.Sitemaps {
		if loc := strings.TrimSpace(s.Loc); loc != "" {
			shards = append(shards, loc)
		}
	}
	return shards, nil
}

// AllSitemapShards discovers every shard URL across all advertised indexes,
// deduplicated and in discovery order. This is the full work-list for a catalog
// sweep (~80945 entries).
func (c *Client) AllSitemapShards(ctx context.Context) ([]string, error) {
	indexes, err := c.SitemapIndexURLs(ctx)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]struct{})
	var all []string
	for _, idx := range indexes {
		shards, err := c.SitemapShards(ctx, idx)
		if err != nil {
			return nil, err
		}
		for _, s := range shards {
			if _, dup := seen[s]; dup {
				continue
			}
			seen[s] = struct{}{}
			all = append(all, s)
		}
	}
	return all, nil
}

// SitemapShardPackages fetches one shard (gzip-decompressing it when needed) and
// returns the app package ids it lists, deduplicated within the shard and in
// document order. Non-app URLs (books, movies, music, …) are skipped.
func (c *Client) SitemapShardPackages(ctx context.Context, shardURL string) ([]string, error) {
	body, err := c.get(ctx, shardURL)
	if err != nil {
		return nil, fmt.Errorf("fetch shard %s: %w", shardURL, err)
	}

	body, err = gunzipIfNeeded(body)
	if err != nil {
		return nil, fmt.Errorf("decompress shard %s: %w", shardURL, err)
	}

	var doc sitemapURLSetDoc
	if err := xml.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("parse shard %s: %w", shardURL, err)
	}

	seen := make(map[string]struct{})
	var pkgs []string
	for _, u := range doc.URLs {
		pkg := appPackageFromLoc(u.Loc)
		if pkg == "" {
			continue
		}
		if _, dup := seen[pkg]; dup {
			continue
		}
		seen[pkg] = struct{}{}
		pkgs = append(pkgs, pkg)
	}
	return pkgs, nil
}

// CatalogOptions configures a full-catalog sweep.
type CatalogOptions struct {
	// Concurrency is how many shards are fetched in parallel. Default 1.
	// The client's WithThrottle still bounds the request rate across workers.
	Concurrency int

	// Shards restricts the sweep to these shard indices into the full shard
	// list (as returned by AllSitemapShards). nil/empty sweeps every shard.
	// Out-of-range indices are skipped. Useful for sampling or resuming a
	// crawl.
	Shards []int

	// OnShardError, when non-nil, is called for each shard that fails to fetch
	// or parse; the sweep continues with the remaining shards. nil ignores
	// shard errors. Called serially with the other callbacks.
	OnShardError func(shardIndex int, shardURL string, err error)

	// OnShardDone, when non-nil, is called after each shard is processed with
	// the number of package ids emitted from it — useful for progress
	// reporting. Called serially with the other callbacks.
	OnShardDone func(shardIndex int, shardURL string, packages int)
}

// EnumerateCatalog sweeps every sitemap shard and calls emit once per app
// package id discovered. It is the full-catalog counterpart to CategoryApps:
// where CategoryApps maximizes the commercially-visible layer of one category,
// EnumerateCatalog walks Google's own sitemap of the entire store.
//
// emit and the option callbacks are invoked serially (guarded by an internal
// lock), so a caller may append to a shared slice or insert into a shared map
// without its own synchronization. emit yields raw ids and does NOT
// deduplicate across shards — a package listed in two shards is emitted twice;
// deduplicate on the caller side if required (within a single shard ids are
// already unique).
//
// The sweep is cooperative-cancellable: when ctx is cancelled no further shards
// are dispatched and EnumerateCatalog returns ctx.Err() with the work done so
// far already emitted (a partial catalog). A single shard's fetch/parse failure
// never aborts the sweep — it is reported via OnShardError and skipped.
//
// This fetches tens of thousands of shards; run it with a sensible WithThrottle
// and Concurrency, and expect on the order of 3 million ids.
func (c *Client) EnumerateCatalog(ctx context.Context, emit func(pkg string), opts CatalogOptions) error {
	shards, err := c.AllSitemapShards(ctx)
	if err != nil {
		return fmt.Errorf("list shards: %w", err)
	}

	indices := opts.Shards
	if len(indices) == 0 {
		indices = make([]int, len(shards))
		for i := range indices {
			indices[i] = i
		}
	}

	var mu sync.Mutex
	return parallelIndexed(ctx, len(indices), opts.Concurrency, func(ctx context.Context, i int) {
		si := indices[i]
		if si < 0 || si >= len(shards) {
			return
		}
		shardURL := shards[si]

		pkgs, err := c.SitemapShardPackages(ctx, shardURL)
		if err != nil {
			if opts.OnShardError != nil {
				mu.Lock()
				opts.OnShardError(si, shardURL, err)
				mu.Unlock()
			}
			return
		}

		mu.Lock()
		for _, p := range pkgs {
			emit(p)
		}
		if opts.OnShardDone != nil {
			opts.OnShardDone(si, shardURL, len(pkgs))
		}
		mu.Unlock()
	})
}

// gunzipIfNeeded decompresses data when it carries the gzip magic header,
// otherwise returns it unchanged. Sitemap shards are served as .xml.gz files
// (the gzip is the resource, not an HTTP transfer encoding, so the transport
// does not transparently decode it), but indexes are plain XML — detecting the
// magic bytes handles both without assuming.
func gunzipIfNeeded(data []byte) ([]byte, error) {
	if len(data) < 2 || data[0] != 0x1f || data[1] != 0x8b {
		return data, nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return io.ReadAll(zr)
}

// appPackageFromLoc returns the package id of an app-listing sitemap loc, or ""
// for any other URL (books/movies/music or a malformed entry). It matches
// exactly the /store/apps/details?id=PKG path so sibling paths like
// /store/apps/dev or /store/apps/collection are not mistaken for apps.
func appPackageFromLoc(loc string) string {
	loc = strings.TrimSpace(loc)
	if loc == "" {
		return ""
	}
	u, err := url.Parse(loc)
	if err != nil {
		return ""
	}
	if u.Path != "/store/apps/details" {
		return ""
	}
	return u.Query().Get("id")
}
