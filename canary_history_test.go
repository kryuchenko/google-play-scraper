package googleplayscraper_test

import (
	"encoding/json"
	"fmt"
	"iter"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	googleplayscraper "github.com/kryuchenko/google-play-scraper"
)

// The canary's history oracle: consistency with the last good run.
//
// The canary's field checks ask "is this field there", mostly on the first
// result. A parse path that starts returning an empty developer for half the
// apps of a list passes every one of them. So the canary also counts, per
// endpoint, how many of the records it already fetched have each key field
// filled, and the median latency of each subtest, and compares them with
// testdata/canary_baseline.json, written by a passing live run with
// UPDATE_CANARY_BASELINE=1.
//
// The thresholds are set against normal store variance:
//
//   - A fill rate may fall by at most 0.25 (absolute) from the baseline. List
//     endpoints return a different set of apps from week to week, and an
//     optional field such as summary moves with it, by a few hundredths on a
//     20-to-60-app list. A broken path does not move by a few hundredths: it
//     drops to zero, or to the share of records that come through another
//     path. A quarter is far above the first and below the second for any
//     field filled in more than a quarter of records; fields rarer than that
//     are too noisy to judge this way and only the floor applies.
//   - Some fields are on every record the store has ever served (an app's id,
//     title, icon). Those have a hard floor in canaryFloors, independent of the
//     baseline, so a baseline taken on a bad day cannot lower the bar.
//   - Latency is only logged, when a subtest's median is three times the
//     baseline. It depends on the runner's network and Google's load as much
//     as on this code; 3x is past ordinary jitter and still worth a look, and a
//     slow store is not drift.
const (
	canaryMaxFillDrop = 0.25
	canarySlowFactor  = 3
	canaryBaselineEnv = "UPDATE_CANARY_BASELINE"
	canaryBaseline    = "testdata/canary_baseline.json"
)

// canaryFloors are fill rates no run may go below, per endpoint and field.
var canaryFloors = map[string]map[string]float64{
	"search":    {"app_id": 1, "title": 0.95, "url": 0.95, "icon": 0.95},
	"list":      {"app_id": 1, "title": 0.95, "url": 0.95, "icon": 0.95},
	"cluster":   {"app_id": 1, "title": 0.95, "url": 0.95, "icon": 0.95},
	"developer": {"app_id": 1, "title": 0.95, "url": 0.95, "icon": 0.95},
	"similar":   {"app_id": 1, "title": 0.95, "url": 0.95, "icon": 0.95},
	"reviews":   {"id": 1, "user_name": 0.9, "date": 1, "score": 1, "url": 0.95},
	"app_page":  {"title": 1, "developer": 1, "icon": 1, "genre_id": 1, "installs": 1, "url": 1},
	"app_rpc":   {"title": 1, "developer": 1, "icon": 1, "genre_id": 1, "url": 1},
}

// canaryFields are the key fields counted per kind of record.
var (
	searchResultFields = map[string]func(googleplayscraper.SearchResult) bool{
		"app_id":    func(r googleplayscraper.SearchResult) bool { return r.AppID != "" },
		"title":     func(r googleplayscraper.SearchResult) bool { return r.Title != "" },
		"url":       func(r googleplayscraper.SearchResult) bool { return r.URL != "" },
		"icon":      func(r googleplayscraper.SearchResult) bool { return r.Icon != "" },
		"developer": func(r googleplayscraper.SearchResult) bool { return r.Developer != "" },
		"score":     func(r googleplayscraper.SearchResult) bool { return r.Score > 0 },
		"summary":   func(r googleplayscraper.SearchResult) bool { return r.Summary != "" },
		"price":     func(r googleplayscraper.SearchResult) bool { return r.PriceKnown },
	}
	reviewFields = map[string]func(googleplayscraper.Review) bool{
		"id":         func(r googleplayscraper.Review) bool { return r.ID != "" },
		"user_name":  func(r googleplayscraper.Review) bool { return r.UserName != "" },
		"user_image": func(r googleplayscraper.Review) bool { return r.UserImage != "" },
		"date":       func(r googleplayscraper.Review) bool { return !r.Date.IsZero() },
		"score":      func(r googleplayscraper.Review) bool { return r.Score >= 1 && r.Score <= 5 },
		"text":       func(r googleplayscraper.Review) bool { return strings.TrimSpace(r.Text) != "" },
		"version":    func(r googleplayscraper.Review) bool { return r.Version != "" },
		"url":        func(r googleplayscraper.Review) bool { return r.URL != "" },
	}
	appFields = map[string]func(*googleplayscraper.App) bool{
		"title":          func(a *googleplayscraper.App) bool { return a.Title != "" },
		"summary":        func(a *googleplayscraper.App) bool { return a.Summary != "" },
		"description":    func(a *googleplayscraper.App) bool { return a.Description != "" },
		"developer":      func(a *googleplayscraper.App) bool { return a.Developer != "" },
		"developer_id":   func(a *googleplayscraper.App) bool { return a.DeveloperID != "" },
		"icon":           func(a *googleplayscraper.App) bool { return a.Icon != "" },
		"header_image":   func(a *googleplayscraper.App) bool { return a.HeaderImage != "" },
		"screenshots":    func(a *googleplayscraper.App) bool { return len(a.Screenshots) > 0 },
		"score":          func(a *googleplayscraper.App) bool { return a.Score > 0 },
		"ratings":        func(a *googleplayscraper.App) bool { return a.Ratings > 0 },
		"installs":       func(a *googleplayscraper.App) bool { return a.Installs != "" },
		"genre_id":       func(a *googleplayscraper.App) bool { return a.GenreID != "" },
		"content_rating": func(a *googleplayscraper.App) bool { return a.ContentRating != "" },
		"updated":        func(a *googleplayscraper.App) bool { return a.Updated > 0 },
		"url":            func(a *googleplayscraper.App) bool { return a.URL != "" },
	}
)

// canaryHistory accumulates one run's fill counts and latencies.
type canaryHistory struct {
	fill    map[string]*fillCount
	latency map[string][]time.Duration
}

type fillCount struct {
	n      int
	filled map[string]int
}

func newCanaryHistory() *canaryHistory {
	return &canaryHistory{fill: map[string]*fillCount{}, latency: map[string][]time.Duration{}}
}

func countFill[T any](h *canaryHistory, endpoint string, fields map[string]func(T) bool, recs []T) {
	fc := h.fill[endpoint]
	if fc == nil {
		fc = &fillCount{filled: map[string]int{}}
		h.fill[endpoint] = fc
	}
	for _, r := range recs {
		fc.n++
		for name, has := range fields {
			if has(r) {
				fc.filled[name]++
			}
		}
	}
}

func (h *canaryHistory) results(endpoint string, rs []googleplayscraper.SearchResult) {
	countFill(h, endpoint, searchResultFields, rs)
}

func (h *canaryHistory) reviews(rs []googleplayscraper.Review) {
	countFill(h, "reviews", reviewFields, rs)
}

func (h *canaryHistory) apps(endpoint string, apps ...*googleplayscraper.App) {
	countFill(h, endpoint, appFields, apps)
}

// took adds the response latencies of a subtest's hook events.
func (h *canaryHistory) took(subtest string, events []googleplayscraper.EvidenceEvent) {
	for _, ev := range events {
		if ev.Hook != "response" || ev.Status == 0 {
			continue
		}
		if d, err := time.ParseDuration(ev.Took); err == nil {
			h.latency[subtest] = append(h.latency[subtest], d)
		}
	}
}

// canaryBaselineDoc is testdata/canary_baseline.json.
type canaryBaselineDoc struct {
	Comment   string                     `json:"comment"`
	Source    string                     `json:"source"`
	Taken     string                     `json:"taken"`
	Fill      map[string]canaryFillEntry `json:"fill"`
	LatencyMS map[string]int64           `json:"latency_ms"`
}

type canaryFillEntry struct {
	N      int                `json:"n"`
	Fields map[string]float64 `json:"fields"`
}

// snapshot turns the run into a baseline document.
func (h *canaryHistory) snapshot(source string) canaryBaselineDoc {
	doc := canaryBaselineDoc{
		Comment:   "Fill rates and median latencies of the last good canary run; see canary_history_test.go. Rewrite with UPDATE_CANARY_BASELINE=1 go test -tags canary -run TestCanary .",
		Source:    source,
		Taken:     time.Now().UTC().Format(time.DateOnly),
		Fill:      map[string]canaryFillEntry{},
		LatencyMS: map[string]int64{},
	}
	for ep, fc := range h.fill {
		e := canaryFillEntry{N: fc.n, Fields: map[string]float64{}}
		for name := range fieldsOf(ep) {
			e.Fields[name] = rate(fc.filled[name], fc.n)
		}
		doc.Fill[ep] = e
	}
	for st, ds := range h.latency {
		doc.LatencyMS[st] = median(ds).Milliseconds()
	}
	return doc
}

// fieldsOf names the fields counted for an endpoint's kind of record.
func fieldsOf(endpoint string) iter.Seq[string] {
	switch endpoint {
	case "reviews":
		return maps.Keys(reviewFields)
	case "app_page", "app_rpc":
		return maps.Keys(appFields)
	}
	return maps.Keys(searchResultFields)
}

func rate(filled, n int) float64 {
	if n == 0 {
		return 0
	}
	// Two decimals: the file is read by people, and finer is noise.
	return float64(int(float64(filled)/float64(n)*100+0.5)) / 100
}

func median(ds []time.Duration) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	s := slices.Clone(ds)
	slices.Sort(s)
	return s[len(s)/2]
}

// compare checks this run against base. failures fail the canary; notes are
// logged.
func (h *canaryHistory) compare(base canaryBaselineDoc) (failures, notes []string) {
	now := h.snapshot("")
	eps := slices.Sorted(maps.Keys(canaryFloors))
	for ep := range base.Fill {
		if !slices.Contains(eps, ep) {
			eps = append(eps, ep)
		}
	}
	for _, ep := range eps {
		cur, ran := now.Fill[ep]
		if !ran || cur.N == 0 {
			notes = append(notes, fmt.Sprintf("%s: no records this run (its subtest failed or did not run); not compared", ep))
			continue
		}
		was, known := base.Fill[ep]
		for _, f := range slices.Sorted(maps.Keys(cur.Fields)) {
			got := cur.Fields[f]
			if floor, ok := canaryFloors[ep][f]; ok && got < floor {
				failures = append(failures, fmt.Sprintf("%s.%s: filled in %.2f of %d records, below the floor of %.2f -- its parse path may have broken",
					ep, f, got, cur.N, floor))
				continue
			}
			prev, ok := was.Fields[f]
			if !known || !ok {
				notes = append(notes, fmt.Sprintf("%s.%s: no baseline (%.2f now); rerun with %s=1 after a good run", ep, f, got, canaryBaselineEnv))
				continue
			}
			if prev-got > canaryMaxFillDrop {
				failures = append(failures, fmt.Sprintf("%s.%s: filled in %.2f of %d records, %.2f in the baseline -- a drop of %.2f, more than %.2f",
					ep, f, got, cur.N, prev, prev-got, canaryMaxFillDrop))
			}
		}
	}
	for _, st := range slices.Sorted(maps.Keys(now.LatencyMS)) {
		ms, prev := now.LatencyMS[st], base.LatencyMS[st]
		if prev > 0 && ms > canarySlowFactor*prev {
			notes = append(notes, fmt.Sprintf("%s: median response %dms, %dx the baseline %dms -- slow, not failed", st, ms, ms/prev, prev))
		}
	}
	sort.Strings(notes)
	return failures, notes
}

func readCanaryBaseline(path string) (canaryBaselineDoc, error) {
	var doc canaryBaselineDoc
	b, err := os.ReadFile(path)
	if err != nil {
		return doc, err
	}
	return doc, json.Unmarshal(b, &doc)
}

func writeCanaryBaseline(path string, doc canaryBaselineDoc) error {
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// TestCanaryHistoryOracle checks the oracle offline, on the frozen search
// fixture: the committed baseline must parse and name every endpoint the
// canary counts; a field emptied in half the records fails; ordinary churn
// does not; a slow run is only a note.
func TestCanaryHistoryOracle(t *testing.T) {
	base, err := readCanaryBaseline(canaryBaseline)
	if err != nil {
		t.Fatalf("%s: %v", canaryBaseline, err)
	}
	for ep := range canaryFloors {
		if _, ok := base.Fill[ep]; !ok {
			t.Errorf("%s has no fill rates for %s", canaryBaseline, ep)
		}
	}

	results := searchFixtureResults(t)
	good := newCanaryHistory()
	good.results("search", results)
	ref := good.snapshot("fixture")

	if f, _ := good.compare(ref); len(f) != 0 {
		t.Errorf("a run compared with itself failed: %v", f)
	}

	// One record in ten loses its summary: churn, not drift.
	churn := slices.Clone(results)
	for i := 0; i < len(churn); i += 10 {
		churn[i].Summary = ""
	}
	h := newCanaryHistory()
	h.results("search", churn)
	if f, _ := h.compare(ref); len(f) != 0 {
		t.Errorf("ordinary churn failed the oracle: %v", f)
	}

	// Developer gone from half the records: drift, and it names the field.
	broken := slices.Clone(results)
	for i := 0; i < len(broken); i += 2 {
		broken[i].Developer = ""
	}
	h = newCanaryHistory()
	h.results("search", broken)
	if f, _ := h.compare(ref); len(f) != 1 || !strings.Contains(f[0], "search.developer") {
		t.Errorf("half the developers gone: %v, want one failure naming search.developer", f)
	}

	// A floor holds whatever the baseline says.
	low := ref
	low.Fill = map[string]canaryFillEntry{"search": {N: 1, Fields: map[string]float64{"icon": 0}}}
	noIcons := slices.Clone(results)
	for i := range noIcons {
		noIcons[i].Icon = ""
	}
	h = newCanaryHistory()
	h.results("search", noIcons)
	if f, _ := h.compare(low); !slices.ContainsFunc(f, func(s string) bool { return strings.Contains(s, "search.icon") && strings.Contains(s, "floor") }) {
		t.Errorf("no icons against a baseline of none: %v, want the floor to fail it", f)
	}

	// Slow is a note, never a failure. Latency comes from the hook events of
	// answered requests; transport failures (status 0) are not latencies.
	h = newCanaryHistory()
	h.results("search", results)
	h.took("Search", []googleplayscraper.EvidenceEvent{
		{Hook: "request"},
		{Hook: "response", Status: 200, Took: "4s"},
		{Hook: "response", Status: 0, Took: "30s"},
	})
	slowBase := ref
	slowBase.LatencyMS = map[string]int64{"Search": 1000}
	f, notes := h.compare(slowBase)
	if len(f) != 0 || !slices.ContainsFunc(notes, func(s string) bool { return strings.Contains(s, "Search: median response 4000ms") }) {
		t.Errorf("slow run: failures %v, notes %v", f, notes)
	}

	// Reviews and apps are counted per record, and a written baseline reads
	// back as the run it was taken from.
	h = newCanaryHistory()
	h.reviews([]googleplayscraper.Review{{ID: "a", Score: 5, Version: "1"}, {ID: "b", Score: 4}})
	h.apps("app_rpc", &googleplayscraper.App{Title: "T", Icon: "i"})
	snap := h.snapshot("unit test")
	if got := snap.Fill["reviews"]; got.N != 2 || got.Fields["id"] != 1 || got.Fields["version"] != 0.5 || got.Fields["text"] != 0 {
		t.Errorf("reviews fill %+v", got)
	}
	if got := snap.Fill["app_rpc"]; got.N != 1 || got.Fields["title"] != 1 || got.Fields["developer"] != 0 {
		t.Errorf("apps fill %+v", got)
	}
	path := filepath.Join(t.TempDir(), "baseline.json")
	if err := writeCanaryBaseline(path, snap); err != nil {
		t.Fatal(err)
	}
	back, err := readCanaryBaseline(path)
	if err != nil || !reflect.DeepEqual(back.Fill, snap.Fill) {
		t.Errorf("baseline round trip: %+v, %v", back.Fill, err)
	}
}

// searchFixtureResults parses testdata/search_page.html as the canary's
// Search would see it, through a client that never leaves the process.
func searchFixtureResults(t *testing.T) []googleplayscraper.SearchResult {
	t.Helper()
	page, err := os.ReadFile("testdata/search_page.html")
	if err != nil {
		t.Fatal(err)
	}
	rs, err := googleplayscraper.ParseSearchPage(page)
	if err != nil || len(rs) < 10 {
		t.Fatalf("search fixture: %d results, %v", len(rs), err)
	}
	return rs
}
