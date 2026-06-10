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

	body, err := c.get(ctx, withLangCountry(absoluteURL(opts.Path), opts.Lang, opts.Country))
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	results, token, err := parseClusterPage(body)
	if err != nil {
		return nil, err
	}

	for len(results) < limit && token != "" {
		more, next, err := c.fetchMoreApps(ctx, token, opts.Lang, opts.Country)
		if err != nil || len(more) == 0 {
			break
		}
		results = append(results, more...)
		token = next
	}

	if opts.Num > 0 && len(results) > opts.Num {
		results = results[:opts.Num]
	}
	return results, nil
}

// parseClusterPage extracts the cluster's apps and its pagination token from
// the initial HTML page. Cluster pages place their apps at [0,1,0,21,0] in one
// of the ds:* data blocks, with the continuation token at the sibling
// [0,1,0,3,0]. App entries share the search-page layout.
func parseClusterPage(body []byte) ([]SearchResult, string, error) {
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
		token := toString(getPath(data, 0, 1, 0, 3, 0))
		return results, token, nil
	}

	return nil, "", nil
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

// fetchMoreApps requests the next page of a list/cluster via the qnKhOb RPC.
// It is the shared pagination primitive used by both search and cluster
// listings. It returns no results once Google reports the token exhausted.
func (c *Client) fetchMoreApps(ctx context.Context, token, lang, country string) ([]SearchResult, string, error) {
	payload := fmt.Sprintf(
		`[[["qnKhOb","[[null,[[10,[10,50]],true,null,[96,27,4,8,57,30,110,79,11,16,49,1,3,9,12,104,55,56,51,10,34,77]],[null,\"%s\"]]",null,"generic"]]]`,
		token,
	)

	reqURL := fmt.Sprintf("%s/_/PlayStoreUi/data/batchexecute?rpcids=qnKhOb&hl=%s&gl=%s", BaseURL, lang, country)
	body, err := c.post(ctx, reqURL, "application/x-www-form-urlencoded;charset=UTF-8", "f.req="+url.QueryEscape(payload))
	if err != nil {
		return nil, "", err
	}

	data, err := decodeBatchEnvelope(body)
	if err != nil || data == nil {
		return nil, "", err
	}

	var results []SearchResult
	if apps, ok := getPath(data, 0, 0, 0).([]interface{}); ok {
		for _, app := range apps {
			if r := parseSearchResult(app); r.AppID != "" {
				results = append(results, r)
			}
		}
	}

	nextToken := toString(getPath(data, 0, 0, 7, 1))
	return results, nextToken, nil
}
