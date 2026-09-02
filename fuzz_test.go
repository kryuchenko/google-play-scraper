package googleplayscraper

import (
	"os"
	"path/filepath"
	"testing"
)

// Fuzzing the parsers.
//
// Everything this package parses is undocumented and comes from a third party
// that changes it without notice: HTML with JSON blobs embedded in script
// tags, an RPC envelope wrapping positional arrays, XML sitemaps. The decoders
// walk those with chains of type assertions -- getPath alone has more callers
// than any other function here -- and a type assertion on a shape that moved
// is a panic, not an error.
//
// A panic in a library is worse than a wrong answer: it takes down the caller,
// and the caller is often a sweep of thousands of pages where one malformed
// response should cost one page. The contract these targets pin is therefore
// narrow and absolute: no input may panic. Returning nothing, or an error, is
// always acceptable.
//
// Seeds come from the recorded fixtures, so the corpus starts from real
// payloads rather than from noise that never reaches the interesting code.

func addFixtureSeeds(f *testing.F, names ...string) {
	f.Helper()
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			f.Fatalf("read fixture %s: %v", name, err)
		}
		f.Add(data)
	}
	// Shapes that are cheap to reach and easy to get wrong: nothing at all,
	// a script tag with no payload, and truncation in the middle of a blob.
	f.Add([]byte(nil))
	f.Add([]byte(`<script>AF_initDataCallback({key: 'ds:5', data:`))
	f.Add([]byte(`)]}'`))
}

// FuzzParseAppPage covers the whole details-page path: the regex scan, the
// JSON decode of each block, and the field extraction that walks the result.
func FuzzParseAppPage(f *testing.F) {
	addFixtureSeeds(f, "app_page.html", "app_page_game.html", "app_unavailable_region.html")

	f.Fuzz(func(t *testing.T, body []byte) {
		// Errors are expected on nearly everything; panics are not.
		_, _ = parseAppPage(body, "com.example.app", "https://example")
	})
}

// FuzzParseDataBlocks isolates the script-tag scan and JSON decode. It is the
// widest funnel in the package: every HTML-backed endpoint goes through it,
// so a panic here reaches app, search, list, cluster, developer and similar.
func FuzzParseDataBlocks(f *testing.F) {
	addFixtureSeeds(f, "app_page.html", "search_page.html", "category_page.html")

	f.Fuzz(func(t *testing.T, body []byte) {
		blocks := parseDataBlocks(body)
		// Walking the result is part of the contract: callers immediately hand
		// these values to getPath, and a decoded-but-hostile shape is exactly
		// what the type assertions there have to survive.
		for _, v := range blocks {
			_ = getPath(v, 0, 1, 2)
			_ = toString(getPath(v, 1))
		}
	})
}

// FuzzBatchExecute covers the RPC envelope behind reviews, permissions and
// suggestions -- the other input shape, with its own framing rules.
func FuzzBatchExecute(f *testing.F) {
	addFixtureSeeds(f, "reviews_batch.bin", "permissions_batch.bin", "suggest_batch.bin")

	f.Fuzz(func(t *testing.T, body []byte) {
		frames, err := decodeBatchEnvelope(body)
		if err != nil {
			return
		}
		for _, fr := range frames {
			_ = getPath(fr, 0, 0)
		}
	})
}

// FuzzParseReviews goes one level further: the envelope, then 150-odd reviews
// decoded out of positional arrays with dates, scores and nested user objects.
// More fields are read here than anywhere else, so more assertions can fail.
func FuzzParseReviews(f *testing.F) {
	addFixtureSeeds(f, "reviews_batch.bin")

	f.Fuzz(func(t *testing.T, body []byte) {
		res, err := parseReviewsResponse(body, "com.example.app")
		if err != nil {
			return
		}
		// A successful parse must not hand back a review that is silently
		// broken in a way callers would trip over.
		for _, r := range res.Reviews {
			_ = r.Text
			_ = r.Date
		}
	})
}

// FuzzResultRows covers the shared row decoder by both routes that reach it.
// search and list read the same positional layout through different wrappers,
// and a shape that breaks one usually breaks the other.
func FuzzResultRows(f *testing.F) {
	addFixtureSeeds(f, "search_page.html", "top_charts_page.html", "cluster_page.html")

	f.Fuzz(func(t *testing.T, body []byte) {
		_, _, _ = parseSearchPage(body, 50)
		_, _ = parseListPage(body, ListOptions{})
		_ = parseClusterURLs(body)
	})
}

// FuzzSitemapLoc is small but runs 3.7 million times per catalog sweep, on
// URLs written by someone else. Anything that panics here fails the whole
// sweep on a single malformed entry.
func FuzzSitemapLoc(f *testing.F) {
	for _, seed := range []string{
		"https://play.google.com/store/apps/details?id=com.example.app",
		"https://play.google.com/store/books/details/B?id=BOOK",
		"https://play.google.com/store/apps/details",
		"https://play.google.com/store/apps/details?id=",
		"not a url at all",
		"",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, loc string) {
		pkg := appPackageFromLoc(loc)
		// The property worth asserting beyond "does not panic": anything
		// returned has to look like a package id. Substring identity would be
		// too strong -- the query value is URL-decoded, so %2E legitimately
		// becomes a dot -- but a parser that emits something no Android
		// package could be named poisons a catalog snapshot in a way nothing
		// downstream can detect. That is how `id=+x` returning " x" was found.
		if pkg != "" && validPackage(pkg) != pkg {
			t.Fatalf("appPackageFromLoc(%q) = %q, which is not a usable package id", loc, pkg)
		}
	})
}
