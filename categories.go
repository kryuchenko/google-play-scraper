package googleplayscraper

import (
	"context"
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
	// Return all known categories from constants
	// This matches the behavior of the original Node.js library
	return AllCategories, nil
}
