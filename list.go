package googleplayscraper

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ListOptions configures the app list request
type ListOptions struct {
	Collection Collection // TOP_FREE, TOP_PAID, GROSSING
	Category   Category   // APPLICATION, GAME, etc.
	Lang       string
	Country    string
	Num        int
}

// List fetches a list of apps from a specific collection/category
func (c *Client) List(ctx context.Context, opts ListOptions) ([]SearchResult, error) {
	if opts.Lang == "" {
		opts.Lang = "en"
	}
	if opts.Country == "" {
		opts.Country = "us"
	}
	if opts.Num == 0 {
		opts.Num = 60
	}
	if opts.Collection == "" {
		opts.Collection = CollectionTopFree
	}
	if opts.Category == "" {
		opts.Category = CategoryApplication
	}

	// Build the URL for top charts page
	// URL format: /store/apps/top/category/{CATEGORY}?hl=en&gl=us
	// Or for collection: /store/apps/collection/{collection}
	var reqURL string
	if opts.Category == CategoryApplication || opts.Category == CategoryGame {
		reqURL = fmt.Sprintf("%s/store/apps/top?hl=%s&gl=%s",
			BaseURL, opts.Lang, opts.Country)
	} else {
		reqURL = fmt.Sprintf("%s/store/apps/category/%s?hl=%s&gl=%s",
			BaseURL, opts.Category, opts.Lang, opts.Country)
	}

	body, err := c.get(ctx, reqURL)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	return parseListPage(body, opts)
}

func parseListPage(body []byte, opts ListOptions) ([]SearchResult, error) {
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

	// Apps are in ds:4[0][1][x][21][0]
	ds4, ok := dataBlocks["ds:4"]
	if !ok {
		return nil, nil
	}

	sections := getPath(ds4, 0, 1)
	if sections == nil {
		return nil, nil
	}

	sectionsArr, ok := sections.([]interface{})
	if !ok {
		return nil, nil
	}

	// Determine which section based on collection type
	// Section 0: Top free, Section 1: Top paid, Section 2: Top grossing (may vary)
	sectionIndex := 0
	switch opts.Collection {
	case CollectionTopFree:
		sectionIndex = 0
	case CollectionTopPaid:
		sectionIndex = 1
	case CollectionGrossing:
		sectionIndex = 2
	}

	var results []SearchResult

	// Try to get apps from the target section
	if sectionIndex < len(sectionsArr) {
		apps := getPath(sectionsArr[sectionIndex], 21, 0)
		if apps != nil {
			appsArr, ok := apps.([]interface{})
			if ok {
				for _, app := range appsArr {
					result := parseListApp(app)
					if result.AppID != "" {
						results = append(results, result)
					}
					if len(results) >= opts.Num {
						break
					}
				}
			}
		}
	}

	// If no results from target section, try all sections
	if len(results) == 0 {
		for _, section := range sectionsArr {
			apps := getPath(section, 21, 0)
			if apps == nil {
				continue
			}
			appsArr, ok := apps.([]interface{})
			if !ok {
				continue
			}
			for _, app := range appsArr {
				result := parseListApp(app)
				if result.AppID != "" {
					results = append(results, result)
				}
				if len(results) >= opts.Num {
					break
				}
			}
			if len(results) >= opts.Num {
				break
			}
		}
	}

	return results, nil
}

func parseListApp(item interface{}) SearchResult {
	arr, ok := item.([]interface{})
	if !ok {
		return SearchResult{}
	}

	result := SearchResult{}

	// AppID at [0][0]
	if v := getPath(arr, 0, 0); v != nil {
		result.AppID = toString(v)
	}

	// Title at [3]
	if v := getPath(arr, 3); v != nil {
		result.Title = toString(v)
	}

	// Icon at [1][3][2]
	if v := getPath(arr, 1, 3, 2); v != nil {
		result.Icon = toString(v)
	}

	// Developer at [14]
	if v := getPath(arr, 14); v != nil {
		result.Developer = toString(v)
	}

	// Score at [4][1]
	if v := getPath(arr, 4, 1); v != nil {
		result.Score = toFloat64(v)
	}

	// ScoreText at [4][0]
	if v := getPath(arr, 4, 0); v != nil {
		result.ScoreText = toString(v)
	}

	// Price info at [8]
	if priceInfo := getPath(arr, 8); priceInfo != nil {
		if priceArr, ok := priceInfo.([]interface{}); ok && len(priceArr) > 1 {
			// Check if free
			if v := getPath(priceArr, 1, 0, 0); v != nil {
				price := toFloat64(v)
				result.Price = price / 1000000
				result.Free = price == 0
			} else {
				result.Free = true
			}
			// Currency
			if v := getPath(priceArr, 1, 0, 1); v != nil {
				result.Currency = toString(v)
			}
		} else {
			result.Free = true
		}
	} else {
		result.Free = true
	}

	if result.AppID != "" {
		result.URL = fmt.Sprintf("%s/store/apps/details?id=%s", BaseURL, result.AppID)
	}

	return result
}
