package googleplayscraper

import (
	"reflect"
	"testing"
)

// rowSet builds nested []interface{} structures on demand and writes value at
// the given index path, growing slices as needed. It lets a test place a known
// value at an exact candidate path so the golden cases below exercise the real
// rowPaths index maps rather than a frozen HTML fixture.
func rowSet(root []any, value any, path ...int) []any {
	idx := path[0]
	for len(root) <= idx {
		root = append(root, nil)
	}
	if len(path) == 1 {
		root[idx] = value
		return root
	}
	child, _ := root[idx].([]any)
	root[idx] = rowSet(child, value, path[1:]...)
	return root
}

// TestDecodeResultRowGolden locks the full SearchResult each of the four row
// layouts must produce. The old per-RPC parsers were proven byte-equivalent
// during the refactor by a throwaway DeepEqual harness; this pins that
// equivalence permanently so a future change to decodeResultRow or a path map
// (e.g. a wrong candidate winning) is caught in -short, not only by the live
// canary.
func TestDecodeResultRowGolden(t *testing.T) {
	tests := []struct {
		name  string
		paths rowPaths
		build func() []any
		want  SearchResult
	}{
		{
			name:  "clusterList (vyAe2)",
			paths: clusterListAppPaths,
			build: func() []any {
				var row []any
				row = rowSet(row, "com.example.game", 0, 0, 0)
				row = rowSet(row, "Example Game", 0, 3)
				row = rowSet(row, "https://icon", 0, 1, 3, 2)
				row = rowSet(row, "Acme Studio", 0, 14)
				row = rowSet(row, "A fun game", 0, 13, 1)
				row = rowSet(row, 4.5, 0, 4, 1)
				row = rowSet(row, "4.5", 0, 4, 0)
				row = rowSet(row, "USD", 0, 8, 1, 0, 1)
				row = rowSet(row, 2990000.0, 0, 8, 1, 0, 0)
				row = rowSet(row, "/store/apps/details?id=com.example.game", 0, 10, 4, 2)
				return row
			},
			want: SearchResult{
				AppID:     "com.example.game",
				Title:     "Example Game",
				Icon:      "https://icon",
				Developer: "Acme Studio",
				Summary:   "A fun game",
				Score:     4.5,
				ScoreText: "4.5",
				Currency:  "USD",
				Price:     2.99,
				Free:      false,
				URL:       BaseURL + "/store/apps/details?id=com.example.game",
			},
		},
		{
			name:  "listApp (top-charts HTML)",
			paths: listAppPaths,
			build: func() []any {
				var row []any
				row = rowSet(row, "com.example.app", 0, 0)
				row = rowSet(row, "Example App", 3)
				row = rowSet(row, "https://icon2", 1, 3, 2)
				row = rowSet(row, "Beta Inc", 14)
				row = rowSet(row, 3.7, 4, 1)
				row = rowSet(row, "3.7", 4, 0)
				row = rowSet(row, "EUR", 8, 1, 0, 1)
				row = rowSet(row, 0.0, 8, 1, 0, 0)
				return row
			},
			want: SearchResult{
				AppID:     "com.example.app",
				Title:     "Example App",
				Icon:      "https://icon2",
				Developer: "Beta Inc",
				Score:     3.7,
				ScoreText: "3.7",
				Currency:  "EUR",
				Price:     0,
				Free:      true, // price tuple present and 0
				URL:       BaseURL + "/store/apps/details?id=com.example.app",
			},
		},
		{
			name:  "searchGrid (singleton-wrapped, requireAppID)",
			paths: searchGridPaths,
			build: func() []any {
				// One inner row wrapped in a singleton, appID at the
				// search-page slot [0][0].
				var inner []any
				inner = rowSet(inner, "com.example.search", 0, 0)
				inner = rowSet(inner, "Searched App", 3)
				inner = rowSet(inner, "https://icon3", 1, 3, 2)
				inner = rowSet(inner, "Gamma LLC", 14)
				inner = rowSet(inner, 4.1, 4, 1)
				inner = rowSet(inner, "4.1", 4, 0)
				return []any{inner}
			},
			want: SearchResult{
				AppID:     "com.example.search",
				Title:     "Searched App",
				Icon:      "https://icon3",
				Developer: "Gamma LLC",
				Score:     4.1,
				ScoreText: "4.1",
				Free:      true, // grid carries no price path → free
				URL:       BaseURL + "/store/apps/details?id=com.example.search",
			},
		},
		{
			name:  "qnKhOb (developerIDLink, urlPathOnly)",
			paths: qnKhObRowPaths,
			build: func() []any {
				var row []any
				row = rowSet(row, "com.example.feed", 12, 0)
				row = rowSet(row, "Feed App", 2)
				row = rowSet(row, "https://icon4", 1, 1, 0, 3, 2)
				row = rowSet(row, "Delta Games", 4, 0, 0, 0)
				row = rowSet(row, "https://play.google.com/store/apps/dev?id=DELTA123", 4, 0, 0, 1, 4, 2)
				row = rowSet(row, "Endless fun", 4, 1, 1, 1, 1)
				row = rowSet(row, 4.8, 6, 0, 2, 1, 1)
				row = rowSet(row, "4.8", 6, 0, 2, 1, 0)
				row = rowSet(row, "USD", 7, 0, 3, 2, 1, 0, 1)
				row = rowSet(row, 990000.0, 7, 0, 3, 2, 1, 0, 0)
				row = rowSet(row, "/store/apps/details?id=com.example.feed", 9, 4, 2)
				return row
			},
			want: SearchResult{
				AppID:       "com.example.feed",
				Title:       "Feed App",
				Icon:        "https://icon4",
				Developer:   "Delta Games",
				DeveloperID: "DELTA123",
				Summary:     "Endless fun",
				Score:       4.8,
				ScoreText:   "4.8",
				Currency:    "USD",
				Price:       0.99,
				Free:        false,
				URL:         BaseURL + "/store/apps/details?id=com.example.feed",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decodeResultRow(tt.build(), tt.paths)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("decodeResultRow mismatch\n got: %+v\nwant: %+v", got, tt.want)
			}
		})
	}
}

// TestDecodeResultRowEdges locks the two behaviours most at risk of silent
// drift: requireAppID must reject a non-package value in the appID slot, and an
// absent price tuple must yield Free=true (not Free=false).
func TestDecodeResultRowEdges(t *testing.T) {
	t.Run("requireAppID rejects non-package", func(t *testing.T) {
		inner := rowSet(nil, "not a package id", 0, 0)
		got := decodeResultRow([]any{inner}, searchGridPaths)
		if got.AppID != "" {
			t.Errorf("AppID = %q, want empty (non-package rejected)", got.AppID)
		}
	})

	t.Run("no price path → free", func(t *testing.T) {
		// urlPathOnly layout with no price tuple at all.
		row := rowSet(nil, "com.example.x", 12, 0)
		got := decodeResultRow(row, qnKhObRowPaths)
		if !got.Free {
			t.Error("Free = false, want true when no price path resolves")
		}
	})
}
