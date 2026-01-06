package googleplayscraper

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"
)

// ReviewsComprehensive fetches reviews by querying each rating separately to maximize unique results.
// This works around Google Play's tendency to return duplicate reviews across different queries.
// Returns up to opts.Count reviews per rating (5 ratings), so total can be up to 5x opts.Count.
func (c *Client) ReviewsComprehensive(ctx context.Context, appID string, opts ReviewOptions) ([]Review, error) {
	seen := make(map[string]bool)
	var allReviews []Review

	countPerRating := opts.Count
	if countPerRating == 0 {
		countPerRating = 200 // default per rating
	}

	for score := 1; score <= 5; score++ {
		scoreOpts := opts
		scoreOpts.FilterScore = score
		scoreOpts.Count = countPerRating
		scoreOpts.NextToken = "" // Reset pagination for each rating

		reviews, err := c.ReviewsAll(ctx, appID, scoreOpts)
		if err != nil {
			// Continue with other ratings on error
			continue
		}

		for _, r := range reviews {
			if !seen[r.ID] {
				seen[r.ID] = true
				allReviews = append(allReviews, r)
			}
		}
	}

	return allReviews, nil
}

// ReviewsAll fetches reviews with auto-pagination up to opts.Count total
func (c *Client) ReviewsAll(ctx context.Context, appID string, opts ReviewOptions) ([]Review, error) {
	var allReviews []Review
	maxTotal := opts.Count
	if maxTotal == 0 {
		maxTotal = 500 // default max
	}
	opts.Count = 150 // per-page size

	for len(allReviews) < maxTotal {
		result, err := c.Reviews(ctx, appID, opts)
		if err != nil {
			return allReviews, err
		}

		allReviews = append(allReviews, result.Reviews...)

		if result.NextToken == "" || len(result.Reviews) == 0 {
			break
		}

		opts.NextToken = result.NextToken
	}

	// Trim to requested count
	if len(allReviews) > maxTotal {
		allReviews = allReviews[:maxTotal]
	}

	return allReviews, nil
}

// Reviews fetches reviews for an app
func (c *Client) Reviews(ctx context.Context, appID string, opts ReviewOptions) (*ReviewsResult, error) {
	if appID == "" {
		return nil, fmt.Errorf("appID is required")
	}

	// Apply defaults
	if opts.Lang == "" {
		opts.Lang = "en"
	}
	if opts.Country == "" {
		opts.Country = "us"
	}
	if opts.Sort == 0 {
		opts.Sort = SortNewest
	}
	if opts.Count == 0 {
		opts.Count = 150
	}

	body := buildReviewsBody(appID, opts)
	reqURL := fmt.Sprintf("%s/_/PlayStoreUi/data/batchexecute?hl=%s&gl=%s", BaseURL, opts.Lang, opts.Country)

	respBody, err := c.post(ctx, reqURL, "application/x-www-form-urlencoded", body)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	return parseReviewsResponse(respBody, appID)
}

func buildReviewsBody(appID string, opts ReviewOptions) string {
	count := opts.Count
	if count > 150 {
		count = 150 // Google Play limit per request
	}

	// Build filter score part (null if not filtering, otherwise the score)
	scorePart := "null"
	if opts.FilterScore >= 1 && opts.FilterScore <= 5 {
		scorePart = fmt.Sprintf("%d", opts.FilterScore)
	}

	var payload string
	if opts.NextToken == "" {
		// Initial request
		payload = fmt.Sprintf(
			`[[["oCPfdb","[null,[2,%d,[%d],null,[null,%s]],[\"%s\",7]]",null,"generic"]]]`,
			opts.Sort, count, scorePart, appID,
		)
	} else {
		// Paginated request
		payload = fmt.Sprintf(
			`[[["oCPfdb","[null,[2,%d,[%d,null,\"%s\"],null,[null,%s]],[\"%s\",7]]",null,"generic"]]]`,
			opts.Sort, count, opts.NextToken, scorePart, appID,
		)
	}

	return "f.req=" + url.QueryEscape(payload)
}

func parseReviewsResponse(body []byte, appID string) (*ReviewsResult, error) {
	// Response starts with )]}'  which we need to skip
	start := 0
	for i := range body {
		if body[i] == '\n' {
			start = i + 1
			break
		}
	}

	if start >= len(body) {
		return nil, fmt.Errorf("invalid response format")
	}

	// Parse outer JSON array
	var outer [][]interface{}
	if err := json.Unmarshal(body[start:], &outer); err != nil {
		return nil, fmt.Errorf("parse outer json: %w", err)
	}

	if len(outer) == 0 || len(outer[0]) < 3 {
		return nil, fmt.Errorf("unexpected response structure")
	}

	// The data is in outer[0][2] as a JSON string
	dataStr, ok := outer[0][2].(string)
	if !ok {
		return nil, fmt.Errorf("data is not a string")
	}

	// Parse the inner JSON
	var data []interface{}
	if err := json.Unmarshal([]byte(dataStr), &data); err != nil {
		return nil, fmt.Errorf("parse inner json: %w", err)
	}

	return extractReviews(data, appID)
}

func extractReviews(data []interface{}, appID string) (*ReviewsResult, error) {
	result := &ReviewsResult{
		Reviews: []Review{},
	}

	if len(data) == 0 {
		return result, nil
	}

	// Extract reviews array from data[0]
	reviewsData, ok := data[0].([]interface{})
	if !ok {
		return result, nil
	}

	for _, item := range reviewsData {
		review, err := parseReview(item, appID)
		if err != nil {
			continue // Skip malformed reviews
		}
		if review.ID == "" {
			continue // Skip empty reviews
		}
		result.Reviews = append(result.Reviews, review)
	}

	// Extract next token from data[1][1]
	if len(data) > 1 {
		if tokenData, ok := data[1].([]interface{}); ok && len(tokenData) > 1 {
			if token, ok := tokenData[1].(string); ok {
				result.NextToken = token
			}
		}
	}

	return result, nil
}

func parseReview(item interface{}, appID string) (Review, error) {
	arr, ok := item.([]interface{})
	if !ok {
		return Review{}, fmt.Errorf("review is not an array")
	}

	review := Review{}

	// ID: [0]
	if len(arr) > 0 {
		if id, ok := arr[0].(string); ok {
			review.ID = id
			review.URL = fmt.Sprintf("%s/store/apps/details?id=%s&reviewId=%s", BaseURL, appID, id)
		}
	}

	// UserName: [1][0]
	if len(arr) > 1 {
		if userData, ok := arr[1].([]interface{}); ok && len(userData) > 0 {
			if name, ok := userData[0].(string); ok {
				review.UserName = name
			}
			// UserImage: [1][1][3][2]
			if len(userData) > 1 {
				if imgData, ok := userData[1].([]interface{}); ok && len(imgData) > 3 {
					if imgInner, ok := imgData[3].([]interface{}); ok && len(imgInner) > 2 {
						if img, ok := imgInner[2].(string); ok {
							review.UserImage = img
						}
					}
				}
			}
		}
	}

	// Score: [2]
	if len(arr) > 2 {
		if score, ok := arr[2].(float64); ok {
			review.Score = int(score)
		}
	}

	// Text: [4]
	if len(arr) > 4 {
		if text, ok := arr[4].(string); ok {
			review.Text = text
		}
	}

	// Date: [5]
	if len(arr) > 5 {
		if dateArr, ok := arr[5].([]interface{}); ok {
			review.Date = parseTimestamp(dateArr)
		}
	}

	// ThumbsUp: [6]
	if len(arr) > 6 {
		if thumbs, ok := arr[6].(float64); ok {
			review.ThumbsUp = int(thumbs)
		}
	}

	// ReplyText: [7][1], ReplyDate: [7][2]
	if len(arr) > 7 {
		if replyData, ok := arr[7].([]interface{}); ok {
			if len(replyData) > 1 {
				if replyText, ok := replyData[1].(string); ok {
					review.ReplyText = replyText
				}
			}
			if len(replyData) > 2 {
				if replyDateArr, ok := replyData[2].([]interface{}); ok {
					review.ReplyDate = parseTimestamp(replyDateArr)
				}
			}
		}
	}

	// Version: [10]
	if len(arr) > 10 {
		if version, ok := arr[10].(string); ok {
			review.Version = version
		}
	}

	return review, nil
}

func parseTimestamp(arr []interface{}) time.Time {
	if len(arr) < 1 {
		return time.Time{}
	}

	seconds, ok := arr[0].(float64)
	if !ok {
		return time.Time{}
	}

	// Convert seconds to milliseconds
	ms := int64(seconds) * 1000
	if len(arr) > 1 {
		if extra, ok := arr[1].(float64); ok {
			// Add milliseconds part
			extraStr := fmt.Sprintf("%d", int(extra))
			if len(extraStr) >= 3 {
				extraStr = extraStr[:3]
			}
			var extraMs int64
			fmt.Sscanf(extraStr, "%d", &extraMs)
			ms += extraMs
		}
	}

	return time.UnixMilli(ms)
}
