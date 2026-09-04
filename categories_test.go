package googleplayscraper

import (
	"context"
	"testing"
)

func TestCategories(t *testing.T) {
	client := NewClient()
	ctx := context.Background()

	categories, err := client.Categories(ctx, CategoriesOptions{})

	if err != nil {
		t.Fatalf("Categories() error = %v", err)
	}

	if len(categories) == 0 {
		t.Fatal("Expected at least one category")
	}

	t.Logf("Found %d categories", len(categories))

	// Print first 10 categories
	for i, cat := range categories {
		if i >= 10 {
			break
		}
		t.Logf("  %s", cat)
	}

	// Verify we have some common categories
	foundGame := false
	foundApp := false
	foundGameAction := false

	for _, cat := range categories {
		if cat == CategoryGame {
			foundGame = true
		}
		if cat == CategoryApplication {
			foundApp = true
		}
		if cat == CategoryGameAction {
			foundGameAction = true
		}
	}

	if !foundGame {
		t.Error("GAME category should be present")
	}
	if !foundApp {
		t.Error("APPLICATION category should be present")
	}
	if !foundGameAction {
		t.Error("GAME_ACTION category should be present")
	}
}

func TestCategoriesCount(t *testing.T) {
	client := NewClient()
	ctx := context.Background()

	categories, err := client.Categories(ctx, CategoriesOptions{})

	if err != nil {
		t.Fatalf("Categories() error = %v", err)
	}

	// Should have all categories from AllCategories
	if len(categories) != len(AllCategories) {
		t.Errorf("Expected %d categories, got %d", len(AllCategories), len(categories))
	}

	t.Logf("Total categories: %d (app: ~36, game: ~18)", len(categories))
}

func TestAllCategoriesConstant(t *testing.T) {
	// Verify AllCategories has expected number of items
	// 36 app categories + 18 game categories = 54 total
	expectedMin := 50

	if len(AllCategories) < expectedMin {
		t.Errorf("AllCategories should have at least %d items, got %d", expectedMin, len(AllCategories))
	}

	// Verify no duplicates
	seen := make(map[Category]bool)
	for _, cat := range AllCategories {
		if seen[cat] {
			t.Errorf("Duplicate category: %s", cat)
		}
		seen[cat] = true
	}
}

// IsGame is the filter most catalog work starts with, so its edges matter more
// than its middle.
func TestCategoryIsGame(t *testing.T) {
	for _, c := range []Category{CategoryGame, CategoryGameAction, CategoryGamePuzzle, CategoryGameWord} {
		if !c.IsGame() {
			t.Errorf("%s should be a game category", c)
		}
	}
	for _, c := range []Category{CategoryApplication, "TOOLS", "EDUCATION", "ENTERTAINMENT"} {
		if c.IsGame() {
			t.Errorf("%s should not be a game category", c)
		}
	}
	// The prefix test requires the underscore: a category merely starting with
	// the letters GAME is not a game genre.
	for _, c := range []Category{"GAMEBOY", "GAMES", "GAMING"} {
		if c.IsGame() {
			t.Errorf("%s matched on a bare GAME prefix", c)
		}
	}
	// GAME itself counts: it is the genre of an app filed under games with no
	// sub-genre, not just a parent label.
	if !CategoryGame.IsGame() {
		t.Error("GAME itself must count as a game category")
	}
}

func TestGameCategoriesPartitionsAllCategories(t *testing.T) {
	games := GameCategories()
	if len(games) == 0 {
		t.Fatal("no game categories")
	}
	for _, c := range games {
		if !c.IsGame() {
			t.Errorf("GameCategories returned %s, which is not a game", c)
		}
	}
	// Every game in the full list must be in the subset, and nothing else.
	var want int
	for _, c := range AllCategories {
		if c.IsGame() {
			want++
		}
	}
	if len(games) != want {
		t.Errorf("GameCategories returned %d of %d game categories in AllCategories", len(games), want)
	}
	if len(games) >= len(AllCategories) {
		t.Errorf("GameCategories returned %d of %d — the filter is not filtering",
			len(games), len(AllCategories))
	}
}
