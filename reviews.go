package googleplayscraper

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"net/url"
	"strconv"
	"time"
)

// ReviewsComprehensive fetches reviews by querying each rating separately to maximize unique results.
// This works around Google Play's tendency to return duplicate reviews across different queries.
// Returns up to opts.Count reviews per rating (5 ratings), so total can be up to 5x opts.Count.
//
// A rating whose pass fails is skipped and the error is not reported: four
// ratings out of five is still an answer. The error is returned only when every
// pass failed, because that is a run that observed nothing rather than an app
// with no reviews.
func (c *Client) ReviewsComprehensive(ctx context.Context, appID string, opts ReviewOptions) ([]Review, error) {
	ctx, endTask := startTask(ctx, traceTaskReviewsCompr)
	defer endTask()
	logTrace(ctx, "app.id", appID)

	seen := make(map[string]bool)
	var allReviews []Review
	var lastErr error

	countPerRating := opts.Count
	if countPerRating == 0 {
		countPerRating = 200 // default per rating
	}

	for score := 1; score <= 5; score++ {
		scoreOpts := opts
		scoreOpts.FilterScore = score
		scoreOpts.Count = countPerRating
		scoreOpts.NextToken = "" // Reset pagination for each rating

		// Streamed rather than collected: the dedup below discards most of
		// what a rating returns anyway, so buffering a full page set per
		// rating only to drop it is wasted. A rating that fails is skipped,
		// as before -- the point of sweeping five of them is that four still
		// produce a useful result.
		var kept int
		for r, err := range c.ReviewsSeq(ctx, appID, scoreOpts) {
			if err != nil {
				lastErr = err
				break
			}
			if !seen[r.ID] {
				seen[r.ID] = true
				allReviews = append(allReviews, r)
			}
			kept++
			if kept >= countPerRating {
				break
			}
		}
	}

	// Four ratings out of five is still a useful answer, which is why a failed
	// pass is skipped -- but five out of five is not an empty result set, it is
	// a run that produced nothing and used to say so with a nil error.
	if len(allReviews) == 0 && lastErr != nil {
		return nil, fmt.Errorf("every rating pass failed; the last error was: %w", lastErr)
	}
	return allReviews, nil
}

// ReviewsAll fetches reviews with auto-pagination up to opts.Count total,
// defaulting to 500 when Count is zero.
//
// Deprecated: this method will be removed in v2. Use [Client.ReviewsSeq],
// which hands reviews over as they arrive instead of buffering the whole run,
// and has no ceiling to guess at. Migration is a change of shape rather than a
// rename, so it is not automated:
//
//	// before
//	reviews, err := c.ReviewsAll(ctx, appID, opts)
//
//	// after
//	for review, err := range c.ReviewsSeq(ctx, appID, opts) {
//		if err != nil {
//			return err
//		}
//		// ... and break wherever the caller actually wants to stop
//	}
func (c *Client) ReviewsAll(ctx context.Context, appID string, opts ReviewOptions) ([]Review, error) {
	maxTotal := opts.Count
	if maxTotal <= 0 {
		// Zero is the documented default; a negative count is a caller's bug,
		// and the slice below would panic on it rather than reporting it.
		maxTotal = 500 // default max
	}

	// Pagination lives in reviewsPages alone. Keeping a second copy here is how
	// the two drift: a fix to the token chain would have to be made twice, and
	// the one that was missed would only show up on a deep run.
	//
	// The page size is the one thing that cannot be shared: ReviewsSeq asks for
	// the largest page there is because it will keep going, while this stops at
	// maxTotal, and a 1000-review page fetched to return ten is a megabyte on
	// the wire per call.
	out := make([]Review, 0, maxTotal)
	for r, err := range c.reviewsPages(ctx, appID, opts, min(maxTotal, reviewsPageMax)) {
		if err != nil {
			return out, err
		}
		out = append(out, r)
		if len(out) >= maxTotal {
			break
		}
	}
	return out, nil
}

// Reviews fetches reviews for an app
func (c *Client) Reviews(ctx context.Context, appID string, opts ReviewOptions) (*ReviewsResult, error) {
	ctx, endTask := startTask(ctx, traceTaskReviews)
	defer endTask()
	logTrace(ctx, "app.id", appID)

	return c.reviewsPage(ctx, appID, opts)
}

// reviewsPage is Reviews without the trace task. reviewsPages walks the token
// chain by calling it once per page, which is where opening a task per call
// would become a task per page of an unbounded sweep.
func (c *Client) reviewsPage(ctx context.Context, appID string, opts ReviewOptions) (*ReviewsResult, error) {
	if appID == "" {
		return nil, fmt.Errorf("appID is required")
	}

	// Apply defaults
	if opts.Lang == "" {
		opts.Lang = "en"
	}
	if opts.Country == "" {
		opts.Country = "us"
	}
	if opts.Sort == 0 {
		opts.Sort = SortNewest
	}
	if opts.Count == 0 {
		// One page, and the caller sees its size in the slice they get back --
		// so this stays where it was rather than following reviewsPageMax up.
		// ReviewsSeq raises its own page size instead, where it is an internal
		// detail and bigger is purely fewer requests.
		opts.Count = 150
	}

	frames, err := c.batchCallFrames(ctx, opts.Lang, opts.Country, []rpcCall{reviewsRPC(appID, opts)})
	if err != nil {
		return nil, err
	}

	// A frame that never arrived and a frame carrying a null payload are the
	// same answer here, and both are an error: neither is a page of reviews.
	// The distinction rpcFrame exists for matters where emptiness is data --
	// a digest reads it as "the app is gone" -- and reviews have no such
	// reading, so they are not told apart.
	return parseReviewsPayload(frames[0].Payload, appID)
}

// reviewsPageMax is how many reviews one request may ask for.
//
// This was 150, commented "Google Play limit per request". It is not a limit
// Google imposes: asked for 150, 200, 300, 500, 1000, 2000 and 3000, the
// endpoint returned exactly that many every time. The number appears to have
// been copied from somewhere and never tested.
//
// It cost a request every 150 reviews. Sweeping a month of one busy app's
// reviews -- 5,785 of them -- took 39 requests at 150 and 6 at 1000, for the
// same reviews: compared by id, nothing was missing in either direction and no
// shared review differed by a character. The operation is throttle-bound (39
// requests at a 500ms interval is 19.5s of pure waiting), so the request count
// is the wall clock.
//
// The ceiling is real but much higher: 3000 and 4000 both return exactly what
// they ask for; 5000 and 10000 come back as a PlayGatewayError frame, which
// this package surfaces as "data is not a string" rather than as reviews.
//
// An earlier version of this comment said those failures were indistinguishable
// from Google's "no more reviews" signal and therefore truncated a sweep in
// silence. That was wrong in both halves -- the error frame is structurally
// different from a null payload, and overshooting fails loudly. 1000 is chosen
// for the latency curve below, not to stay clear of a silent failure.
//
// 1000 is also close to where the curve stops paying. Latency grows with the
// page, sub-linearly at first and then not, so past the point where it exceeds
// the throttle each extra review costs roughly its own transfer time and the
// wall clock flattens. Measured on the same month of reviews, projecting one
// page's best-of-three latency over the pages it would take at a 500ms
// interval:
//
//	page    latency   pages   wall
//	 150      136ms      39   19.5s
//	 500      250ms      12    6.0s
//	1000      546ms       6    3.3s
//	2000     1057ms       3    3.2s
//	3000     1272ms       2    2.5s
//
// Going from 1000 to 2000 buys 3%. Going to 3000 buys 22% and puts the request
// one step from a size that fails by returning nothing. 1000 takes 83% of the
// available gain and leaves the margin that a silent failure mode deserves.
//
// A trace of the sweep confirms the bottleneck moved: at 150 the throttle was
// binding (39 requests at 500ms is 19.5s of the 19.3s run) and no throttle-wait
// region opened at all at 1000, because each 686ms request already outlasts the
// interval. Fewer requests was the right lever; it is now spent.
const reviewsPageMax = 1000

// The reviews filter array, and what is not in it.
//
// The payload this builds is
//
//	[null,[2,SORT,[COUNT,null,TOKEN],null,[null,SCORE]],["APPID",7]]
//
// and the filter list is the fifth element of the inner array -- the
// [null,SCORE] group. (An earlier version of this comment pointed at the last
// group, ["APPID",7]; that one is the app id and a content-type constant, and
// changing its 7 is rejected outright.) Google's own client, and the widely
// used Python scraper that copies it, send nine elements there:
//
//	[null, SCORE, null, null, null, null, null, null, DEVICE]
//
// Probing every slot against a live app:
//
//	slot 0    ignored
//	slot 1    SCORE, the star filter this package sends
//	slot 2    string; every value tried returned an empty result
//	slot 3    integer; 1 selects a different population, 2 is rejected,
//	          0 and 3..10 return nothing
//	slot 4,5  ignored
//	slot 6    integer, accepts only 1 and 2, and changes the result set
//	slot 7    string; like slot 2
//	slot 8    DEVICE: 3 and "TABLET" select the same different population,
//	          1 and "phone" are no-ops
//
// There is no arity gate. All-null at 1, 2, 5, 9 and 11 elements returns the
// same reviews in the same order, so the short form this package sends is not
// a truncation Google tolerates -- it is simply the prefix that matters.
//
// So there are live filters here that this package does not expose. What slots
// 3, 6 and 8 select was not pinned down, and the reason is that nobody went
// back to do it rather than anything about the endpoint: repeated identical
// requests return byte-identical id sequences, so a filter's effect is
// perfectly distinguishable from drift. Recorded as a known unknown.
//
// None of them is a country. Slots 2 and 7 take strings and returned nothing
// for every country code, locale and version string tried; the response
// carries no geographic field; and gl selects the storefront without changing
// which reviews come back. That was checked on kz.kaspi.mobile, a bank used
// almost entirely from one country, where ru/kz and ru/us are identical id for
// id. The oldest community request for a country parameter (facundoolano/
// google-play-scraper#276, 2018) was closed without one.
//
// Which makes a common practice wrong rather than merely imprecise. Answers on
// Stack Overflow and several published datasets loop over country codes,
// tagging each review with the country it was "fetched from":
//
//	for country in countries:
//	    rs = reviews(app, country=country)
//	    for r in rs: r['country'] = country
//
// Every pass returns the same reviews, so the resulting country column is
// invented. Reviews can only be separated by language, and language is not
// country: ru spans Russia, Belarus, Ukraine and Kazakhstan at once.
//
// What *is* per-country is the App.Reviews count -- Spotify reads 1,850,613 in
// the US, 83,329 in Russia, 22,700 in Kazakhstan -- and the shape of the
// ratings histogram. It moves with gl and not at all with hl, the mirror of
// the reviews themselves. App.Ratings is not per-country: it is the same
// global total in every market (36,228,6xx for Spotify, varying only by
// seconds of drift), so the histogram is a reweighting of one total rather
// than a partition of it. The review count is the evidence that Google knows
// where a review came from; the histogram, on its own, is not.
//
// Do not trust that count market by market without looking at it. gl=am
// reports 12.1M reviews for Spotify -- a third of every rating the app has,
// from a country of three million -- and gl=zz, which is not a country at all,
// is answered rather than refused. The mechanism is real; some storefronts
// return a figure that is plainly not their own.
func reviewsRPC(appID string, opts ReviewOptions) rpcCall {
	count := opts.Count
	if count > reviewsPageMax {
		count = reviewsPageMax
	}

	// Build filter score part (null if not filtering, otherwise the score)
	scorePart := "null"
	if opts.FilterScore >= 1 && opts.FilterScore <= 5 {
		scorePart = fmt.Sprintf("%d", opts.FilterScore)
	}

	// The page group is [COUNT] on a first request and [COUNT,null,TOKEN] on a
	// continuation, which is the whole of pagination: the token is the third
	// slot and nothing else moves.
	page := strconv.Itoa(count)
	if opts.NextToken != "" {
		page += ",null," + string(jsonString(opts.NextToken))
	}

	return rpcCall{
		id: "oCPfdb",
		payload: fmt.Sprintf(`[null,[2,%d,[%s],null,[null,%s]],[%s,7]]`,
			opts.Sort, page, scorePart, jsonString(appID)),
	}
}

// buildReviewsBody renders the form body one reviews call puts on the wire.
//
// It composes the same two functions the request path composes -- reviewsRPC
// and buildFReq -- so it cannot describe a request the client does not make.
// It exists because the payload above is a format, and the tests that pin the
// format should be able to read it without standing up a server to catch it.
func buildReviewsBody(appID string, opts ReviewOptions) string {
	return "f.req=" + url.QueryEscape(buildFReq([]rpcCall{reviewsRPC(appID, opts)}))
}

// parseReviewsResponse decodes a page into ReviewsResult.
//
// It decodes the payload into any-trees and then walks them, which the
// allocation profile says is half the cost: arrayInterface alone is 49% of the
// allocations, boxing every element of Google's positional arrays. Two smaller
// ideas were tried and rejected -- a json.Decoder to skip Unmarshal's separate
// validation pass makes allocated bytes worse (6.9MB to 10.3MB), and Go 1.27's
// json v2 is 0.4% different with allocation counts identical to the unit.
//
// The idea that does work is On-Demand parsing (Keiser & Lemire, 2024,
// arXiv:2312.17149): iterate the document with a pointer and materialise only
// what is read. The paper is clear about where the gain comes from -- skipping
// unread bytes, and writing straight into the target instead of an
// intermediate tree -- and notes the first mechanism vanishes when the whole
// document is consumed.
//
// Both apply here, which an earlier note in this repository got backwards. A
// captured 1000-review page is 822KB of review entries, of which the fields
// this function reads are 389KB: 47% read, 53% skippable. (That figure was
// first written as 290KB and 35%, which omitted the user avatar URL -- 99KB
// across the page, and a field this function does populate.) A prototype that
// finds each element's byte range without decoding it, then unmarshals only
// the wanted positions, measured against this exact path:
//
//	              time      bytes    allocs
//	current    6.94 ms    3.66 MB    79,046
//	on-demand  3.97 ms    1.36 MB    10,937
//	           1.75x       2.7x        7.2x
//
// It is not implemented here, and the reason is not doubt about the numbers.
// It is a hand-written JSON scanner on the busiest parse path, and the two
// scanners this package already has earned their place by being held against
// the implementations they replaced under differential fuzzing. This one needs
// the same, which is more than a release already verified end to end should
// absorb. The win is also memory rather than wall clock: a month of one app's
// reviews spends 93ms parsing against 4s of network.

func parseReviewsResponse(body []byte, appID string) (*ReviewsResult, error) {
	frames, err := decodeBatchFrames(body)
	if err != nil {
		return nil, err
	}
	payload, ok := soleFrame(frames)
	if !ok {
		payload = frames["0"]
	}
	return parseReviewsPayload(payload, appID)
}

// parseReviewsPayload reads the inner JSON of one oCPfdb frame, split from the
// envelope handling so the request path can decode the envelope once and hand
// the payload straight over -- the same division suggest.go and permissions.go
// use.
//
// An empty payload is an error rather than an empty page. Google returns a
// frame whose data slot is null for a request it will not serve -- an overshot
// page size, a token it no longer honours -- and a run that read that as "no
// more reviews" would end in silence at an arbitrary point.
func parseReviewsPayload(payload, appID string) (*ReviewsResult, error) {
	if payload == "" {
		return nil, fmt.Errorf("data is not a string")
	}

	var data []any
	if err := json.Unmarshal([]byte(payload), &data); err != nil {
		return nil, fmt.Errorf("parse inner json: %w", err)
	}

	return extractReviews(data, appID)
}

func extractReviews(data []any, appID string) (*ReviewsResult, error) {
	result := &ReviewsResult{
		Reviews: []Review{},
	}

	if len(data) == 0 {
		return result, nil
	}

	// Extract reviews array from data[0]
	reviewsData, ok := data[0].([]any)
	if !ok {
		return result, nil
	}

	for _, item := range reviewsData {
		review, err := parseReview(item, appID)
		if err != nil {
			continue // Skip malformed reviews
		}
		if review.ID == "" {
			continue // Skip empty reviews
		}
		result.Reviews = append(result.Reviews, review)
	}

	// Extract next token from data[1][1]
	if len(data) > 1 {
		if tokenData, ok := data[1].([]any); ok && len(tokenData) > 1 {
			if token, ok := tokenData[1].(string); ok {
				result.NextToken = token
			}
		}
	}

	return result, nil
}

func parseReview(item any, appID string) (Review, error) {
	arr, ok := item.([]any)
	if !ok {
		return Review{}, fmt.Errorf("review is not an array")
	}

	review := Review{}

	// ID: [0]
	if len(arr) > 0 {
		if id, ok := arr[0].(string); ok {
			review.ID = id
			// Concatenation rather than Sprintf: this runs once per review,
			// and at a thousand reviews a page the formatter's machinery was
			// showing up in the allocation profile for a string whose shape
			// never varies.
			review.URL = BaseURL + "/store/apps/details?id=" + appID + "&reviewId=" + id
		}
	}

	// UserName: [1][0]
	if len(arr) > 1 {
		if userData, ok := arr[1].([]any); ok && len(userData) > 0 {
			if name, ok := userData[0].(string); ok {
				review.UserName = name
			}
			// UserImage: [1][1][3][2]
			if len(userData) > 1 {
				if imgData, ok := userData[1].([]any); ok && len(imgData) > 3 {
					if imgInner, ok := imgData[3].([]any); ok && len(imgInner) > 2 {
						if img, ok := imgInner[2].(string); ok {
							review.UserImage = img
						}
					}
				}
			}
		}
	}

	// Score: [2]
	if len(arr) > 2 {
		if score, ok := arr[2].(float64); ok {
			review.Score = int(score)
		}
	}

	// Text: [4]
	if len(arr) > 4 {
		if text, ok := arr[4].(string); ok {
			review.Text = text
		}
	}

	// Date: [5]
	if len(arr) > 5 {
		if dateArr, ok := arr[5].([]any); ok {
			review.Date = parseTimestamp(dateArr)
		}
	}

	// ThumbsUp: [6]
	if len(arr) > 6 {
		if thumbs, ok := arr[6].(float64); ok {
			review.ThumbsUp = int(thumbs)
		}
	}

	// ReplyText: [7][1], ReplyDate: [7][2]
	if len(arr) > 7 {
		if replyData, ok := arr[7].([]any); ok {
			if len(replyData) > 1 {
				if replyText, ok := replyData[1].(string); ok {
					review.ReplyText = replyText
				}
			}
			if len(replyData) > 2 {
				if replyDateArr, ok := replyData[2].([]any); ok {
					review.ReplyDate = parseTimestamp(replyDateArr)
				}
			}
		}
	}

	// Version: [10]
	if len(arr) > 10 {
		if version, ok := arr[10].(string); ok {
			review.Version = version
		}
	}

	return review, nil
}

func parseTimestamp(arr []any) time.Time {
	if len(arr) < 1 {
		return time.Time{}
	}

	seconds, ok := arr[0].(float64)
	if !ok {
		return time.Time{}
	}

	// Convert seconds to milliseconds
	ms := int64(seconds) * 1000
	if len(arr) > 1 {
		if extra, ok := arr[1].(float64); ok {
			// Add milliseconds part
			extraStr := fmt.Sprintf("%d", int(extra))
			if len(extraStr) >= 3 {
				extraStr = extraStr[:3]
			}
			// Truncated to at most three digits above, so this parses unless
			// the payload handed us something that was never a number. On
			// failure the sub-second part is simply absent, which is the same
			// outcome as a payload that never carried one.
			if extraMs, perr := strconv.ParseInt(extraStr, 10, 64); perr == nil {
				ms += extraMs
			}
		}
	}

	return time.UnixMilli(ms)
}

// ReviewsSeq is ReviewsAll as an iterator: it yields reviews page by page and
// keeps paginating until the caller stops or the store runs out.
//
//	for review, err := range client.ReviewsSeq(ctx, appID, opts) {
//		if err != nil {
//			return err
//		}
//		if review.Date.Before(cutoff) {
//			break
//		}
//	}
//
// Two things this fixes. ReviewsAll accumulates every review in memory before
// returning any of them, which is unbounded in the app's popularity; here they
// are handed over as they arrive. And ReviewsAll stops at 500 by default, a
// limit the caller cannot see and can only raise by guessing a number --
// opts.Count is ignored here, because the loop body is a better place to
// decide when to stop than a count chosen in advance.
//
// A page that fails to fetch ends the sequence: pagination is a token chain,
// and without the next token there is nowhere to continue from. The error is
// the final element, paired with a zero Review. Reviews already yielded stay
// valid, so a caller that hits an error midway keeps what it has.
func (c *Client) ReviewsSeq(ctx context.Context, appID string, opts ReviewOptions) iter.Seq2[Review, error] {
	return c.reviewsPages(ctx, appID, opts, reviewsPageMax)
}

// reviewsPages is ReviewsSeq with the page size as a parameter.
//
// ReviewsSeq wants the largest page there is, because for it a page is purely a
// request count. ReviewsAll has a ceiling it must not exceed, and asking for a
// thousand reviews to return ten is a megabyte of transfer per call.
func (c *Client) reviewsPages(ctx context.Context, appID string, opts ReviewOptions, pageSize int) iter.Seq2[Review, error] {
	return func(yield func(Review, error) bool) {
		ctx, endTask := startTask(ctx, traceTaskReviewsSeq)
		defer endTask()
		logTrace(ctx, "app.id", appID)

		opts.Count = pageSize // page size, not a total
		for {
			result, err := c.reviewsPage(ctx, appID, opts)
			if err != nil {
				yield(Review{}, err)
				return
			}
			for _, r := range result.Reviews {
				if !yield(r, nil) {
					return
				}
			}
			if result.NextToken == "" || len(result.Reviews) == 0 {
				return
			}
			opts.NextToken = result.NextToken
		}
	}
}
