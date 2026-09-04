package googleplayscraper

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"slices"
	"sync"
)

// Digests: asking the details RPC for a few fields instead of the whole record.
//
// AppsMany returns everything an app page holds, which is right when a caller
// wants an app and wrong when it wants one number about three million of them.
// The RPC carries a field selector, and trimming it is worth far more than it
// sounds: the genre alone comes back in 178-243 bytes against 15,880 for the
// full record, so a genre pass over the whole catalog moves from 62GB to 0.8GB.
//
// The smaller answer is also what makes the pack size large. Batch size here is
// bounded by response bytes rather than by any count Google enforces -- asked
// for 24,576 lean calls in one request it answered all of them -- so the
// constant below is derived from a byte budget rather than guessed.
//
// What this cannot do is stand in for AppsMany. A digest knows only the fields
// it asked for, and the response shape changes with the selector, which is
// exactly the trap digestField exists to handle.

// AppDigest is what one app answered.
type AppDigest struct {
	AppID string `json:"appId"`

	// Listed is true when Google returned a listing. False means the store
	// answered with nothing, which it does for a removed app and for an id
	// that never existed -- the two are indistinguishable here. It is not a
	// regional block: ids that answer with nothing at gl=us answer with
	// nothing at gl=de and gl=in too, verified.
	//
	// Absence only becomes a *removal* against a previous snapshot, which is
	// the caller's business rather than this package's.
	Listed bool `json:"listed"`

	Genre   string `json:"genre,omitempty"`   // "Casual", varies with Lang
	GenreID string `json:"genreId,omitempty"` // "GAME_CASUAL", stable

	// Err is set when nothing was learned about this app. When it is non-nil
	// no other field means anything -- in particular Listed is false because
	// the question went unanswered, not because the app is gone.
	Err error `json:"-"`
}

// DigestFields selects what a digest asks for. Each field costs bytes on the
// wire, and bytes are what bound the pack size, so this is not free.
type DigestFields uint32

const (
	// DigestGenre asks for the primary genre: the display name and the stable
	// id. This is Ws7gDc field 80, found by asking for each of the 49 fields
	// the app page requests and seeing which one carried a genre.
	DigestGenre DigestFields = 1 << iota
)

// digestFieldSpecs is what each selectable field costs.
//
// The byte figures are measured, not estimated. A field added here without one
// is a pack size nobody has tested, and an untested pack size is how a
// truncated response starts looking like data.
var digestFieldSpecs = map[DigestFields]struct {
	field int
	bytes int
}{
	DigestGenre: {field: 80, bytes: 243}, // worst of the observed 178-243
}

// digestResponseBudget is the response size one request aims at.
//
// It is derived from the pack size that was verified live rather than the
// other way round: 1024 genre answers at 243 bytes each. 1024 is not Google's
// ceiling -- 24,576 calls in one request were answered in full -- but
// throughput per connection plateaus at about 2,000 apps a second from 2048
// onwards, so beyond that a larger pack buys nothing and costs blast radius:
// one failed request loses every app in it.
const digestResponseBudget = 1024 * 243

// digestPackSize is how many lookups one request should carry for a selector.
func digestPackSize(fields DigestFields) int {
	cost := 0
	for f, spec := range digestFieldSpecs {
		if fields&f != 0 {
			cost += spec.bytes
		}
	}
	if cost <= 0 {
		return 1
	}
	n := digestResponseBudget / cost
	return max(1, min(n, 2048))
}

// digestFieldNumbers is the selector for a set of fields, in ascending order so
// the request is stable between runs.
func digestFieldNumbers(fields DigestFields) []int {
	var out []int
	for f, spec := range digestFieldSpecs {
		if fields&f != 0 {
			out = append(out, spec.field)
		}
	}
	slices.Sort(out)
	return out
}

// DigestOptions configures a digest pass.
type DigestOptions struct {
	Lang    string
	Country string

	// Fields selects what to ask for. The zero value means DigestGenre.
	Fields DigestFields

	// PackSize overrides how many lookups ride in one request. Zero derives it
	// from Fields and the response budget.
	PackSize int

	// Concurrency is how many requests are in flight. Zero uses the client's.
	//
	// It matters here in a way it does not elsewhere: a lean request takes
	// about a second, so under a throttle shorter than that the throttle only
	// binds once there are enough workers to cover the latency.
	Concurrency int

	// Progress, when non-nil, is called after each completed request. Called
	// serially.
	Progress func(DigestProgress)
}

// DigestProgress reports how far a pass has got.
type DigestProgress struct {
	Requests int
	Apps     int
	Absent   int
}

// DigestsSeq resolves a few fields for many apps, streaming.
//
// It takes a sequence rather than a slice because the intended input is the
// whole catalog: 3.36 million ids is 160MB held as a slice before any work
// starts, and the results would be more again. Fed from a file or a database
// cursor, peak memory is one pack per worker regardless of how many apps there
// are. slices.Values turns a slice into one for the small case.
//
// Per-app failures ride in AppDigest.Err and the pass continues. The
// sequence's own error slot carries terminal failures only -- a cancelled
// context -- so the obvious `if err != nil { return }` does not abandon three
// thousand requests because one batch failed.
func (c *Client) DigestsSeq(ctx context.Context, appIDs iter.Seq[string], opts DigestOptions) iter.Seq2[AppDigest, error] {
	return func(yield func(AppDigest, error) bool) {
		ctx, endTask := startTask(ctx, traceTaskDigests)
		defer endTask()

		// The producer's only escape from a full `ordered` buffer is ctx.Done(),
		// so a consumer that stops early has to be able to cancel. Without this
		// the break below parks the consumer on wg.Wait(), the workers on
		// `range jobs` and the producer on its send -- all three, permanently.
		// CatalogShardSeq derives the same context for the same reason.
		parent := ctx
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		if opts.Lang == "" {
			opts.Lang = "en"
		}
		if opts.Country == "" {
			opts.Country = "us"
		}
		if opts.Fields == 0 {
			opts.Fields = DigestGenre
		}
		pack := opts.PackSize
		if pack <= 0 {
			pack = digestPackSize(opts.Fields)
		}
		workers := opts.Concurrency
		if workers <= 0 {
			workers = c.concurrency
		}
		if workers <= 0 {
			workers = 1
		}
		fields := digestFieldNumbers(opts.Fields)

		// Requests run concurrently; results are yielded in the order the packs
		// were formed. A caller writing to a database wants a stable sequence,
		// not whichever request finished first -- and ordering costs nothing
		// here because each pack carries its own completion signal.
		type batch struct {
			ids     []string
			results []AppDigest
			done    chan struct{}
		}

		jobs := make(chan *batch)
		ordered := make(chan *batch, workers)

		var wg sync.WaitGroup
		for range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for b := range jobs {
					b.results = c.digestBatch(ctx, b.ids, fields, opts)
					close(b.done)
				}
			}()
		}

		go func() {
			defer close(jobs)
			defer close(ordered)
			var cur []string
			send := func(ids []string) bool {
				b := &batch{ids: ids, done: make(chan struct{})}
				// ordered first: it is buffered, and a full buffer is the
				// backpressure that keeps memory bounded when the consumer is
				// slower than the network.
				select {
				case ordered <- b:
				case <-ctx.Done():
					return false
				}
				select {
				case jobs <- b:
					return true
				case <-ctx.Done():
					return false
				}
			}
			for id := range appIDs {
				cur = append(cur, id)
				if len(cur) < pack {
					continue
				}
				if !send(cur) {
					return
				}
				cur = nil
			}
			if len(cur) > 0 {
				send(cur)
			}
		}()

		var progress DigestProgress
		for b := range ordered {
			select {
			case <-b.done:
			case <-ctx.Done():
				err := ctx.Err() // read before cancel, so a deadline stays a deadline
				cancel()
				wg.Wait()
				yield(AppDigest{}, err)
				return
			}

			progress.Requests++
			progress.Apps += len(b.results)
			for _, d := range b.results {
				if d.Err == nil && !d.Listed {
					progress.Absent++
				}
			}
			if opts.Progress != nil {
				opts.Progress(progress)
			}
			for _, d := range b.results {
				if !yield(d, nil) {
					cancel() // release the producer and the workers first
					wg.Wait()
					return
				}
			}
		}
		wg.Wait()

		// The parent, not the derived context: cancel() above only runs on
		// paths that return immediately, so a run that got here was never
		// cancelled by this iterator.
		if err := parent.Err(); err != nil {
			yield(AppDigest{}, err)
		}
	}
}

// digestBatch resolves one pack.
func (c *Client) digestBatch(ctx context.Context, ids []string, fields []int, opts DigestOptions) []AppDigest {
	out := make([]AppDigest, len(ids))
	calls := make([]rpcCall, 0, len(ids))
	slots := make([]int, 0, len(ids))
	for i, id := range ids {
		out[i].AppID = id
		if id == "" {
			out[i].Err = fmt.Errorf("appID is required")
			continue
		}
		calls = append(calls, ws7gdcRPC(id, fields))
		slots = append(slots, i)
	}
	if len(calls) == 0 {
		return out
	}

	frames, err := c.batchCallFrames(ctx, opts.Lang, opts.Country, calls)
	if err != nil {
		for _, i := range slots {
			out[i].Err = err
		}
		return out
	}

	for j, i := range slots {
		f := frames[j]
		if !f.Present {
			// No frame at all is not an answer. Reporting it as "gone" would
			// turn one short response into a thousand apps deleted at once.
			out[i].Err = fmt.Errorf("no frame returned for %s", shortenID(out[i].AppID))
			continue
		}
		if f.Payload == "" {
			out[i].Listed = false
			continue
		}
		d, err := parseDigest(f.Payload, out[i].AppID, fields)
		if err != nil {
			out[i].Err = err
			continue
		}
		d.AppID = out[i].AppID
		out[i] = d
	}
	return out
}

// parseDigest reads one lean payload.
func parseDigest(payload, appID string, fields []int) (AppDigest, error) {
	var ds5 any
	if err := json.Unmarshal([]byte(payload), &ds5); err != nil {
		return AppDigest{}, fmt.Errorf("parse %s: %w", shortenID(appID), err)
	}

	// The response echoes the id it answered for. That is the only thing that
	// can catch a frame paired with the wrong call, and it costs nothing.
	if echoed := digestEcho(ds5); echoed != "" && echoed != appID {
		return AppDigest{}, fmt.Errorf("answer for %s arrived in %s's slot",
			shortenID(echoed), shortenID(appID))
	}

	node := getPath(ds5, 1, 2)
	if node == nil {
		return AppDigest{}, fmt.Errorf("no app node for %s", shortenID(appID))
	}

	d := AppDigest{AppID: appID, Listed: true}
	if g := digestField(node, 80); g != nil {
		d.Genre = toString(getPath(g, 0, 0, 0))
		d.GenreID = toString(getPath(g, 0, 0, 2))
	}
	return d, nil
}

// digestEcho reads the package id the response says it answered for, at
// [1][3]["12"][0][0].
func digestEcho(ds5 any) string {
	meta, _ := getPath(ds5, 1, 3).(map[string]any)
	if meta == nil {
		return ""
	}
	twelve, _ := meta["12"].([]any)
	if len(twelve) == 0 {
		return ""
	}
	pair, _ := twelve[0].([]any)
	if len(pair) == 0 {
		return ""
	}
	s, _ := pair[0].(string)
	return s
}

// digestField reads Ws7gDc field n out of an app node.
//
// The same datum lives in two places depending on how much was asked for.
// Google's positional encoding puts field n at array index n-1 while the
// response is dense, which is why extractAppData reads the genre at [79]. A
// request for one field comes back sparse instead -- [{"80": ...}] -- where
// index 79 does not exist and the field number is a map key.
//
// getPath cannot be used for this: given a map it looks up the *index* as a
// string, so asking it for 79 on a sparse node reads key "79", which is field
// 79 and not the one wanted. Silently, and with a plausible-looking value.
func digestField(appNode any, n int) any {
	switch t := appNode.(type) {
	case map[string]any:
		return t[fmt.Sprint(n)]
	case []any:
		// Sparse responses wrap the map in a one-element array.
		if len(t) == 1 {
			if m, ok := t[0].(map[string]any); ok {
				return m[fmt.Sprint(n)]
			}
		}
		if n-1 < len(t) {
			return t[n-1]
		}
	}
	return nil
}
