package googleplayscraper

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// DeveloperOptions configures the developer apps request
type DeveloperOptions struct {
	DevID   string // Developer ID or name
	Lang    string
	Country string
	Num     int
}

// Developer fetches all apps by a developer
func (c *Client) Developer(ctx context.Context, opts DeveloperOptions) ([]SearchResult, error) {
	if opts.DevID == "" {
		return nil, fmt.Errorf("developer ID is required")
	}

	if opts.Lang == "" {
		opts.Lang = "en"
	}
	if opts.Country == "" {
		opts.Country = "us"
	}
	if opts.Num == 0 {
		opts.Num = 60
	}

	// Check if devId is numeric (dev ID) or string (developer name)
	_, isNumeric := strconv.Atoi(opts.DevID)

	var path string
	if isNumeric == nil {
		path = "/store/apps/dev"
	} else {
		path = "/store/apps/developer"
	}

	devURL := fmt.Sprintf("%s%s?id=%s&hl=%s&gl=%s",
		BaseURL, path, url.QueryEscape(opts.DevID), opts.Lang, opts.Country)

	body, err := c.get(ctx, devURL)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	return parseDeveloperPage(body, isNumeric == nil, opts.Num)
}

func parseDeveloperPage(body []byte, isNumericID bool, num int) ([]SearchResult, error) {
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

	// Apps are in ds:3
	ds3, ok := dataBlocks["ds:3"]
	if !ok {
		return nil, nil
	}

	// Path depends on whether devId is numeric or name
	var appsPath []int
	if isNumericID {
		appsPath = []int{0, 1, 0, 21, 0}
	} else {
		appsPath = []int{0, 1, 0, 22, 0}
	}

	appsSection := getPath(ds3, appsPath...)
	if appsSection == nil {
		return nil, nil
	}

	apps, ok := appsSection.([]interface{})
	if !ok {
		return nil, nil
	}

	var results []SearchResult
	for _, app := range apps {
		result := parseDeveloperApp(app, isNumericID)
		if result.AppID != "" {
			results = append(results, result)
		}
		if len(results) >= num {
			break
		}
	}

	return results, nil
}

func parseDeveloperApp(item interface{}, isNumericID bool) SearchResult {
	arr, ok := item.([]interface{})
	if !ok {
		return SearchResult{}
	}

	result := SearchResult{}

	if isNumericID {
		// Numeric dev ID format - data directly in arr
		// appId: [0][0]
		if v := getPath(arr, 0, 0); v != nil {
			result.AppID = toString(v)
		}
		// title: [3]
		if v := getPath(arr, 3); v != nil {
			result.Title = toString(v)
		}
		// icon: [1][3][2]
		if v := getPath(arr, 1, 3, 2); v != nil {
			result.Icon = toString(v)
		}
		// developer: [14]
		if v := getPath(arr, 14); v != nil {
			result.Developer = toString(v)
		}
		// score: [4][1]
		if v := getPath(arr, 4, 1); v != nil {
			result.Score = toFloat64(v)
		}
		// scoreText: [4][0]
		if v := getPath(arr, 4, 0); v != nil {
			result.ScoreText = toString(v)
		}
	} else {
		// Developer name format - data in arr[0]
		// appId: [0][0][0]
		if v := getPath(arr, 0, 0, 0); v != nil {
			result.AppID = toString(v)
		}
		// title: [0][3]
		if v := getPath(arr, 0, 3); v != nil {
			result.Title = toString(v)
		}
		// icon: [0][1][3][2]
		if v := getPath(arr, 0, 1, 3, 2); v != nil {
			result.Icon = toString(v)
		}
		// developer: [0][14]
		if v := getPath(arr, 0, 14); v != nil {
			result.Developer = toString(v)
		}
		// score: [0][4][1]
		if v := getPath(arr, 0, 4, 1); v != nil {
			result.Score = toFloat64(v)
		}
		// scoreText: [0][4][0]
		if v := getPath(arr, 0, 4, 0); v != nil {
			result.ScoreText = toString(v)
		}
	}

	result.Free = true

	if result.AppID != "" {
		result.URL = fmt.Sprintf("%s/store/apps/details?id=%s", BaseURL, result.AppID)
	}

	return result
}
