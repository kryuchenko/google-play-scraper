package googleplayscraper

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// Packing several RPCs into one batchexecute POST.
//
// Every method here that talks to /_/PlayStoreUi/data/batchexecute used to send
// exactly one RPC per HTTP request. The endpoint does not require that: the
// f.req body is an array of call tuples, and Google answers each one. Measured
// live, 64 calls in a single POST came back as 64 frames, and mixing different
// rpcids in one request works too.
//
// This matters because the throttle meters *requests*, not RPCs. Fetching
// permissions for 64 apps at a 200ms interval takes 64 slots one way and 2 the
// other; measured end to end that was 18.94s against 0.65s, with the returned
// payloads byte-identical to the one-at-a-time fetch.
//
// What is not established is how Google's own rate limiting counts. The
// wall-clock saving is arithmetic and certain -- there are genuinely fewer
// requests -- but whether the quota sees one request or thirty-two RPCs was not
// measured, and this package has already learnt once this cycle that an
// endpoint tolerating a burst is not an endpoint tolerating sustained load. So
// the pack size stays well under what the endpoint accepts.
//
// The distinction is worth stating precisely, because it decides whether
// packing buys quota headroom or only wall-clock. Writing q(K) for the quota
// units charged for a request carrying K calls, useful throughput is
// Q*K*p(K)/q(K) for an allowance of Q units per second. If q(K) = 1, packing
// multiplies throughput by K. If q(K) = K, it buys nothing from the quota and
// the gain here is purely the client-side throttle spacing fewer requests.
// Both accounting policies are common, and the endpoint returns no quota
// headers of any kind -- checked -- so only a sustained load experiment could
// tell them apart, and that is the experiment that cost this package an outage
// once already.
//
// The ceiling on K is not the endpoint's limit either way. Where a batch is
// all-or-nothing and items fail independently with probability f, expected
// useful work per request is K*(1-f)^K, maximised near K = 1/f: past that,
// bigger batches lose more to blast radius than they gain. That model is
// pessimistic here -- Google returns a frame per call, so a bad id costs its
// own slot and not its neighbours' -- but a failed *request* still costs all K,
// and 32 leaves room under both that and the observed 64.
const maxRPCsPerRequest = 32

// rpcCall is one call in a batchexecute request. payload is the raw inner JSON
// for the RPC, unescaped: buildFReq encodes it as a JSON string, which is
// exactly the quoting Google expects and is easier to get right than writing
// the backslashes by hand.
type rpcCall struct {
	id      string
	payload string
}

// buildFReq renders the f.req form value: [[ [id, payload, null, "i"], ... ]].
//
// The index is the call's position in calls, and it is what decodeBatchFrames
// matches on when the answers come back out of order.
func buildFReq(calls []rpcCall) string {
	var b strings.Builder
	b.WriteString("[[")
	for i, call := range calls {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('[')
		b.Write(jsonString(call.id))
		b.WriteByte(',')
		b.Write(jsonString(call.payload))
		b.WriteString(",null,")
		b.Write(jsonString(strconv.Itoa(i)))
		b.WriteByte(']')
	}
	b.WriteString("]]")
	return b.String()
}

// jsonString quotes s as a JSON string. Marshalling a string cannot fail, so
// the error is discarded rather than propagated through every caller.
func jsonString(s string) []byte {
	b, _ := json.Marshal(s)
	return b
}

// batchCall sends calls as a single POST and returns their payloads, indexed to
// match calls. An RPC that produced no frame gets an empty string; the caller
// decides whether that is an error for its own shape of data.
//
// It does not chunk: callers hold the list they want split, and splitting it
// here would hide the request count from the throttle accounting the caller can
// see. Use chunked() for that.
func (c *Client) batchCall(ctx context.Context, lang, country string, calls []rpcCall) ([]string, error) {
	got, err := c.batchCallFrames(ctx, lang, country, calls)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(got))
	for i, f := range got {
		out[i] = f.Payload
	}
	return out, nil
}

// rpcFrame is one call's answer, keeping apart the two things an empty payload
// can mean.
//
// Google answers an id it will not serve with a frame that is present and
// carries nothing -- that is an answer. A frame that never arrived is not: it
// means the response was short, truncated, or otherwise not what was asked
// for. decodeBatchFrames distinguishes them by the comma-ok of its map, and
// this is where that distinction survives to the caller.
//
// It matters because callers read meaning into emptiness. A genre lookup takes
// "present and empty" to mean the app is gone; at a thousand calls a request,
// a silently dropped response would otherwise report a thousand apps deleted
// at once, and nothing downstream could tell that from a real mass removal.
type rpcFrame struct {
	Payload string
	Present bool
}

// batchCallFrames is batchCall keeping the present/absent distinction.
func (c *Client) batchCallFrames(ctx context.Context, lang, country string, calls []rpcCall) ([]rpcFrame, error) {
	if len(calls) == 0 {
		return nil, nil
	}

	// Linear scan over the distinct ids seen so far. That is O(calls x
	// distinct), and distinct is bounded by the six rpcids this package knows
	// -- in practice one, since a pack is built from a single operation -- so
	// it is linear in the pack size. A map would be slower at this size.
	var ids []string
	for _, call := range calls {
		if !slices.Contains(ids, call.id) {
			ids = append(ids, call.id)
		}
	}

	reqURL := fmt.Sprintf("%s/_/PlayStoreUi/data/batchexecute?rpcids=%s&hl=%s&gl=%s",
		BaseURL, strings.Join(ids, ","), url.QueryEscape(lang), url.QueryEscape(country))

	body, err := c.post(ctx, reqURL,
		"application/x-www-form-urlencoded;charset=UTF-8",
		"f.req="+url.QueryEscape(buildFReq(calls)))
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	frames, err := decodeBatchFrames(body)
	if err != nil {
		return nil, err
	}

	out := make([]rpcFrame, len(calls))

	// One call has exactly one possible answer, so the index is not needed to
	// disambiguate -- and insisting on it would break every recorded fixture,
	// which carries whatever index the code sent when it was captured ("1",
	// "generic") rather than today's. Matching by index is what makes a *batch*
	// safe; a single call is safe without it.
	if len(calls) == 1 && len(frames) == 1 {
		for _, payload := range frames {
			out[0] = rpcFrame{Payload: payload, Present: true}
		}
		return out, nil
	}

	for i := range calls {
		payload, present := frames[strconv.Itoa(i)]
		out[i] = rpcFrame{Payload: payload, Present: present}
	}
	return out, nil
}

// chunked splits items into runs of at most size, so a caller can turn a long
// fan-out into a handful of requests without writing the index arithmetic.
func chunked[T any](items []T, size int) [][]T {
	if size <= 0 {
		size = maxRPCsPerRequest
	}
	var out [][]T
	for s := 0; s < len(items); s += size {
		out = append(out, items[s:min(s+size, len(items))])
	}
	return out
}

// shortenID trims an app id for use in an error message. Ids come from callers
// and callers pass odd things: a four-thousand-character id produces a
// four-thousand-character error that ends up in someone's log for every row of
// a bad input file.
func shortenID(id string) string {
	const max = 64
	if len(id) <= max {
		return id
	}
	return id[:max] + "..."
}
