package googleplayscraper_test

import (
	"context"
	"testing"
	"time"

	"github.com/kryuchenko/google-play-scraper"
)

// ============== APP TESTS ==============

func TestApp(t *testing.T) {
	client := googleplayscraper.NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	app, err := client.App(ctx, "com.google.android.apps.maps", googleplayscraper.AppOptions{
		Lang:    "en",
		Country: "us",
	})
	if err != nil {
		t.Fatalf("App() error: %v", err)
	}

	assertApp(t, app)
	if app.AppID != "com.google.android.apps.maps" {
		t.Errorf("AppID = %q, want %q", app.AppID, "com.google.android.apps.maps")
	}
	if !app.Free {
		t.Error("Google Maps should be free")
	}

	t.Logf("App: %s by %s (%.1f stars)", app.Title, app.Developer, app.Score)
}

func TestAppNotFound(t *testing.T) {
	client := googleplayscraper.NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := client.App(ctx, "com.nonexistent.app.xyz123", googleplayscraper.AppOptions{})
	if err == nil {
		t.Error("Expected error for non-existent app")
	}
}

func TestAppLocalization(t *testing.T) {
	client := googleplayscraper.NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	locales := []struct {
		lang, country string
	}{
		{"es", "es"},
		{"ru", "ru"},
		{"ja", "jp"},
	}

	for _, loc := range locales {
		app, err := client.App(ctx, "com.google.android.apps.maps", googleplayscraper.AppOptions{
			Lang:    loc.lang,
			Country: loc.country,
		})
		if err != nil {
			t.Errorf("App(%s/%s) error: %v", loc.lang, loc.country, err)
			continue
		}
		assertApp(t, app)
		t.Logf("%s/%s: %s", loc.lang, loc.country, app.Title)
	}
}

func TestAppPaid(t *testing.T) {
	client := googleplayscraper.NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	app, err := client.App(ctx, "com.mojang.minecraftpe", googleplayscraper.AppOptions{})
	if err != nil {
		t.Fatalf("App() error: %v", err)
	}

	assertApp(t, app)
	if app.Free {
		t.Error("Minecraft should be paid")
	}
	t.Logf("Minecraft: %s (%.2f)", app.PriceText, app.Price)
}

// ============== REVIEWS TESTS ==============

func TestReviews(t *testing.T) {
	client := googleplayscraper.NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := client.Reviews(ctx, "com.google.android.apps.maps", googleplayscraper.ReviewOptions{
		Sort:  googleplayscraper.SortNewest,
		Count: 10,
	})
	if err != nil {
		t.Fatalf("Reviews() error: %v", err)
	}

	if len(result.Reviews) == 0 {
		t.Fatal("Expected at least one review")
	}

	for _, r := range result.Reviews {
		assertReview(t, r)
	}

	t.Logf("Got %d reviews", len(result.Reviews))
}

func TestReviewsSorting(t *testing.T) {
	client := googleplayscraper.NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sorts := []googleplayscraper.Sort{googleplayscraper.SortNewest, googleplayscraper.SortHelpfulness, googleplayscraper.SortRating}

	for _, sort := range sorts {
		result, err := client.Reviews(ctx, "com.instagram.android", googleplayscraper.ReviewOptions{
			Sort:  sort,
			Count: 5,
		})
		if err != nil {
			t.Errorf("Reviews(sort=%d) error: %v", sort, err)
			continue
		}
		t.Logf("Sort %d: got %d reviews", sort, len(result.Reviews))
	}
}

func TestReviewsPagination(t *testing.T) {
	client := googleplayscraper.NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	page1, err := client.Reviews(ctx, "com.instagram.android", googleplayscraper.ReviewOptions{
		Sort:  googleplayscraper.SortNewest,
		Count: 20,
	})
	if err != nil {
		t.Fatalf("Page 1 error: %v", err)
	}

	if page1.NextToken == "" {
		t.Skip("No pagination token")
	}

	page2, err := client.Reviews(ctx, "com.instagram.android", googleplayscraper.ReviewOptions{
		Sort:      googleplayscraper.SortNewest,
		Count:     20,
		NextToken: page1.NextToken,
	})
	if err != nil {
		t.Fatalf("Page 2 error: %v", err)
	}

	if len(page1.Reviews) > 0 && len(page2.Reviews) > 0 {
		if page1.Reviews[0].ID == page2.Reviews[0].ID {
			t.Error("Pages should have different reviews")
		}
	}

	t.Logf("Page 1: %d, Page 2: %d", len(page1.Reviews), len(page2.Reviews))
}

func TestReviewsFilterScore(t *testing.T) {
	client := googleplayscraper.NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := client.Reviews(ctx, "com.instagram.android", googleplayscraper.ReviewOptions{
		Count:       10,
		FilterScore: 1,
	})
	if err != nil {
		t.Fatalf("Reviews() error: %v", err)
	}

	for _, r := range result.Reviews {
		if r.Score != 1 {
			t.Errorf("Expected score 1, got %d", r.Score)
		}
	}

	t.Logf("Got %d 1-star reviews", len(result.Reviews))
}

func TestReviewsAll(t *testing.T) {
	client := googleplayscraper.NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Fetch 200 reviews with auto-pagination (should be 2 pages)
	reviews, err := client.ReviewsAll(ctx, "com.instagram.android", googleplayscraper.ReviewOptions{
		Count: 200,
	})
	if err != nil {
		t.Fatalf("ReviewsAll() error: %v", err)
	}

	if len(reviews) < 150 {
		t.Errorf("Expected at least 150 reviews (multi-page), got %d", len(reviews))
	}

	t.Logf("ReviewsAll fetched %d reviews", len(reviews))
}

// ============== SEARCH TESTS ==============

func TestSearch(t *testing.T) {
	client := googleplayscraper.NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	results, err := client.Search(ctx, googleplayscraper.SearchOptions{
		Term: "maps",
		Num:  10,
	})
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}

	if len(results) == 0 {
		t.Fatal("Expected at least one result")
	}

	for _, r := range results {
		assertSearchResult(t, r)
	}

	t.Logf("Got %d results for 'maps'", len(results))
}

func TestSearchLocalization(t *testing.T) {
	client := googleplayscraper.NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	results, err := client.Search(ctx, googleplayscraper.SearchOptions{
		Term:    "juegos",
		Lang:    "es",
		Country: "es",
		Num:     5,
	})
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}

	t.Logf("Spanish search: %d results", len(results))
}

func TestSearchFreeOnly(t *testing.T) {
	client := googleplayscraper.NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	results, err := client.Search(ctx, googleplayscraper.SearchOptions{
		Term:  "game",
		Num:   10,
		Price: "free",
	})
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}

	for _, r := range results {
		if !r.Free {
			t.Errorf("%s should be free", r.Title)
		}
	}
}

// ============== DEVELOPER TESTS ==============

func TestDeveloper(t *testing.T) {
	client := googleplayscraper.NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	results, err := client.Developer(ctx, googleplayscraper.DeveloperOptions{
		DevID: "Google LLC",
		Num:   10,
	})
	if err != nil {
		t.Fatalf("Developer() error: %v", err)
	}

	if len(results) == 0 {
		t.Fatal("Expected at least one app")
	}

	for _, r := range results {
		assertSearchResult(t, r)
		if r.Developer != "Google LLC" {
			t.Errorf("Developer = %q, want Google LLC", r.Developer)
		}
	}

	t.Logf("Got %d apps from Google LLC", len(results))
}

func TestDeveloperNumericID(t *testing.T) {
	client := googleplayscraper.NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	results, err := client.Developer(ctx, googleplayscraper.DeveloperOptions{
		DevID: "5700313618786177705", // Meta
		Num:   5,
	})
	if err != nil {
		t.Fatalf("Developer() error: %v", err)
	}

	t.Logf("Got %d apps from numeric ID", len(results))
}

// ============== SIMILAR TESTS ==============

func TestSimilar(t *testing.T) {
	client := googleplayscraper.NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	results, err := client.Similar(ctx, googleplayscraper.SimilarOptions{
		AppID: "com.google.android.apps.maps",
	})
	if err != nil {
		t.Fatalf("Similar() error: %v", err)
	}

	if len(results) == 0 {
		t.Fatal("Expected at least one similar app")
	}

	for _, r := range results {
		assertSearchResult(t, r)
	}

	t.Logf("Got %d similar apps", len(results))
}

func TestSimilarDifferentDevelopers(t *testing.T) {
	client := googleplayscraper.NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	results, err := client.Similar(ctx, googleplayscraper.SimilarOptions{
		AppID: "com.instagram.android",
	})
	if err != nil {
		t.Fatalf("Similar() error: %v", err)
	}

	devs := make(map[string]bool)
	for _, r := range results {
		devs[r.Developer] = true
	}

	t.Logf("Similar apps from %d unique developers", len(devs))
}

// ============== SUGGEST TESTS ==============

func TestSuggest(t *testing.T) {
	client := googleplayscraper.NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	suggestions, err := client.Suggest(ctx, googleplayscraper.SuggestOptions{
		Term: "whats",
	})
	if err != nil {
		t.Fatalf("Suggest() error: %v", err)
	}

	if len(suggestions) == 0 {
		t.Fatal("Expected at least one suggestion")
	}

	t.Logf("Suggestions for 'whats': %v", suggestions)
}

// ============== PERMISSIONS TESTS ==============

func TestPermissions(t *testing.T) {
	client := googleplayscraper.NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	perms, err := client.Permissions(ctx, googleplayscraper.PermissionsOptions{
		AppID: "com.instagram.android",
	})
	if err != nil {
		t.Fatalf("Permissions() error: %v", err)
	}

	if len(perms) == 0 {
		t.Fatal("Expected at least one permission")
	}

	t.Logf("Got %d permissions", len(perms))
	for i, p := range perms {
		if i >= 3 {
			break
		}
		t.Logf("  %s: %s", p.Type, p.Permission)
	}
}

// ============== DATASAFETY TESTS ==============

func TestDataSafety(t *testing.T) {
	client := googleplayscraper.NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	safety, err := client.DataSafety(ctx, googleplayscraper.DataSafetyOptions{
		AppID: "com.instagram.android",
	})
	if err != nil {
		t.Fatalf("DataSafety() error: %v", err)
	}

	t.Logf("Collected: %d, Shared: %d, Practices: %d",
		len(safety.CollectedData), len(safety.SharedData), len(safety.SecurityPractices))

	if safety.PrivacyPolicyURL != "" {
		t.Logf("Privacy policy: %s", safety.PrivacyPolicyURL)
	}
}

// ============== LIST TESTS ==============

func TestList(t *testing.T) {
	client := googleplayscraper.NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	results, err := client.List(ctx, googleplayscraper.ListOptions{
		Num: 10,
	})
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}

	if len(results) == 0 {
		t.Fatal("Expected at least one app")
	}

	for _, r := range results {
		assertSearchResult(t, r)
	}

	t.Logf("Got %d popular apps", len(results))
}

// ============== CATEGORIES TESTS ==============

func TestCategories(t *testing.T) {
	client := googleplayscraper.NewClient()
	ctx := context.Background()

	categories, err := client.Categories(ctx, googleplayscraper.CategoriesOptions{})
	if err != nil {
		t.Fatalf("Categories() error: %v", err)
	}

	if len(categories) < 50 {
		t.Errorf("Expected at least 50 categories, got %d", len(categories))
	}

	t.Logf("Got %d categories", len(categories))
}

// ============== HELPERS ==============

func assertApp(t *testing.T, app *googleplayscraper.App) {
	t.Helper()
	if app.AppID == "" {
		t.Error("AppID is empty")
	}
	if app.Title == "" {
		t.Error("Title is empty")
	}
	if app.Developer == "" {
		t.Error("Developer is empty")
	}
	if app.Score < 0 || app.Score > 5 {
		t.Errorf("Score out of range: %f", app.Score)
	}
}

func assertReview(t *testing.T, r googleplayscraper.Review) {
	t.Helper()
	if r.ID == "" {
		t.Error("Review ID is empty")
	}
	if r.Score < 1 || r.Score > 5 {
		t.Errorf("Review score out of range: %d", r.Score)
	}
}

func assertSearchResult(t *testing.T, r googleplayscraper.SearchResult) {
	t.Helper()
	if r.AppID == "" {
		t.Error("AppID is empty")
	}
	if r.Title == "" {
		t.Error("Title is empty")
	}
	if r.Score < 0 || r.Score > 5 {
		t.Errorf("Score out of range: %f", r.Score)
	}
}
