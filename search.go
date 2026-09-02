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
	ctx, endTask := startTask(ctx, traceTaskSearch)
	defer endTask()

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

// fetchMoreApps requests the next page of search results via the qnKhOb RPC,
// using the legacy compact payload. It is Search's pagination primitive only:
// Cluster moved to fetchQnKhOb (qnkhob.go) with the current browser payload,
// since Google now rejects this older flag set on the category feed. It returns
// no results once Google reports the token exhausted.
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
	if apps, ok := getPath(data, 0, 0, 0).([]any); ok {
		for _, app := range apps {
			if r := parseSearchResult(app); r.AppID != "" {
				results = append(results, r)
			}
		}
	}

	nextToken := toString(getPath(data, 0, 0, 7, 1))
	return results, nextToken, nil
}

func parseSearchPage(body []byte, num int) ([]SearchResult, string, error) {
	dataBlocks := parseDataBlocks(body)
	return extractSearchResults(dataBlocks)
}

func extractSearchResults(data map[string]any) ([]SearchResult, string, error) {
	var results []SearchResult
	var token string

	// Try ds:4 first (search), ds:3 (developer), then ds:1
	var appsData any
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

	var apps []any
	for _, path := range paths {
		if section := getPath(appsData, path...); section != nil {
			if arr, ok := section.([]any); ok && len(arr) > 0 {
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
func findAppsInData(data any) []any {
	best := bestAppsArray(data)
	if best == nil {
		return nil
	}
	return best
}

func bestAppsArray(data any) []any {
	arr, ok := data.([]any)
	if !ok {
		return nil
	}

	var best []any
	bestCount := 0

	// A candidate is an array whose direct children look like app entries.
	count := 0
	for _, item := range arr {
		if itemArr, ok := item.([]any); ok && hasAppIDPattern(itemArr) {
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
				if ea, ok := e.([]any); ok && hasAppIDPattern(ea) {
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

func hasAppIDPattern(arr []any) bool {
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

// searchGridPaths is the search/cluster grid row layout. Rows may arrive wrapped
// ([[actual]]) and carry no price node (the grid never prices apps, so rows stay
// free). The appID slot can hold non-package noise across the page variants, so
// candidates are gated on a package-name prefix (requireAppID).
var searchGridPaths = rowPaths{
	unwrapSingleton: true,
	requireAppID:    true,
	appID: [][]int{
		{0, 0, 0, 0}, // developer page format with extra wrap
		{0, 0, 0},    // developer page format
		{0, 0},       // search page format
	},
	title:     [][]int{{3}},
	icon:      [][]int{{1, 3, 2}, {0, 1, 3, 2}},
	developer: [][]int{{14}},
	score:     [][]int{{4, 1}},
	scoreText: [][]int{{4, 0}},
}

// parseSearchResultNew handles the search/cluster grid data format.
func parseSearchResultNew(item any) SearchResult {
	return decodeResultRow(item, searchGridPaths)
}

// qnKhObRowPaths is the legacy qnKhOb feed-pagination row layout — a distinct
// shape from the grid: it carries a ready-made URL path, a developer link (the
// only layout that surfaces DeveloperID), a deep price tuple and summary.
var qnKhObRowPaths = rowPaths{
	appID:           [][]int{{12, 0}},
	title:           [][]int{{2}},
	icon:            [][]int{{1, 1, 0, 3, 2}},
	developer:       [][]int{{4, 0, 0, 0}},
	developerIDLink: [][]int{{4, 0, 0, 1, 4, 2}},
	summary:         [][]int{{4, 1, 1, 1, 1}},
	score:           [][]int{{6, 0, 2, 1, 1}},
	scoreText:       [][]int{{6, 0, 2, 1, 0}},
	currency:        [][]int{{7, 0, 3, 2, 1, 0, 1}},
	price:           [][]int{{7, 0, 3, 2, 1, 0, 0}},
	urlPath:         [][]int{{9, 4, 2}},
	urlPathOnly:     true,
}

func parseSearchResult(item any) SearchResult {
	return decodeResultRow(item, qnKhObRowPaths)
}

// enrichSearchResults replaces each result with full app details.
//
// This used to fetch one HTML page per result, in parallel: for a listing of
// 32 that is 32 requests and about thirty megabytes of markup, and List
// accepts Num up to 660. It now asks for them in batches over the RPC the
// details page is itself built from, which is the same data at 32 apps per
// request.
//
// Measured against the live store at a 300ms throttle, a 32-result listing
// with FullDetail went from 9.73s over 33 requests to 0.44s over 2. The
// throttle meters requests, so the ratio is the point; parallelism is not,
// and WithConcurrency no longer has anything to do here.
//
// Output order matches input, and a result whose app could not be fetched
// keeps its original un-enriched value -- the same per-item fallback as
// before, so a single missing app never costs the listing.
func (c *Client) enrichSearchResults(ctx context.Context, results []SearchResult, lang, country string) ([]SearchResult, error) {
	enriched := make([]SearchResult, len(results))
	copy(enriched, results)

	ids := make([]string, len(results))
	for i, r := range results {
		ids[i] = r.AppID
	}

	for i, got := range c.AppsMany(ctx, ids, AppOptions{Lang: lang, Country: country}) {
		if got.Err != nil || got.App == nil {
			continue
		}
		enriched[i] = searchResultFromApp(got.App)
	}
	return enriched, nil
}

// searchResultFromApp narrows a full App to the fields a SearchResult carries.
func searchResultFromApp(app *App) SearchResult {
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
