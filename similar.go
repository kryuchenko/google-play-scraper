package googleplayscraper

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// SimilarOptions configures the similar apps request
type SimilarOptions struct {
	AppID      string
	Lang       string
	Country    string
	FullDetail bool // Fetch full details for each app
}

// Similar fetches apps similar to the given app
func (c *Client) Similar(ctx context.Context, opts SimilarOptions) ([]SearchResult, error) {
	if opts.AppID == "" {
		return nil, fmt.Errorf("appID is required")
	}

	if opts.Lang == "" {
		opts.Lang = "en"
	}
	if opts.Country == "" {
		opts.Country = "us"
	}

	// Step 1: Get app details page to find similar apps cluster URL
	appURL := fmt.Sprintf("%s/store/apps/details?id=%s&hl=%s&gl=%s",
		BaseURL, opts.AppID, opts.Lang, opts.Country)

	body, err := c.get(ctx, appURL)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	// Parse page and find similar apps cluster
	clusterURL, err := findSimilarCluster(body)
	if err != nil {
		return nil, err
	}

	if clusterURL == "" {
		return nil, fmt.Errorf("similar apps not found")
	}

	// Step 2: Fetch the cluster page
	fullClusterURL := BaseURL + clusterURL + "&gl=" + opts.Country + "&hl=" + opts.Lang

	clusterBody, err := c.get(ctx, fullClusterURL)
	if err != nil {
		return nil, fmt.Errorf("cluster request failed: %w", err)
	}

	results, err := parseSimilarPage(clusterBody)
	if err != nil {
		return nil, err
	}

	// Fetch full details if requested
	if opts.FullDetail {
		return c.enrichSearchResults(ctx, results, opts.Lang, opts.Country)
	}

	return results, nil
}

func findSimilarCluster(body []byte) (string, error) {
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

	// Look for clusters with "Similar" in title
	// Clusters are typically in ds:7 or ds:8, path [1][1]
	for _, key := range []string{"ds:7", "ds:8", "ds:6"} {
		ds, ok := dataBlocks[key]
		if !ok {
			continue
		}

		clusters := getPath(ds, 1, 1)
		if clusters == nil {
			continue
		}

		clustersArr, ok := clusters.([]interface{})
		if !ok {
			continue
		}

		for _, cluster := range clustersArr {
			clusterArr, ok := cluster.([]interface{})
			if !ok {
				continue
			}

			// Check title at [21][1][0]
			title := getPath(clusterArr, 21, 1, 0)
			if title != nil {
				titleStr := toString(title)
				if strings.Contains(titleStr, "Similar") {
					// URL at [21][1][2][4][2]
					url := getPath(clusterArr, 21, 1, 2, 4, 2)
					if url != nil {
						return toString(url), nil
					}
				}
			}
		}
	}

	return "", nil
}

func parseSimilarPage(body []byte) ([]SearchResult, error) {
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

	// Apps in ds:3 -> [0][1][0][21][0]
	ds3, ok := dataBlocks["ds:3"]
	if !ok {
		return nil, nil
	}

	appsSection := getPath(ds3, 0, 1, 0, 21, 0)
	if appsSection == nil {
		return nil, nil
	}

	apps, ok := appsSection.([]interface{})
	if !ok {
		return nil, nil
	}

	var results []SearchResult
	for _, app := range apps {
		result := parseSimilarApp(app)
		if result.AppID != "" {
			results = append(results, result)
		}
	}

	return results, nil
}

func parseSimilarApp(item interface{}) SearchResult {
	arr, ok := item.([]interface{})
	if !ok {
		return SearchResult{}
	}

	result := SearchResult{}

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

	result.Free = true

	if result.AppID != "" {
		result.URL = fmt.Sprintf("%s/store/apps/details?id=%s", BaseURL, result.AppID)
	}

	return result
}
