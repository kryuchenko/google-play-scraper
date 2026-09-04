package googleplayscraper

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"testing"
)

// A census has no sampling error. The finite population correction is what
// delivers that, and getting it wrong is invisible on small samples and
// nonsense at the end: an estimator that still reports an interval after
// reading everything is claiming uncertainty about data it holds.
func TestSummariseGivesACensusNoInterval(t *testing.T) {
	counts := make([]float64, 100)
	for i := range counts {
		counts[i] = float64(40 + i%9)
	}
	got := summarise(Generation{Date: "2026-08-23", Run: "1", Shards: 100}, counts, 100)

	if got.HalfWidth != 0 {
		t.Errorf("half-width = %v after reading every shard, want 0", got.HalfWidth)
	}
	var sum float64
	for _, c := range counts {
		sum += c
	}
	if got.Apps != int(sum) {
		t.Errorf("apps = %d, want the actual total %d", got.Apps, int(sum))
	}
}

// The estimator is (N/n) * sum, so a sample of identical shards must scale
// exactly and claim no error: with zero spread there is nothing to be unsure
// about.
func TestSummariseScalesAndReportsNoSpread(t *testing.T) {
	counts := make([]float64, 50)
	for i := range counts {
		counts[i] = 42
	}
	got := summarise(Generation{}, counts, 1000)

	if got.Apps != 42000 {
		t.Errorf("apps = %d, want 42*1000", got.Apps)
	}
	if got.HalfWidth != 0 {
		t.Errorf("half-width = %v with no spread in the sample, want 0", got.HalfWidth)
	}
	if got.MeanPerShard != 42 {
		t.Errorf("mean = %v, want 42", got.MeanPerShard)
	}
}

// Halving the interval takes four times the sample. This is the property that
// makes an exact count expensive and it should be visible in the arithmetic,
// not just in the documentation.
func TestSummariseHalfWidthFallsAsSqrtOfSample(t *testing.T) {
	build := func(n int) []float64 {
		c := make([]float64, n)
		for i := range c {
			// A fixed repeating pattern, so the sample variance is the same
			// at every size and only n moves.
			c[i] = float64(35 + i%15)
		}
		return c
	}
	const N = 100000
	small := summarise(Generation{}, build(100), N)
	large := summarise(Generation{}, build(400), N)

	ratio := small.HalfWidth / large.HalfWidth
	if math.Abs(ratio-2) > 0.05 {
		t.Errorf("quadrupling the sample changed the half-width by %.3fx, want 2x", ratio)
	}
}

// Dispersion is the check that decides whether sampling is legitimate at all.
// Under a uniform hash the per-shard counts are Poisson, so variance equals
// mean and the ratio is 1; a set of shards that all hold exactly the same
// number is balanced, not hashed, and must not read as 1.
func TestSummariseDetectsWhetherShardsLookHashed(t *testing.T) {
	// Balanced: no spread at all.
	flat := make([]float64, 200)
	for i := range flat {
		flat[i] = 42
	}
	if got := summarise(Generation{}, flat, 100000); got.Dispersion != 0 {
		t.Errorf("dispersion = %v for perfectly balanced shards, want 0", got.Dispersion)
	}

	// Organised: a few shards holding almost everything.
	lumpy := make([]float64, 200)
	for i := range lumpy {
		if i%20 == 0 {
			lumpy[i] = 800
		} else {
			lumpy[i] = 2
		}
	}
	got := summarise(Generation{}, lumpy, 100000)
	if got.Dispersion <= 1 {
		t.Errorf("dispersion = %v for lumpy shards, want well above 1", got.Dispersion)
	}
	if got.HashLooksUniform() {
		t.Errorf("z = %v; lumpy shards were accepted as uniformly hashed", got.DispersionZ)
	}
}

// A real sample: Poisson counts around the measured mean of 42 should pass the
// dispersion test, because that is exactly what generated them.
func TestSummariseAcceptsPoissonCounts(t *testing.T) {
	// A fixed spread whose variance is close to its mean, standing in for the
	// Poisson draw without needing a generator in a test.
	counts := []float64{
		36, 48, 41, 39, 45, 42, 37, 50, 44, 38,
		43, 40, 46, 35, 49, 41, 42, 47, 39, 44,
		38, 43, 45, 36, 41, 48, 40, 42, 37, 46,
	}
	got := summarise(Generation{}, counts, 83445)
	if !got.HashLooksUniform() {
		t.Errorf("z = %+.2f (dispersion %.3f); a Poisson-like sample was rejected",
			got.DispersionZ, got.Dispersion)
	}
	if got.Apps < 3_000_000 || got.Apps > 4_000_000 {
		t.Errorf("apps = %d, want roughly 42*83445", got.Apps)
	}
}

// The relative half-width is the number a caller actually reasons about, and
// it must be derived from the count rather than stored separately, or the two
// drift apart the moment one is recomputed.
func TestRelativeHalfWidthAgreesWithTheCount(t *testing.T) {
	s := CatalogSize{Apps: 3_500_000, HalfWidth: 35_000}
	if got := s.RelativeHalfWidth(); math.Abs(got-0.01) > 1e-9 {
		t.Errorf("relative half-width = %v, want 0.01", got)
	}
	exact := CatalogSize{Apps: 3_500_000, Exact: true}
	if got := exact.RelativeHalfWidth(); got != 0 {
		t.Errorf("exact count reported a relative half-width of %v", got)
	}
	if !exact.HashLooksUniform() {
		t.Error("an exact count was reported as resting on a hash assumption it does not use")
	}
}

// The seed must depend on the generation and nothing else: the same build has
// to draw the same shards on every run, and a new build has to draw fresh
// ones rather than inherit a partition that has stopped meaning anything.
func TestSampleSeedIsStablePerGenerationAndMovesBetweenThem(t *testing.T) {
	a := Generation{Date: "2026-08-23", Run: "1787500934", Shards: 83445}
	again := Generation{Date: "2026-08-23", Run: "1787500934", Shards: 83445}
	b := Generation{Date: "2026-08-27", Run: "1787846534", Shards: 83500}

	if a.SampleSeed() != again.SampleSeed() {
		t.Error("the same generation produced two different seeds")
	}
	if a.SampleSeed() == b.SampleSeed() {
		t.Error("two generations produced the same seed")
	}
	if a.SampleSeed() == 0 {
		t.Error("seed is zero, which CatalogSize treats as 'derive one'")
	}
}

func TestMeanStdDevHandlesShortInput(t *testing.T) {
	if m, s := meanStdDev(nil); m != 0 || s != 0 {
		t.Errorf("empty = (%v, %v), want (0, 0)", m, s)
	}
	if m, s := meanStdDev([]float64{7}); m != 7 || s != 0 {
		t.Errorf("single = (%v, %v), want (7, 0)", m, s)
	}
	// Sample standard deviation, n-1 in the denominator: the population form
	// would give 1 here and understate every interval this package reports.
	m, s := meanStdDev([]float64{1, 3})
	if m != 2 || math.Abs(s-math.Sqrt2) > 1e-12 {
		t.Errorf("(%v, %v), want (2, sqrt(2))", m, s)
	}
}

// ── the two paths end to end, against a mock store ──────────────────────────

// sizeMock builds a client serving a catalog of `shards` shards whose n-th
// shard holds counts[n] apps, with the ids distinct across the whole store.
func sizeMock(t *testing.T, counts []int) *Client {
	t.Helper()
	gen := "play_sitemaps_2026-08-23_1787500934"
	idx := BaseURL + "/sitemaps/sitemaps-index-0.xml"

	shardURL := func(i int) string {
		return fmt.Sprintf("%s/sitemaps/%s-%05d-of-%05d.xml.gz", BaseURL, gen, i, len(counts))
	}
	urls := make([]string, len(counts))
	for i := range counts {
		urls[i] = shardURL(i)
	}

	next := 0
	bodies := make(map[string][]byte, len(counts))
	for i, n := range counts {
		locs := make([]string, n)
		for j := range n {
			locs[j] = fmt.Sprintf("https://play.google.com/store/apps/details?id=com.example.a%d", next)
			next++
		}
		bodies[fmt.Sprintf("/sitemaps/%s-%05d-of-%05d.xml.gz", gen, i, len(counts))] = urlsetXML(locs...)
	}

	return newMockClient(t,
		routePath("/robots.txt", robotsBody(idx)),
		routePath("/sitemaps/sitemaps-index-0.xml", indexXML(urls...)),
		func(req *http.Request) (mockResponse, bool) {
			if body, ok := bodies[req.URL.Path]; ok {
				return mockResponse{Body: body}, true
			}
			return mockResponse{}, false
		},
	)
}

// -exact must return the number that is actually there, with nothing attached
// to it. Anything else and the flag does not mean what it says.
func TestCatalogSizeExactCountsEveryShard(t *testing.T) {
	counts := []int{3, 0, 7, 12, 5, 9, 1, 4}
	want := 41

	got, err := sizeMock(t, counts).CatalogSize(context.Background(), SizeOptions{Exact: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.Apps != want {
		t.Errorf("apps = %d, want %d", got.Apps, want)
	}
	if !got.Exact || got.HalfWidth != 0 {
		t.Errorf("exact=%v half-width=%v; a census must carry no interval", got.Exact, got.HalfWidth)
	}
	if got.ShardsRead != len(counts) {
		t.Errorf("read %d shards of %d", got.ShardsRead, len(counts))
	}
	if got.Generation.ID() != "2026-08-23_1787500934" {
		t.Errorf("generation = %q", got.Generation.ID())
	}
}

// A shard that fails to load is not a shard with no apps in it. -exact cannot
// paper over one: silently returning a short count under the word "exact" is
// worse than returning nothing.
func TestCatalogSizeExactRefusesAnUnreadableShard(t *testing.T) {
	gen := "play_sitemaps_2026-08-23_1787500934"
	idx := BaseURL + "/sitemaps/sitemaps-index-0.xml"
	var urls []string
	for i := range 4 {
		urls = append(urls, fmt.Sprintf("%s/sitemaps/%s-%05d-of-00004.xml.gz", BaseURL, gen, i))
	}
	c := newMockClient(t,
		routePath("/robots.txt", robotsBody(idx)),
		routePath("/sitemaps/sitemaps-index-0.xml", indexXML(urls...)),
		routePathStatus(fmt.Sprintf("/sitemaps/%s-00002-of-00004.xml.gz", gen), http.StatusInternalServerError),
		func(req *http.Request) (mockResponse, bool) {
			return mockResponse{Body: urlsetXML("https://play.google.com/store/apps/details?id=com.example.a")}, true
		},
	)

	if got, err := c.CatalogSize(context.Background(), SizeOptions{Exact: true}); err == nil {
		t.Errorf("returned %d apps as exact despite a shard that would not load", got.Apps)
	}
}

// The estimate must land on the truth and say so honestly. With every shard
// holding the same number there is no sampling error, so the estimate is the
// exact answer and the interval is zero -- which is the cleanest case in which
// to check that the scaling is right.
func TestCatalogSizeEstimateScalesToTheWholeCatalog(t *testing.T) {
	counts := make([]int, 400)
	for i := range counts {
		counts[i] = 10
	}
	got, err := sizeMock(t, counts).CatalogSize(context.Background(), SizeOptions{
		Precision: 0.05, Pilot: 40,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Exact {
		t.Error("an estimate reported itself as exact")
	}
	if got.Apps != 4000 {
		t.Errorf("apps = %d, want 4000", got.Apps)
	}
	if got.ShardsRead >= len(counts) {
		t.Errorf("read %d of %d shards; the point of an estimate is to read fewer",
			got.ShardsRead, len(counts))
	}
}

// A tighter target must read more shards. If it does not, -precision is
// decoration.
func TestCatalogSizeTighterPrecisionReadsMoreShards(t *testing.T) {
	counts := make([]int, 3000)
	for i := range counts {
		counts[i] = 20 + i%25 // a spread the pilot can actually measure
	}

	loose, err := sizeMock(t, counts).CatalogSize(context.Background(), SizeOptions{Precision: 0.05, Pilot: 50})
	if err != nil {
		t.Fatal(err)
	}
	tight, err := sizeMock(t, counts).CatalogSize(context.Background(), SizeOptions{Precision: 0.01, Pilot: 50})
	if err != nil {
		t.Fatal(err)
	}

	if tight.ShardsRead <= loose.ShardsRead {
		t.Errorf("1%% read %d shards, 5%% read %d; the tighter target must cost more",
			tight.ShardsRead, loose.ShardsRead)
	}
	if tight.RelativeHalfWidth() > 0.01 {
		t.Errorf("asked for 1%%, achieved %.3f%%", tight.RelativeHalfWidth()*100)
	}
	if loose.RelativeHalfWidth() > 0.05 {
		t.Errorf("asked for 5%%, achieved %.3f%%", loose.RelativeHalfWidth()*100)
	}
}

// Zero Precision and no Exact would otherwise mean "an interval of zero
// width", which is a full sweep asked for by accident. It must be the explicit
// thing instead.
func TestCatalogSizeDefaultsToACensusRatherThanAnImpossibleTarget(t *testing.T) {
	counts := []int{2, 3, 4}
	got, err := sizeMock(t, counts).CatalogSize(context.Background(), SizeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Exact || got.Apps != 9 {
		t.Errorf("exact=%v apps=%d, want an exact 9", got.Exact, got.Apps)
	}
}

// The same generation must draw the same shards twice running, or two counts
// of an unchanged catalog differ for no reason a caller can see.
func TestCatalogSizeIsReproducibleForAGeneration(t *testing.T) {
	counts := make([]int, 500)
	for i := range counts {
		counts[i] = 10 + i%30
	}
	a, err := sizeMock(t, counts).CatalogSize(context.Background(), SizeOptions{Precision: 0.05, Pilot: 30})
	if err != nil {
		t.Fatal(err)
	}
	b, err := sizeMock(t, counts).CatalogSize(context.Background(), SizeOptions{Precision: 0.05, Pilot: 30})
	if err != nil {
		t.Fatal(err)
	}
	if a.Apps != b.Apps || a.ShardsRead != b.ShardsRead {
		t.Errorf("two runs of one generation disagreed: %d apps/%d shards vs %d/%d",
			a.Apps, a.ShardsRead, b.Apps, b.ShardsRead)
	}
}

// A catalog of one shard is a census whose sample happens to be everything.
// The textbook correction divides by N-1 to say the error is exactly zero, and
// at N == 1 that is 0/0: a public struct came back full of NaN, which compares
// false against everything and marshals as invalid JSON.
func TestSummariseSingleShardIsNotNaN(t *testing.T) {
	got := summarise(Generation{Date: "20260101", Run: "1", Shards: 1}, []float64{42}, 1)

	if math.IsNaN(got.HalfWidth) {
		t.Errorf("HalfWidth is NaN for a one-shard catalog")
	}
	if got.HalfWidth != 0 {
		t.Errorf("HalfWidth = %v, want 0: reading every shard leaves no sampling error", got.HalfWidth)
	}
	if got.Apps != 42 {
		t.Errorf("Apps = %d, want 42", got.Apps)
	}
	if math.IsNaN(got.RelativeHalfWidth()) {
		t.Error("RelativeHalfWidth is NaN")
	}
}
