package googleplayscraper

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"
)

// gzipBytes gzip-compresses b, mirroring how Google serves .xml.gz shards.
func gzipBytes(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(b); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func robotsBody(indexURLs ...string) []byte {
	var sb bytes.Buffer
	sb.WriteString("User-agent: *\nDisallow: /search\n")
	for _, u := range indexURLs {
		fmt.Fprintf(&sb, "Sitemap: %s\n", u)
	}
	return sb.Bytes()
}

func indexXML(shardURLs ...string) []byte {
	var sb bytes.Buffer
	sb.WriteString(`<?xml version='1.0' encoding='UTF-8'?><sitemapindex xmlns='http://www.sitemaps.org/schemas/sitemap/0.9'>`)
	for _, u := range shardURLs {
		fmt.Fprintf(&sb, "<sitemap><loc>%s</loc></sitemap>", u)
	}
	sb.WriteString(`</sitemapindex>`)
	return sb.Bytes()
}

func urlsetXML(locs ...string) []byte {
	var sb bytes.Buffer
	sb.WriteString(`<?xml version='1.0' encoding='UTF-8'?><urlset xmlns='http://www.sitemaps.org/schemas/sitemap/0.9'>`)
	for _, l := range locs {
		// Real sitemaps XML-escape & in query strings as &amp;.
		fmt.Fprintf(&sb, "<url><loc>%s</loc></url>", strings.ReplaceAll(l, "&", "&amp;"))
	}
	sb.WriteString(`</urlset>`)
	return sb.Bytes()
}

func TestAppPackageFromLoc(t *testing.T) {
	tests := []struct {
		loc  string
		want string
	}{
		{"https://play.google.com/store/apps/details?id=com.example.app", "com.example.app"},
		{"https://play.google.com/store/apps/details?id=com.x.y&hl=en", "com.x.y"},
		{"https://play.google.com/store/books/details/Foo?id=bgtLAQAAMAAJ", ""},
		{"https://play.google.com/store/apps/dev?id=DEV123", ""},
		{"https://play.google.com/store/apps/collection/cluster?id=com.x", ""},
		{"https://play.google.com/store/apps/details", ""},
		{"  https://play.google.com/store/apps/details?id=com.spaced  ", "com.spaced"},
		{"", ""},
		{"://bad url", ""},
	}
	for _, tt := range tests {
		if got := appPackageFromLoc(tt.loc); got != tt.want {
			t.Errorf("appPackageFromLoc(%q) = %q, want %q", tt.loc, got, tt.want)
		}
	}
}

func TestGunzipIfNeeded(t *testing.T) {
	plain := []byte("<urlset></urlset>")
	if got, err := gunzipIfNeeded(plain); err != nil || !bytes.Equal(got, plain) {
		t.Errorf("plain passthrough: got %q err %v", got, err)
	}

	gz := gzipBytes(t, plain)
	got, err := gunzipIfNeeded(gz)
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Errorf("gunzip = %q, want %q", got, plain)
	}

	// Truncated/invalid gzip (magic present, body garbage) must error.
	bad := []byte{0x1f, 0x8b, 0x00, 0x01}
	if _, err := gunzipIfNeeded(bad); err == nil {
		t.Error("expected error on invalid gzip body")
	}
}

func TestSitemapIndexURLs(t *testing.T) {
	idx0 := BaseURL + "/sitemaps/sitemaps-index-0.xml"
	idx1 := BaseURL + "/sitemaps/sitemaps-index-1.xml"
	c := newMockClient(t, routePath("/robots.txt", robotsBody(idx0, idx1)))

	got, err := c.SitemapIndexURLs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != idx0 || got[1] != idx1 {
		t.Errorf("SitemapIndexURLs = %v", got)
	}
}

func TestSitemapIndexURLsNoneFails(t *testing.T) {
	c := newMockClient(t, routePath("/robots.txt", robotsBody()))
	if _, err := c.SitemapIndexURLs(context.Background()); err == nil {
		t.Error("expected error when robots.txt advertises no sitemaps")
	}
}

func TestSitemapShards(t *testing.T) {
	s0 := BaseURL + "/sitemaps/shard-0.xml.gz"
	s1 := BaseURL + "/sitemaps/shard-1.xml.gz"
	c := newMockClient(t, routePath("/sitemaps/sitemaps-index-0.xml", indexXML(s0, s1)))

	got, err := c.SitemapShards(context.Background(), BaseURL+"/sitemaps/sitemaps-index-0.xml")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != s0 || got[1] != s1 {
		t.Errorf("SitemapShards = %v", got)
	}
}

func TestAllSitemapShardsDedups(t *testing.T) {
	idx0 := BaseURL + "/sitemaps/sitemaps-index-0.xml"
	idx1 := BaseURL + "/sitemaps/sitemaps-index-1.xml"
	shared := BaseURL + "/sitemaps/shard-x.xml.gz"
	c := newMockClient(t,
		routePath("/robots.txt", robotsBody(idx0, idx1)),
		routePath("/sitemaps/sitemaps-index-0.xml", indexXML(BaseURL+"/sitemaps/shard-0.xml.gz", shared)),
		routePath("/sitemaps/sitemaps-index-1.xml", indexXML(shared, BaseURL+"/sitemaps/shard-1.xml.gz")),
	)

	got, err := c.AllSitemapShards(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		BaseURL + "/sitemaps/shard-0.xml.gz",
		shared,
		BaseURL + "/sitemaps/shard-1.xml.gz",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("AllSitemapShards = %v, want %v (deduped, in order)", got, want)
	}
}

func TestSitemapShardPackages(t *testing.T) {
	shard := urlsetXML(
		"https://play.google.com/store/apps/details?id=com.a",
		"https://play.google.com/store/books/details/Foo?id=BOOK1",
		"https://play.google.com/store/apps/details?id=com.b&hl=en",
		"https://play.google.com/store/apps/details?id=com.a", // dup within shard
		"https://play.google.com/store/movies/details?id=MOVIE",
	)
	c := newMockClient(t, routePath("/sitemaps/shard-0.xml.gz", gzipBytes(t, shard)))

	got, err := c.SitemapShardPackages(context.Background(), BaseURL+"/sitemaps/shard-0.xml.gz")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"com.a", "com.b"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("SitemapShardPackages = %v, want %v", got, want)
	}
}

func TestSitemapShardPackagesPlainXML(t *testing.T) {
	// A shard served uncompressed (no gzip magic) must still parse.
	shard := urlsetXML("https://play.google.com/store/apps/details?id=com.plain")
	c := newMockClient(t, routePath("/sitemaps/shard-0.xml.gz", shard))

	got, err := c.SitemapShardPackages(context.Background(), BaseURL+"/sitemaps/shard-0.xml.gz")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got) != fmt.Sprint([]string{"com.plain"}) {
		t.Errorf("got %v", got)
	}
}

// catalogRoutes wires a robots.txt -> 1 index -> N shards graph for the
// orchestrator tests.
func catalogRoutes(t *testing.T, shardBodies map[string][]byte) []routeFunc {
	t.Helper()
	idx0 := BaseURL + "/sitemaps/sitemaps-index-0.xml"
	shardURLs := make([]string, 0, len(shardBodies))
	for u := range shardBodies {
		shardURLs = append(shardURLs, u)
	}
	sort.Strings(shardURLs) // deterministic shard order
	routes := []routeFunc{
		routePath("/robots.txt", robotsBody(idx0)),
		routePath("/sitemaps/sitemaps-index-0.xml", indexXML(shardURLs...)),
	}
	for u, b := range shardBodies {
		// capture path
		path := u[len(BaseURL):]
		routes = append(routes, routePath(path, b))
	}
	return routes
}

func TestEnumerateCatalog(t *testing.T) {
	s0 := BaseURL + "/sitemaps/shard-0.xml.gz"
	s1 := BaseURL + "/sitemaps/shard-1.xml.gz"
	bodies := map[string][]byte{
		s0: gzipBytes(t, urlsetXML(
			"https://play.google.com/store/apps/details?id=com.a",
			"https://play.google.com/store/books/details/B?id=BOOK",
			"https://play.google.com/store/apps/details?id=com.b",
		)),
		s1: gzipBytes(t, urlsetXML(
			"https://play.google.com/store/apps/details?id=com.c",
		)),
	}
	c := newMockClient(t, catalogRoutes(t, bodies)...)

	var (
		mu      sync.Mutex
		got     []string
		doneSum int
		doneCnt int
	)
	err := c.EnumerateCatalog(context.Background(), func(pkg string) {
		got = append(got, pkg) // callbacks are serialized; no lock needed
	}, CatalogOptions{
		Concurrency: 4,
		OnShardDone: func(_ int, _ string, n int) {
			mu.Lock()
			doneSum += n
			doneCnt++
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	sort.Strings(got)
	want := []string{"com.a", "com.b", "com.c"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("emitted = %v, want %v", got, want)
	}
	if doneCnt != 2 || doneSum != 3 {
		t.Errorf("OnShardDone: cnt=%d sum=%d, want cnt=2 sum=3", doneCnt, doneSum)
	}
}

func TestEnumerateCatalogShardSubset(t *testing.T) {
	s0 := BaseURL + "/sitemaps/shard-0.xml.gz"
	s1 := BaseURL + "/sitemaps/shard-1.xml.gz"
	bodies := map[string][]byte{
		s0: gzipBytes(t, urlsetXML("https://play.google.com/store/apps/details?id=com.a")),
		s1: gzipBytes(t, urlsetXML("https://play.google.com/store/apps/details?id=com.b")),
	}
	// shard 0 is shard-0 (sorted first); request only index 1.
	c := newMockClient(t, catalogRoutes(t, bodies)...)

	var got []string
	err := c.EnumerateCatalog(context.Background(), func(pkg string) {
		got = append(got, pkg)
	}, CatalogOptions{Shards: []int{1}})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got) != fmt.Sprint([]string{"com.b"}) {
		t.Errorf("subset emitted = %v, want [com.b]", got)
	}
}

func TestEnumerateCatalogShardErrorContinues(t *testing.T) {
	s0 := BaseURL + "/sitemaps/shard-0.xml.gz"
	s1 := BaseURL + "/sitemaps/shard-1.xml.gz"
	idx0 := BaseURL + "/sitemaps/sitemaps-index-0.xml"
	routes := []routeFunc{
		routePath("/robots.txt", robotsBody(idx0)),
		routePath("/sitemaps/sitemaps-index-0.xml", indexXML(s0, s1)),
		routePath("/sitemaps/shard-0.xml.gz", gzipBytes(t, urlsetXML("https://play.google.com/store/apps/details?id=com.ok"))),
		routePathStatus("/sitemaps/shard-1.xml.gz", http.StatusInternalServerError),
	}
	c := newMockClient(t, routes...)

	var (
		got      []string
		errCount int
	)
	err := c.EnumerateCatalog(context.Background(), func(pkg string) {
		got = append(got, pkg)
	}, CatalogOptions{
		OnShardError: func(_ int, _ string, _ error) { errCount++ },
	})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got) != fmt.Sprint([]string{"com.ok"}) {
		t.Errorf("emitted = %v, want [com.ok] (failed shard skipped)", got)
	}
	if errCount != 1 {
		t.Errorf("OnShardError calls = %d, want 1", errCount)
	}
}

func TestEnumerateCatalogCancel(t *testing.T) {
	idx0 := BaseURL + "/sitemaps/sitemaps-index-0.xml"
	c := newMockClient(t,
		routePath("/robots.txt", robotsBody(idx0)),
		routePath("/sitemaps/sitemaps-index-0.xml", indexXML(BaseURL+"/sitemaps/shard-0.xml.gz")),
		routePath("/sitemaps/shard-0.xml.gz", gzipBytes(t, urlsetXML("https://play.google.com/store/apps/details?id=com.a"))),
	)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before any shard dispatch

	err := c.EnumerateCatalog(ctx, func(string) {}, CatalogOptions{})
	if err == nil {
		t.Error("expected ctx.Err() from a cancelled sweep")
	}
}
