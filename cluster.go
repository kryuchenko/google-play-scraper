package googleplayscraper

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// clusterPaginationGuard caps how many apps Cluster will collect when the
// caller asks for "everything" (Num == 0), protecting against runaway loops.
const clusterPaginationGuard = 5000

// ClusterInfo describes a single app cluster ("Popular apps", "New releases", …)
// found on a category or top-charts page.
type ClusterInfo struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// ClusterURLsOptions configures discovery of clusters for a category page.
type ClusterURLsOptions struct {
	// Category selects /store/apps/category/{Category}. When empty, the
	// /store/apps/top page is used as the cluster source instead.
	Category Category
	Lang     string
	Country  string
}

// ClusterOptions configures fetching the apps of a single cluster.
type ClusterOptions struct {
	// Path is the cluster page location, either an absolute Google Play URL
	// (as returned in ClusterInfo.URL) or a relative path.
	Path    string
	Lang    string
	Country string
	// Num caps the number of apps returned. Zero means "as many as Google
	// will paginate", bounded by clusterPaginationGuard. Values above 5000
	// (clusterPaginationGuard) are clamped to 5000.
	Num int
	// FeedMode selects how the page's "recommended for you" feed is followed
	// (see FeedMode for the trade-offs). The zero value, FeedNone, returns only
	// the initial grid. FeedBrowser additionally requires FeedPaginator.
	//
	// FeedMode supersedes FollowFeed. When FeedMode is left at FeedNone but
	// FollowFeed is true, it is treated as FeedLightweight for backward
	// compatibility (see effectiveFeedMode).
	FeedMode FeedMode

	// FeedPaginator supplies the browser-driven deep paginator used by
	// FeedBrowser. It is ignored in other modes. The implementation lives in the
	// lightfeed submodule so the root package stays dependency-free.
	FeedPaginator FeedPaginator

	// FollowFeed opts into extending the result set with the page's qnKhOb
	// recommendation topics (paginateQnKhOb). When set, each "recommended for
	// you" section on the cluster/category page is fetched as one extra request,
	// adding the apps it surfaces (verified live 2026-06-12: a GAME_ACTION
	// category page grows from ~18 to ~77 apps, GAME_PUZZLE to ~50, SOCIAL to
	// ~42, with no duplicates).
	//
	// It is OFF by default so a plain Cluster call stays a single request; the
	// cost is one extra request per recommendation section. It pays off on the
	// category landing page (/store/apps/category/{id}), where the feed adds
	// ~25-60 apps beyond the initial grid. On a plain collection/cluster page
	// (the URLs ClusterURLs returns) the feed tokens usually exist too, but every
	// app they return is already in that cluster's own grid, so the extra request
	// adds 0 unique apps. CategoryApps therefore leaves it off: its cluster sweep
	// already covers everything the feed would surface (verified live 2026-06-12).
	//
	// Deprecated: use FeedMode (FollowFeed:true == FeedMode:FeedLightweight).
	FollowFeed bool
}

// ClusterURLs returns the clusters advertised on a category or top-charts page.
func (c *Client) ClusterURLs(ctx context.Context, opts ClusterURLsOptions) ([]ClusterInfo, error) {
	if opts.Lang == "" {
		opts.Lang = "en"
	}
	if opts.Country == "" {
		opts.Country = "us"
	}

	var reqURL string
	if opts.Category == "" {
		reqURL = fmt.Sprintf("%s/store/apps/top?hl=%s&gl=%s", BaseURL, opts.Lang, opts.Country)
	} else {
		reqURL = fmt.Sprintf("%s/store/apps/category/%s?hl=%s&gl=%s", BaseURL, opts.Category, opts.Lang, opts.Country)
	}

	body, err := c.get(ctx, reqURL)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	return parseClusterURLs(body), nil
}

// parseClusterURLs scans every ds:* data block for cluster sections. Each
// section exposes its title at [21,1,0] and the cluster page URL at
// [21,1,2,4,2]. Sections without a cluster link (e.g. inlined app grids) are
// skipped.
func parseClusterURLs(body []byte) []ClusterInfo {
	html := string(body)
	matches := scriptDataRegex.FindAllStringSubmatch(html, -1)

	var clusters []ClusterInfo
	seen := make(map[string]bool)

	for _, match := range matches {
		if len(match) < 3 {
			continue
		}
		var data interface{}
		if err := json.Unmarshal([]byte(strings.TrimSpace(match[2])), &data); err != nil {
			continue
		}

		sections, ok := getPath(data, 0, 1).([]interface{})
		if !ok {
			continue
		}
		for _, section := range sections {
			path := toString(getPath(section, 21, 1, 2, 4, 2))
			if !strings.Contains(path, "/store/apps/collection/cluster") {
				continue
			}
			fullURL := absoluteURL(path)
			if seen[fullURL] {
				continue
			}
			seen[fullURL] = true
			clusters = append(clusters, ClusterInfo{
				Title: toString(getPath(section, 21, 1, 0)),
				URL:   fullURL,
			})
		}
	}

	return clusters
}

// Cluster fetches the apps listed in a single cluster, following pagination
// while results are available, up to opts.Num.
func (c *Client) Cluster(ctx context.Context, opts ClusterOptions) ([]SearchResult, error) {
	if opts.Path == "" {
		return nil, fmt.Errorf("cluster path is required")
	}
	if opts.Lang == "" {
		opts.Lang = "en"
	}
	if opts.Country == "" {
		opts.Country = "us"
	}

	limit := opts.Num
	if limit <= 0 || limit > clusterPaginationGuard {
		limit = clusterPaginationGuard
	}

	// Validate the feed configuration up front so a misconfigured FeedBrowser
	// call fails fast regardless of how many apps the initial grid happens to
	// hold (rather than silently succeeding when Num <= grid size).
	mode := opts.effectiveFeedMode()
	if mode == FeedBrowser && opts.FeedPaginator == nil {
		return nil, ErrFeedPaginatorRequired
	}

	body, err := c.get(ctx, withLangCountry(absoluteURL(opts.Path), opts.Lang, opts.Country))
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	results, err := parseClusterPage(body)
	if err != nil {
		return nil, err
	}

	if len(results) < limit {
		switch mode {
		case FeedLightweight:
			results = c.paginateQnKhOb(ctx, results, body, opts, limit)
		case FeedBrowser:
			results, err = c.paginateBrowser(ctx, results, body, opts, limit)
			if err != nil {
				return nil, fmt.Errorf("browser feed pagination failed: %w", err)
			}
		}
	}

	if opts.Num > 0 && len(results) > opts.Num {
		results = results[:opts.Num]
	}
	return results, nil
}

// paginateQnKhOb extends a cluster's results by fetching the page's qnKhOb
// recommendation topics, one request per topic.
//
// The tokens come from extractFeedTokens: every "recommended for you" section on
// the page exposes a recs_topic continuation token (rewrapped from the section's
// cluster gsr blob). Each token is stateless and fetches its topic's full app
// set in a single request — so we iterate the tokens rather than chasing a
// next-token chain. The deeper "next topic" pointer a response carries
// ([0][3][0]) is server-stateful and NULLs on replay, so it is ignored.
//
// fsid/bl are read from the already-downloaded page HTML (no extra GET).
// source-path is the cluster's own path, sans query string. Results are
// de-duplicated against the initial grid and across topics, since recommendation
// batches overlap. Pagination stops at limit, on the first request error, or
// when the topics are exhausted.
func (c *Client) paginateQnKhOb(ctx context.Context, results []SearchResult, pageHTML []byte, opts ClusterOptions, limit int) []SearchResult {
	tokens := extractFeedTokens(pageHTML)
	if len(tokens) == 0 {
		return results // no recommendation feed on this page
	}

	fsid, bl, ok := extractWizData(pageHTML)
	if !ok {
		return results // page lacks session metadata; can't form a valid request
	}

	params := qnkhobParams{
		lang:       opts.Lang,
		country:    opts.Country,
		sourcePath: clusterSourcePath(opts.Path),
		fsid:       fsid,
		bl:         bl,
	}

	seen := make(map[string]bool, len(results))
	for _, r := range results {
		seen[r.AppID] = true
	}

	for _, token := range tokens {
		if len(results) >= limit {
			break
		}
		more, _, err := c.fetchQnKhOb(ctx, token, params)
		if err != nil {
			break
		}
		for _, r := range more {
			if seen[r.AppID] {
				continue
			}
			seen[r.AppID] = true
			results = append(results, r)
		}
	}
	return results
}

// clusterSourcePath turns a cluster path or absolute URL into the source-path
// query value the qnKhOb RPC expects: the path component without its query
// string.
func clusterSourcePath(path string) string {
	if u, err := url.Parse(path); err == nil && u.Path != "" {
		return u.Path
	}
	if i := strings.IndexByte(path, '?'); i >= 0 {
		return path[:i]
	}
	return path
}

// parseClusterPage extracts the cluster's apps from the initial HTML page.
// Cluster pages place their apps at [0,1,0,21,0] in one of the ds:* data blocks;
// app entries share the search-page layout.
//
// It deliberately does NOT read the sibling [0,1,0,3,0] "continuation token":
// that token re-references the page's current topic and is answered with a NULL
// payload on replay. Feed continuation is driven instead by extractFeedTokens,
// which derives a working token per recommendation section from its cluster URL.
func parseClusterPage(body []byte) ([]SearchResult, error) {
	html := string(body)
	matches := scriptDataRegex.FindAllStringSubmatch(html, -1)

	for _, match := range matches {
		if len(match) < 3 {
			continue
		}
		var data interface{}
		if err := json.Unmarshal([]byte(strings.TrimSpace(match[2])), &data); err != nil {
			continue
		}

		apps, ok := getPath(data, 0, 1, 0, 21, 0).([]interface{})
		if !ok || len(apps) == 0 {
			continue
		}

		results := make([]SearchResult, 0, len(apps))
		for _, app := range apps {
			if r := parseSearchResultNew(app); r.AppID != "" {
				results = append(results, r)
			}
		}
		return results, nil
	}

	return nil, nil
}

func absoluteURL(path string) string {
	if path == "" || strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	return BaseURL + path
}

func withLangCountry(rawURL, lang, country string) string {
	sep := "?"
	if strings.Contains(rawURL, "?") {
		sep = "&"
	}
	return fmt.Sprintf("%s%shl=%s&gl=%s", rawURL, sep, lang, country)
}
