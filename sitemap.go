package googleplayscraper

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/url"
	"strings"
	"sync"
)

// This file implements full-catalog enumeration via Google Play's public
// sitemaps — the one channel that lists the *entire* store rather than the
// commercially-visible ~200-per-request top of a category.
//
// The structure (verified live, snapshot 2026-06):
//
//	robots.txt
//	  Sitemap: https://play.google.com/sitemaps/sitemaps-index-0.xml   (shards 00000..49999)
//	  Sitemap: https://play.google.com/sitemaps/sitemaps-index-1.xml   (shards 50000..end)
//	index-N.xml         <sitemapindex> -> <sitemap><loc> .../play_sitemaps_<date>_<run>-NNNNN-of-NNNNN.xml.gz
//	shard.xml.gz        gzip -> <urlset> -> <url><loc> ...
//
// A shard is NOT apps-only: it interleaves books, movies, music and app URLs
// for the whole store. App listings are exactly the `/store/apps/details?id=PKG`
// locs; everything else is skipped. There are ~83k shards of ~400 URLs each,
// of which ~30–55 are apps, so a full sweep yields on the order of 3 million
// app package ids.
//
// Rate limiting reuses the client's WithThrottle setting (every fetch goes
// through c.get); crawl parallelism is a per-call option, not a Client field,
// because the sitemap sweep is a different rate regime from normal API calls.

// SitemapIndexURLs returns the sitemap-index URLs Google advertises in
// robots.txt (the `Sitemap:` directives). At the time of writing there are two,
// together covering every shard, and the count is read from robots.txt
// rather than assumed: it grows. These comments said 80,945 for a while, which
// was true when they were written and was 83,445 by the time anyone checked.
// rather than hardcoded so it tracks Google's own advertisement.
func (c *Client) SitemapIndexURLs(ctx context.Context) ([]string, error) {
	body, err := c.get(ctx, BaseURL+"/robots.txt")
	if err != nil {
		return nil, fmt.Errorf("fetch robots.txt: %w", err)
	}

	var indexes []string
	for line := range strings.SplitSeq(string(body), "\n") {
		line = strings.TrimSpace(line)
		const prefix = "sitemap:"
		if len(line) < len(prefix) || !strings.EqualFold(line[:len(prefix)], prefix) {
			continue
		}
		if u := strings.TrimSpace(line[len(prefix):]); u != "" {
			indexes = append(indexes, u)
		}
	}
	if len(indexes) == 0 {
		return nil, fmt.Errorf("no Sitemap directives in robots.txt")
	}
	return indexes, nil
}

// SitemapShards fetches one sitemap-index and returns the shard URLs it lists
// (the per-shard `<loc>` values, typically `…-NNNNN-of-NNNNN.xml.gz`).
func (c *Client) SitemapShards(ctx context.Context, indexURL string) ([]string, error) {
	body, err := c.get(ctx, indexURL)
	if err != nil {
		return nil, fmt.Errorf("fetch sitemap index %s: %w", indexURL, err)
	}

	return indexShards(body), nil
}

// indexShards scans a <sitemapindex> for its shard URLs.
//
// This used to build a document tree, the same way the shard parser did before
// it was replaced. An index is 6MB of XML holding 50,000 <loc> entries and
// nothing else worth reading, and unmarshalling it allocated 94MB to produce
// 7.2MB of strings -- thirteen times the useful output, paid before a sweep
// fetches its first shard. For a sampled sweep of ten shards that fixed cost
// is 81% of the whole run.
//
// The scan is the same shape as shardPackages: find <loc>, find </loc>, take
// what is between. Nothing else in the document is read, so there is nothing
// for a tree to be built out of.
func indexShards(body []byte) []string {
	// One entry per <loc>, which is the only tag that appears more than once.
	shards := make([]string, 0, bytes.Count(body, locOpen))

	rest := body
	for {
		i := bytes.Index(rest, locOpen)
		if i < 0 {
			return shards
		}
		rest = rest[i+len(locOpen):]

		j := bytes.Index(rest, locClose)
		if j < 0 {
			return shards
		}
		loc := strings.TrimSpace(unescapeXMLEntities(rest[:j]))
		if loc != "" {
			shards = append(shards, loc)
		}
		rest = rest[j+len(locClose):]
	}
}

// AllSitemapShards discovers every shard URL across all advertised indexes,
// deduplicated and in discovery order. This is the full work-list for a catalog
// sweep (~83k entries).
func (c *Client) AllSitemapShards(ctx context.Context) ([]string, error) {
	indexes, err := c.SitemapIndexURLs(ctx)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]struct{})
	var all []string
	for _, idx := range indexes {
		shards, err := c.SitemapShards(ctx, idx)
		if err != nil {
			return nil, err
		}
		for _, s := range shards {
			if _, dup := seen[s]; dup {
				continue
			}
			seen[s] = struct{}{}
			all = append(all, s)
		}
	}
	return all, nil
}

// SitemapShardPackages fetches one shard (gzip-decompressing it when needed) and
// returns the app package ids it lists, deduplicated within the shard and in
// document order. Non-app URLs (books, movies, music, …) are skipped.
func (c *Client) SitemapShardPackages(ctx context.Context, shardURL string) ([]string, error) {
	body, err := c.get(ctx, shardURL)
	if err != nil {
		return nil, fmt.Errorf("fetch shard %s: %w", shardURL, err)
	}

	// Streamed rather than decompressed into one buffer: see
	// shardPackagesFrom for why a shard's shape makes that worth doing.
	if len(body) >= 2 && body[0] == 0x1f && body[1] == 0x8b {
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("decompress shard %s: %w", shardURL, err)
		}
		defer func() { _ = zr.Close() }()

		pkgs, err := shardPackagesFrom(zr)
		if err != nil {
			return nil, fmt.Errorf("decompress shard %s: %w", shardURL, err)
		}
		return pkgs, nil
	}

	return shardPackages(body), nil
}

// CatalogOptions configures a full-catalog sweep.
type CatalogOptions struct {
	// Concurrency is how many shards are fetched in parallel. Default 1.
	// The client's WithThrottle still bounds the request rate across workers.
	Concurrency int

	// ShardURLs sweeps exactly these shards, skipping index discovery. Empty
	// discovers them as usual.
	//
	// This is how a run resumes. A shard index names a shard only within one
	// generation -- the list is rebuilt when Google republishes -- so a
	// resumable job records URLs and hands back the ones it did not finish.
	ShardURLs []string

	// Shards restricts the sweep to these indices into the shard list, which
	// is ShardURLs when given and the discovered list otherwise. nil/empty
	// sweeps every shard.
	// Out-of-range indices are skipped. Useful for sampling or resuming a
	// crawl.
	Shards []int

	// OnShardError, when non-nil, is called for each shard that fails to fetch
	// or parse; the sweep continues with the remaining shards. nil ignores
	// shard errors. Called serially with the other callbacks.
	OnShardError func(shardIndex int, shardURL string, err error)

	// OnShardDone, when non-nil, is called after each shard is processed with
	// the number of package ids emitted from it — useful for progress
	// reporting. Called serially with the other callbacks.
	OnShardDone func(shardIndex int, shardURL string, packages int)
}

// Deprecated: this method will be removed in v2. Use [Client.CatalogSeq],
// which takes the same options and adds what a callback cannot express --
// stopping the sweep, and reporting why it stopped. A full sweep is 83k
// requests, so the ability to walk away early is not a small difference.
// Migration changes the shape of the call:
//
//	// before
//	err := c.EnumerateCatalog(ctx, func(pkg string) { use(pkg) }, opts)
//
//	// after
//	for pkg, err := range c.CatalogSeq(ctx, opts) {
//		if err != nil {
//			return err
//		}
//		use(pkg)
//	}
//
// EnumerateCatalog sweeps every sitemap shard and calls emit once per app
// package id discovered. It is the full-catalog counterpart to CategoryApps:
// where CategoryApps maximizes the commercially-visible layer of one category,
// EnumerateCatalog walks Google's own sitemap of the entire store.
//
// emit and the option callbacks are invoked serially (guarded by an internal
// lock), so a caller may append to a shared slice or insert into a shared map
// without its own synchronization. emit yields raw ids and does NOT
// deduplicate across shards — a package listed in two shards is emitted twice;
// deduplicate on the caller side if required (within a single shard ids are
// already unique).
//
// The sweep is cooperative-cancellable: when ctx is cancelled no further shards
// are dispatched and EnumerateCatalog returns ctx.Err() with the work done so
// far already emitted (a partial catalog). A single shard's fetch/parse failure
// never aborts the sweep — it is reported via OnShardError and skipped.
//
// This fetches tens of thousands of shards; run it with a sensible WithThrottle
// and Concurrency, and expect on the order of 3 million ids.
func (c *Client) EnumerateCatalog(ctx context.Context, emit func(pkg string), opts CatalogOptions) error {
	ctx, endTask := startTask(ctx, traceTaskCatalog)
	defer endTask()
	return c.sweepCatalog(ctx, func(sh CatalogShard) {
		if sh.Err != nil {
			if opts.OnShardError != nil {
				opts.OnShardError(sh.Index, sh.URL, sh.Err)
			}
			return
		}
		for _, p := range sh.Packages {
			emit(p)
		}
		if opts.OnShardDone != nil {
			opts.OnShardDone(sh.Index, sh.URL, len(sh.Packages))
		}
	}, opts)
}

// sweepCatalog walks every requested shard and calls emit once per app package
// id. Shards are fetched concurrently; emit is called serially, so it needs no
// locking of its own.
//
// This is the engine both entry points share. CatalogSeq calls it directly
// rather than going through EnumerateCatalog: building the supported API on
// top of the deprecated one would mean the deprecated path could not be
// removed without rewriting the supported one.
func (c *Client) sweepCatalog(ctx context.Context, emit func(CatalogShard), opts CatalogOptions) error {
	shards := opts.ShardURLs
	if len(shards) == 0 {
		var err error
		shards, err = c.AllSitemapShards(ctx)
		if err != nil {
			return fmt.Errorf("list shards: %w", err)
		}
	}

	indices := opts.Shards
	if len(indices) == 0 {
		indices = make([]int, len(shards))
		for i := range indices {
			indices[i] = i
		}
	}

	var mu sync.Mutex
	return parallelIndexed(ctx, len(indices), opts.Concurrency, func(ctx context.Context, i int) {
		si := indices[i]
		if si < 0 || si >= len(shards) {
			return
		}
		shardURL := shards[si]

		pkgs, err := c.SitemapShardPackages(ctx, shardURL)

		// Serialised so the callbacks and the emit see one shard at a time,
		// which is what lets a consumer treat a shard as the unit of progress.
		mu.Lock()
		defer mu.Unlock()
		emit(CatalogShard{Index: si, URL: shardURL, Packages: pkgs, Err: err})
	})
}

// CatalogShard is one shard's worth of a sweep.
//
// It exists because the unit of *resumability* is the shard, not the package.
// A sweep is 83,445 requests over hours; an interrupted one has to start again
// from the URLs it did not reach, and that is impossible to work out from a
// flat stream of ids. Emitting shards lets a caller record what it has
// finished and pick up from there -- which every batch consumer needs, and
// which cmd/gpscrape previously had to bypass this package to get.
type CatalogShard struct {
	// Index is the shard's position in the full shard list.
	//
	// It names the shard only within one generation: the list is rebuilt when
	// Google republishes, so an index recorded against one build addresses
	// something else in the next. Store URL, or store the generation beside
	// the index.
	Index int

	// URL is the shard fetched, which does name it across generations.
	URL string

	// Packages are the app ids it held, nil when Err is set.
	Packages []string

	// Err is why this shard produced nothing. The sweep continues past it:
	// one shard of 83,445 failing is not a reason to abandon the rest, and a
	// caller that records the URL can retry it alone.
	Err error
}

// CatalogShardSeq sweeps the catalog a shard at a time.
//
// Failed shards arrive with Err set rather than through a callback: with a
// sequence the callback would be a second channel for the same information,
// and a caller that wants to retry a shard needs it in the stream where the
// rest of its bookkeeping is.
//
// The sequence's own error slot carries terminal failures only -- being unable
// to list the shards at all, or a cancelled context.
func (c *Client) CatalogShardSeq(ctx context.Context, opts CatalogOptions) iter.Seq2[CatalogShard, error] {
	return func(yield func(CatalogShard, error) bool) {
		ctx, endTask := startTask(ctx, traceTaskCatalogSeq)
		defer endTask()

		parent := ctx
		ctx, cancel := context.WithCancel(ctx)
		out := make(chan CatalogShard)
		done := make(chan struct{})
		var sweepErr error

		go func() {
			defer close(done)
			defer close(out)
			sweepErr = c.sweepCatalog(ctx, func(sh CatalogShard) {
				select {
				case out <- sh:
				case <-ctx.Done():
				}
			}, opts)
		}()

		defer func() {
			cancel()
			<-done
		}()

		for sh := range out {
			// A shard that failed because the consumer stopped did not fail.
			if sh.Err != nil && errors.Is(sh.Err, context.Canceled) && parent.Err() == nil {
				continue
			}
			if !yield(sh, nil) {
				return
			}
		}
		if sweepErr != nil && (!errors.Is(sweepErr, context.Canceled) || parent.Err() != nil) {
			yield(CatalogShard{}, sweepErr)
		}
	}
}

// gunzipIfNeeded decompresses data when it carries the gzip magic header,
// otherwise returns it unchanged. Sitemap shards are served as .xml.gz files
// (the gzip is the resource, not an HTTP transfer encoding, so the transport
// does not transparently decode it), but indexes are plain XML — detecting the
// magic bytes handles both without assuming.
// gunzipHintMax caps the pre-allocation taken from a gzip trailer.
//
// A shard decompresses to about 8MB. The cap exists because the trailer is
// attacker-controlled: it is four bytes at the end of the stream that anyone
// serving the response chooses, so an unbounded hint is an unbounded
// allocation from an unauthenticated source. Beyond the cap the buffer simply
// grows as it always did.
const gunzipHintMax = 64 << 20

func gunzipIfNeeded(data []byte) ([]byte, error) {
	if len(data) < 2 || data[0] != 0x1f || data[1] != 0x8b {
		return data, nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	// A corrupt stream surfaces from the read below, not from Close, so the
	// close error carries nothing a caller could act on.
	defer func() { _ = zr.Close() }()

	// Allocate once from the size the format already states.
	//
	// io.ReadAll grows from nothing, doubling and copying: decompressing an
	// 8MB shard that way allocates 16.7MB and takes 6.4ms, against 8.07MB and
	// 4.4ms when the buffer is the right size to begin with. A catalog sweep
	// does this 83,445 times.
	//
	// RFC 1952 puts ISIZE -- the uncompressed length modulo 2^32 -- in the
	// last four bytes. Modulo, so it is a hint and not a promise: a stream
	// over 4GB reports the remainder, and the buffer grows from there as
	// normal. For a sitemap shard it is exact.
	buf := bytes.NewBuffer(make([]byte, 0, gunzipHint(data)))
	if _, err := buf.ReadFrom(zr); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// gunzipHint reads the uncompressed size a gzip stream declares, clamped to
// something it is safe to allocate on that word alone.
func gunzipHint(data []byte) int {
	if len(data) < 4 {
		return 0
	}
	size := binary.LittleEndian.Uint32(data[len(data)-4:])
	if size > gunzipHintMax {
		return gunzipHintMax
	}
	return int(size)
}

// appPackageFromLoc returns the package id of an app-listing sitemap loc, or ""
// for any other URL (books/movies/music or a malformed entry). It matches
// exactly the /store/apps/details?id=PKG path so sibling paths like
// /store/apps/dev or /store/apps/collection are not mistaken for apps.
func appPackageFromLoc(loc string) string {
	loc = strings.TrimSpace(loc)
	if loc == "" {
		return ""
	}
	u, err := url.Parse(loc)
	if err != nil {
		return ""
	}
	if u.Path != "/store/apps/details" {
		return ""
	}
	return validPackage(u.Query().Get("id"))
}

// validPackage returns id if it looks like an Android package name, and ""
// otherwise.
//
// The query value is URL-decoded on the way out of Query().Get, so `id=+x`
// arrives as " x" -- a leading space that is not a package name and would
// nonetheless have been written into a catalog snapshot, where nothing
// downstream could tell it from a real one. Every package on Play has at
// least one dot and is made of letters, digits, underscores and dots; that is
// enough of a shape to reject the rest without risking a real id.
func validPackage(id string) string {
	if id == "" || strings.HasPrefix(id, ".") || strings.HasSuffix(id, ".") {
		return ""
	}
	dots := 0
	prevDot := false
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c == '.':
			if prevDot {
				return "" // empty segment
			}
			dots++
			prevDot = true
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9', c == '_':
			prevDot = false
		default:
			return ""
		}
	}
	if dots == 0 {
		return ""
	}
	return id
}

// CatalogSeq is EnumerateCatalog as an iterator: it yields every app package
// id in the store, and the caller stops whenever it likes.
//
//	for pkg, err := range client.CatalogSeq(ctx, opts) {
//		if err != nil {
//			return err
//		}
//		if seen(pkg) {
//			break
//		}
//	}
//
// The break is the reason this exists. EnumerateCatalog takes a callback,
// which cannot stop the sweep and cannot return an error, so a caller that
// only wants the first matching id still pays for all 83k shards.
//
// The error slot carries terminal errors only -- a failure to list the shards,
// or the context being cancelled. A single shard that fails to fetch or parse
// does not end the sweep and is reported through opts.OnShardError exactly as
// it is for EnumerateCatalog; the two error kinds are genuinely different and
// keeping them separate is deliberate. When a terminal error does arrive it is
// the final element of the sequence, paired with an empty id.
//
// Note that ranging with one variable silently drops the error, as it does for
// any iter.Seq2. That yields empty strings on failure rather than ids.
//
// Shards are still fetched concurrently per opts.Concurrency; the ids arrive
// serialized. Stopping early cancels the sweep and waits for its workers, so
// no goroutine outlives the loop.
func (c *Client) CatalogSeq(ctx context.Context, opts CatalogOptions) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		// A flattening of CatalogShardSeq. The shard is the unit the sweep
		// actually works in; this is the view for callers who only want ids
		// and have no bookkeeping to do.
		//
		// Failed shards go to OnShardError here rather than into the stream,
		// because a caller asking for ids has nowhere to put a shard.
		for sh, err := range c.CatalogShardSeq(ctx, opts) {
			if err != nil {
				yield("", err)
				return
			}
			if sh.Err != nil {
				if opts.OnShardError != nil {
					opts.OnShardError(sh.Index, sh.URL, sh.Err)
				}
				continue
			}
			for _, pkg := range sh.Packages {
				if !yield(pkg, nil) {
					return
				}
			}
			if opts.OnShardDone != nil {
				opts.OnShardDone(sh.Index, sh.URL, len(sh.Packages))
			}
		}
	}
}

// Markers for scanning a shard without building a document tree.
var (
	locOpen     = []byte("<loc>")
	locClose    = []byte("</loc>")
	appPathMark = []byte("/store/apps/details?id=")
)

// shardPackages returns the app package ids a sitemap shard lists.
//
// A shard is a flat list of <url><loc>…</loc></url>, of which only the app
// entries matter -- about one in eight, the rest being books, films and music.
// Unmarshalling it into a document tree builds ~400 strings so that ~50 can be
// read, which measured 12x more expensive than scanning for the delimiters:
// 730us against 58us per shard. Over a full sweep that is 57 seconds of CPU
// against 3.
//
// The URL itself is still parsed by appPackageFromLoc, so the id extraction is
// unchanged; only the XML layer is skipped. Entities are decoded because the
// XML parser would have decoded them and a byte scan does not. Alternate
// <xhtml:link> hreflang entries carry an href rather than a <loc>, so they are
// skipped here exactly as they were before.
func shardPackages(body []byte) []string {
	seen := make(map[string]struct{})
	var pkgs []string

	rest := body
	for {
		i := bytes.Index(rest, locOpen)
		if i < 0 {
			return pkgs
		}
		rest = rest[i+len(locOpen):]

		j := bytes.Index(rest, locClose)
		if j < 0 {
			return pkgs
		}
		loc := rest[:j]
		rest = rest[j+len(locClose):]

		// Cheap rejection before parsing: seven entries in eight are not apps.
		if !bytes.Contains(loc, appPathMark) {
			continue
		}

		pkg := appPackageFromLoc(unescapeXMLEntities(loc))
		if pkg == "" {
			continue
		}
		if _, dup := seen[pkg]; dup {
			continue
		}
		seen[pkg] = struct{}{}
		pkgs = append(pkgs, pkg)
	}
}

// shardScanChunk is how much of the decompressed stream shardPackagesFrom
// holds at a time, and shardScanCarryMax bounds what is carried across reads
// when a <loc> is opened and not yet closed.
//
// A sitemap loc is a URL. Anything longer than the carry bound is not one, and
// without the bound a stream containing a single unterminated <loc> would
// buffer the whole document that streaming exists to avoid -- which is the
// same unbounded-allocation-from-an-untrusted-source problem gunzipHintMax
// exists for.
const (
	shardScanChunk    = 32 << 10
	shardScanCarryMax = 64 << 10
)

// shardPackagesFrom is shardPackages over a stream rather than a buffer.
//
// It exists because of the shape of a shard, which is not what the format
// suggests. A shard decompresses to about 7.5MB, and the <loc> elements are
// 0.6% of it: 46KB of URLs wrapped in megabytes of <xhtml:link> hreflang
// alternates, roughly 270 locale variants per app listing. Only about 43 of
// its ~400 locs are apps at all -- the rest are books, films and music.
//
// Those figures are means over ten random shards; the spread is real (35 to 61
// apps, 6.75 to 8.70MB). They were first written from a single shard, which
// happened to hold 54 apps and 8.0MB, and the 54 then disagreed with the 42.2
// per shard that catalogsize.go measured over 1,500 of them.
//
// Materialising that costs a whole-shard allocation to read 46KB of it. A full
// sweep is 83,445 shards, so the buffer alone is on the order of 620GB of
// allocation churn, and at the concurrency a sweep actually uses there are as
// many multi-megabyte buffers live at once as there are workers.
//
// The scan is character-for-character the one shardPackages does; only the
// window it walks is bounded.
func shardPackagesFrom(r io.Reader) ([]string, error) {
	seen := make(map[string]struct{})
	var pkgs []string

	buf := make([]byte, 0, shardScanChunk+shardScanCarryMax)
	chunk := make([]byte, shardScanChunk)

	for {
		n, err := r.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
			tail := scanLocs(buf, seen, &pkgs)
			if len(tail) > shardScanCarryMax {
				// A <loc> still unclosed after more bytes than any URL runs
				// to. shardPackages stops at an unterminated marker, so this
				// stops too: the two must not disagree about a document, and
				// growing the window instead is the unbounded allocation the
				// bound exists to prevent.
				return pkgs, nil
			}
			// copy handles the overlap: tail is a suffix of buf.
			buf = buf[:copy(buf[:cap(buf)], tail)]
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return pkgs, nil
			}
			return pkgs, err
		}
	}
}

// scanLocs consumes every complete <loc>…</loc> in buf, appending the app
// package ids it finds, and returns the bytes it could not yet interpret.
func scanLocs(buf []byte, seen map[string]struct{}, pkgs *[]string) []byte {
	rest := buf
	for {
		i := bytes.Index(rest, locOpen)
		if i < 0 {
			// No open marker. Keep only enough to catch one straddling the
			// boundary between this read and the next.
			if n := len(locOpen) - 1; len(rest) > n {
				return rest[len(rest)-n:]
			}
			return rest
		}

		after := rest[i+len(locOpen):]
		j := bytes.Index(after, locClose)
		if j < 0 {
			return rest[i:] // incomplete: resume from the open marker
		}
		loc := after[:j]
		rest = after[j+len(locClose):]

		// Cheap rejection before parsing: seven entries in eight are not apps.
		if !bytes.Contains(loc, appPathMark) {
			continue
		}
		pkg := appPackageFromLoc(unescapeXMLEntities(loc))
		if pkg == "" {
			continue
		}
		if _, dup := seen[pkg]; dup {
			continue
		}
		seen[pkg] = struct{}{}
		*pkgs = append(*pkgs, pkg)
	}
}

// unescapeXMLEntities expands the five predefined XML entities. Google's
// shards do not currently use them in app URLs, but the document tree this
// replaced would have expanded them, and a parser that silently stops matching
// the one it replaced is worse than a slower one.
func unescapeXMLEntities(b []byte) string {
	if !bytes.ContainsRune(b, '&') {
		return string(b)
	}
	return xmlEntityReplacer.Replace(string(b))
}

var xmlEntityReplacer = strings.NewReplacer(
	"&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'",
)
