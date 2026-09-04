package googleplayscraper

import (
	"context"
	"strings"
)

// CategoriesOptions configures the categories request
type CategoriesOptions struct {
	Lang    string
	Country string
}

// Categories returns all known app categories from Google Play.
// Note: Google Play web interface no longer exposes categories as HTML links,
// so this function returns the predefined list of known categories.
func (c *Client) Categories(ctx context.Context, opts CategoriesOptions) ([]Category, error) {
	// The context is deliberately unused past this point: this makes no
	// request. The task is still opened so that a trace shows the call was
	// made -- and shows, by having no http.request region under it, that it
	// cost nothing. An operation absent from a trace is indistinguishable
	// from one that was never called.
	_, endTask := startTask(ctx, traceTaskCategories)
	defer endTask()

	// Return all known categories from constants
	// This matches the behavior of the original Node.js library
	return AllCategories, nil
}

// IsGame reports whether the category is a game category.
//
// The store's genre ids for games are GAME and its seventeen GAME_* children,
// and an app's genreId carries exactly one of them -- so this is the test that
// turns "every app in the catalog" into "every game", which is the filter most
// catalog work starts with.
//
// GAME itself counts: it is the genre of an app filed under games without a
// sub-genre, not merely a parent label.
func (c Category) IsGame() bool {
	return c == CategoryGame || strings.HasPrefix(string(c), "GAME_")
}

// GameCategories returns the game categories in the order AllCategories lists
// them.
func GameCategories() []Category {
	out := make([]Category, 0, 18)
	for _, c := range AllCategories {
		if c.IsGame() {
			out = append(out, c)
		}
	}
	return out
}
