package googleplayscraper

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// SearchOptions configures the search request
type SearchOptions struct {
	Term       string
	Lang       string
	Country    string
	Num        int
	Price      string // "free", "paid", "all"
	FullDetail bool
}

// SearchResult represents a search result item
type SearchResult struct {
	AppID       string  `json:"appId"`
	Title       string  `json:"title"`
	URL         string  `json:"url"`
	Icon        string  `json:"icon"`
	Developer   string  `json:"developer"`
	DeveloperID string  `json:"developerId"`
	Currency    string  `json:"currency"`
	Price       float64 `json:"price"`
	Free        bool    `json:"free"`
	Summary     string  `json:"summary"`
	ScoreText   string  `json:"scoreText"`
	Score       float64 `json:"score"`
}

// Search searches for apps on Google Play
func (c *Client) Search(ctx context.Context, opts SearchOptions) ([]SearchResult, error) {
	if opts.Term == "" {
		return nil, fmt.Errorf("search term is required")
	}

	if opts.Num > 250 {
		return nil, fmt.Errorf("number of results can't exceed 250")
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
		moreResults, nextToken, err := c.fetchMoreSearchResults(ctx, token, opts)
		if err != nil {
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
	html := string(body)

	// Find data blocks
	dataBlocks := make(map[string]interface{})
	matches := scriptDataRegex.FindAllStringSubmatch(html, -1)

	for _, match := range matches {
		if len(match) < 3 {
			continue
		}
		key := match[1]
		dataStr := strings.TrimSpace(match[2])

		var data interface{}
		if err := json.Unmarshal([]byte(dataStr), &data); err != nil {
			continue
		}
		dataBlocks[key] = data
	}

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
		{0, 1, 0, 0, 0},  // search pages
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

// findAppsInData recursively searches for apps array in data
func findAppsInData(data interface{}) []interface{} {
	arr, ok := data.([]interface{})
	if !ok {
		return nil
	}

	// Check if this looks like an apps array (has appId-like structures)
	for _, item := range arr {
		if itemArr, ok := item.([]interface{}); ok {
			// Look for com.* pattern in nested arrays indicating appId
			if hasAppIdPattern(itemArr) {
				return arr
			}
		}
	}

	// Recurse into nested arrays
	for _, item := range arr {
		if result := findAppsInData(item); result != nil {
			return result
		}
	}

	return nil
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

func (c *Client) fetchMoreSearchResults(ctx context.Context, token string, opts SearchOptions) ([]SearchResult, string, error) {
	// Use batchexecute for pagination
	payload := fmt.Sprintf(`[[["qnKhOb","[[null,[[10,[10,50]],true,null,[96,27,4,8,57,30,110,79,11,16,49,1,3,9,12,104,55,56,51,10,34,77]],[null,\"%s\"]]",null,"generic"]]]`, token)

	reqURL := fmt.Sprintf("%s/_/PlayStoreUi/data/batchexecute?hl=%s&gl=%s", BaseURL, opts.Lang, opts.Country)
	body, err := c.post(ctx, reqURL, "application/x-www-form-urlencoded", "f.req="+url.QueryEscape(payload))
	if err != nil {
		return nil, "", err
	}

	return parseSearchBatchResponse(body)
}

func parseSearchBatchResponse(body []byte) ([]SearchResult, string, error) {
	// Skip the )]}'  prefix
	start := 0
	for i := range body {
		if body[i] == '\n' {
			start = i + 1
			break
		}
	}

	if start >= len(body) {
		return nil, "", fmt.Errorf("invalid response")
	}

	var outer [][]interface{}
	if err := json.Unmarshal(body[start:], &outer); err != nil {
		return nil, "", err
	}

	if len(outer) == 0 || len(outer[0]) < 3 {
		return nil, "", nil
	}

	dataStr, ok := outer[0][2].(string)
	if !ok {
		return nil, "", nil
	}

	var data []interface{}
	if err := json.Unmarshal([]byte(dataStr), &data); err != nil {
		return nil, "", err
	}

	var results []SearchResult
	var nextToken string

	// Apps in data[0][0][0]
	if apps := getPath(data, 0, 0, 0); apps != nil {
		if appsArr, ok := apps.([]interface{}); ok {
			for _, app := range appsArr {
				result := parseSearchResult(app)
				if result.AppID != "" {
					results = append(results, result)
				}
			}
		}
	}

	// Token in data[0][0][7][1]
	if t := getPath(data, 0, 0, 7, 1); t != nil {
		nextToken = toString(t)
	}

	return results, nextToken, nil
}

func (c *Client) enrichSearchResults(ctx context.Context, results []SearchResult, lang, country string) ([]SearchResult, error) {
	enriched := make([]SearchResult, len(results))
	for i, r := range results {
		app, err := c.App(ctx, r.AppID, AppOptions{
			Lang:    lang,
			Country: country,
		})
		if err != nil {
			// Keep original result if enrichment fails
			enriched[i] = r
			continue
		}
		// Convert App to SearchResult with full details
		enriched[i] = SearchResult{
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
	return enriched, nil
}
