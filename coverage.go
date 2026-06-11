package googleplayscraper

import (
	"context"
	"sort"
)

// Coverage orchestration
// =======================
//
// A single Google Play listing endpoint returns at most ~200 apps anonymously,
// and the catalogue cannot be paginated past that ceiling without a session.
// CategoryApps works around the cap by unioning many *independent* slices of the
// same category — different collections, locales, age buckets, search terms and
// a similar/developer graph walk — and deduplicating the results. Each slice is
// individually capped, but their union is far larger than any single request.
//
// Every slice is fetched through the existing public methods (List, Cluster,
// Search, Suggest, Similar, Developer), so throttling and context handling are
// inherited for free.

// Coverage tuning defaults. They are deliberately conservative: a coverage run
// can issue thousands of requests, so the safety valves matter more than raw
// reach.
const (
	defaultMaxApps             = 50000
	defaultGraphSeeds          = 20
	defaultSaturationWindow    = 8
	defaultSaturationThreshold = 0.01

	// coverageSearchNum is the per-term Search depth. Search caps at
	// searchMaxNum (250); we ask for the maximum because Search is the main
	// multiplier in a coverage run.
	coverageSearchNum = searchMaxNum

	// coverageClusterNum bounds how deep each category cluster is paginated.
	// Clusters rarely exceed a few hundred apps; this keeps a single cluster
	// from dominating the request budget.
	coverageClusterNum = 200

	// coverageQueueGuard caps the search-term queue so ExpandSuggest cannot
	// grow it without bound.
	coverageQueueGuard = 2000
)

// Locale is a country/language pair used to fetch a region-specific slice of a
// category. Different locales surface different apps, which is the cheapest way
// to multiply coverage.
type Locale struct {
	Country string
	Lang    string
}

// CoverageOptions configures a CategoryApps run.
type CoverageOptions struct {
	Category Category

	// Collections to sweep in the core phase. Defaults to all three
	// (TopFree, TopPaid, Grossing).
	Collections []Collection

	// Locales to fetch every phase in. Defaults to {{"us","en"}}. Use
	// CoverageLocales for a high-dispersion preset.
	Locales []Locale

	// Ages to apply as an extra core-phase axis. Empty means no age filter.
	Ages []Age

	// SearchTerms seeds the search phase. When nil, defaultSearchTerms(Category)
	// is used.
	SearchTerms []string

	// ExpandSuggest runs each search term through Suggest and feeds the
	// derived terms back into the queue, deduplicated and bounded.
	ExpandSuggest bool

	// GraphDepth is the number of BFS levels to walk over Similar/Developer.
	// Zero disables the graph phase.
	GraphDepth int

	// GraphSeeds is how many highest-scoring, not-yet-expanded apps to use as
	// seeds at each BFS level. Defaults to defaultGraphSeeds.
	GraphSeeds int

	// MaxApps is a hard ceiling on unique apps collected. Zero applies
	// defaultMaxApps; pass a negative value only if you genuinely want no cap
	// (treated as unlimited).
	MaxApps int

	// SaturationWindow is the number of trailing sources inspected when
	// deciding whether the run has saturated. Defaults to
	// defaultSaturationWindow.
	SaturationWindow int

	// SaturationThreshold is the fraction of new apps (new / fetched) over the
	// window below which the expensive tail phases (search, graph) stop early.
	// Defaults to defaultSaturationThreshold. The core and cluster phases
	// always run in full.
	SaturationThreshold float64

	// Progress, if set, is called after every source with that source's label,
	// the number of new unique apps it contributed, and the running total.
	Progress func(CoverageProgress)
}

// CoverageProgress is a single observability event emitted per source.
type CoverageProgress struct {
	Source      string
	NewCount    int
	TotalUnique int
}

// CoverageResult summarises a completed run.
type CoverageResult struct {
	Apps         []SearchResult
	SourcesRun   int
	PerSourceNew map[string]int
	RequestsMade int
	Saturated    bool
}

// CategoryApps collects the most complete unique set of apps for a category by
// unioning many independent listing slices.
//
// Phases run in order of increasing cost:
//
//  1. Core: List over Collections × Locales × Ages.
//  2. Clusters: ClusterURLs(Category) → Cluster, per locale.
//  3. Search: each term (optionally Suggest-expanded) × locale × price.
//  4. Graph: BFS over Similar/Developer from the highest-scoring apps.
//
// Saturation only short-circuits the expensive tail (phases 3–4); phases 1–2
// always run in full. Failure of an individual source is logged (as zero new
// apps) and the run continues — only context cancellation aborts and returns an
// error.
func (c *Client) CategoryApps(ctx context.Context, opts CoverageOptions) (CoverageResult, error) {
	opts = withCoverageDefaults(opts)

	rs := newResultSet()
	run := &coverageRun{
		client:  c,
		opts:    opts,
		results: rs,
	}

	if err := run.corePhase(ctx); err != nil {
		return run.result(), err
	}
	if err := run.clusterPhase(ctx); err != nil {
		return run.result(), err
	}
	if err := run.searchPhase(ctx); err != nil {
		return run.result(), err
	}
	if err := run.graphPhase(ctx); err != nil {
		return run.result(), err
	}

	return run.result(), nil
}

// withCoverageDefaults fills unset options with their defaults without mutating
// the caller's slices.
func withCoverageDefaults(opts CoverageOptions) CoverageOptions {
	if len(opts.Collections) == 0 {
		opts.Collections = []Collection{CollectionTopFree, CollectionTopPaid, CollectionGrossing}
	}
	if len(opts.Locales) == 0 {
		opts.Locales = []Locale{{Country: "us", Lang: "en"}}
	}
	if opts.SearchTerms == nil {
		opts.SearchTerms = defaultSearchTerms(opts.Category)
	}
	if opts.GraphSeeds <= 0 {
		opts.GraphSeeds = defaultGraphSeeds
	}
	if opts.MaxApps == 0 {
		opts.MaxApps = defaultMaxApps
	}
	if opts.SaturationWindow <= 0 {
		opts.SaturationWindow = defaultSaturationWindow
	}
	if opts.SaturationThreshold <= 0 {
		opts.SaturationThreshold = defaultSaturationThreshold
	}
	return opts
}

// coverageRun holds the mutable state of a single CategoryApps invocation. It
// keeps CategoryApps itself readable as a phase pipeline and gives each phase a
// shared place to record requests, saturation samples and seed bookkeeping.
type coverageRun struct {
	client  *Client
	opts    CoverageOptions
	results *resultSet

	requests int
	// satSamples holds the new/fetched ratio of recent tail-phase sources, used
	// to detect saturation over a sliding window.
	satSamples []float64
	saturated  bool

	// seeded tracks app IDs already used as BFS seeds, so each app expands at
	// most once across all graph levels.
	seeded map[string]bool
}

// record ingests one source's batch: it dedups into the result set, updates the
// request counter, fires the progress callback and records a saturation sample
// when the source belongs to an expensive tail phase.
func (r *coverageRun) record(source string, batch []SearchResult, err error, tail bool) {
	r.requests++

	newCount := 0
	if err == nil {
		newCount = r.results.addBatch(source, batch)
	} else {
		// Keep the source visible in PerSourceNew even on failure, so a run
		// that silently fetched nothing is distinguishable from one that was
		// never attempted.
		r.results.noteSource(source)
	}

	if tail && err == nil {
		fetched := len(batch)
		ratio := 1.0
		if fetched > 0 {
			ratio = float64(newCount) / float64(fetched)
		}
		r.satSamples = append(r.satSamples, ratio)
	}

	if r.opts.Progress != nil {
		r.opts.Progress(CoverageProgress{
			Source:      source,
			NewCount:    newCount,
			TotalUnique: r.results.len(),
		})
	}
}

// capReached reports whether the unique-app ceiling has been hit.
func (r *coverageRun) capReached() bool {
	return r.opts.MaxApps > 0 && r.results.len() >= r.opts.MaxApps
}

// checkSaturation evaluates the sliding window and latches r.saturated once the
// average new-app ratio over a full window drops below the threshold.
func (r *coverageRun) checkSaturation() {
	w := r.opts.SaturationWindow
	if len(r.satSamples) < w {
		return
	}
	window := r.satSamples[len(r.satSamples)-w:]
	var sum float64
	for _, s := range window {
		sum += s
	}
	if sum/float64(w) < r.opts.SaturationThreshold {
		r.saturated = true
	}
}

func (r *coverageRun) result() CoverageResult {
	return CoverageResult{
		Apps:         r.results.sortedResults(),
		SourcesRun:   r.results.sourceCount(),
		PerSourceNew: r.results.perSourceSnapshot(),
		RequestsMade: r.requests,
		Saturated:    r.saturated,
	}
}

// corePhase: List × Collections × Locales × Ages. Always runs in full.
func (r *coverageRun) corePhase(ctx context.Context) error {
	ages := r.opts.Ages
	if len(ages) == 0 {
		ages = []Age{AgeAll}
	}

	for _, loc := range r.opts.Locales {
		for _, col := range r.opts.Collections {
			for _, age := range ages {
				if err := ctx.Err(); err != nil {
					return err
				}
				if r.capReached() {
					return nil
				}
				batch, err := r.client.List(ctx, ListOptions{
					Collection: col,
					Category:   r.opts.Category,
					Age:        age,
					Lang:       loc.Lang,
					Country:    loc.Country,
					Num:        listMaxNum,
				})
				r.record(sourceLabel("list", string(col), loc, string(age)), batch, err, false)
			}
		}
	}
	return nil
}

// clusterPhase: ClusterURLs(Category) → Cluster, per locale. Always runs in
// full.
func (r *coverageRun) clusterPhase(ctx context.Context) error {
	for _, loc := range r.opts.Locales {
		if err := ctx.Err(); err != nil {
			return err
		}
		if r.capReached() {
			return nil
		}

		clusters, err := r.client.ClusterURLs(ctx, ClusterURLsOptions{
			Category: r.opts.Category,
			Lang:     loc.Lang,
			Country:  loc.Country,
		})
		// ClusterURLs is discovery, not a result source; count the request but
		// do not record it as an app source.
		r.requests++
		if err != nil {
			continue
		}

		for _, cl := range clusters {
			if err := ctx.Err(); err != nil {
				return err
			}
			if r.capReached() {
				return nil
			}
			batch, cErr := r.client.Cluster(ctx, ClusterOptions{
				Path:    cl.URL,
				Lang:    loc.Lang,
				Country: loc.Country,
				Num:     coverageClusterNum,
			})
			r.record(sourceLabel("cluster", cl.Title, loc, ""), batch, cErr, false)
		}
	}
	return nil
}

// searchPhase is the main multiplier: a queue of terms, each run × locale ×
// price, optionally expanded via Suggest. It is a tail phase and honours
// saturation.
func (r *coverageRun) searchPhase(ctx context.Context) error {
	queue := newTermQueue(r.opts.SearchTerms)

	for {
		term, ok := queue.next()
		if !ok {
			return nil
		}
		if r.saturated || r.capReached() {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		for _, loc := range r.opts.Locales {
			if r.opts.ExpandSuggest {
				suggestions, sErr := r.client.Suggest(ctx, SuggestOptions{
					Term:    term,
					Lang:    loc.Lang,
					Country: loc.Country,
				})
				r.requests++
				if sErr == nil {
					queue.enqueue(suggestions)
				}
			}

			for _, price := range []string{"all", "paid"} {
				if err := ctx.Err(); err != nil {
					return err
				}
				if r.capReached() {
					return nil
				}
				batch, err := r.client.Search(ctx, SearchOptions{
					Term:    term,
					Lang:    loc.Lang,
					Country: loc.Country,
					Num:     coverageSearchNum,
					Price:   price,
				})
				r.record(sourceLabel("search:"+price, term, loc, ""), batch, err, true)
			}
		}

		r.checkSaturation()
	}
}

// graphPhase walks the similar/developer graph breadth-first from the
// highest-scoring apps collected so far. It is a tail phase and honours
// saturation.
//
// Developer expansion uses SearchResult.DeveloperID when present. Most listing
// sources do not populate it, so in practice the developer walk only fires for
// apps that arrived via paginated search results — we deliberately avoid an
// extra App() lookup per seed, which would dominate the request budget.
func (r *coverageRun) graphPhase(ctx context.Context) error {
	if r.opts.GraphDepth <= 0 {
		return nil
	}
	r.seeded = make(map[string]bool)

	// The graph phase walks a single locale (the first), as Similar/Developer
	// pages are dominated by the app identity, not the locale, and adding
	// locale fan-out here multiplies an already expensive phase.
	loc := r.opts.Locales[0]

	for level := 0; level < r.opts.GraphDepth; level++ {
		if r.saturated || r.capReached() {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		seeds := r.topUnseededSeeds(r.opts.GraphSeeds)
		if len(seeds) == 0 {
			return nil
		}

		for _, seed := range seeds {
			if err := ctx.Err(); err != nil {
				return err
			}
			if r.capReached() {
				return nil
			}
			r.seeded[seed.AppID] = true

			sim, sErr := r.client.Similar(ctx, SimilarOptions{
				AppID:   seed.AppID,
				Lang:    loc.Lang,
				Country: loc.Country,
			})
			r.record(sourceLabel("similar", seed.AppID, loc, ""), sim, sErr, true)

			if seed.DeveloperID != "" {
				dev, dErr := r.client.Developer(ctx, DeveloperOptions{
					DevID:   seed.DeveloperID,
					Lang:    loc.Lang,
					Country: loc.Country,
					Num:     coverageClusterNum,
				})
				r.record(sourceLabel("developer", seed.DeveloperID, loc, ""), dev, dErr, true)
			}
		}

		r.checkSaturation()
	}
	return nil
}

// topUnseededSeeds returns up to n collected apps with the highest Score that
// have not yet been used as BFS seeds.
func (r *coverageRun) topUnseededSeeds(n int) []SearchResult {
	all := r.results.sortedResults()
	sort.SliceStable(all, func(i, j int) bool {
		return all[i].Score > all[j].Score
	})

	seeds := make([]SearchResult, 0, n)
	for _, app := range all {
		if app.AppID == "" || r.seeded[app.AppID] {
			continue
		}
		seeds = append(seeds, app)
		if len(seeds) >= n {
			break
		}
	}
	return seeds
}

// termQueue is a FIFO of search terms with case-insensitive deduplication and a
// hard size guard, so Suggest expansion cannot grow it without bound.
type termQueue struct {
	pending []string
	seen    map[string]bool
}

func newTermQueue(initial []string) *termQueue {
	q := &termQueue{seen: make(map[string]bool)}
	q.enqueue(initial)
	return q
}

func (q *termQueue) enqueue(terms []string) {
	for _, t := range terms {
		key := normalizeTerm(t)
		if key == "" || q.seen[key] {
			continue
		}
		if len(q.seen) >= coverageQueueGuard {
			return
		}
		q.seen[key] = true
		q.pending = append(q.pending, t)
	}
}

func (q *termQueue) next() (string, bool) {
	if len(q.pending) == 0 {
		return "", false
	}
	t := q.pending[0]
	q.pending = q.pending[1:]
	return t, true
}

// sourceLabel builds a stable, human-readable source key for PerSourceNew and
// progress events: "<kind>[:detail]@<country>/<lang>".
func sourceLabel(kind, detail string, loc Locale, extra string) string {
	label := kind
	if detail != "" {
		label += ":" + detail
	}
	if extra != "" {
		label += ":" + extra
	}
	return label + "@" + loc.Country + "/" + loc.Lang
}
