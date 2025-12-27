package googleplayscraper

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// AppOptions configures the app details request
type AppOptions struct {
	Lang    string
	Country string
}

// App fetches application details
func (c *Client) App(ctx context.Context, appID string, opts AppOptions) (*App, error) {
	if appID == "" {
		return nil, fmt.Errorf("appID is required")
	}

	if opts.Lang == "" {
		opts.Lang = "en"
	}
	if opts.Country == "" {
		opts.Country = "us"
	}

	url := fmt.Sprintf("%s/store/apps/details?id=%s&hl=%s&gl=%s", BaseURL, appID, opts.Lang, opts.Country)

	body, err := c.get(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	return parseAppPage(body, appID, url)
}

var scriptDataRegex = regexp.MustCompile(`AF_initDataCallback\(\{key:\s*'(ds:\d+)'.*?data:(.*?), sideChannel:`)

func parseAppPage(body []byte, appID, pageURL string) (*App, error) {
	html := string(body)

	// Find all script data blocks
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

	return extractAppData(dataBlocks, appID, pageURL)
}

func extractAppData(data map[string]interface{}, appID, url string) (*App, error) {
	app := &App{
		AppID:     appID,
		URL:       url,
		Available: true,
	}

	// Main data is in ds:5
	ds5, ok := data["ds:5"]
	if !ok {
		return nil, fmt.Errorf("main data block not found")
	}

	// Navigate: [1][2] contains app info
	appData := getPath(ds5, 1, 2)
	if appData == nil {
		return nil, fmt.Errorf("app data not found")
	}

	// Title: [0][0]
	if v := getPath(appData, 0, 0); v != nil {
		app.Title = toString(v)
	}

	// Description HTML: [72][0][1]
	if v := getPath(appData, 72, 0, 1); v != nil {
		app.DescriptionHTML = toString(v)
		app.Description = stripHTML(app.DescriptionHTML)
	}

	// Summary: [73][0][1]
	if v := getPath(appData, 73, 0, 1); v != nil {
		app.Summary = toString(v)
	}

	// Installs: [13][0]
	if v := getPath(appData, 13, 0); v != nil {
		app.Installs = toString(v)
	}

	// MinInstalls: [13][1]
	if v := getPath(appData, 13, 1); v != nil {
		app.MinInstalls = toInt64(v)
	}

	// MaxInstalls: [13][2]
	if v := getPath(appData, 13, 2); v != nil {
		app.MaxInstalls = toInt64(v)
	}

	// Score: [51][0][1]
	if v := getPath(appData, 51, 0, 1); v != nil {
		app.Score = toFloat64(v)
	}

	// ScoreText: [51][0][0]
	if v := getPath(appData, 51, 0, 0); v != nil {
		app.ScoreText = toString(v)
	}

	// Ratings: [51][2][1]
	if v := getPath(appData, 51, 2, 1); v != nil {
		app.Ratings = toInt(v)
	}

	// Reviews count: [51][3][1]
	if v := getPath(appData, 51, 3, 1); v != nil {
		app.Reviews = toInt(v)
	}

	// Histogram: [51][1]
	if hist := getPath(appData, 51, 1); hist != nil {
		app.Histogram = extractHistogram(hist)
	}

	// Price: [57][0][0][0][0][1][0][0]
	if v := getPath(appData, 57, 0, 0, 0, 0, 1, 0, 0); v != nil {
		price := toFloat64(v)
		app.Price = price / 1000000
		app.Free = price == 0
	} else {
		app.Free = true
	}

	// Currency: [57][0][0][0][0][1][0][1]
	if v := getPath(appData, 57, 0, 0, 0, 0, 1, 0, 1); v != nil {
		app.Currency = toString(v)
	}

	// PriceText: [57][0][0][0][0][1][0][2]
	if v := getPath(appData, 57, 0, 0, 0, 0, 1, 0, 2); v != nil {
		app.PriceText = toString(v)
	}

	// Developer: [68][0]
	if v := getPath(appData, 68, 0); v != nil {
		app.Developer = toString(v)
	}

	// DeveloperID: [68][1][4][2]
	if v := getPath(appData, 68, 1, 4, 2); v != nil {
		app.DeveloperID = toString(v)
	}

	// DeveloperEmail: [69][1][0]
	if v := getPath(appData, 69, 1, 0); v != nil {
		app.DeveloperEmail = toString(v)
	}

	// DeveloperWebsite: [69][0][5][2]
	if v := getPath(appData, 69, 0, 5, 2); v != nil {
		app.DeveloperWebsite = toString(v)
	}

	// DeveloperAddress: [69][2][0]
	if v := getPath(appData, 69, 2, 0); v != nil {
		app.DeveloperAddress = toString(v)
	}

	// Genre: [79][0][0][0]
	if v := getPath(appData, 79, 0, 0, 0); v != nil {
		app.Genre = toString(v)
	}

	// GenreID: [79][0][0][2]
	if v := getPath(appData, 79, 0, 0, 2); v != nil {
		app.GenreID = toString(v)
	}

	// Icon: [95][0][3][2]
	if v := getPath(appData, 95, 0, 3, 2); v != nil {
		app.Icon = toString(v)
	}

	// Version: [140][0][0][0]
	if v := getPath(appData, 140, 0, 0, 0); v != nil {
		app.Version = toString(v)
	}

	// AndroidVersion: [140][1][1][0][0][1]
	if v := getPath(appData, 140, 1, 1, 0, 0, 1); v != nil {
		app.AndroidVersion = toString(v)
	}

	// ContentRating: [9][0]
	if v := getPath(appData, 9, 0); v != nil {
		app.ContentRating = toString(v)
	}

	// Released: [10][1][0]
	if v := getPath(appData, 10, 1, 0); v != nil {
		app.Released = toString(v)
	}

	// Updated: [145][0][1][0]
	if v := getPath(appData, 145, 0, 1, 0); v != nil {
		app.Updated = toInt64(v)
	}

	// Screenshots
	if screenshots := getPath(appData, 78, 0); screenshots != nil {
		app.Screenshots = extractScreenshots(screenshots)
	}

	// PrivacyPolicy: [99][0][5][2]
	if v := getPath(appData, 99, 0, 5, 2); v != nil {
		app.PrivacyPolicy = toString(v)
	}

	return app, nil
}

func getPath(data interface{}, indices ...int) interface{} {
	current := data
	for _, idx := range indices {
		switch v := current.(type) {
		case []interface{}:
			if idx >= len(v) {
				return nil
			}
			current = v[idx]
		case map[string]interface{}:
			// Handle maps with numeric string keys (e.g., "138", "100")
			key := fmt.Sprintf("%d", idx)
			val, ok := v[key]
			if !ok {
				return nil
			}
			current = val
		default:
			return nil
		}
	}
	return current
}

func toString(v interface{}) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

func toInt(v interface{}) int {
	if v == nil {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case string:
		i, _ := strconv.Atoi(n)
		return i
	}
	return 0
}

func toInt64(v interface{}) int64 {
	if v == nil {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	case string:
		i, _ := strconv.ParseInt(n, 10, 64)
		return i
	}
	return 0
}

func toFloat64(v interface{}) float64 {
	if v == nil {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case string:
		f, _ := strconv.ParseFloat(n, 64)
		return f
	}
	return 0
}

func extractHistogram(data interface{}) [5]int {
	var hist [5]int
	arr, ok := data.([]interface{})
	if !ok {
		return hist
	}
	for i := 0; i < 5 && i < len(arr); i++ {
		if inner, ok := arr[i].([]interface{}); ok && len(inner) > 1 {
			hist[4-i] = toInt(inner[1]) // Reverse: 5-star is first in response
		}
	}
	return hist
}

func extractScreenshots(data interface{}) []string {
	arr, ok := data.([]interface{})
	if !ok {
		return nil
	}
	var screenshots []string
	for _, item := range arr {
		if inner, ok := item.([]interface{}); ok && len(inner) > 3 {
			if imgData, ok := inner[3].([]interface{}); ok && len(imgData) > 2 {
				if url, ok := imgData[2].(string); ok {
					screenshots = append(screenshots, url)
				}
			}
		}
	}
	return screenshots
}

var htmlTagRegex = regexp.MustCompile(`<[^>]*>`)

func stripHTML(s string) string {
	// Replace <br> with newlines
	s = strings.ReplaceAll(s, "<br>", "\n")
	s = strings.ReplaceAll(s, "<br/>", "\n")
	s = strings.ReplaceAll(s, "<br />", "\n")
	// Remove all other tags
	return htmlTagRegex.ReplaceAllString(s, "")
}
