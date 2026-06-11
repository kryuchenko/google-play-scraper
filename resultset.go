package googleplayscraper

import (
	"sort"
	"strings"
)

// resultSet is the deduplicating accumulator behind CategoryApps. It keys apps
// by AppID, merges complementary fields when the same app arrives from multiple
// sources, and tracks how many *new* apps each source contributed.
//
// It is not safe for concurrent use; CategoryApps drives it from a single
// goroutine.
type resultSet struct {
	byID      map[string]SearchResult
	perSource map[string]int
	// order preserves first-seen insertion order; sortedResults reorders by
	// AppID for a fully deterministic snapshot regardless of arrival order.
	order []string
}

func newResultSet() *resultSet {
	return &resultSet{
		byID:      make(map[string]SearchResult),
		perSource: make(map[string]int),
	}
}

// addBatch ingests one source's results, returning the number of app IDs not
// previously seen. Existing entries are merged field-by-field: a non-empty
// field already stored is never overwritten, but empty fields are filled from
// the incoming record. This matters because different sources populate
// different fields — Search sets Free=true with no price, while List supplies
// price, currency and summary.
func (rs *resultSet) addBatch(source string, batch []SearchResult) int {
	rs.noteSource(source)

	newCount := 0
	for _, r := range batch {
		if r.AppID == "" {
			continue
		}
		existing, ok := rs.byID[r.AppID]
		if !ok {
			rs.byID[r.AppID] = r
			rs.order = append(rs.order, r.AppID)
			newCount++
			continue
		}
		rs.byID[r.AppID] = mergeResult(existing, r)
	}
	rs.perSource[source] += newCount
	return newCount
}

// noteSource registers a source even if it produced zero new apps (or errored),
// so it remains visible in PerSourceNew.
func (rs *resultSet) noteSource(source string) {
	if _, ok := rs.perSource[source]; !ok {
		rs.perSource[source] = 0
	}
}

func (rs *resultSet) len() int { return len(rs.byID) }

func (rs *resultSet) sourceCount() int { return len(rs.perSource) }

// perSourceSnapshot returns a copy of the per-source new-app counts, safe for
// the caller to retain and mutate.
func (rs *resultSet) perSourceSnapshot() map[string]int {
	out := make(map[string]int, len(rs.perSource))
	for k, v := range rs.perSource {
		out[k] = v
	}
	return out
}

// sortedResults returns all collected apps ordered by AppID, giving a stable
// output independent of source ordering or map iteration.
func (rs *resultSet) sortedResults() []SearchResult {
	ids := make([]string, 0, len(rs.byID))
	for id := range rs.byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	out := make([]SearchResult, 0, len(ids))
	for _, id := range ids {
		out = append(out, rs.byID[id])
	}
	return out
}

// mergeResult fills empty fields of base from incoming without overwriting any
// value base already holds. Free is a special case: a stored "paid" flag
// (Free=false with a non-zero Price) is authoritative and never reverted to the
// default true that listing parsers stamp on every result.
func mergeResult(base, incoming SearchResult) SearchResult {
	base.Title = preferNonEmpty(base.Title, incoming.Title)
	base.URL = preferNonEmpty(base.URL, incoming.URL)
	base.Icon = preferNonEmpty(base.Icon, incoming.Icon)
	base.Developer = preferNonEmpty(base.Developer, incoming.Developer)
	base.DeveloperID = preferNonEmpty(base.DeveloperID, incoming.DeveloperID)
	base.Currency = preferNonEmpty(base.Currency, incoming.Currency)
	base.Summary = preferNonEmpty(base.Summary, incoming.Summary)
	base.ScoreText = preferNonEmpty(base.ScoreText, incoming.ScoreText)

	if base.Score == 0 {
		base.Score = incoming.Score
	}

	// Price/Free: prefer any concrete paid price over the default-free stamp.
	if base.Price == 0 && incoming.Price > 0 {
		base.Price = incoming.Price
		base.Free = false
	} else if !incoming.Free && incoming.Price > 0 && base.Price == 0 {
		base.Free = false
	}

	return base
}

func preferNonEmpty(base, incoming string) string {
	if strings.TrimSpace(base) != "" {
		return base
	}
	return incoming
}

// normalizeTerm canonicalises a search term for queue deduplication.
func normalizeTerm(t string) string {
	return strings.ToLower(strings.TrimSpace(t))
}
