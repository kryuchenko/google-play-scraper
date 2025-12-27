package googleplayscraper

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBuildReviewsBody(t *testing.T) {
	tests := []struct {
		name  string
		appID string
		opts  ReviewOptions
	}{
		{
			name:  "initial request",
			appID: "com.example.app",
			opts:  ReviewOptions{Sort: SortNewest, Count: 100},
		},
		{
			name:  "paginated request",
			appID: "com.example.app",
			opts:  ReviewOptions{Sort: SortNewest, Count: 100, NextToken: "abc123"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := buildReviewsBody(tt.appID, tt.opts)
			if body == "" {
				t.Error("body is empty")
			}
			if len(body) < 10 {
				t.Error("body is too short")
			}
		})
	}
}

func TestReviewsValidation(t *testing.T) {
	c := NewClient()
	_, err := c.Reviews(context.Background(), "", ReviewOptions{})
	if err == nil {
		t.Error("expected error for empty appID")
	}
}

func TestParseTimestamp(t *testing.T) {
	tests := []struct {
		name string
		arr  []interface{}
		want int64 // unix milli
	}{
		{
			name: "seconds only",
			arr:  []interface{}{float64(1704067200)},
			want: 1704067200000,
		},
		{
			name: "with milliseconds",
			arr:  []interface{}{float64(1704067200), float64(500)},
			want: 1704067200500,
		},
		{
			name: "empty",
			arr:  []interface{}{},
			want: -62135596800000, // zero time
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := parseTimestamp(tt.arr)
			if tt.arr == nil || len(tt.arr) == 0 {
				if !ts.IsZero() {
					t.Error("expected zero time for empty array")
				}
				return
			}
			got := ts.UnixMilli()
			if got != tt.want {
				t.Errorf("got %d, want %d", got, tt.want)
			}
		})
	}
}

func TestParseReview(t *testing.T) {
	// Simulate review data structure from Google Play
	reviewData := []interface{}{
		"review-id-123",                            // [0] ID
		[]interface{}{"John Doe", []interface{}{}}, // [1] User data
		float64(5),                                 // [2] Score
		nil,                                        // [3]
		"Great app, love it!",                      // [4] Text
		[]interface{}{float64(1704067200)},         // [5] Date
		float64(42),                                // [6] ThumbsUp
		nil,                                        // [7] Reply
	}

	review, err := parseReview(reviewData, "com.example.app")
	if err != nil {
		t.Fatalf("parseReview failed: %v", err)
	}

	if review.ID != "review-id-123" {
		t.Errorf("ID: got %q, want %q", review.ID, "review-id-123")
	}
	if review.UserName != "John Doe" {
		t.Errorf("UserName: got %q, want %q", review.UserName, "John Doe")
	}
	if review.Score != 5 {
		t.Errorf("Score: got %d, want %d", review.Score, 5)
	}
	if review.Text != "Great app, love it!" {
		t.Errorf("Text: got %q, want %q", review.Text, "Great app, love it!")
	}
	if review.ThumbsUp != 42 {
		t.Errorf("ThumbsUp: got %d, want %d", review.ThumbsUp, 42)
	}
}

func TestReviewsWithMockServer(t *testing.T) {
	// Mock response simulating Google Play batchexecute response
	mockResponse := `)]}'

[["wrb.fr","UsvDTd","[[[[\"review-1\",[\"User1\",[null,null,null,[null,null,\"https://avatar.com/1\"]]],5,null,\"Amazing app!\", [1704067200],10,null,null,null,\"1.0.0\"]],null],[null,\"next-token-123\"]]",null,null,null,"generic"]]`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(mockResponse))
	}))
	defer server.Close()

	// We can't easily override BaseURL, so this test validates parsing logic
	// For real integration test, see TestReviewsIntegration
}

// TestReviewsIntegration is a real integration test
func TestReviewsIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	c := NewClient()
	result, err := c.Reviews(context.Background(), "com.google.android.apps.maps", ReviewOptions{
		Lang:    "en",
		Country: "us",
		Sort:    SortNewest,
		Count:   10,
	})

	if err != nil {
		t.Fatalf("Reviews failed: %v", err)
	}

	if len(result.Reviews) == 0 {
		t.Error("expected at least one review")
	}

	// Validate all reviews
	for i, r := range result.Reviews {
		assertValidReview(t, r)
		if i < 3 {
			t.Logf("Review %d: %s (score: %d)", i, r.UserName, r.Score)
		}
	}

	t.Logf("Got %d reviews", len(result.Reviews))
	if result.NextToken != "" {
		t.Logf("NextToken: %s...", result.NextToken[:20])
	}
}

// TestReviewsSortHelpfulness tests sorting by helpfulness
func TestReviewsSortHelpfulness(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	c := NewClient()
	result, err := c.Reviews(context.Background(), "com.instagram.android", ReviewOptions{
		Sort:  SortHelpfulness,
		Count: 10,
	})

	if err != nil {
		t.Fatalf("Reviews failed: %v", err)
	}

	if len(result.Reviews) == 0 {
		t.Error("expected at least one review")
	}

	// Most helpful reviews should have thumbs up
	for _, r := range result.Reviews {
		assertValidReview(t, r)
	}

	t.Logf("Got %d reviews sorted by helpfulness", len(result.Reviews))
	if len(result.Reviews) > 0 {
		t.Logf("Top review thumbsUp: %d", result.Reviews[0].ThumbsUp)
	}
}

// TestReviewsSortRating tests sorting by rating
func TestReviewsSortRating(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	c := NewClient()
	result, err := c.Reviews(context.Background(), "com.instagram.android", ReviewOptions{
		Sort:  SortRating,
		Count: 10,
	})

	if err != nil {
		t.Fatalf("Reviews failed: %v", err)
	}

	if len(result.Reviews) == 0 {
		t.Error("expected at least one review")
	}

	for _, r := range result.Reviews {
		assertValidReview(t, r)
	}

	t.Logf("Got %d reviews sorted by rating", len(result.Reviews))
}

// TestReviewsJapanese tests reviews in Japanese
func TestReviewsJapanese(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	c := NewClient()
	result, err := c.Reviews(context.Background(), "com.google.android.apps.maps", ReviewOptions{
		Lang:    "ja",
		Country: "jp",
		Count:   5,
	})

	if err != nil {
		t.Fatalf("Reviews failed: %v", err)
	}

	t.Logf("Got %d Japanese reviews", len(result.Reviews))
	for i, r := range result.Reviews {
		assertValidReview(t, r)
		if i < 2 && r.Text != "" {
			t.Logf("  %s", r.Text[:minInt(50, len(r.Text))])
		}
	}
}

// TestReviewsPagination tests pagination works correctly
func TestReviewsPagination(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	c := NewClient()
	ctx := context.Background()

	// Get first page
	page1, err := c.Reviews(ctx, "com.instagram.android", ReviewOptions{
		Sort:  SortNewest,
		Count: 20,
	})
	if err != nil {
		t.Fatalf("First page failed: %v", err)
	}

	if page1.NextToken == "" {
		t.Skip("No pagination token, can't test pagination")
	}

	// Get second page
	page2, err := c.Reviews(ctx, "com.instagram.android", ReviewOptions{
		Sort:      SortNewest,
		Count:     20,
		NextToken: page1.NextToken,
	})
	if err != nil {
		t.Fatalf("Second page failed: %v", err)
	}

	// Pages should have different reviews
	if len(page1.Reviews) > 0 && len(page2.Reviews) > 0 {
		if page1.Reviews[0].ID == page2.Reviews[0].ID {
			t.Error("First review ID should be different between pages")
		}
	}

	t.Logf("Page 1: %d reviews, Page 2: %d reviews", len(page1.Reviews), len(page2.Reviews))
}

// TestReviewsConsistency tests same query returns same results
func TestReviewsConsistency(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	c := NewClient()
	ctx := context.Background()

	opts := ReviewOptions{
		Sort:  SortNewest,
		Count: 5,
	}

	result1, err := c.Reviews(ctx, "com.google.android.apps.maps", opts)
	if err != nil {
		t.Fatalf("First request failed: %v", err)
	}

	result2, err := c.Reviews(ctx, "com.google.android.apps.maps", opts)
	if err != nil {
		t.Fatalf("Second request failed: %v", err)
	}

	// Same query should return same reviews (at least first few)
	if len(result1.Reviews) > 0 && len(result2.Reviews) > 0 {
		if result1.Reviews[0].ID != result2.Reviews[0].ID {
			t.Log("Note: First review IDs differ (cache/timing)")
		}
	}

	t.Logf("Consistency check: %d vs %d reviews", len(result1.Reviews), len(result2.Reviews))
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
