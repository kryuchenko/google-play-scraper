package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	googleplayscraper "github.com/kryuchenko/google-play-scraper"
)

// TestFullScaleCatalogRehearsal runs the whole-catalog pipeline -- sweep,
// genres, apps -- at catalog size against a fake Play generated on the fly:
// 87,600 sitemap shards of real shape (about 46 apps each, some non-app locs,
// every document closed with </urlset>), two indexes, robots.txt, and digest
// answers for the genre pass. Nothing leaves the process. It is what a
// five-hour live run would exercise of this code, in minutes: the 175
// checkpoints, finish at four million ids, the delta against a previous
// generation, the genre table walk with absent and gone ids, and the games
// filter -- with every count known in advance and checked.
//
// What it cannot show is anything about Google: rate limits over hours, real
// shard tails, a generation rolling mid-run. Those need a live run.
//
// Opt-in, a few minutes:
//
//	GPSCRAPE_FULLSCALE=1 go test -run TestFullScaleCatalogRehearsal -v -timeout 30m ./cmd/gpscrape
//
// GPSCRAPE_FULLSCALE_SHARDS overrides the shard count (default 87,600).
func TestFullScaleCatalogRehearsal(t *testing.T) {
	if os.Getenv("GPSCRAPE_FULLSCALE") == "" {
		t.Skip("set GPSCRAPE_FULLSCALE=1 to run (catalog-sized, minutes)")
	}
	shards := 87600
	if v := os.Getenv("GPSCRAPE_FULLSCALE_SHARDS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 2 {
			t.Fatalf("GPSCRAPE_FULLSCALE_SHARDS=%q", v)
		}
		shards = n
	}

	fp := newFakePlay(shards)
	srv := httptest.NewServer(fp)
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	// Its own transport, keeping as many idle connections as there are
	// workers. Live, the client talks HTTP/2 to one host over one
	// connection; against this HTTP/1.1 server, http.DefaultTransport's two
	// idle connections per host would open and drop one per request and run
	// the machine out of ephemeral ports before the sweep was a fifth done.
	tr := &http.Transport{MaxIdleConns: 64, MaxIdleConnsPerHost: 64}
	t.Cleanup(tr.CloseIdleConnections)
	useFakeClient(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		r2 := req.Clone(req.Context())
		r2.URL.Scheme, r2.URL.Host, r2.Host = "http", host, host
		return tr.RoundTrip(r2)
	}))

	// What the generator will serve, computed once up front.
	var ids []string
	for i := range shards {
		ids = append(ids, fp.shardIDs(i)...)
	}
	slices.Sort(ids)
	t.Logf("fake catalog: %d shards, %d app ids", shards, len(ids))

	// The previous generation: every 200th id missing (they come back as
	// added) and 5,000 ids that are gone now (removed). Planted directly, with
	// a genre table for it, so the sweep diffs against it and the genre pass
	// walks a real-sized table.
	dir := t.TempDir()
	prevGen := googleplayscraper.Generation{Date: "2026-08-19", Run: "1787100000", Shards: shards}
	var prevIDs, added, removed []string
	for i, id := range ids {
		if i%200 == 0 {
			added = append(added, id)
			continue
		}
		prevIDs = append(prevIDs, id)
	}
	for i := range 5000 {
		old := fmt.Sprintf("com.zz.removed%05d", i)
		prevIDs = append(prevIDs, old)
		removed = append(removed, old)
	}
	slices.Sort(prevIDs)
	slices.Sort(added)
	slices.Sort(removed)
	prevFile := "snapshot-" + prevGen.ID() + ".txt.gz"
	if _, err := writeSnapshot(filepath.Join(dir, prevFile), prevIDs); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(dir, "manifest-"+prevGen.ID()+".json"),
		manifest{Generation: prevGen, File: prevFile, IDs: len(prevIDs)}); err != nil {
		t.Fatal(err)
	}
	// The table says what the genre was last time: genreOf, except every
	// 500th id, which had another genre (so this run reports it changed).
	if err := writeGenreLines(genresPath(dir), prevIDs, func(id string) string {
		if fp.oldGenre(id) {
			return "TOOLS"
		}
		return genreOf(id)
	}); err != nil {
		t.Fatal(err)
	}

	// Every expectation is computed now, before the run, and the bookkeeping
	// behind it dropped: two 4M-id slices and a 4M-entry map are more memory
	// than the pipeline uses, and a peak measured with them in the heap says
	// nothing about the pipeline.
	wantIDs, wantDigest := len(ids), digestOf(ids)
	var wantGone, wantChanged, wantFirst, wantGames int
	table := make(map[string]bool, len(prevIDs)) // final rows, by id
	for _, id := range prevIDs {
		table[id] = true
	}
	for _, id := range ids {
		absent, alive := fp.absent(id), fp.aliveElsewhere(id)
		known := table[id]
		switch {
		case absent && !alive:
			if known {
				delete(table, id)
			}
			wantGone++
			continue
		case !known:
			wantFirst++
		case fp.oldGenre(id) && genreOf(id) != "TOOLS":
			wantChanged++
		}
		table[id] = true
	}
	for id := range table {
		if strings.HasPrefix(genreOrOld(fp, id, ids), "GAME_") {
			wantGames++
		}
	}
	wantRows := len(table)
	// ids, prevIDs and table are dead from here: the collector does not see
	// them as live, so the GC below returns their memory.
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	baseline := ms.HeapInuse
	peak := samplePeakHeap(t)
	phasePeak := func() float64 { // MB above the baseline since the last call
		p := peak.reset()
		if p < baseline {
			return 0
		}
		return float64(p-baseline) / 1e6
	}
	peak.reset()

	// ---- sweep ----
	start := time.Now()
	_, stderr, err := runVerb(t, cmdSync, "-dir", dir, "-concurrency", "16", "-throttle", "0", "-yes")
	if err != nil {
		t.Fatalf("sweep: %v\n%s", err, tail(stderr, 2000))
	}
	sweepTook, sweepPeak := time.Since(start), phasePeak()

	var m manifest
	readJSON(t, filepath.Join(dir, "manifest-"+fp.gen.ID()+".json"), &m)
	if m.IDs != wantIDs || m.FailedShard != 0 || m.SamplePct != 0 || m.Generation.ID() != fp.gen.ID() {
		t.Errorf("manifest = %+v, want %d ids of %s, complete", m, wantIDs, fp.gen.ID())
	}
	got, err := readSnapshot(filepath.Join(dir, m.File))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != wantIDs || digestOf(got) != wantDigest {
		t.Errorf("snapshot holds %d ids, not the %d generated", len(got), wantIDs)
	}
	raw, err := os.ReadFile(filepath.Join(dir, m.File))
	if err != nil {
		t.Fatal(err)
	}
	if sum := sha256.Sum256(raw); hex.EncodeToString(sum[:]) != m.SHA256 {
		t.Error("the manifest's sha256 is not the snapshot file's")
	}
	var d delta
	readJSON(t, filepath.Join(dir, deltaName(prevGen, fp.gen)), &d)
	if !slices.Equal(d.Added, added) || !slices.Equal(d.Removed, removed) {
		t.Errorf("delta +%d -%d, want +%d -%d", len(d.Added), len(d.Removed), len(added), len(removed))
	}
	for _, name := range []string{"state.json", "partial-" + fp.gen.ID() + ".txt"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Errorf("%s left behind by a finished sweep", name)
		}
	}
	runtime.GC()
	phasePeak()

	// ---- genres ----
	start = time.Now()
	out, stderr, err := runVerb(t, catalogGenres, "-dir", dir, "-concurrency", "8", "-throttle", "0", "-confirm-gone", "de")
	if err != nil {
		t.Fatalf("genres: %v\n%s", err, tail(stderr, 2000))
	}
	genresTook, genresPeak := time.Since(start), phasePeak()
	summary := fmt.Sprintf("%d apps, %d changed, %d gone", wantIDs, wantChanged, wantGone)
	if !strings.Contains(stderr, summary) {
		t.Errorf("genres summary: want %q in\n%s", summary, tail(stderr, 500))
	}
	if n := strings.Count(out, `"change":"first_seen"`); n != wantFirst {
		t.Errorf("%d first_seen records, want %d", n, wantFirst)
	}
	if _, n, err := streamGenres(dir, func(string, string) {}); err != nil || n != wantRows {
		t.Errorf("genre table has %d rows (%v), want %d", n, err, wantRows)
	}
	runtime.GC()
	phasePeak()

	// ---- apps ----
	start = time.Now()
	out, _, err = runVerb(t, catalogApps, "-dir", dir, "-genre", "GAME_*", "-ids-only")
	if err != nil {
		t.Fatalf("apps: %v", err)
	}
	appsTook, appsPeak := time.Since(start), phasePeak()
	if n := strings.Count(out, "\n"); n != wantGames {
		t.Errorf("catalog apps -genre GAME_* listed %d, want %d", n, wantGames)
	}

	t.Logf("sweep %s, %d shard requests, peak +%.0fMB; genres %s, %d digest requests, peak +%.0fMB; "+
		"apps %s, peak +%.0fMB (heap above a %.0fMB baseline holding the fake's expectations)",
		sweepTook.Round(time.Second), fp.shardHits.Load(), sweepPeak,
		genresTook.Round(time.Second), fp.digestHits.Load(), genresPeak,
		appsTook.Round(time.Millisecond), appsPeak, float64(baseline)/1e6)
	// Ceilings over the measured peaks (see the log line), with room: the
	// sweep's is finish sorting four million ids, the genre pass's is the
	// absent ids plus the records this test captures from stdout.
	for name, c := range map[string]struct{ got, max float64 }{
		"sweep": {sweepPeak, 700}, "genres": {genresPeak, 400}, "apps": {appsPeak, 200},
	} {
		if c.got > c.max {
			t.Errorf("%s peak heap +%.0fMB over its %.0fMB ceiling", name, c.got, c.max)
		}
	}
}

// genreOrOld is the genre the final table holds for id: the old table's for
// ids the run did not see, genreOf for the rest.
func genreOrOld(fp *fakePlay, id string, seen []string) string {
	if _, found := slices.BinarySearch(seen, id); !found {
		if fp.oldGenre(id) {
			return "TOOLS"
		}
	}
	return genreOf(id)
}

// heapPeak is a running maximum of HeapInuse, sampled every 5ms until the
// test ends.
type heapPeak struct{ v atomic.Uint64 }

// reset returns the peak since the last reset and starts a new one.
func (h *heapPeak) reset() uint64 { return h.v.Swap(0) }

func samplePeakHeap(t *testing.T) *heapPeak {
	h := &heapPeak{}
	stop := make(chan struct{})
	go func() {
		var ms runtime.MemStats
		tk := time.NewTicker(5 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tk.C:
				runtime.ReadMemStats(&ms)
				for {
					cur := h.v.Load()
					if ms.HeapInuse <= cur || h.v.CompareAndSwap(cur, ms.HeapInuse) {
						break
					}
				}
			}
		}
	}()
	t.Cleanup(func() { close(stop) })
	return h
}

func digestOf(ids []string) string {
	h := sha256.New()
	for _, id := range ids {
		h.Write([]byte(id))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func tail(s string, n int) string {
	if len(s) > n {
		return "..." + s[len(s)-n:]
	}
	return s
}

// ---- the fake ----

// fakePlay serves one generation of a catalog-sized Play, generating every
// shard from its index so nothing but the request counters is held.
type fakePlay struct {
	gen        googleplayscraper.Generation
	shards     int
	shardHits  atomic.Int64
	digestHits atomic.Int64
}

func newFakePlay(shards int) *fakePlay {
	return &fakePlay{
		gen:    googleplayscraper.Generation{Date: "2026-08-23", Run: "1787500934", Shards: shards},
		shards: shards,
	}
}

var shardIndexRe = regexp.MustCompile(`-(\d+)-of-\d+\.xml\.gz$`)

func (fp *fakePlay) shardPath(i int) string {
	return fmt.Sprintf("/sitemaps/play_sitemaps_%s_%s-%05d-of-%05d.xml.gz", fp.gen.Date, fp.gen.Run, i, fp.shards)
}

// shardIDs is shard i's apps: 35 to 57 of them, about 46 on average, the
// spread measured on real shards.
func (fp *fakePlay) shardIDs(i int) []string {
	n := 35 + int(hash32(fmt.Sprint("n", i))%23)
	out := make([]string, n)
	for j := range out {
		out[j] = fmt.Sprintf("com.s%05d.app%02d", i, j)
	}
	return out
}

func (fp *fakePlay) shardBody(i int) []byte {
	var b bytes.Buffer
	b.WriteString(`<?xml version='1.0' encoding='UTF-8'?><urlset xmlns='http://www.sitemaps.org/schemas/sitemap/0.9' xmlns:xhtml='http://www.w3.org/1999/xhtml'>`)
	for j, id := range fp.shardIDs(i) {
		fmt.Fprintf(&b, "<url><loc>%s/store/apps/details?id=%s</loc>"+
			"<xhtml:link rel='alternate' hreflang='de' href='%s/store/apps/details?id=%s&amp;hl=de'/></url>",
			googleplayscraper.BaseURL, id, googleplayscraper.BaseURL, id)
		if j%8 == 0 { // books and info pages share the shards, as they do live
			fmt.Fprintf(&b, "<url><loc>%s/store/books/details?id=B%05d%02d</loc></url>", googleplayscraper.BaseURL, i, j)
		}
	}
	b.WriteString(`</urlset>`)
	var z bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&z, gzip.BestSpeed)
	_, _ = zw.Write(b.Bytes())
	_ = zw.Close()
	return z.Bytes()
}

// genreOf is an app's genre: about 3% games, the rest spread over others.
func genreOf(id string) string {
	switch h := hash32(id) % 100; {
	case h < 2:
		return "GAME_PUZZLE"
	case h < 3:
		return "GAME_ACTION"
	default:
		return []string{"TOOLS", "EDUCATION", "SHOPPING", "SOCIAL", "MUSIC_AND_AUDIO"}[h%5]
	}
}

// absent: about 9% of ids are not listed in the first storefront; half of
// those are alive in the confirmation storefront.
func (fp *fakePlay) absent(id string) bool { return hash32("abs"+id)%100 < 9 }
func (fp *fakePlay) aliveElsewhere(id string) bool {
	return fp.absent(id) && hash32("alive"+id)%2 == 0
}

// oldGenre: every 500th id or so had another genre in the previous table.
func (fp *fakePlay) oldGenre(id string) bool { return hash32("old"+id)%500 == 1 }

func (fp *fakePlay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch p := r.URL.Path; {
	case p == "/robots.txt":
		_, _ = w.Write(robotsTestBody(googleplayscraper.BaseURL+indexPath0, googleplayscraper.BaseURL+indexPath1))
	case p == indexPath0 || p == indexPath1:
		lo, hi := 0, fp.shards/2
		if p == indexPath1 {
			lo, hi = fp.shards/2, fp.shards
		}
		urls := make([]string, 0, hi-lo)
		for i := lo; i < hi; i++ {
			urls = append(urls, googleplayscraper.BaseURL+fp.shardPath(i))
		}
		_, _ = w.Write(indexTestXML(urls...))
	case strings.HasPrefix(p, "/sitemaps/play_sitemaps_"):
		m := shardIndexRe.FindStringSubmatch(p)
		if m == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		i, _ := strconv.Atoi(m[1])
		if i >= fp.shards {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		fp.shardHits.Add(1)
		_, _ = w.Write(fp.shardBody(i))
	case p == pathBatch:
		fp.digestHits.Add(1)
		fp.serveDigest(w, r)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (fp *fakePlay) serveDigest(w http.ResponseWriter, r *http.Request) {
	gl := r.URL.Query().Get("gl")
	ids := requestedIDsNoT(r)
	byIndex := map[string]string{}
	order := make([]string, 0, len(ids))
	for i, id := range ids {
		idx := strconv.Itoa(i)
		order = append(order, idx)
		switch {
		case gl == "us" && fp.absent(id), gl != "us" && !fp.aliveElsewhere(id):
			byIndex[idx] = "" // present, no listing
		default:
			g := genreOf(id)
			byIndex[idx] = genrePayload(g, strings.ToLower(g))
		}
	}
	_, _ = w.Write(framesEnvelope("Ws7gDc", byIndex, order))
}

// requestedIDsNoT is requestedIDs without a *testing.T: the fake runs on the
// server's goroutines, where t.Fatalf is not allowed.
func requestedIDsNoT(r *http.Request) []string {
	body := new(bytes.Buffer)
	_, _ = body.ReadFrom(r.Body)
	decoded := body.String()
	if u, err := url.QueryUnescape(decoded); err == nil {
		decoded = u
	}
	var ids []string
	for _, m := range appRe.FindAllStringSubmatch(decoded, -1) {
		ids = append(ids, m[1])
	}
	return ids
}

func hash32(s string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return h.Sum32()
}
