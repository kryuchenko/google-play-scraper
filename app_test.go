package googleplayscraper

import (
	"context"
	"testing"
)

func TestAppValidation(t *testing.T) {
	c := NewClient()
	_, err := c.App(context.Background(), "", AppOptions{})
	if err == nil {
		t.Error("expected error for empty appID")
	}
}

func TestGetPath(t *testing.T) {
	data := []interface{}{
		"zero",
		[]interface{}{
			"one-zero",
			[]interface{}{
				"two-zero",
				"two-one",
			},
		},
	}

	tests := []struct {
		name    string
		indices []int
		want    string
	}{
		{"root level", []int{0}, "zero"},
		{"nested", []int{1, 0}, "one-zero"},
		{"deep nested", []int{1, 1, 1}, "two-one"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getPath(data, tt.indices...)
			if got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetPathNil(t *testing.T) {
	data := []interface{}{"zero", nil}

	if got := getPath(data, 5); got != nil {
		t.Errorf("expected nil for out of bounds, got %v", got)
	}
	if got := getPath(data, 1, 0); got != nil {
		t.Errorf("expected nil for nil element, got %v", got)
	}
}

func TestToString(t *testing.T) {
	tests := []struct {
		input interface{}
		want  string
	}{
		{nil, ""},
		{"hello", "hello"},
		{123, "123"},
		{45.67, "45.67"},
	}

	for _, tt := range tests {
		got := toString(tt.input)
		if got != tt.want {
			t.Errorf("toString(%v) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestToInt(t *testing.T) {
	tests := []struct {
		input interface{}
		want  int
	}{
		{nil, 0},
		{float64(42), 42},
		{123, 123},
		{"456", 456},
	}

	for _, tt := range tests {
		got := toInt(tt.input)
		if got != tt.want {
			t.Errorf("toInt(%v) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestExtractHistogram(t *testing.T) {
	// Simulate histogram data from Google Play
	// Order in response: 5-star, 4-star, 3-star, 2-star, 1-star
	data := []interface{}{
		[]interface{}{5, float64(1000)}, // 5-star
		[]interface{}{4, float64(500)},  // 4-star
		[]interface{}{3, float64(200)},  // 3-star
		[]interface{}{2, float64(100)},  // 2-star
		[]interface{}{1, float64(50)},   // 1-star
	}

	hist := extractHistogram(data)

	// Result should be [1-star, 2-star, 3-star, 4-star, 5-star]
	expected := [5]int{50, 100, 200, 500, 1000}
	if hist != expected {
		t.Errorf("got %v, want %v", hist, expected)
	}
}

func TestStripHTML(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"<p>Hello</p>", "Hello"},
		{"Line1<br>Line2", "Line1\nLine2"},
		{"<b>Bold</b> and <i>italic</i>", "Bold and italic"},
		{"No tags", "No tags"},
	}

	for _, tt := range tests {
		got := stripHTML(tt.input)
		if got != tt.want {
			t.Errorf("stripHTML(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// TestAppIntegration is a real integration test
func TestAppIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	c := NewClient()
	app, err := c.App(context.Background(), "com.google.android.apps.maps", AppOptions{
		Lang:    "en",
		Country: "us",
	})

	if err != nil {
		t.Fatalf("App failed: %v", err)
	}

	assertValidFullApp(t, app)

	if app.AppID != "com.google.android.apps.maps" {
		t.Errorf("AppID: got %q, want %q", app.AppID, "com.google.android.apps.maps")
	}
	if !app.Free {
		t.Error("Google Maps should be free")
	}

	t.Logf("Title: %s", app.Title)
	t.Logf("Developer: %s", app.Developer)
	t.Logf("Score: %.2f (%d ratings)", app.Score, app.Ratings)
	t.Logf("Installs: %s", app.Installs)
	t.Logf("Version: %s", app.Version)
}

// TestAppNotFound tests 404 error for non-existent app
func TestAppNotFound(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	c := NewClient()
	_, err := c.App(context.Background(), "com.invalid.nonexistent.app.xyz123", AppOptions{})

	if err == nil {
		t.Error("Expected error for non-existent app")
	}
	t.Logf("Got expected error: %v", err)
}

// TestAppSpanish tests localization to Spanish
func TestAppSpanish(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	c := NewClient()
	app, err := c.App(context.Background(), "com.google.android.apps.maps", AppOptions{
		Lang:    "es",
		Country: "es",
	})

	if err != nil {
		t.Fatalf("App failed: %v", err)
	}

	assertValidFullApp(t, app)
	t.Logf("Spanish title: %s", app.Title)
	t.Logf("Spanish summary: %s", app.Summary[:min(100, len(app.Summary))])
}

// TestAppRussian tests localization to Russian
func TestAppRussian(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	c := NewClient()
	app, err := c.App(context.Background(), "com.google.android.apps.maps", AppOptions{
		Lang:    "ru",
		Country: "ru",
	})

	if err != nil {
		t.Fatalf("App failed: %v", err)
	}

	assertValidFullApp(t, app)
	t.Logf("Russian title: %s", app.Title)
}

// TestAppPaid tests a paid app
func TestAppPaid(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	c := NewClient()
	// Minecraft is a well-known paid app
	app, err := c.App(context.Background(), "com.mojang.minecraftpe", AppOptions{
		Lang:    "en",
		Country: "us",
	})

	if err != nil {
		t.Fatalf("App failed: %v", err)
	}

	assertValidFullApp(t, app)

	if app.Free {
		t.Error("Minecraft should not be free")
	}
	if app.Price == 0 {
		t.Error("Minecraft should have a price")
	}
	t.Logf("Minecraft price: %s (%.2f)", app.PriceText, app.Price)
}

// TestAppFullDetails validates all fields are populated
func TestAppFullDetails(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	c := NewClient()
	app, err := c.App(context.Background(), "com.instagram.android", AppOptions{})

	if err != nil {
		t.Fatalf("App failed: %v", err)
	}

	assertValidFullApp(t, app)

	// Check optional but expected fields
	if app.Summary == "" {
		t.Log("Warning: Summary is empty")
	}
	if app.Installs == "" {
		t.Log("Warning: Installs is empty")
	}
	if app.Genre == "" {
		t.Log("Warning: Genre is empty")
	}
	if len(app.Screenshots) == 0 {
		t.Log("Warning: No screenshots")
	}
	if app.ContentRating == "" {
		t.Log("Warning: ContentRating is empty")
	}

	t.Logf("App: %s", app.Title)
	t.Logf("Genre: %s (ID: %s)", app.Genre, app.GenreID)
	t.Logf("Content Rating: %s", app.ContentRating)
	t.Logf("Screenshots: %d", len(app.Screenshots))
	t.Logf("Reviews: %d", app.Reviews)

	// Validate histogram
	total := 0
	for _, count := range app.Histogram {
		total += count
	}
	if total == 0 {
		t.Log("Warning: Histogram is empty")
	} else {
		t.Logf("Histogram: %v (total: %d)", app.Histogram, total)
	}
}

func TestToInt64(t *testing.T) {
	tests := []struct {
		input interface{}
		want  int64
	}{
		{nil, 0},
		{float64(42), 42},
		{float64(1000000000), 1000000000},
		{123, 123},
		{"456789", 456789},
		{"invalid", 0},
	}

	for _, tt := range tests {
		got := toInt64(tt.input)
		if got != tt.want {
			t.Errorf("toInt64(%v) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestToFloat64(t *testing.T) {
	tests := []struct {
		input interface{}
		want  float64
	}{
		{nil, 0},
		{float64(3.14), 3.14},
		{42, 42.0},
		{"3.5", 3.5},
		{"invalid", 0},
	}

	for _, tt := range tests {
		got := toFloat64(tt.input)
		if got != tt.want {
			t.Errorf("toFloat64(%v) = %f, want %f", tt.input, got, tt.want)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
