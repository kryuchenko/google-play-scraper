package lightfeed

import (
	"strings"

	gps "github.com/kryuchenko/google-play-scraper"
)

// linkSet accumulates distinct app links harvested across scroll rounds,
// preserving first-seen order so callers get a stable, deterministic result.
//
// The SearchResults it yields are deliberately THIN: a browser scroll exposes
// only what the anchor element carries — package id, listing URL, and sometimes
// a title (aria-label/text) and icon (img src). Rich fields (score, developer,
// price) are absent; the root package fills them from the initial grid where the
// two overlap, and callers wanting full detail should enrich via App().
type linkSet struct {
	order []string
	apps  map[string]gps.SearchResult
}

func newLinkSet() *linkSet {
	return &linkSet{apps: make(map[string]gps.SearchResult)}
}

// addRaw parses the collector JS output — newline-separated `id\thref\ttitle\ticon`
// records — and adds any apps not already seen.
func (s *linkSet) addRaw(raw string) {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		id := strings.TrimSpace(fields[0])
		if id == "" || s.apps[id].AppID != "" {
			continue
		}
		s.order = append(s.order, id)
		s.apps[id] = gps.SearchResult{
			AppID: id,
			URL:   field(fields, 1),
			Title: field(fields, 2),
			Icon:  field(fields, 3),
		}
	}
}

func (s *linkSet) len() int { return len(s.order) }

// results returns the harvested apps in first-seen order, capped at limit
// (limit <= 0 means "all").
func (s *linkSet) results(limit int) []gps.SearchResult {
	out := make([]gps.SearchResult, 0, len(s.order))
	for _, id := range s.order {
		if limit > 0 && len(out) >= limit {
			break
		}
		out = append(out, s.apps[id])
	}
	return out
}

// field safely reads index i from fields, returning "" when out of range.
func field(fields []string, i int) string {
	if i < len(fields) {
		return strings.TrimSpace(fields[i])
	}
	return ""
}
