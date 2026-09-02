package googleplayscraper

import (
	"context"
	"encoding/json"
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
	// Country is the gl country code that was just probed (lowercase).
	Country string `json:"country" example:"us"`
	// Status is the probe outcome for Country.
	Status Status `json:"status"`
	// DoneCount is the running count of probed countries so far.
	DoneCount int `json:"doneCount" minimum:"0" example:"42"`
	// TotalCount is the total number of countries in the sweep.
	TotalCount int `json:"totalCount" minimum:"0" example:"242"`
}

// AvailabilityResult summarizes a completed (or context-cancelled) sweep.
type AvailabilityResult struct {
	// AppID is the probed app.
	AppID string `json:"appId" example:"com.google.android.apps.maps"`
	// Statuses maps each probed gl country code to its Status. Countries that
	// were never reached (e.g. due to context cancellation) are absent.
	Statuses map[string]Status `json:"statuses"`
	// Errors maps a country to the underlying error, populated only for
	// StatusFetchError outcomes. It is nil when no probe errored. Serialized as a
	// country-to-message map.
	Errors map[string]error `json:"errors,omitempty" swaggertype:"object,string"`
	// GloballyRemoved is true only when at least one country was conclusively
	// probed and every conclusive (non-error) probe returned StatusNotFound —
	// i.e. the app appears to have no listing anywhere it was checked. It is only
	// meaningful on a full AllCountries sweep; on a narrow Countries set it
	// merely reflects that subset. Note that AllCountries excludes markets
	// without an official Play Store (cn/ir/kp/sy), so "globally" means across
	// the Play markets, not literally every country.
	GloballyRemoved bool `json:"globallyRemoved" example:"false"`
	// Checked is the number of countries that produced a conclusive status
	// (anything other than StatusFetchError).
	Checked int `json:"checked" minimum:"0" example:"242"`
}

// MarshalJSON serializes Errors as a country-to-message map (err.Error()) rather
// than the empty objects a bare error interface would yield, keeping the JSON
// output and the OpenAPI "object,string" schema truthful. The Go-side Errors
// field stays map[string]error so callers retain the typed errors.
func (r AvailabilityResult) MarshalJSON() ([]byte, error) {
	type rawResult AvailabilityResult // alias drops MarshalJSON, avoiding recursion
	out := struct {
		rawResult
		Errors map[string]string `json:"errors,omitempty"`
	}{rawResult: rawResult(r)}
	if len(r.Errors) > 0 {
		out.Errors = make(map[string]string, len(r.Errors))
		for country, err := range r.Errors {
			out.Errors[country] = err.Error()
		}
	}
	return json.Marshal(out)
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
	ctx, endTask := startTask(ctx, traceTaskAvailability)
	defer endTask()
	logTrace(ctx, "app.id", appID)

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

	// workers seeds the pool size; parallelIndexed clamps it to [1, len].
	workers := opts.Concurrency
	if workers <= 0 {
		workers = c.concurrency
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

	// Probes fan out over a worker pool that records each result via the closure
	// above (serialized by mu) and stops dispatching new countries once ctx is
	// done, returning ctx.Err() in that case. The partial result built so far is
	// still returned alongside it.
	cancelled := parallelIndexed(ctx, len(countries), workers, func(ctx context.Context, i int) {
		country := countries[i]
		status, err := c.checkOne(ctx, appID, country, lang)
		record(country, status, err)
	})

	finalizeAvailability(&result)
	return result, cancelled
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

// checkOne probes a single country and classifies the outcome.
//
// It asks the RPC the details page is built from rather than fetching the page.
// A sweep is one request per country either way -- the country is a query
// parameter, so it cannot be batched, and nothing in the payload enumerates
// countries -- so this changes no request count and no wall-clock time under a
// throttle. What it changes is weight: measured over 20 countries, 25.6MB of
// markup becomes 400KB, and the default 177-country sweep drops from about
// 227MB to 3.5MB. Round-trip time roughly halves too (162ms to 101ms), which
// shows up when the throttle is not the binding constraint.
//
// The three outcomes map exactly onto what the page returned, verified live on
// each case including the awkward one:
//
//	page 404              -> empty payload   -> StatusNotFound
//	page 200, [18] == []  -> [18] == []      -> StatusNotInRegion
//	page 200, [18][0] == 2-> [18][0] == 2    -> StatusAvailable
//
// The awkward case is an embargoed country: com.google.android.apps.tachyon in
// "kp" gives a 404 from the page and an empty payload from the RPC, so both
// call it StatusNotFound rather than "not in region". A missing app id behaves
// identically. Keeping those two indistinguishable is the pre-existing
// behaviour, not something introduced here.
func (c *Client) checkOne(ctx context.Context, appID, country, lang string) (Status, error) {
	payloads, err := c.batchCall(ctx, lang, country, []rpcCall{appDetailsRPC(appID)})
	if err != nil {
		var se *StatusError
		if errors.As(err, &se) && se.Code == http.StatusNotFound {
			return StatusNotFound, nil
		}
		return StatusFetchError, err
	}

	// Google answers an id it cannot serve here with a frame carrying a null
	// payload -- the same signal the page gave as a 404.
	if payloads[0] == "" {
		return StatusNotFound, nil
	}

	var ds5 any
	if err := json.Unmarshal([]byte(payloads[0]), &ds5); err != nil {
		return StatusFetchError, fmt.Errorf("parse %s/%s: %w", appID, country, err)
	}
	appData := getPath(ds5, 1, 2)
	if appData == nil {
		// A payload with no recognizable app node is layout drift or a soft
		// block, not a clean availability signal -- surface it as an error
		// rather than silently calling it "not in region".
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
