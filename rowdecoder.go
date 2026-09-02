package googleplayscraper

import (
	"fmt"
	"strings"
)

// rowPaths is a table-driven description of where each SearchResult field lives
// inside one app-row of an RPC response. Google serves several different row
// layouts (vyAe2 list, top-charts HTML, search/cluster grid, qnKhOb feed); each
// caller supplies its OWN rowPaths, and decodeResultRow applies them uniformly.
//
// Each field is a list of CANDIDATE paths, tried in order; the first one that
// yields a non-empty value wins. This mirrors the layout drift Google
// occasionally ships within a single RPC (the old parseSearchResultNew already
// carried candidate lists for appID and icon). Adding a new candidate path when
// a layout shifts is then a one-line change in one place, applied to whichever
// parser needs it — instead of hunting across four near-identical functions.
//
// A nil/empty field list means "this layout does not carry that field"; the
// decoder simply leaves the zero value. Layouts are deliberately NOT forced into
// a common shape — only the extraction/assembly logic is shared.
type rowPaths struct {
	// unwrapSingleton unwraps a one-element wrapper row ([[actual]] -> actual)
	// before applying any path. Only the search/cluster grid needs this.
	unwrapSingleton bool

	appID     [][]int
	title     [][]int
	icon      [][]int
	developer [][]int
	score     [][]int
	scoreText [][]int
	summary   [][]int

	// developerIDLink points at a "…?id=DEV" developer URL; the id is taken from
	// the query. Used by the qnKhOb layout, which carries no bare developer id.
	developerIDLink [][]int

	// currency and price share a price tuple. price is the raw value in micro
	// units (divided by 1e6); when a price path resolves the row is free iff the
	// value is 0. When no price path resolves at all, the row is treated as free.
	currency [][]int
	price    [][]int

	// urlPath points at a ready-made "/store/apps/…" path. When set and present
	// it is preferred; otherwise the URL is synthesized from AppID — unless
	// urlPathOnly is set, in which case an absent path leaves URL empty (the
	// qnKhOb layout never synthesized a fallback URL). requireAppID gates
	// AppID-candidate acceptance on a package-name prefix check (the grid layout
	// needs this because its appID slot can hold non-package noise).
	urlPath      [][]int
	urlPathOnly  bool
	requireAppID bool
}

// decodeResultRow extracts a SearchResult from one app row using the supplied
// rowPaths. It centralizes the assembly logic every per-RPC parser used to
// duplicate: pick the first non-empty candidate per field, derive Free from the
// price, divide the micro-unit price, and build the canonical URL from AppID
// when no explicit path is given.
func decodeResultRow(item any, paths rowPaths) SearchResult {
	arr, ok := item.([]any)
	if !ok {
		return SearchResult{}
	}

	if paths.unwrapSingleton && len(arr) == 1 {
		if inner, ok := arr[0].([]any); ok {
			arr = inner
		}
	}

	var r SearchResult

	r.AppID = firstAppID(arr, paths.appID, paths.requireAppID)
	r.Title = firstString(arr, paths.title)
	r.Icon = firstString(arr, paths.icon)
	r.Developer = firstString(arr, paths.developer)
	r.Summary = firstString(arr, paths.summary)
	r.ScoreText = firstString(arr, paths.scoreText)
	r.Currency = firstString(arr, paths.currency)

	if v := firstValue(arr, paths.score); v != nil {
		r.Score = toFloat64(v)
	}

	r.DeveloperID = developerIDFromLink(firstString(arr, paths.developerIDLink))

	if v := firstValue(arr, paths.price); v != nil {
		price := toFloat64(v)
		r.Price = price / 1000000
		r.Free = price == 0
	} else {
		r.Free = true
	}

	r.URL = resultURL(firstString(arr, paths.urlPath), r.AppID, paths.urlPathOnly)

	return r
}

// firstValue returns the value at the first candidate path that resolves to a
// non-nil node, or nil when none do.
func firstValue(arr []any, candidates [][]int) any {
	for _, path := range candidates {
		if v := getPath(arr, path...); v != nil {
			return v
		}
	}
	return nil
}

// firstString returns the string form of the first candidate path that resolves
// to a non-empty string, or "" when none do.
func firstString(arr []any, candidates [][]int) string {
	for _, path := range candidates {
		if v := getPath(arr, path...); v != nil {
			if s := toString(v); s != "" {
				return s
			}
		}
	}
	return ""
}

// firstAppID returns the first candidate that looks like an app id. When
// requirePrefix is set the candidate must carry a package-name prefix (the grid
// layout shares its appID slot with non-package values); otherwise the first
// non-empty candidate wins.
func firstAppID(arr []any, candidates [][]int, requirePrefix bool) string {
	for _, path := range candidates {
		v := getPath(arr, path...)
		if v == nil {
			continue
		}
		s := toString(v)
		if s == "" {
			continue
		}
		if requirePrefix && !hasPackagePrefix(s) {
			continue
		}
		return s
	}
	return ""
}

// developerIDFromLink extracts the id from a "…?id=DEV" developer URL, returning
// "" when the link is absent or carries no id.
func developerIDFromLink(link string) string {
	if !strings.Contains(link, "?id=") {
		return ""
	}
	parts := strings.Split(link, "?id=")
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}

// resultURL prefers an explicit "/store/apps/…" path (resolved against BaseURL).
// When the path is absent it synthesizes the canonical listing URL from appID,
// unless pathOnly is set — then an absent path simply yields an empty URL.
func resultURL(path, appID string, pathOnly bool) string {
	if path != "" {
		return BaseURL + path
	}
	if pathOnly || appID == "" {
		return ""
	}
	return fmt.Sprintf("%s/store/apps/details?id=%s", BaseURL, appID)
}
