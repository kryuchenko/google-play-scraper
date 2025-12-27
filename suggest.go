package googleplayscraper

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

// SuggestOptions configures the search suggestions request
type SuggestOptions struct {
	Term    string
	Lang    string
	Country string
}

// Suggest returns search suggestions for a query
func (c *Client) Suggest(ctx context.Context, opts SuggestOptions) ([]string, error) {
	if opts.Term == "" {
		return nil, fmt.Errorf("term is required")
	}

	if opts.Lang == "" {
		opts.Lang = "en"
	}
	if opts.Country == "" {
		opts.Country = "us"
	}

	reqURL := fmt.Sprintf("%s/_/PlayStoreUi/data/batchexecute?rpcids=IJ4APc&hl=%s&gl=%s",
		BaseURL, opts.Lang, opts.Country)

	term := url.QueryEscape(opts.Term)
	body := fmt.Sprintf(`f.req=%%5B%%5B%%5B%%22IJ4APc%%22%%2C%%22%%5B%%5Bnull%%2C%%5B%%5C%%22%s%%5C%%22%%5D%%2C%%5B10%%5D%%2C%%5B2%%5D%%2C4%%5D%%5D%%22%%5D%%5D%%5D`, term)

	respBody, err := c.post(ctx, reqURL, "application/x-www-form-urlencoded;charset=UTF-8", body)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	return parseSuggestResponse(respBody)
}

func parseSuggestResponse(body []byte) ([]string, error) {
	// Skip the )]}'  prefix
	start := 0
	for i := range body {
		if body[i] == '\n' {
			start = i + 1
			break
		}
	}

	if start >= len(body) {
		return nil, fmt.Errorf("invalid response")
	}

	var outer [][]interface{}
	if err := json.Unmarshal(body[start:], &outer); err != nil {
		return nil, err
	}

	if len(outer) == 0 || len(outer[0]) < 3 {
		return nil, nil
	}

	dataStr, ok := outer[0][2].(string)
	if !ok {
		return nil, nil
	}

	var data []interface{}
	if err := json.Unmarshal([]byte(dataStr), &data); err != nil {
		return nil, err
	}

	if data == nil {
		return nil, nil
	}

	// Suggestions in data[0][0]
	suggestions := getPath(data, 0, 0)
	if suggestions == nil {
		return nil, nil
	}

	suggestionsArr, ok := suggestions.([]interface{})
	if !ok {
		return nil, nil
	}

	var result []string
	for _, s := range suggestionsArr {
		if arr, ok := s.([]interface{}); ok && len(arr) > 0 {
			if str, ok := arr[0].(string); ok {
				result = append(result, str)
			}
		}
	}

	return result, nil
}
