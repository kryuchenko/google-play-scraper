package googleplayscraper

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Benchmarks over the recorded fixtures.
//
// These exist for two reasons beyond the usual one. The first is that several
// claims about this package's performance -- that Go 1.27's json v2 speeds up
// unmarshalling, that Green Tea helps a workload built on millions of small
// []any and map[string]any -- are untestable assertions without them.
//
// The second is that the numbers are only comparable within one toolchain.
// Coverage instrumentation already changed between 1.26 and 1.27 enough to
// move the reported figure by six points on identical code; the allocator and
// collector changed at least as much. So a benchstat comparison is valid
// across commits, not across Go versions, and a baseline has to be rewritten
// when the toolchain moves rather than read as a regression.
//
// Written with b.Loop rather than a `for range b.N` loop: it keeps the
// arguments and results alive without a sink variable, and 1.26 fixed the
// inlining it used to suppress inside the loop body, which mattered most for
// exactly the small-allocation code below.

// readBenchFixture mirrors readFixture for benchmarks, which get *testing.B.
func readBenchFixture(b *testing.B, name string) []byte {
	b.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		b.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

// BenchmarkParseAppPage covers the whole path a details page takes: regex over
// ~1MB of HTML, JSON decode of the embedded blobs, then field extraction. It
// is the most common single operation this package performs.
func BenchmarkParseAppPage(b *testing.B) {
	body := readBenchFixture(b, "app_page.html")
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()

	for b.Loop() {
		if _, err := parseAppPage(body, "com.example.app", "https://example"); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkExtractAppData isolates the field extraction from the HTML scan and
// the JSON decode, so a regression can be attributed. Everything here is
// getPath walking decoded payloads, which is where the call graph says the
// package spends its time: getPath has more callers than any other function.
func BenchmarkExtractAppData(b *testing.B) {
	body := readBenchFixture(b, "app_page.html")
	blocks := parseDataBlocks(body)
	if len(blocks) == 0 {
		b.Fatal("fixture produced no data blocks")
	}
	b.ReportAllocs()

	for b.Loop() {
		if _, err := extractAppData(blocks, "com.example.app", "https://example"); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkScriptData measures the regex scan and JSON decode alone: the part
// that scales with page size rather than with how many fields are read.
func BenchmarkScriptData(b *testing.B) {
	body := readBenchFixture(b, "app_page.html")
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()

	for b.Loop() {
		if blocks := parseDataBlocks(body); len(blocks) == 0 {
			b.Fatal("no blocks")
		}
	}
}

// BenchmarkBatchExecute covers the other input shape: the RPC envelope behind
// reviews, permissions and suggestions. Its cost profile is different from the
// HTML pages -- less scanning, more nested decoding.
func BenchmarkBatchExecute(b *testing.B) {
	body := readBenchFixture(b, "reviews_batch.bin")
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()

	for b.Loop() {
		if _, err := decodeBatchEnvelope(body); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkParseReviews is the full reviews path: envelope, then 150 reviews
// decoded out of positional arrays. Pagination multiplies this by however many
// pages a caller pulls, so it is the hot loop of any large review job.
func BenchmarkParseReviews(b *testing.B) {
	body := readBenchFixture(b, "reviews_batch.bin")
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()

	for b.Loop() {
		res, err := parseReviewsResponse(body, "com.example.app")
		if err != nil {
			b.Fatal(err)
		}
		if len(res.Reviews) == 0 {
			b.Fatal("fixture produced no reviews")
		}
	}
}

// BenchmarkParseSearchPage and BenchmarkParseListPage cover the two result-row
// decoders, which share rowdecoder.go but reach it by different routes.
func BenchmarkParseSearchPage(b *testing.B) {
	body := readBenchFixture(b, "search_page.html")
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()

	for b.Loop() {
		if _, _, err := parseSearchPage(body, 50); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseListPage(b *testing.B) {
	body := readBenchFixture(b, "top_charts_page.html")
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()

	for b.Loop() {
		if _, err := parseListPage(body, ListOptions{}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSitemapShard is the catalog sweep's inner loop, run 83k times per
// full sweep: gunzip, XML parse, filter app URLs out of a mixed urlset. At
// that multiplier a few microseconds per shard is minutes of wall clock.
func BenchmarkSitemapShard(b *testing.B) {
	// The recorded shard fixtures are plain XML; the live ones are gzipped and
	// gunzipIfNeeded handles both, so this measures the parse rather than the
	// decompression.
	body := benchShardBody()
	body = append(body, []byte(`</urlset>`)...)

	b.SetBytes(int64(len(body)))
	b.ReportAllocs()

	for b.Loop() {
		if len(shardPackages(body)) == 0 {
			b.Fatal("no packages parsed")
		}
	}
}

// getPath is called from more places than anything else in the package, so its
// cost is multiplied across every parser. Benchmarked on a shape close to what
// Google actually sends: deeply nested, mostly []any.
func BenchmarkGetPath(b *testing.B) {
	var nested any = "leaf"
	for range 12 {
		nested = []any{nil, nil, nested, nil}
	}
	b.ReportAllocs()

	for b.Loop() {
		if v := getPath(nested, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2); v != "leaf" {
			b.Fatalf("getPath = %v", v)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// BenchmarkAvailabilityProbe is the operation an Availability sweep repeats
// once per country -- 177 times for one app by default. It reads a single node
// out of ds:5 and ignores the other twelve blocks on the page, which is why
// it is measured separately from the full page parse.
func BenchmarkAvailabilityProbe(b *testing.B) {
	body := readBenchFixture(b, "app_page.html")
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()

	for b.Loop() {
		appData, ok := appDataNode(body)
		if !ok {
			b.Fatal("fixture has no app data node")
		}
		_ = classifyAvailability(appData)
	}
}

// BenchmarkParseReviews uses a 150-review page; ReviewsSeq asks for 1000, so
// that benchmark measures a request shape the library stopped making. This one
// is a real captured 1000-review response.
//
// Committing an 831KB fixture used to be a cost every consumer paid in their
// module download. Since testdata/ carries a go.mod and is excluded from the
// zip, a representative fixture is now free downstream -- which is the point of
// having done that.
func BenchmarkParseReviewsFullPage(b *testing.B) {
	body := readBenchFixture(b, "reviews_batch_1000.bin")
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		r, err := parseReviewsResponse(body, "ru.yandex.taxi")
		if err != nil || len(r.Reviews) == 0 {
			b.Fatalf("parse: %v", err)
		}
	}
}

// The "before" side of the parser table in CHANGELOG 1.4.0.
//
// Those numbers were published without anything in the repository that could
// reproduce them, which is how one row came to be measured against a fixture
// that was replaced in the same commit and quietly stopped being true. The
// oracles in differential_test.go are the implementations that were replaced,
// so benchmarking them against the fixtures actually shipped makes the whole
// table re-measurable:
//
//	go test -run '^$' -bench 'Oracle|ParseAppPage|ParseSearchPage|ParseListPage|SitemapShard' -benchtime 200x .
func benchOracleBlocks(b *testing.B, fixture string) {
	b.Helper()
	body := readBenchFixture(b, fixture)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		if len(oracleParseDataBlocks(body)) == 0 {
			b.Fatal("oracle found no blocks")
		}
	}
}

func BenchmarkOracleParseAppPage(b *testing.B)    { benchOracleBlocks(b, "app_page.html") }
func BenchmarkOracleParseSearchPage(b *testing.B) { benchOracleBlocks(b, "search_page.html") }
func BenchmarkOracleParseListPage(b *testing.B)   { benchOracleBlocks(b, "top_charts_page.html") }

func BenchmarkOracleSitemapShard(b *testing.B) {
	body := benchShardBody()
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		if len(oracleShardPackages(body)) == 0 {
			b.Fatal("oracle found no packages")
		}
	}
}

// benchShardBody builds the synthetic shard both the scanner and the oracle
// benchmark measure, so the two rows of the table are comparable.
func benchShardBody() []byte {
	body := []byte(`<?xml version='1.0' encoding='UTF-8'?><urlset xmlns='http://www.sitemaps.org/schemas/sitemap/0.9'>`)
	for i := range 400 {
		if i%8 == 0 {
			body = append(body, []byte(`<url><loc>https://play.google.com/store/apps/details?id=com.example.app`+itoa(i)+`</loc></url>`)...)
			continue
		}
		body = append(body, []byte(`<url><loc>https://play.google.com/store/books/details/B?id=BOOK`+itoa(i)+`</loc></url>`)...)
	}
	// Closed, because the oracle is a real XML parser and rejects a truncated
	// document outright. The scanner never cared, which is why this was missing.
	return append(body, []byte(`</urlset>`)...)
}

// benchRealisticShard builds a shard with the shape a live one has, measured
// on play_sitemaps_2026-08-23_1787500934-00000-of-83445: 8.0MB decompressed,
// 408 <loc> entries of which 54 are apps, and ~242 <xhtml:link> hreflang
// alternates per entry carrying 99.4% of the bytes.
func benchRealisticShard() []byte {
	var b bytes.Buffer
	b.Grow(8 << 20)
	b.WriteString(`<?xml version='1.0' encoding='UTF-8'?><urlset xmlns='http://www.sitemaps.org/schemas/sitemap/0.9'>`)
	for i := range 408 {
		var loc string
		if i%8 == 0 {
			loc = fmt.Sprintf("https://play.google.com/store/apps/details?id=com.example.package%d", i)
		} else {
			loc = fmt.Sprintf("https://play.google.com/store/books/details/Some_Book_Title_%d?id=aBcDeFgH%d", i, i)
		}
		b.WriteString("<url><loc>" + loc + "</loc>")
		for j := range 242 {
			fmt.Fprintf(&b, `<xhtml:link rel="alternate" hreflang="%d" href="%s&amp;hl=%d"/>`, j, loc, j)
		}
		b.WriteString("</url>")
	}
	b.WriteString(`</urlset>`)
	return b.Bytes()
}

// The two ways to read a shard, on a shard the size of a real one. The
// buffered path allocates the whole document to read 0.58% of it; a sweep does
// this 83,445 times.
func BenchmarkShardBuffered(b *testing.B) {
	plain := benchRealisticShard()
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write(plain)
	_ = zw.Close()
	body := gz.Bytes()

	b.SetBytes(int64(len(plain)))
	b.ReportAllocs()
	for b.Loop() {
		out, err := gunzipIfNeeded(body)
		if err != nil {
			b.Fatal(err)
		}
		if len(shardPackages(out)) == 0 {
			b.Fatal("no packages")
		}
	}
}

func BenchmarkShardStreamed(b *testing.B) {
	plain := benchRealisticShard()
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write(plain)
	_ = zw.Close()
	body := gz.Bytes()

	b.SetBytes(int64(len(plain)))
	b.ReportAllocs()
	for b.Loop() {
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			b.Fatal(err)
		}
		pkgs, err := shardPackagesFrom(zr)
		if err != nil {
			b.Fatal(err)
		}
		_ = zr.Close()
		if len(pkgs) == 0 {
			b.Fatal("no packages")
		}
	}
}
