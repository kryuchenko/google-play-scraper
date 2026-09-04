package googleplayscraper

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/iotest"
)

// Two parsers here were replaced by substring scanners because the originals
// were pathologically slow: a regular expression with two lazy quantifiers
// backtracking across a megabyte of HTML, and an XML document tree built to
// read one field in eight of its entries.
//
// A faster parser that decodes something slightly different is not an
// optimisation, it is a silent behaviour change -- and the inputs are
// undocumented payloads from a third party, so "slightly different" is exactly
// what a hand-written scanner gets wrong. The originals are kept here as
// oracles and the replacements are held against them on arbitrary input.
//
// They live in the test file rather than the package so they cannot be reached
// by accident, and so their cost is never paid in production.

// ── oracle: the regular expression parseDataBlocks used to use ──────────────

var oracleScriptDataRegex = regexp.MustCompile(
	`AF_initDataCallback\(\{key:\s*'(ds:\d+)'.*?data:(.*?), sideChannel:`)

func oracleParseDataBlocks(body []byte) map[string]any {
	blocks := make(map[string]any)
	for _, m := range oracleScriptDataRegex.FindAllStringSubmatch(string(body), -1) {
		if len(m) < 3 {
			continue
		}
		var data any
		if err := json.Unmarshal([]byte(strings.TrimSpace(m[2])), &data); err != nil {
			continue
		}
		blocks[m[1]] = data
	}
	return blocks
}

// ── oracle: the document tree shardPackages used to build ──────────────────

type oracleURLSetDoc struct {
	XMLName xml.Name `xml:"urlset"`
	URLs    []struct {
		Loc string `xml:"loc"`
	} `xml:"url"`
}

func oracleShardPackages(body []byte) []string {
	var doc oracleURLSetDoc
	if err := xml.Unmarshal(body, &doc); err != nil {
		return nil
	}
	seen := make(map[string]struct{})
	var pkgs []string
	for _, u := range doc.URLs {
		pkg := appPackageFromLoc(u.Loc)
		if pkg == "" {
			continue
		}
		if _, dup := seen[pkg]; dup {
			continue
		}
		seen[pkg] = struct{}{}
		pkgs = append(pkgs, pkg)
	}
	return pkgs
}

// ── the recorded pages, which is where a divergence would matter most ──────

func TestDataBlockScanMatchesTheRegexItReplaced(t *testing.T) {
	for _, name := range []string{
		"app_page.html", "app_page_game.html", "app_unavailable_region.html",
		"search_page.html", "category_page.html", "cluster_page.html",
		"developer_page.html", "similar_page.html", "top_charts_page.html",
		"datasafety_page.html",
	} {
		body := readFixture(t, name)
		want := oracleParseDataBlocks(body)
		got := parseDataBlocks(body)

		if len(want) == 0 {
			t.Errorf("%s: the oracle found no blocks; the fixture cannot prove anything", name)
			continue
		}
		if !reflect.DeepEqual(want, got) {
			t.Errorf("%s: scanner and regex disagree (regex %d blocks, scanner %d)",
				name, len(want), len(got))
			for _, k := range slices.Sorted(maps.Keys(want)) {
				if _, ok := got[k]; !ok {
					t.Errorf("  scanner lost %s", k)
				}
			}
			for _, k := range slices.Sorted(maps.Keys(got)) {
				if _, ok := want[k]; !ok {
					t.Errorf("  scanner invented %s", k)
				}
			}
		}
	}
}

// dataBlockSeq must yield in document order. parseDataBlocks collapses to a
// map, so a regression there would be invisible until a category page listed
// its clusters in a different order on every run.
func TestDataBlockSeqIsOrdered(t *testing.T) {
	body := []byte(
		`<script>AF_initDataCallback({key: 'ds:7', hash: '1', data:[7], sideChannel: {}});</script>` +
			`<script>AF_initDataCallback({key: 'ds:3', hash: '2', data:[3], sideChannel: {}});</script>` +
			`<script>AF_initDataCallback({key: 'ds:5', hash: '3', data:[5], sideChannel: {}});</script>`)

	var keys []string
	for k := range dataBlockSeq(body) {
		keys = append(keys, k)
	}
	if want := []string{"ds:7", "ds:3", "ds:5"}; !slices.Equal(keys, want) {
		t.Errorf("keys = %v, want %v (document order, not sorted)", keys, want)
	}
}

func TestDataBlockSeqStopsOnBreak(t *testing.T) {
	body := readFixture(t, "app_page.html")
	var n int
	for range dataBlockSeq(body) {
		n++
		break
	}
	if n != 1 {
		t.Errorf("yielded %d blocks after break, want 1", n)
	}
}

// ── differential fuzzing: the fixtures prove the happy path, this proves the
// rest. Both replacements are hand-written scanners over hostile input.

func FuzzDataBlockScanMatchesRegex(f *testing.F) {
	for _, name := range []string{"app_page.html", "search_page.html", "top_charts_page.html"} {
		f.Add(readFixtureF(f, name))
	}
	// Shapes a scanner gets wrong: truncation at each marker, a payload that
	// is not JSON, and the markers out of order.
	f.Add([]byte(`AF_initDataCallback({key: 'ds:1', data:[1], sideChannel:`))
	f.Add([]byte(`AF_initDataCallback({key: 'ds:1', data:`))
	f.Add([]byte(`AF_initDataCallback({key: 'ds:1'`))
	f.Add([]byte(`AF_initDataCallback({key:`))
	f.Add([]byte(`, sideChannel: data: 'ds:1' AF_initDataCallback({key:`))
	f.Add([]byte(`AF_initDataCallback({key: 'ds:1', data:not-json, sideChannel:`))
	f.Add([]byte(nil))

	f.Fuzz(func(t *testing.T, body []byte) {
		want := oracleParseDataBlocks(body)
		got := parseDataBlocks(body)
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("scanner diverged from the regex it replaced\n  input:   %q\n  regex:   %#v\n  scanner: %#v",
				body, want, got)
		}
	})
}

func FuzzShardScanMatchesXML(f *testing.F) {
	f.Add([]byte(`<?xml version='1.0'?><urlset><url><loc>https://play.google.com/store/apps/details?id=com.a</loc></url></urlset>`))
	f.Add([]byte(`<urlset><url><loc>https://play.google.com/store/books/details/B?id=X</loc></url></urlset>`))
	f.Add([]byte(`<urlset><url><loc>https://play.google.com/store/apps/details?id=com.a&amp;hl=en</loc></url></urlset>`))
	f.Add([]byte(`<urlset><url><loc></loc></url></urlset>`))
	f.Add([]byte(`<urlset><url><loc>`))
	f.Add([]byte(`<loc>https://play.google.com/store/apps/details?id=com.a</loc>`))
	f.Add([]byte(nil))

	f.Fuzz(func(t *testing.T, body []byte) {
		want := oracleShardPackages(body)
		// The oracle returns nil on malformed XML; the scanner reads what it
		// can. Only compare where the oracle could parse the document at all,
		// which is the contract that matters: every shard Google actually
		// serves is well-formed.
		var doc oracleURLSetDoc
		if xml.Unmarshal(body, &doc) != nil {
			return
		}
		if locScanDivergesByDesign(body, "url") {
			return
		}
		got := shardPackages(body)
		if !slices.Equal(want, got) {
			t.Fatalf("scanner diverged from the XML parser it replaced\n  input: %q\n  xml:   %v\n  scan:  %v",
				body, want, got)
		}
	})
}

// readFixtureF is readFixture for *testing.F, which is not a *testing.T.
func readFixtureF(f *testing.F, name string) []byte {
	f.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		f.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

// parseAppPage decodes ds:5 alone. extractAppData reads only that block, but
// the page carries thirteen and the parser used to decode all of them --
// 4.8x the time and 6.7x the allocation for a result that must be identical.
// "Must be" is the part worth pinning: this test fails if extractAppData ever
// grows a second block it needs and parseAppPage is not updated with it.
func TestParseAppPageNeedsOnlyDS5(t *testing.T) {
	for _, name := range []string{"app_page.html", "app_page_game.html", "app_unavailable_region.html"} {
		body := readFixture(t, name)

		full, errFull := extractAppData(parseDataBlocks(body), "com.x", "https://example/x")
		lean, errLean := parseAppPage(body, "com.x", "https://example/x")

		if (errFull == nil) != (errLean == nil) {
			t.Errorf("%s: all-blocks err=%v, ds:5-only err=%v", name, errFull, errLean)
			continue
		}
		if errFull != nil {
			continue
		}
		if !reflect.DeepEqual(full, lean) {
			t.Errorf("%s: decoding every block gives a different App than decoding ds:5 alone", name)
		}
	}
}

// ── oracle: the document tree SitemapShards used to build ───────────────────

type oracleIndexDoc struct {
	XMLName  xml.Name         `xml:"sitemapindex"`
	Sitemaps []oracleIndexLoc `xml:"sitemap"`
}

type oracleIndexLoc struct {
	Loc string `xml:"loc"`
}

func oracleIndexShards(body []byte) []string {
	var doc oracleIndexDoc
	if err := xml.Unmarshal(body, &doc); err != nil {
		return nil
	}
	shards := make([]string, 0, len(doc.Sitemaps))
	for _, s := range doc.Sitemaps {
		if loc := strings.TrimSpace(s.Loc); loc != "" {
			shards = append(shards, loc)
		}
	}
	return shards
}

// The index parser was the last one still building a tree. An index is 6MB of
// XML holding 50,000 <loc> entries and nothing else worth reading, so the tree
// cost 94MB to produce 7.2MB of strings -- and it was paid before a sweep
// fetched its first shard.
func FuzzIndexScanMatchesXML(f *testing.F) {
	f.Add([]byte(`<?xml version='1.0'?><sitemapindex><sitemap><loc>https://play.google.com/sitemaps/a.xml.gz</loc></sitemap></sitemapindex>`))
	f.Add([]byte(`<sitemapindex><sitemap><loc>a&amp;b</loc></sitemap><sitemap><loc>  spaced  </loc></sitemap></sitemapindex>`))
	f.Add([]byte(`<sitemapindex><sitemap><loc></loc></sitemap></sitemapindex>`))
	f.Add([]byte(`<sitemapindex><sitemap><loc>`))
	f.Add([]byte(`<loc>orphan</loc>`))
	f.Add([]byte(nil))

	f.Fuzz(func(t *testing.T, body []byte) {
		// The oracle returns nil on malformed XML; the scanner reads what it
		// can. Compare only where the tree could parse the document at all,
		// which is the contract that matters: every index Google serves is
		// well-formed.
		var doc oracleIndexDoc
		if xml.Unmarshal(body, &doc) != nil {
			return
		}
		if locScanDivergesByDesign(body, "sitemap") {
			return
		}
		want := oracleIndexShards(body)
		got := indexShards(body)
		if !slices.Equal(want, got) {
			t.Fatalf("index scanner diverged from the XML parser it replaced\n  input: %q\n  xml:   %v\n  scan:  %v",
				body, want, got)
		}
	})
}

// The one place the scanners and the tree are known to disagree, stated as an
// assertion rather than left to a fuzzer to rediscover once a month. Both
// inputs are also committed as seeds under testdata/fuzz/, so the excluded case
// stays exercised even when the fuzz step does not run.
func TestIndexScanLocOutsideSitemapEntry(t *testing.T) {
	body := []byte(`<sitemapindex><loc>0</loc></sitemapindex>`)

	if got := oracleIndexShards(body); len(got) != 0 {
		t.Errorf("the document tree reads an unwrapped <loc>: %v", got)
	}
	if got := indexShards(body); !slices.Equal(got, []string{"0"}) {
		t.Errorf("indexShards(%q) = %v, want [0]: the scan reads a <loc> wherever it is", body, got)
	}
	if !locScanDivergesByDesign(body, "sitemap") {
		t.Error("the fuzz property does not exclude the divergence it is documented to allow")
	}
}

func TestShardScanLocOutsideURLEntry(t *testing.T) {
	body := []byte(`<urlset><loc>https://play.google.com/store/apps/details?id=com.a</loc></urlset>`)

	if got := oracleShardPackages(body); len(got) != 0 {
		t.Errorf("the document tree reads an unwrapped <loc>: %v", got)
	}
	if got := shardPackages(body); !slices.Equal(got, []string{"com.a"}) {
		t.Errorf("shardPackages(%q) = %v, want [com.a]: the scan reads a <loc> wherever it is", body, got)
	}
	if !locScanDivergesByDesign(body, "url") {
		t.Error("the fuzz property does not exclude the divergence it is documented to allow")
	}
}

// The exclusion has to be narrow: a guard that skipped every document would
// make both differential fuzzers pass by testing nothing.
func TestLocScanDivergenceIsNarrow(t *testing.T) {
	for _, body := range []string{
		`<sitemapindex><sitemap><loc>https://play.google.com/sitemaps/a.xml.gz</loc></sitemap></sitemapindex>`,
		`<sitemapindex><sitemap><loc>a&amp;b</loc></sitemap><sitemap><loc>  spaced  </loc></sitemap></sitemapindex>`,
		`<sitemapindex><sitemap><loc></loc></sitemap></sitemapindex>`,
		`<sitemapindex></sitemapindex>`,
	} {
		if locScanDivergesByDesign([]byte(body), "sitemap") {
			t.Errorf("excluded a document the scan and the tree agree on: %s", body)
		}
	}
	for _, body := range []string{
		`<urlset><url><loc>https://play.google.com/store/apps/details?id=com.a</loc></url></urlset>`,
		`<urlset><url><loc>https://play.google.com/store/apps/details?id=com.a&amp;hl=en</loc></url></urlset>`,
	} {
		if locScanDivergesByDesign([]byte(body), "url") {
			t.Errorf("excluded a document the scan and the tree agree on: %s", body)
		}
	}
}

// ── the streaming shard scanner against the buffered one ────────────────────
//
// shardPackagesFrom walks a bounded window instead of an 8MB buffer. It is the
// same scan, but "the same" is a claim about a hand-written parser reading an
// undocumented third-party format across chunk boundaries -- which is exactly
// where a streaming rewrite goes wrong. A marker split between two reads, a
// <loc> that straddles the window, an empty read: each is invisible on the
// happy path and each changes the answer.

func TestShardStreamMatchesTheBufferOnRecordedShards(t *testing.T) {
	for _, name := range []string{"sitemap_shard.xml", "sitemap_index.xml"} {
		body, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			continue // fixture set differs between checkouts; the fuzz covers the rest
		}
		want := shardPackages(body)
		got, err := shardPackagesFrom(bytes.NewReader(body))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !slices.Equal(want, got) {
			t.Errorf("%s: stream %d ids, buffer %d", name, len(got), len(want))
		}
	}
}

// The window is 32KB and the carry 64KB, so a shard only exercises a boundary
// when it is bigger than that. A real one is 8MB; this builds the same shape
// at a size the test can hold, then reads it back through readers that hand
// out awkward chunk sizes.
func TestShardStreamAcrossChunkBoundaries(t *testing.T) {
	var b bytes.Buffer
	b.WriteString(`<?xml version='1.0'?><urlset>`)
	var want []string
	for i := range 4000 {
		pkg := fmt.Sprintf("com.example.app%d", i)
		want = append(want, pkg)
		b.WriteString(`<url><loc>https://play.google.com/store/apps/details?id=` + pkg + `</loc>`)
		// The alternates are 99% of a real shard and carry no <loc> at all.
		for j := range 12 {
			fmt.Fprintf(&b, `<xhtml:link rel="alternate" hreflang="l%d" href="https://play.google.com/store/apps/details?id=%s&amp;hl=l%d"/>`, j, pkg, j)
		}
		b.WriteString(`</url>`)
	}
	b.WriteString(`</urlset>`)
	body := b.Bytes()

	if got := shardPackages(body); !slices.Equal(want, got) {
		t.Fatalf("the buffered scan itself disagrees: %d ids, want %d", len(got), len(want))
	}

	// One byte at a time is the worst case for a boundary bug: every marker in
	// the document is split.
	for _, size := range []int{1, 3, 7, 4096, 32 << 10, 1 << 20} {
		got, err := shardPackagesFrom(iotest.OneByteReader(&chunkReader{body: body, size: size}))
		if err != nil {
			t.Errorf("chunk %d: %v", size, err)
			continue
		}
		if !slices.Equal(want, got) {
			t.Errorf("chunk %d: stream found %d ids, want %d", size, len(got), len(want))
		}
	}
}

// chunkReader hands out at most size bytes per Read, so a caller sees markers
// split wherever the size falls.
type chunkReader struct {
	body []byte
	size int
	off  int
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if r.off >= len(r.body) {
		return 0, io.EOF
	}
	n := min(min(len(p), r.size), len(r.body)-r.off)
	copy(p, r.body[r.off:r.off+n])
	r.off += n
	return n, nil
}

func FuzzShardStreamMatchesBuffer(f *testing.F) {
	f.Add([]byte(`<urlset><url><loc>https://play.google.com/store/apps/details?id=com.a</loc></url></urlset>`))
	f.Add([]byte(`<loc>https://play.google.com/store/apps/details?id=com.a</loc><loc>https://play.google.com/store/apps/details?id=com.a</loc>`))
	f.Add([]byte(`<loc>https://play.google.com/store/apps/details?id=com.a&amp;hl=en</loc>`))
	f.Add([]byte(`<loc><loc>https://play.google.com/store/apps/details?id=com.a</loc>`))
	f.Add([]byte(`<lo`))
	f.Add([]byte(`<loc>`))
	f.Add([]byte(`</loc>`))
	f.Add([]byte(nil))

	f.Fuzz(func(t *testing.T, body []byte) {
		// The window is bounded on purpose, so the two agree exactly on any
		// document whose locs close within it -- which every shard Google
		// serves does, by four orders of magnitude. Past the bound both give
		// up, but not necessarily at the same place, so those inputs are out
		// of contract rather than out of spec.
		if unterminatedBeyondCarry(body) {
			return
		}
		want := shardPackages(body)
		got, err := shardPackagesFrom(bytes.NewReader(body))
		if err != nil {
			t.Fatalf("stream failed on input the buffer read: %v", err)
		}
		if !slices.Equal(want, got) {
			t.Fatalf("stream diverged from the buffered scan\n  input:  %q\n  buffer: %v\n  stream: %v",
				body, want, got)
		}
	})
}

// locScanDivergesByDesign reports whether this document is one where a byte
// scan for <loc> and a document tree are allowed to disagree.
//
// The scanners are deliberately not XML parsers -- the tree cost 94MB per index
// and 12x the CPU per shard, which is the whole reason they exist (see the
// contract on indexShards). So there is a set of documents where the two
// legitimately differ, and the property has to exclude it rather than pretend
// it is not there: CI hit one such input on 2026-09-03 (run 33804791480) and
// nothing was committed with it, so the same class of input can fail the fuzz
// step again at any time. Teaching the scan the element stack,
// entity table and comment syntax of XML would buy nothing -- every document
// Google serves is outside the excluded set -- and would cost state on a hot
// path that also has to survive chunk boundaries.
//
// wrapper is the element the oracle reads a <loc> out of: <sitemap> for an
// index, <url> for a shard. Excluded:
//
//   - a <loc> that is not directly inside a wrapper that is itself directly
//     inside the root element. The scan does not know what encloses a marker;
//     the tree reads only the wrapped ones. This is the input CI found.
//   - a second <loc> inside one wrapper. The tree holds one string per entry,
//     so the last one wins and the earlier ones vanish; the scan returns every
//     one of them.
//   - a loc element not spelled exactly <loc>…</loc>: attributes, a namespace
//     prefix, whitespace inside the tag, the self-closing form. The scan
//     matches five bytes, not the grammar.
//   - a <loc> the tree does not see at all, inside a comment, a CDATA section
//     or an attribute value. Same reason, other direction.
//   - markup, a numeric character reference or a carriage return in the text.
//     The scan decodes the five named entities Google's sitemaps use and copies
//     everything else through, where the tree also resolves &#NN; and folds CR
//     into LF.
//
// Named entities stay inside the property: they are what real sitemaps carry
// (an app URL is full of &amp;), so unescapeXMLEntities is still held against
// the tree on every input that uses them.
//
// A document the tokeniser cannot walk is excluded too. The callers have
// already returned on a document xml.Unmarshal rejects, so what reaches here
// and still fails is trailing junk after the root element -- which the tree
// ignores and the scan reads.
func locScanDivergesByDesign(body []byte, wrapper string) bool {
	dec := xml.NewDecoder(bytes.NewReader(body))
	var stack []string
	locs, locsHere := 0, 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return true
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			if _, end := tok.(xml.EndElement); end {
				if len(stack) == 0 {
					return true
				}
				stack = stack[:len(stack)-1]
			}
			continue
		}
		if start.Name.Local != "loc" {
			if len(stack) == 1 {
				locsHere = 0 // a new entry under the root
			}
			stack = append(stack, start.Name.Local)
			continue
		}
		if len(stack) != 2 || stack[1] != wrapper {
			return true
		}
		locsHere++
		if locsHere > 1 {
			return true
		}
		if !bytes.HasSuffix(body[:dec.InputOffset()], locOpen) {
			return true
		}
		if !locTextIsLiteral(dec, body) {
			return true
		}
		locs++
	}
	// A marker the tree never reported is one hidden in a comment, a CDATA
	// section or an attribute value, where only the scan can see it.
	return locs != bytes.Count(body, locOpen) || locs != bytes.Count(body, locClose)
}

// locTextIsLiteral consumes one <loc> element, having just read its start tag,
// and reports whether the bytes between the tags are what the scan will hand to
// unescapeXMLEntities: character data, closed by a literal </loc>, with no
// markup, numeric character reference or carriage return in it. A CDATA section
// arrives as character data, so the byte check is what rules it out.
func locTextIsLiteral(dec *xml.Decoder, body []byte) bool {
	from := dec.InputOffset()
	for {
		tok, err := dec.Token()
		if err != nil {
			return false
		}
		switch tok.(type) {
		case xml.CharData:
		case xml.EndElement:
			to := dec.InputOffset()
			if !bytes.HasSuffix(body[:to], locClose) {
				return false
			}
			text := body[from : to-int64(len(locClose))]
			return !bytes.ContainsAny(text, "<\r") && !bytes.Contains(text, []byte("&#"))
		default: // a comment, a processing instruction, a nested element
			return false
		}
	}
}

// unterminatedBeyondCarry reports whether the document contains a <loc> that
// stays open for longer than the streaming window carries.
func unterminatedBeyondCarry(body []byte) bool {
	rest := body
	for {
		i := bytes.Index(rest, locOpen)
		if i < 0 {
			return false
		}
		after := rest[i+len(locOpen):]
		j := bytes.Index(after, locClose)
		if j < 0 {
			return len(after) > shardScanCarryMax-len(locOpen)
		}
		if j > shardScanCarryMax-len(locOpen) {
			return true
		}
		rest = after[j+len(locClose):]
	}
}
