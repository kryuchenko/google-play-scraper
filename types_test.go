package googleplayscraper

import (
	"encoding/json"
	"testing"
	"time"
)

func TestReviewJSON(t *testing.T) {
	review := Review{
		ID:        "abc123",
		UserName:  "John Doe",
		UserImage: "https://example.com/avatar.jpg",
		Date:      time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC),
		Score:     5,
		Text:      "Great app!",
		ThumbsUp:  42,
		URL:       "https://play.google.com/review/abc123",
	}

	data, err := json.Marshal(review)
	if err != nil {
		t.Fatalf("failed to marshal review: %v", err)
	}

	var decoded Review
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal review: %v", err)
	}

	if decoded.ID != review.ID {
		t.Errorf("ID mismatch: got %q, want %q", decoded.ID, review.ID)
	}
	if decoded.Score != review.Score {
		t.Errorf("Score mismatch: got %d, want %d", decoded.Score, review.Score)
	}
	if decoded.Text != review.Text {
		t.Errorf("Text mismatch: got %q, want %q", decoded.Text, review.Text)
	}
}

func TestDefaultReviewOptions(t *testing.T) {
	opts := DefaultReviewOptions()

	if opts.Lang != "en" {
		t.Errorf("Lang: got %q, want %q", opts.Lang, "en")
	}
	if opts.Country != "us" {
		t.Errorf("Country: got %q, want %q", opts.Country, "us")
	}
	if opts.Sort != SortNewest {
		t.Errorf("Sort: got %d, want %d", opts.Sort, SortNewest)
	}
	if opts.Count != 150 {
		t.Errorf("Count: got %d, want %d", opts.Count, 150)
	}
}

func TestAppJSON(t *testing.T) {
	app := App{
		AppID:       "com.example.app",
		Title:       "Example App",
		Developer:   "Example Inc",
		Score:       4.5,
		Ratings:     10000,
		Free:        true,
		Installs:    "1,000,000+",
		MinInstalls: 1000000,
	}

	data, err := json.Marshal(app)
	if err != nil {
		t.Fatalf("failed to marshal app: %v", err)
	}

	var decoded App
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal app: %v", err)
	}

	if decoded.AppID != app.AppID {
		t.Errorf("AppID mismatch: got %q, want %q", decoded.AppID, app.AppID)
	}
	if decoded.Score != app.Score {
		t.Errorf("Score mismatch: got %f, want %f", decoded.Score, app.Score)
	}
	if decoded.Free != app.Free {
		t.Errorf("Free mismatch: got %v, want %v", decoded.Free, app.Free)
	}
}

func TestSortConstants(t *testing.T) {
	if SortHelpfulness != 1 {
		t.Errorf("SortHelpfulness: got %d, want 1", SortHelpfulness)
	}
	if SortNewest != 2 {
		t.Errorf("SortNewest: got %d, want 2", SortNewest)
	}
	if SortRating != 3 {
		t.Errorf("SortRating: got %d, want 3", SortRating)
	}
}
