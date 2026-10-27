package googleplayscraper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// Fetching app details over batchexecute instead of the details page.
//
// App() reads the "ds:5" script block out of a ~1MB HTML page, one page per
// app. That block is not authored by the page: it is the response of an RPC the
// page issues, and the page says so. Every app page carries an
// AF_dataServiceRequests map keyed by ds: id, and its entry for ds:5 names both
// the RPC -- Ws7gDc -- and the exact request body used to produce it. This
// template is that body, copied verbatim with the package id replaced.
//
// So this is a documented reverse-engineering rather than a guess, and it stays
// checkable: TestWs7gDcTemplateStillMatchesThePage re-extracts the request from
// a live page and fails if Google changes it. To re-capture by hand, fetch any
// app page and read AF_dataServiceRequests['ds:5'].request -- no browser
// devtools needed, unlike the qnKhOb payload next door.
//
// The payload the RPC returns is structurally identical to the page's ds:5
// block, so extractAppData parses it unchanged.
//
// The win is request count. App details are the highest-volume operation there
// is -- a catalog sweep produces millions of ids and details are what a caller
// wants for each -- and the throttle meters requests, so packing 32 apps into
// one request is worth 32 intervals. It also stops transferring a megabyte of
// HTML per app: the RPC answers in about 20KB.
// ws7gdcFullFields is the field selector the app page itself sends -- the
// leading array of field numbers in the request the page publishes for ds:5.
// Asking for all of them is what App and AppsMany do, because they return the
// whole record.
var ws7gdcFullFields = []int{1, 9, 10, 11, 13, 14, 19, 20, 38, 43, 47, 49, 52, 58, 59, 63, 69, 70, 73, 74, 75, 78, 79, 80, 91, 92, 95, 96, 97, 100, 101, 103, 106, 112, 119, 129, 137, 140, 139, 141, 145, 146, 149, 151, 155, 169, 183, 187, 193}

// ws7gdcFieldAvailability is the field carrying the region-availability node.
//
// The field number is one more than the index the dense parser reads: Google's
// positional encoding puts field n at array index n-1, which is why
// extractAppData finds availability at [18].
//
// Asking for it alone does not change that. Captured live, a one-field request
// answers with a nineteen-element array of nulls carrying [2] at index 18 --
// still positional, merely empty everywhere else. That is worth recording
// because it is not the only encoding: digest.go's one-field request for field
// 80 comes back as a map keyed by the field number instead. Readers therefore
// go through digestField, which knows both.
const ws7gdcFieldAvailability = 19

// ws7gdcAvailabilityFields is what an availability probe asks for: the one
// field it reads, and nothing else.
var ws7gdcAvailabilityFields = []int{ws7gdcFieldAvailability}

// ws7gdcRequestShape is the request with the selector and the package id left
// open. Everything outside those two is copied verbatim from what the page
// sends; nothing here is understood well enough to shorten.
const ws7gdcRequestShape = `[null,null,[[%s]],[[[1,null,1],null,[[[]]],null,null,null,null,[null,2],null,null,null,null,null,null,null,null,null,null,null,null,null,null,[1]],[null,[[[]]],null,null,[1]],[null,[[[]]],null,[1]],[null,[[[]]]],null,null,null,null,[[[[]]]],[[[[]]]]],null,[[%s,7]]]`

// ws7gdcRPC builds a Ws7gDc call asking for exactly the given fields.
//
// The selector is what makes a lean call possible: asking for one field
// returns 178-243 bytes per app against 15,880 for the whole record, which is
// the difference between a genre pass over the catalog costing 0.8GB and 62GB.
func ws7gdcRPC(appID string, fields []int) rpcCall {
	var sel strings.Builder
	for i, f := range fields {
		if i > 0 {
			sel.WriteByte(',')
		}
		sel.WriteString(strconv.Itoa(f))
	}
	return rpcCall{
		id:      "Ws7gDc",
		payload: fmt.Sprintf(ws7gdcRequestShape, sel.String(), jsonString(appID)),
	}
}

// appDetailsRPC builds the Ws7gDc call for one app, asking for everything.
func appDetailsRPC(appID string) rpcCall {
	return ws7gdcRPC(appID, ws7gdcFullFields)
}

// ErrNotFound reports an app the requested storefront does not list: its
// details page answers 404 for that country. That is not always "the id does
// not exist". Most storefronts show a region-locked app with App.Available
// false, but some answer 404 instead: in Vietnam and South Korea, games that
// are not distributed there (8 Ball Pool, Pokerist) return 404 while the same
// pages open in other countries. For availability, both mean "not offered".
var ErrNotFound = errors.New("app not found in this storefront")

// errEmptyPayload marks a batched item whose frame came back empty. Alone it
// does not say whether the app exists: in a large sweep about half of these
// are transient and come back on a second request.
var errEmptyPayload = errors.New("no data returned")

// AppResult is one app's outcome in an AppsMany fan-out.
type AppResult struct {
	AppID string
	App   *App
	Err   error
}

// AppsMany fetches details for many apps, packing up to maxRPCsPerRequest of
// them into each HTTP request.
//
// It is the batched form of App, and it differs in more than the request count:
// App reads a rendered HTML page, this calls the RPC that page is built from.
// The parsed result is the same, but a field that only ever appeared in the
// page's markup rather than in ds:5 would not be here -- App remains the
// reference for a single app.
//
// Results are positional: out[i] describes appIDs[i], whatever order Google
// answers in. A request that fails marks every app in that pack and leaves the
// rest of the run intact.
//
// The packs go out over WithConcurrency workers.
//
// An item that comes back empty is not reported as such straight away. Google
// sometimes leaves one app of a pack unanswered while answering the others, and
// in a 293-app, 177-country sweep about half of those came back on a second
// request. So empty items are asked for once more, in a pack of their own, and
// any still empty are settled with one details-page request each: 404 is
// ErrNotFound (not listed in that storefront), anything else is the app. See
// recoverEmpty.
func (c *Client) AppsMany(ctx context.Context, appIDs []string, opts AppOptions) []AppResult {
	ctx, endTask := startTask(ctx, traceTaskAppsMany)
	defer endTask()

	if opts.Lang == "" {
		opts.Lang = "en"
	}
	if opts.Country == "" {
		opts.Country = "us"
	}

	out := make([]AppResult, len(appIDs))
	for i, id := range appIDs {
		out[i].AppID = id
	}

	c.fanOutPacked(ctx, opts.Lang, opts.Country, appIDs,
		func(i int, id string) (rpcCall, bool) {
			// An empty entry is rejected here rather than sent. The singular
			// form validates up front, and a batch that quietly spent a request
			// slot on a blank id -- then reported "no data returned for " --
			// was the same call behaving differently for no reason a caller
			// could see.
			if id == "" {
				out[i].Err = errors.New("appID is required")
				return rpcCall{}, false
			}
			return appDetailsRPC(id), true
		},
		func(i int, id string, frame rpcFrame, err error) {
			if err != nil {
				out[i].Err = err
				return
			}
			// A frame that never arrived carries an empty payload, which is the
			// same thing parseAppRPCPayload reports as "no data returned": for
			// a whole app record there is nothing an absent frame could mean
			// that an empty one does not.
			out[i].App, out[i].Err = parseAppRPCPayload(
				frame.Payload, id, appPageURL(id, opts.Lang, opts.Country))
		})
	c.recoverEmpty(ctx, out, opts)
	return out
}

// recoverEmpty gives the empty items of an AppsMany run a second chance.
//
// Both steps are extra requests, so both draw on the client's retry budget
// (RetryPolicy.Budget) and go through the normal throttle and breaker. A token
// is spent only for a request that is actually sent, and the requests are
// marked as recovery so that they do not earn budget back as first attempts.
// Both steps are skipped when most of the run came back empty: that is not a
// few dropped items but Google declining the whole batch, and asking again item
// by item would multiply exactly the traffic it declined. A refusal or a
// cancellation during recovery ends it and is reported on the items it
// reached, so a caller checking for a block still sees one.
func (c *Client) recoverEmpty(ctx context.Context, out []AppResult, opts AppOptions) {
	var empty []int
	for i := range out {
		if errors.Is(out[i].Err, errEmptyPayload) {
			empty = append(empty, i)
		}
	}
	if len(empty) == 0 || ctx.Err() != nil {
		return
	}
	if len(out) >= 8 && 2*len(empty) > len(out) {
		c.log().LogAttrs(ctx, slog.LevelDebug, "apps-many: most items empty, not retrying",
			slog.Int("empty", len(empty)), slog.Int("of", len(out)))
		return
	}
	rctx := withRecovery(ctx)

	// Step 1: one more batched request for the empty items only. Each pack is
	// one request and takes one token; packs the budget cannot pay for are
	// not sent. Results are addressed by position in empty, never by id, so a
	// list that repeats an id cannot send two workers to the same slot.
	ids := make([]string, len(empty))
	for k, i := range empty {
		ids[k] = out[i].AppID
	}
	packs := (len(ids) + maxRPCsPerRequest - 1) / maxRPCsPerRequest
	paid := 0
	for paid < packs && c.takeRetry() {
		paid++
	}
	ids = ids[:min(paid*maxRPCsPerRequest, len(ids))]
	var stop error // a refusal or cancellation that ends recovery
	if len(ids) > 0 {
		unsent := make([]bool, packs)
		var mu sync.Mutex
		c.fanOutPacked(rctx, opts.Lang, opts.Country, ids,
			func(_ int, id string) (rpcCall, bool) { return appDetailsRPC(id), true },
			func(k int, id string, frame rpcFrame, err error) {
				i := empty[k]
				if err != nil {
					mu.Lock()
					defer mu.Unlock()
					if notSent(rctx, err) {
						unsent[k/maxRPCsPerRequest] = true
					}
					if isBlockErr(err) || rctx.Err() != nil {
						out[i].Err = err
						if stop == nil {
							stop = err
						}
					}
					return // otherwise keep the first answer: this was a second try
				}
				app, perr := parseAppRPCPayload(frame.Payload, id, appPageURL(id, opts.Lang, opts.Country))
				if perr == nil {
					out[i].App, out[i].Err = app, nil
				}
			})
		for _, u := range unsent {
			if u {
				c.refundRetry()
			}
		}
	}
	recovered := 0
	for _, i := range empty {
		if out[i].Err == nil {
			recovered++
		}
	}

	// Step 2: settle what is still empty with the details page: 404 means the
	// storefront does not list the app.
	notFound, settled := 0, 0
	for _, i := range empty {
		if stop != nil || ctx.Err() != nil {
			break
		}
		if !errors.Is(out[i].Err, errEmptyPayload) {
			continue
		}
		if !c.takeRetry() {
			break
		}
		app, err := c.App(rctx, out[i].AppID, opts)
		var se *StatusError
		switch {
		case err == nil:
			out[i].App, out[i].Err = app, nil
			settled++
		case errors.As(err, &se) && se.Code == http.StatusNotFound:
			out[i].Err = fmt.Errorf("%w: %s", ErrNotFound, shortenID(out[i].AppID))
			notFound++
		default:
			if notSent(rctx, err) {
				c.refundRetry()
			}
			if isBlockErr(err) || rctx.Err() != nil {
				out[i].Err = err
				stop = err
			}
		}
	}
	c.log().LogAttrs(ctx, slog.LevelDebug, "apps-many: empty items",
		slog.Int("empty", len(empty)), slog.Int("recovered_by_retry", recovered),
		slog.Int("recovered_by_page", settled), slog.Int("not_found", notFound),
		slog.Int("unresolved", len(empty)-recovered-settled-notFound),
		slog.Any("stopped_by", stop))
}

// notSent reports whether a request ended before reaching the server: the
// breaker refused to admit it, or the context ended while it waited.
func notSent(ctx context.Context, err error) bool {
	return errors.Is(err, ErrBlocked) || (ctx.Err() != nil && errors.Is(err, ctx.Err()))
}

// appPageURL is the canonical page address for an app. The batched path never
// fetches it, but App records it in the result and callers rely on it, so a
// batched result must carry the same value.
func appPageURL(appID, lang, country string) string {
	return fmt.Sprintf("%s/store/apps/details?id=%s&hl=%s&gl=%s", BaseURL, appID, lang, country)
}

// parseAppRPCPayload feeds a Ws7gDc payload through the same extractor the HTML
// path uses. The payload is what the page would have carried in ds:5, so it is
// presented under exactly that key rather than parsed a second way.
func parseAppRPCPayload(payload, appID, pageURL string) (*App, error) {
	if payload == "" {
		return nil, fmt.Errorf("%w for %s", errEmptyPayload, shortenID(appID))
	}
	var ds5 any
	if err := json.Unmarshal([]byte(payload), &ds5); err != nil {
		return nil, fmt.Errorf("parse %s: %w", shortenID(appID), err)
	}
	return extractAppData(map[string]any{"ds:5": ds5}, appID, pageURL)
}
