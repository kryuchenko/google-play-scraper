package googleplayscraper

import (
	"net/url"
	"strings"
	"testing"
)

// assertValidApp validates that an app/search result has required fields
func assertValidApp(t *testing.T, app SearchResult) {
	t.Helper()

	if app.AppID == "" {
		t.Error("AppID should not be empty")
	}
	if app.Title == "" {
		t.Error("Title should not be empty")
	}
	if app.URL == "" {
		t.Error("URL should not be empty")
	}
	if !isValidURL(app.URL) {
		t.Errorf("URL is not valid: %s", app.URL)
	}
	if app.Icon != "" && !isValidURL(app.Icon) {
		t.Errorf("Icon URL is not valid: %s", app.Icon)
	}
	if app.Score < 0 || app.Score > 5 {
		t.Errorf("Score should be between 0 and 5, got %f", app.Score)
	}
}

// assertValidFullApp validates full app details
func assertValidFullApp(t *testing.T, app *App) {
	t.Helper()

	if app.AppID == "" {
		t.Error("AppID should not be empty")
	}
	if app.Title == "" {
		t.Error("Title should not be empty")
	}
	if app.URL == "" {
		t.Error("URL should not be empty")
	}
	if app.Description == "" {
		t.Error("Description should not be empty")
	}
	if app.Developer == "" {
		t.Error("Developer should not be empty")
	}
	if app.Icon != "" && !isValidURL(app.Icon) {
		t.Errorf("Icon URL is not valid: %s", app.Icon)
	}
	if app.Score < 0 || app.Score > 5 {
		t.Errorf("Score should be between 0 and 5, got %f", app.Score)
	}
	if app.Ratings < 0 {
		t.Errorf("Ratings should not be negative: %d", app.Ratings)
	}
	if app.Reviews < 0 {
		t.Errorf("Reviews should not be negative: %d", app.Reviews)
	}
}

// assertValidReview validates a review has required fields
func assertValidReview(t *testing.T, review Review) {
	t.Helper()

	if review.ID == "" {
		t.Error("Review ID should not be empty")
	}
	if review.UserName == "" {
		t.Error("UserName should not be empty")
	}
	if review.Score < 1 || review.Score > 5 {
		t.Errorf("Score should be between 1 and 5, got %d", review.Score)
	}
	if review.Date.IsZero() {
		t.Error("Date should not be zero")
	}
}

// isValidURL checks if a string is a valid URL
func isValidURL(s string) bool {
	if s == "" {
		return false
	}
	// Handle protocol-relative URLs
	if strings.HasPrefix(s, "//") {
		s = "https:" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return false
	}
	return u.Scheme != "" && u.Host != ""
}
