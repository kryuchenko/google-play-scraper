package googleplayscraper

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
)

// Status is the region-level availability of an app, as determined by probing a
// single country's listing. It distinguishes the four outcomes a probe can have:
// installable, present-but-not-offered, no listing at all, and a transport error
// that left availability unknown for that country.
type Status int

const (
	// StatusUnknown is the zero value: no probe result was recorded. It appears
	// in a Statuses map only if a country was never reached.
	StatusUnknown Status = iota
	// StatusAvailable means the app is installable in the country ([18][0]==2).
	StatusAvailable
	// StatusNotInRegion means the listing exists but the app is not offered in
	// the country — either region-locked ([18] empty) or a pre-registration
	// entry ([18][0]==1), neither of which is installable.
	StatusNotInRegion
	// StatusNotFound means Google returned 404: there is no listing for the app
	// in that country (it may be region-removed or globally delisted).
	StatusNotFound
	// StatusFetchError means the probe failed for a transport/HTTP reason other
	// than 404 (e.g. a timeout or rate-limit), so availability is genuinely
	// unknown for that country. The underlying error is recorded in
	// Result.Errors.
	//
	// Note: this enum member is named StatusFetchError, not StatusError, because
	// StatusError is already the exported HTTP-status error type in request.go.
	// Reusing that identifier for a Status constant would shadow the type and
	// break errors.As call sites. See the report for the architect.
	StatusFetchError
)

// String returns a lowercase, human-readable name for the status.
func (s Status) String() string {
	switch s {
	case StatusAvailable:
		return "available"
	case StatusNotInRegion:
		return "not_in_region"
	case StatusNotFound:
		return "not_found"
	case StatusFetchError:
		return "error"
	default:
		return "unknown"
	}
}

// AvailabilityOptions configures an Availability sweep.
type AvailabilityOptions struct {
	// Countries to probe, as gl codes (case-insensitive; normalized to
	// lowercase and deduplicated). Empty means AllCountries.
	Countries []string
	// Lang is the hl value for every probe. Empty defaults to "en". The page is
	// region-agnostic in content, so the language only affects error/localized
	// text, not the availability signal.
	Lang string
	// Concurrency is the number of countries probed in parallel. Zero falls back
	// to the client's configured concurrency (default 1, i.e. sequential). The
	// shared throttle still bounds the overall request rate across workers.
	Concurrency int
	// Progress, if set, is called once per probed country with that country's
	// outcome and the running count. It is invoked serially (under the result
	// lock), so it need not be goroutine-safe itself.
	Progress func(AvailabilityProgress)
}

// AvailabilityProgress is a single observability event emitted per country.
type AvailabilityProgress struct {
	Country    string
	Status     Status
	DoneCount  int
	TotalCount int
}

// AvailabilityResult summarizes a completed (or context-cancelled) sweep.
type AvailabilityResult struct {
	// AppID is the probed app.
	AppID string
	// Statuses maps each probed country to its Status. Countries that were never
	// reached (e.g. due to context cancellation) are absent.
	Statuses map[string]Status
	// Errors maps a country to the underlying error, populated only for
	// StatusFetchError outcomes. It is nil when no probe errored.
	Errors map[string]error
	// GloballyRemoved is true only when at least one country was conclusively
	// probed and every conclusive (non-error) probe returned StatusNotFound —
	// i.e. the app appears to have no listing anywhere it was checked. It is only
	// meaningful on a full AllCountries sweep; on a narrow Countries set it
	// merely reflects that subset. Note that AllCountries excludes markets
	// without an official Play Store (cn/ir/kp/sy), so "globally" means across
	// the Play markets, not literally every country.
	GloballyRemoved bool
	// Checked is the number of countries that produced a conclusive status
	// (anything other than StatusFetchError).
	Checked int
}

// Availability probes the app's listing in each requested country and reports
// the per-country region availability. It is far cheaper than a full App() call
// per country: each probe fetches the page and reads only the [18] availability
// node plus the HTTP status, skipping the full parse.
//
// A single country's failure (404 or transport error) never aborts the sweep;
// only context cancellation does, in which case the partial result is returned
// alongside ctx.Err().
func (c *Client) Availability(ctx context.Context, appID string, opts AvailabilityOptions) (AvailabilityResult, error) {
	if appID == "" {
		return AvailabilityResult{}, fmt.Errorf("appID is required")
	}

	countries := opts.Countries
	if len(countries) == 0 {
		countries = AllCountries
	}
	countries = normalizeCountries(countries)

	lang := opts.Lang
	if lang == "" {
		lang = "en"
	}

	workers := opts.Concurrency
	if workers <= 0 {
		workers = c.concurrency
	}
	if workers < 1 {
		workers = 1
	}
	if workers > len(countries) {
		workers = len(countries)
	}

	result := AvailabilityResult{
		AppID:    appID,
		Statuses: make(map[string]Status, len(countries)),
	}

	// All shared state (the result maps and the progress counter) is guarded by
	// mu. Probes run concurrently; recording is serialized, which also lets the
	// Progress callback stay single-threaded.
	var mu sync.Mutex
	record := func(country string, status Status, err error) {
		mu.Lock()
		defer mu.Unlock()
		result.Statuses[country] = status
		if status == StatusFetchError && err != nil {
			if result.Errors == nil {
				result.Errors = make(map[string]error)
			}
			result.Errors[country] = err
		}
		if opts.Progress != nil {
			opts.Progress(AvailabilityProgress{
				Country:    country,
				Status:     status,
				DoneCount:  len(result.Statuses),
				TotalCount: len(countries),
			})
		}
	}

	probe := func(country string) {
		status, err := c.checkOne(ctx, appID, country, lang)
		record(country, status, err)
	}

	var cancelled error
	if workers <= 1 {
		for _, country := range countries {
			if err := ctx.Err(); err != nil {
				cancelled = err
				break
			}
			probe(country)
		}
	} else {
		cancelled = runProbes(ctx, countries, workers, probe)
	}

	finalizeAvailability(&result)
	return result, cancelled
}

// runProbes fans the countries out over a fixed worker pool, preserving the
// "context cancellation stops the sweep" contract: workers drain the queue but
// skip any country once the context is done, and the first observed ctx error is
// returned. Probe results are recorded by the probe closure itself.
func runProbes(ctx context.Context, countries []string, workers int, probe func(string)) error {
	indexes := make(chan int)
	var (
		wg       sync.WaitGroup
		once     sync.Once
		canceled error
	)

	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for i := range indexes {
				if err := ctx.Err(); err != nil {
					once.Do(func() { canceled = err })
					continue
				}
				probe(countries[i])
			}
		}()
	}
	for i := range countries {
		indexes <- i
	}
	close(indexes)
	wg.Wait()
	return canceled
}

// finalizeAvailability computes the aggregate fields once every probe is
// recorded: Checked counts conclusive (non-error) statuses, and GloballyRemoved
// latches only when there is at least one conclusive status and all of them are
// StatusNotFound.
func finalizeAvailability(r *AvailabilityResult) {
	checked := 0
	allNotFound := true
	for _, status := range r.Statuses {
		if status == StatusFetchError {
			continue
		}
		checked++
		if status != StatusNotFound {
			allNotFound = false
		}
	}
	r.Checked = checked
	r.GloballyRemoved = checked > 0 && allNotFound
}

// checkOne probes a single country's listing and classifies the outcome without
// running the full App parser. It maps a 404 to StatusNotFound, any other
// transport/HTTP error to StatusError (returning the error for the caller to
// record), and otherwise reads the [18] availability node via classifyAvailability.
func (c *Client) checkOne(ctx context.Context, appID, country, lang string) (Status, error) {
	url := fmt.Sprintf("%s/store/apps/details?id=%s&hl=%s&gl=%s", BaseURL, appID, lang, country)

	body, err := c.get(ctx, url)
	if err != nil {
		var se *StatusError
		if errors.As(err, &se) && se.Code == http.StatusNotFound {
			return StatusNotFound, nil
		}
		return StatusFetchError, err
	}

	appData, ok := appDataNode(body)
	if !ok {
		// A 200 whose body has no recognizable app node is a layout-drift or
		// soft-block surprise, not a clean availability signal — surface it as an
		// error rather than silently calling it "not in region".
		return StatusFetchError, fmt.Errorf("app data not found for %s/%s", appID, country)
	}
	return classifyAvailability(appData), nil
}

// AvailableCountries is a thin convenience wrapper over Availability that returns
// just the sorted list of countries where the app is installable (StatusAvailable).
// It carries the same options and the same partial-result-on-cancellation
// semantics as Availability.
func (c *Client) AvailableCountries(ctx context.Context, appID string, opts AvailabilityOptions) ([]string, error) {
	result, err := c.Availability(ctx, appID, opts)

	available := make([]string, 0)
	for country, status := range result.Statuses {
		if status == StatusAvailable {
			available = append(available, country)
		}
	}
	sort.Strings(available)
	return available, err
}

// normalizeCountries lowercases, trims and deduplicates country codes while
// preserving first-seen order, so a sweep is deterministic and never probes the
// same country twice.
func normalizeCountries(countries []string) []string {
	seen := make(map[string]bool, len(countries))
	out := make([]string, 0, len(countries))
	for _, c := range countries {
		code := strings.ToLower(strings.TrimSpace(c))
		if code == "" || seen[code] {
			continue
		}
		seen[code] = true
		out = append(out, code)
	}
	return out
}
