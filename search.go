package googleplayscraper

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// searchMaxNum is the largest number of results Search will return. Higher
// requested values are clamped to it rather than rejected.
const searchMaxNum = 250

// SearchOptions configures the search request
type SearchOptions struct {
	Term    string
	Lang    string
	Country string
	// Num caps the number of results returned. Default 20. Values above 250
	// are clamped to 250 (searchMaxNum).
	Num        int
	Price      string // "free", "paid", "all"
	FullDetail bool
}

// SearchResult is the compact app model returned by search, list, cluster and
// developer endpoints. It is a subset of App with no detail-page fields.
type SearchResult struct {
	// AppID is the app package name.
	AppID string `json:"appId" example:"com.king.candycrushsaga"`
	// Title is the localized app name.
	Title string `json:"title" example:"Candy Crush Saga"`
	// URL is the canonical Play Store listing URL.
	URL string `json:"url" format:"uri" example:"https://play.google.com/store/apps/details?id=com.king.candycrushsaga"`
	// Icon is the app icon URL.
	Icon string `json:"icon" format:"uri" example:"https://play-lh.googleusercontent.com/...=s64"`
	// Developer is the developer's display name.
	Developer string `json:"developer" example:"King"`
	// DeveloperID is the developer listing id.
	DeveloperID string `json:"developerId" example:"King"`
	// Currency is the ISO 4217 code for Price; often empty in compact results.
	Currency string `json:"currency" example:""`
	// Price is the purchase price in major currency units; 0 for free apps.
	Price float64 `json:"price" minimum:"0" example:"0"`
	// Free is true when Price == 0.
	Free bool `json:"free" example:"true"`
	// Summary is the short tagline; may contain HTML entities as served.
	Summary string `json:"summary"`
	// ScoreText is the rounded, one-decimal rating label, e.g. "4.6".
	ScoreText string `json:"scoreText" example:"4.5"`
	// Score is the average star rating, a float in [0,5] at full precision.
	Score float64 `json:"score" minimum:"0" maximum:"5" example:"4.5100965"`
}

// Search searches for apps on Google Play
func (c *Client) Search(ctx context.Context, opts SearchOptions) ([]SearchResult, error) {
	if opts.Term == "" {
		return nil, fmt.Errorf("search term is required")
	}

	if opts.Lang == "" {
		opts.Lang = "en"
	}
	if opts.Country == "" {
		opts.Country = "us"
	}
	if opts.Num == 0 {
		opts.Num = 20
	}
	if opts.Num > searchMaxNum {
		opts.Num = searchMaxNum
	}

	price := getPriceValue(opts.Price)
	searchURL := fmt.Sprintf("%s/store/search?q=%s&hl=%s&gl=%s&price=%d&c=apps",
		BaseURL, url.QueryEscape(opts.Term), opts.Lang, opts.Country, price)

	body, err := c.get(ctx, searchURL)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	results, token, err := parseSearchPage(body, opts.Num)
	if err != nil {
		return nil, err
	}

	// Fetch more results if needed
	for len(results) < opts.Num && token != "" {
		moreResults, nextToken, err := c.fetchMoreApps(ctx, token, opts.Lang, opts.Country)
		if err != nil || len(moreResults) == 0 {
			break
		}
		results = append(results, moreResults...)
		token = nextToken
	}

	// Trim to requested number
	if len(results) > opts.Num {
		results = results[:opts.Num]
	}

	// Fetch full details if requested
	if opts.FullDetail {
		return c.enrichSearchResults(ctx, results, opts.Lang, opts.Country)
	}

	return results, nil
}

func getPriceValue(price string) int {
	switch strings.ToLower(price) {
	case "free":
		return 1
	case "paid":
		return 2
	default:
		return 0 // all
	}
}

func parseSearchPage(body []byte, num int) ([]SearchResult, string, error) {
	dataBlocks := parseDataBlocks(body)
	return extractSearchResults(dataBlocks)
}

func extractSearchResults(data map[string]interface{}) ([]SearchResult, string, error) {
	var results []SearchResult
	var token string

	// Try ds:4 first (search), ds:3 (developer), then ds:1
	var appsData interface{}
	if ds4, ok := data["ds:4"]; ok {
		appsData = ds4
	} else if ds3, ok := data["ds:3"]; ok {
		appsData = ds3
	} else if ds1, ok := data["ds:1"]; ok {
		appsData = ds1
	} else {
		return results, "", nil
	}

	// Navigate to apps: [0][1][0][0][0] or variations
	// Developer pages: [0][1][0][22][0]
	// Search pages: [0][1][0][0][0]
	paths := [][]int{
		{0, 1, 0, 22, 0}, // developer pages
		{0, 1, 0, 21, 0},
		{0, 1, 0, 0, 0}, // search pages
	}

	var apps []interface{}
	for _, path := range paths {
		if section := getPath(appsData, path...); section != nil {
			if arr, ok := section.([]interface{}); ok && len(arr) > 0 {
				// Verify this looks like apps data
				apps = arr
				break
			}
		}
	}

	if apps == nil {
		// Try to find apps by scanning structure
		apps = findAppsInData(appsData)
	}

	for _, app := range apps {
		result := parseSearchResultNew(app)
		if result.AppID != "" {
			results = append(results, result)
		}
	}

	// Get token for pagination
	if tokenData := getPath(appsData, 0, 1, 0, 0, 3, 0); tokenData != nil {
		token = toString(tokenData)
	}

	return results, token, nil
}

// findAppsInData recursively searches for the apps array in data, returning the
// candidate that yields the most app entries. Google Play search pages nest a
// short "did you mean"/featured array alongside the full results grid; picking
// the first match by depth-first order can land on the short one, so we compare
// all candidates and keep the largest.
func findAppsInData(data interface{}) []interface{} {
	best := bestAppsArray(data)
	if best == nil {
		return nil
	}
	return best
}

func bestAppsArray(data interface{}) []interface{} {
	arr, ok := data.([]interface{})
	if !ok {
		return nil
	}

	var best []interface{}
	bestCount := 0

	// A candidate is an array whose direct children look like app entries.
	count := 0
	for _, item := range arr {
		if itemArr, ok := item.([]interface{}); ok && hasAppIdPattern(itemArr) {
			count++
		}
	}
	if count > 0 {
		best, bestCount = arr, count
	}

	// Still recurse: a larger results grid may live deeper than a small
	// inline candidate at this level.
	for _, item := range arr {
		if cand := bestAppsArray(item); cand != nil {
			candCount := 0
			for _, e := range cand {
				if ea, ok := e.([]interface{}); ok && hasAppIdPattern(ea) {
					candCount++
				}
			}
			if candCount > bestCount {
				best, bestCount = cand, candCount
			}
		}
	}

	return best
}

func hasAppIdPattern(arr []interface{}) bool {
	// Check common positions for appId
	// Search: [0][0] = "com.xxx"
	// Developer wrapped: [0][0][0][0] = "com.xxx" (because each app is [[app_data]])
	paths := [][]int{
		{0, 0},
		{0, 0, 0},
		{0, 0, 0, 0}, // developer pages have extra wrapping
		{12, 0},
	}
	for _, path := range paths {
		val := getPath(arr, path...)
		if val == nil {
			continue
		}
		if s, ok := val.(string); ok {
			if len(s) > 3 && (hasPackagePrefix(s)) {
				return true
			}
		}
	}
	return false
}

func hasPackagePrefix(s string) bool {
	prefixes := []string{"com.", "org.", "io.", "me.", "net.", "app.", "dev."}
	for _, p := range prefixes {
		if len(s) > len(p) && s[:len(p)] == p {
			return true
		}
	}
	return false
}

// parseSearchResultNew handles the new data format
func parseSearchResultNew(item interface{}) SearchResult {
	arr, ok := item.([]interface{})
	if !ok {
		return SearchResult{}
	}

	// Each item might be wrapped: [[actual_app_data]]
	// Unwrap if needed
	if len(arr) == 1 {
		if inner, ok := arr[0].([]interface{}); ok {
			arr = inner
		}
	}

	result := SearchResult{}

	// AppID: Try multiple paths
	// Format 1: [0][0] is array like ["com.xxx", 7]
	// Format 2: [0][0][0] for developer pages (after unwrap)
	appIDPaths := [][]int{
		{0, 0, 0, 0}, // developer page format with extra wrap
		{0, 0, 0},    // developer page format
		{0, 0},       // search page format
	}
	for _, path := range appIDPaths {
		if v := getPath(arr, path...); v != nil {
			s := toString(v)
			if hasPackagePrefix(s) {
				result.AppID = s
				break
			}
		}
	}

	// Title: [3]
	if v := getPath(arr, 3); v != nil {
		result.Title = toString(v)
	}

	// Icon: Try multiple paths
	iconPaths := [][]int{
		{1, 3, 2},
		{0, 1, 3, 2},
	}
	for _, path := range iconPaths {
		if v := getPath(arr, path...); v != nil {
			if s := toString(v); s != "" {
				result.Icon = s
				break
			}
		}
	}

	// Developer: [14]
	if v := getPath(arr, 14); v != nil {
		result.Developer = toString(v)
	}

	// Score: [4][1]
	if v := getPath(arr, 4, 1); v != nil {
		result.Score = toFloat64(v)
	}

	// ScoreText: [4][0]
	if v := getPath(arr, 4, 0); v != nil {
		result.ScoreText = toString(v)
	}

	// Free by default
	result.Free = true

	// URL
	if result.AppID != "" {
		result.URL = fmt.Sprintf("%s/store/apps/details?id=%s", BaseURL, result.AppID)
	}

	return result
}

func parseSearchResult(item interface{}) SearchResult {
	arr, ok := item.([]interface{})
	if !ok {
		return SearchResult{}
	}

	result := SearchResult{}

	// Title: [2]
	if v := getPath(arr, 2); v != nil {
		result.Title = toString(v)
	}

	// AppID: [12][0]
	if v := getPath(arr, 12, 0); v != nil {
		result.AppID = toString(v)
	}

	// URL: [9][4][2]
	if v := getPath(arr, 9, 4, 2); v != nil {
		path := toString(v)
		if path != "" {
			result.URL = BaseURL + path
		}
	}

	// Icon: [1][1][0][3][2]
	if v := getPath(arr, 1, 1, 0, 3, 2); v != nil {
		result.Icon = toString(v)
	}

	// Developer: [4][0][0][0]
	if v := getPath(arr, 4, 0, 0, 0); v != nil {
		result.Developer = toString(v)
	}

	// DeveloperID: [4][0][0][1][4][2]
	if v := getPath(arr, 4, 0, 0, 1, 4, 2); v != nil {
		link := toString(v)
		if strings.Contains(link, "?id=") {
			parts := strings.Split(link, "?id=")
			if len(parts) > 1 {
				result.DeveloperID = parts[1]
			}
		}
	}

	// Currency: [7][0][3][2][1][0][1]
	if v := getPath(arr, 7, 0, 3, 2, 1, 0, 1); v != nil {
		result.Currency = toString(v)
	}

	// Price: [7][0][3][2][1][0][0]
	if v := getPath(arr, 7, 0, 3, 2, 1, 0, 0); v != nil {
		price := toFloat64(v)
		result.Price = price / 1000000
		result.Free = price == 0
	} else {
		result.Free = true
	}

	// Summary: [4][1][1][1][1]
	if v := getPath(arr, 4, 1, 1, 1, 1); v != nil {
		result.Summary = toString(v)
	}

	// ScoreText: [6][0][2][1][0]
	if v := getPath(arr, 6, 0, 2, 1, 0); v != nil {
		result.ScoreText = toString(v)
	}

	// Score: [6][0][2][1][1]
	if v := getPath(arr, 6, 0, 2, 1, 1); v != nil {
		result.Score = toFloat64(v)
	}

	return result
}

// enrichSearchResults replaces each result with full App() details, fetched by
// a pool of c.concurrency workers (default 1 — sequential). Output order
// matches input: workers write into a preallocated slice by index. If a single
// App() call fails, that slot keeps its original, un-enriched result — the same
// per-item fallback the sequential version used. The error return is reserved
// for future use and is currently always nil.
func (c *Client) enrichSearchResults(ctx context.Context, results []SearchResult, lang, country string) ([]SearchResult, error) {
	// Seed every slot with its original result, then overwrite in place with the
	// enriched version. Output order matches input (each worker owns slot i), and
	// any slot left untouched — because enrichOne hit an App() error, or because
	// ctx cancellation stopped the pool from dispatching that index — keeps its
	// original, un-enriched result rather than a zero value. fn never needs to
	// surface an error, so parallelIndexed's only return (ctx cancellation) is
	// intentionally ignored: enrich always yields a full-length slice.
	enriched := make([]SearchResult, len(results))
	copy(enriched, results)

	_ = parallelIndexed(ctx, len(results), c.concurrency, func(ctx context.Context, i int) {
		enriched[i] = c.enrichOne(ctx, results[i], lang, country)
	})

	return enriched, nil
}

// enrichOne fetches full details for a single result, falling back to the
// original result if the App() call fails.
func (c *Client) enrichOne(ctx context.Context, r SearchResult, lang, country string) SearchResult {
	app, err := c.App(ctx, r.AppID, AppOptions{
		Lang:    lang,
		Country: country,
	})
	if err != nil {
		return r
	}
	return SearchResult{
		AppID:       app.AppID,
		Title:       app.Title,
		URL:         app.URL,
		Icon:        app.Icon,
		Developer:   app.Developer,
		DeveloperID: app.DeveloperID,
		Currency:    app.Currency,
		Price:       app.Price,
		Free:        app.Free,
		Summary:     app.Summary,
		ScoreText:   app.ScoreText,
		Score:       app.Score,
	}
}
