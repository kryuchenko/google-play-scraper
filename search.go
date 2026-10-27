package googleplayscraper

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// searchMaxNum is the largest number of results Search will return. Higher
// requested values are clamped to it rather than rejected.
//
// It was 250, from an older API. The web store no longer pages a query: the
// search page shows about 20 results, and scrolling loads related queries, not
// more of this one. The lGYRle RPC behind the page takes a page size, and
// returns up to about 50; at 60 it returns nothing (issue #16).
const searchMaxNum = 50

// searchPageSize is how many results the search page itself carries.
const searchPageSize = 20

// searchPayloadTemplate is the lGYRle request body a browser sends for a search
// result block, captured from play.google.com on 2026-10-27, with the query
// and the page size replaced by __QUERY__ (a JSON string) and __SIZE__. It
// needs no session parameters: rpcids, hl and gl are enough.
//
//go:embed search_payload.txt
var searchPayloadTemplate string

// SearchOptions configures the search request
type SearchOptions struct {
	Term    string
	Lang    string
	Country string
	// Num caps the number of results returned. Default 20. Values above 50
	// are clamped to 50 (searchMaxNum): that is as many as Google returns for
	// one query. Above 20, results come from one lGYRle RPC rather than the
	// search page, except with a Price filter, which only the page applies.
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

	// The page is parsed once, inside the check: whether it holds results is
	// exactly what tells a search with no matches from a page that is not a
	// search at all.
	var results []SearchResult
	var token string
	_, err := c.getPage(ctx, searchURL, func(body []byte) (Verdict, string) {
		results, token, _ = parseSearchPage(body, opts.Num)
		return classifySearchPage(body, results)
	})
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	_ = token // the page's continuation token; nothing pages a query any more

	// More than a page: ask the RPC for the whole list at once and merge it
	// into the page's results. A failed call keeps the page's results --
	// unless Google is refusing the client, which the caller has to hear
	// about rather than mistake for a short list.
	if opts.Num > len(results) && opts.Num > searchPageSize && price == 0 && len(results) > 0 {
		more, err := c.searchRPC(ctx, opts)
		if isBlockErr(err) {
			return nil, fmt.Errorf("more results: %w", err)
		}
		if err == nil {
			results = mergeSearchResults(results, more)
		}
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

// classifySearchPage judges a search page from what its parser found. Results
// mean ok; none, with Google's zero-results section present, mean a search that
// matched nothing; none and no such section mean the page is not the search
// the parser knows.
func classifySearchPage(body []byte, results []SearchResult) (Verdict, string) {
	if len(results) > 0 {
		return VerdictOK, ""
	}
	if v, reason := validateHTMLPage(body); v != VerdictOK {
		return v, reason
	}
	if ds4, ok := dataBlock(body, "ds:4"); ok && isZeroResultsSearch(ds4) {
		return VerdictEmpty, ""
	}
	return VerdictMalformed, "search-without-results-or-marker"
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

// mergeSearchResults keeps the page's results in the page's order, fills in
// fields the RPC knows and the page row did not (the RPC's featured card
// carries the developer link, for one), and appends the RPC's other results in
// the RPC's order. The two lists mostly agree, but the RPC can order them
// differently, and neither is guaranteed to contain the other.
func mergeSearchResults(page, more []SearchResult) []SearchResult {
	byID := make(map[string]SearchResult, len(more))
	for _, r := range more {
		if _, seen := byID[r.AppID]; !seen {
			byID[r.AppID] = r
		}
	}
	out := make([]SearchResult, 0, len(page)+len(more))
	have := make(map[string]bool, len(page)+len(more))
	for _, r := range page {
		if m, ok := byID[r.AppID]; ok {
			r = fillSearchResult(r, m)
		}
		out = append(out, r)
		have[r.AppID] = true
	}
	for _, r := range more {
		if !have[r.AppID] {
			out = append(out, r)
			have[r.AppID] = true
		}
	}
	return out
}

// fillSearchResult copies into r every field r lacks and m has.
func fillSearchResult(r, m SearchResult) SearchResult {
	for _, f := range []struct{ dst, src *string }{
		{&r.Title, &m.Title}, {&r.URL, &m.URL}, {&r.Icon, &m.Icon}, {&r.Developer, &m.Developer},
		{&r.DeveloperID, &m.DeveloperID}, {&r.Currency, &m.Currency}, {&r.Summary, &m.Summary},
		{&r.ScoreText, &m.ScoreText},
	} {
		if *f.dst == "" {
			*f.dst = *f.src
		}
	}
	if r.Score == 0 {
		r.Score = m.Score
	}
	if r.Price == 0 && m.Price != 0 {
		r.Price, r.Free = m.Price, m.Free
	}
	return r
}

// searchRPC fetches up to opts.Num results for a query with one lGYRle call.
//
// The response carries the same section layout as the search page's ds:4
// block, so extractSearchResults parses it unchanged. The layout varies by
// storefront: in the US the grid is a [22] card after the featured app, in
// Germany a [21] card after a small cluster; extractSearchResults keeps the
// largest app list, which is the grid in both.
func (c *Client) searchRPC(ctx context.Context, opts SearchOptions) ([]SearchResult, error) {
	q, err := json.Marshal(opts.Term)
	if err != nil {
		return nil, err
	}
	payload := strings.NewReplacer(
		"__QUERY__", string(q),
		"__SIZE__", strconv.Itoa(opts.Num),
	).Replace(searchPayloadTemplate)

	frames, err := c.batchCallFrames(ctx, opts.Lang, opts.Country, []rpcCall{{id: "lGYRle", payload: payload}})
	if err != nil {
		return nil, err
	}
	if len(frames) == 0 || frames[0].Payload == "" {
		return nil, nil
	}
	var data any
	if err := json.Unmarshal([]byte(frames[0].Payload), &data); err != nil {
		return nil, fmt.Errorf("parse lGYRle: %w", err)
	}
	results, _, err := extractSearchResults(map[string]any{"ds:4": data})
	return results, err
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

	// The featured card sits outside the grid, above it, so it goes first.
	if featured, ok := parseFeaturedResult(appsData); ok {
		results = append(results, featured)
	}

	for _, app := range apps {
		result := parseSearchResultNew(app)
		if result.AppID != "" && !containsAppID(results, result.AppID) {
			results = append(results, result)
		}
	}

	// Get token for pagination
	if tokenData := getPath(appsData, 0, 1, 0, 0, 3, 0); tokenData != nil {
		token = toString(tokenData)
	}

	return results, token, nil
}

// featuredRowPaths is the featured card Google shows above the search grid when
// one app matches the query outright. The card is a full app-details block, not
// a grid row, so its fields sit where the details page keeps them. It carries
// no bare app id; parseFeaturedResult takes it from the listing URL.
var featuredRowPaths = rowPaths{
	title:           [][]int{{0, 0}},
	icon:            [][]int{{95, 0, 3, 2}},
	developer:       [][]int{{68, 0}},
	developerIDLink: [][]int{{68, 1, 4, 2}},
	summary:         [][]int{{73, 0, 1}},
	score:           [][]int{{51, 0, 1}},
	scoreText:       [][]int{{51, 0, 0}},
	currency:        [][]int{{57, 0, 0, 0, 0, 1, 0, 1}},
	price:           [][]int{{57, 0, 0, 0, 0, 1, 0, 0}},
}

// parseFeaturedResult returns the featured card of a search page, or false
// when the page has none. The card is the first section in the US but not
// everywhere (in Germany it follows a small cluster), so every section is
// looked at.
func parseFeaturedResult(appsData any) (SearchResult, bool) {
	sections, _ := getPath(appsData, 0, 1).([]any)
	var block any
	for _, s := range sections {
		if b := getPath(s, 23, 16, 2); b != nil {
			block = b
			break
		}
	}
	if block == nil {
		return SearchResult{}, false
	}
	r := decodeResultRow(block, featuredRowPaths)
	r.AppID = appIDFromDetailsURL(toString(getPath(block, 41, 0, 2)))
	if r.AppID == "" {
		return SearchResult{}, false
	}
	r.URL = resultURL("", r.AppID, false)
	return r, true
}

// appIDFromDetailsURL extracts the id from a ".../store/apps/details?id=APP"
// URL, returning "" when there is none.
func appIDFromDetailsURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || !strings.HasSuffix(u.Path, "/store/apps/details") {
		return ""
	}
	return u.Query().Get("id")
}

func containsAppID(results []SearchResult, appID string) bool {
	for _, r := range results {
		if r.AppID == appID {
			return true
		}
	}
	return false
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

// qnKhObRowPaths is the qnKhOb feed row layout — a distinct
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
		if isBlockErr(got.Err) {
			return nil, fmt.Errorf("full detail: %w", got.Err)
		}
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
