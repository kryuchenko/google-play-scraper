package googleplayscraper

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
)

// Counting the catalog.
//
// There are two honest answers to "how many apps does Google Play list", and
// they cost four orders of magnitude apart.
//
// The exact one is a full sweep: 83,445 shards, about four and a half hours,
// and it is exact because every id is read. Nothing cheaper is exact. That is
// not an implementation limit but a theorem -- Charikar, Chaudhuri, Motwani
// and Narasayya (PODS 2000, "Towards Estimation Error Guarantees for Distinct
// Values") show that any estimator reading r of n rows suffers ratio error at
// least sqrt((n-r)/2r · log(1/δ)) with probability δ on some input. Partial
// data cannot pin a count down in general.
//
// The estimate is a sample of shards, and it is cheap for a reason specific to
// how Google builds them: shards are hash-partitioned, so each is an unbiased
// miniature of the whole catalog. That is measured here rather than assumed --
// see Dispersion.
//
// Which puts this problem in the easy corner of the literature above, not the
// hard one. Charikar's lower bound is driven by singletons hiding among
// repetitions, and there are no repetitions here: an app appears in exactly
// one shard (verified across 63,305 ids, zero duplicates). With known
// inclusion probabilities and one copy per object, the textbook estimator
// applies and its variance is computable rather than merely bounded.
//
// For the same reason the species-richness family -- Chao1, Chao2, jackknife,
// Good-Turing -- does not apply at all, despite looking superficially apt.
// They infer unseen classes from how many were seen exactly once, and here
// *everything* is seen exactly once, so the frequency-of-frequencies signal
// they read is identically empty.

// CatalogSize is a count of the store's app listings, and what the count knows
// about its own accuracy.
type CatalogSize struct {
	// Generation is the sitemap build the count refers to. A count is a
	// statement about one generation, never about "now": the sitemap is
	// Google's own snapshot, republished every few days.
	Generation Generation

	// Apps is the number of app package ids. Exact when Exact is set,
	// otherwise the estimate.
	Apps int

	// Exact reports whether every shard was read. When false, Apps is an
	// estimate and HalfWidth says how far off it may be.
	Exact bool

	// HalfWidth is the 95% confidence half-width on Apps, in ids. Zero when
	// Exact -- a census has no sampling error.
	HalfWidth float64

	// ShardsRead and ShardsTotal are how much of the catalog was actually
	// looked at.
	ShardsRead, ShardsTotal int

	// MeanPerShard is the average number of app ids in a shard read.
	MeanPerShard float64

	// Dispersion is the ratio of the between-shard variance to the mean, and
	// it is the check that decides whether the estimate means anything.
	//
	// If a shard's membership is a hash of the package name, each shard's
	// count is Poisson and this ratio is 1. Below 1 means Google balances the
	// shards, which only makes the estimate better than advertised. Above 1
	// means shards are organised by something -- and then they are not
	// interchangeable, a sample of them is not a sample of the catalog, and
	// the interval understates the error.
	//
	// Kish's design effect for cluster sampling is 1+(b-1)ρ, and hash-formed
	// clusters have ρ=0 in expectation, so DEFF=1. In expectation. Cochran is
	// explicit that the realised partition still has to be measured, which is
	// what this is. Zero when Exact.
	Dispersion float64

	// Target is the relative half-width that was asked for, so a caller can
	// see whether it was met. It often is not, and not through a bug: the
	// sample size is solved from the pilot's spread, and the pilot's spread is
	// itself an estimate. A pilot that understates it stops the run short --
	// asking for 1% and achieving 1.02% is an ordinary outcome, and silence
	// about it is what makes it a problem.
	Target float64

	// DispersionZ is Dispersion expressed as a standard normal deviate, so it
	// can be judged without a chi-square table. Beyond about ±3 the uniform
	// hash assumption is in doubt. Zero when Exact.
	DispersionZ float64
}

// RelativeHalfWidth is HalfWidth as a fraction of the count. Zero when Exact.
func (s CatalogSize) RelativeHalfWidth() float64 {
	if s.Exact || s.Apps == 0 {
		return 0
	}
	return s.HalfWidth / float64(s.Apps)
}

// MetTarget reports whether the interval actually achieved is as tight as the
// one requested. False is not an error; see Target.
func (s CatalogSize) MetTarget() bool {
	return s.Exact || s.Target <= 0 || s.RelativeHalfWidth() <= s.Target
}

// HashLooksUniform reports whether the between-shard spread is what a uniform
// hash produces. When it is false the estimate should not be trusted and a
// full count is the only sound answer.
func (s CatalogSize) HashLooksUniform() bool {
	return s.Exact || math.Abs(s.DispersionZ) <= 3
}

func (s CatalogSize) String() string {
	if s.Exact {
		return fmt.Sprintf("%d apps in %s (exact, %d shards)", s.Apps, s.Generation.ID(), s.ShardsRead)
	}
	return fmt.Sprintf("%d apps in %s (+/- %.0f, 95%%; %d of %d shards)",
		s.Apps, s.Generation.ID(), s.HalfWidth, s.ShardsRead, s.ShardsTotal)
}

// SizeOptions configures CatalogSize.
type SizeOptions struct {
	// Precision is the relative half-width to aim for -- 0.01 for one
	// percent. Zero, or Exact, counts every shard.
	//
	// The cost is quadratic in the reciprocal, because the error of a mean
	// falls as the square root of the sample. One percent is about 900 shards
	// and ninety seconds; a tenth of a percent is about half the full sweep,
	// at which point sampling has stopped being cheaper than counting.
	Precision float64

	// Exact reads every shard. Slow and certain, and the only way to a number
	// with no interval attached.
	//
	// The gpscrape CLI deliberately has no flag for this, and the asymmetry is
	// on purpose. On a command line one character would separate a
	// ninety-second run from a four-hour one, and the exact count is already
	// reachable there through `catalog sweep`, which costs the same pass and
	// keeps the ids instead of discarding them.
	//
	// It stays in the API because a caller who genuinely wants the number
	// without the ids should not have to hand-roll it. Summing CatalogShardSeq
	// looks like four lines and contains one trap: a shard that fails to load
	// is not a shard with no apps in it, and counting it as zero undercounts
	// silently under the word "exact". This refuses instead.
	Exact bool

	// Pilot is how many shards to read before deciding how many are needed.
	// Default 200.
	//
	// The sample size depends on a spread that is not known in advance, so it
	// is measured first and the sample topped up -- Stein's two-stage
	// procedure (Stein 1945, Ann. Math. Statist. 16:243-258), which unlike
	// stopping the moment a running interval looks narrow enough has an
	// actual coverage guarantee. Re-testing after every observation and
	// stopping at the first narrow interval undercovers: the rule
	// preferentially stops when the sample happens to look tidy, so the
	// interval it reports is exactly the one that was lucky.
	Pilot int

	// Concurrency is how many shards are fetched at once. Default 8.
	Concurrency int

	// Seed fixes which shards are drawn. Zero derives one from the generation
	// id, so a repeated run reads the same shards and a new build reads fresh
	// ones. An unreproducible sample cannot be compared with anything,
	// including itself.
	Seed uint64

	// Progress, when set, is called as shards are read.
	Progress func(SizeProgress)
}

// SizeProgress reports how far a count has got.
type SizeProgress struct {
	Stage             string // "pilot", "sample" or "exact"
	ShardsRead, Total int
	Apps              int
}

// CatalogSize counts the app listings in the current sitemap generation.
//
// With Exact it reads every shard and the result carries no interval. With
// Precision it reads a pilot, works out how large a sample the measured spread
// needs, tops the sample up to that size, and returns the count with the
// interval it actually achieved -- not the one that was asked for.
func (c *Client) CatalogSize(ctx context.Context, opts SizeOptions) (CatalogSize, error) {
	if opts.Pilot <= 0 {
		opts.Pilot = 200
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = 8
	}
	if !opts.Exact && opts.Precision <= 0 {
		opts.Exact = true
	}

	gen, err := c.SitemapGeneration(ctx)
	if err != nil {
		return CatalogSize{}, fmt.Errorf("read generation: %w", err)
	}
	shards, err := c.AllSitemapShards(ctx)
	if err != nil {
		return CatalogSize{}, fmt.Errorf("list shards: %w", err)
	}
	total := len(shards)
	if total == 0 {
		return CatalogSize{}, fmt.Errorf("sitemap index listed no shards")
	}

	if opts.Exact {
		return c.exactSize(ctx, gen, shards, opts)
	}
	return c.sampledSize(ctx, gen, shards, opts)
}

func (c *Client) exactSize(ctx context.Context, gen Generation, shards []string, opts SizeOptions) (CatalogSize, error) {
	// An app is listed in exactly one shard -- checked over 63,305 ids across
	// 1,500 shards, with no id appearing twice -- so the count is a sum and
	// needs no set. Holding one would cost 225MB to remove nothing.
	var apps, read int
	for sh, err := range c.CatalogShardSeq(ctx, CatalogOptions{
		ShardURLs: shards, Concurrency: opts.Concurrency,
	}) {
		if err != nil {
			return CatalogSize{}, err
		}
		if sh.Err != nil {
			return CatalogSize{}, fmt.Errorf("shard %d: %w", sh.Index, sh.Err)
		}
		apps += len(sh.Packages)
		read++
		if opts.Progress != nil {
			opts.Progress(SizeProgress{Stage: "exact", ShardsRead: read, Total: len(shards), Apps: apps})
		}
	}
	return CatalogSize{
		Generation: gen, Apps: apps, Exact: true,
		ShardsRead: read, ShardsTotal: len(shards),
		MeanPerShard: float64(apps) / float64(max(read, 1)),
	}, nil
}

func (c *Client) sampledSize(ctx context.Context, gen Generation, shards []string, opts SizeOptions) (CatalogSize, error) {
	total := len(shards)
	seed := opts.Seed
	if seed == 0 {
		seed = gen.SampleSeed()
	}
	order := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)).Perm(total)

	pilot := min(opts.Pilot, total)
	counts, err := c.countShards(ctx, shards, order[:pilot], "pilot", total, opts)
	if err != nil {
		return CatalogSize{}, err
	}
	if len(counts) < 2 {
		return CatalogSize{}, fmt.Errorf("pilot read %d shards, need at least 2 to measure a spread", len(counts))
	}

	// Stage two: how many shards does the measured spread require? Solving
	// the finite-population variance for n gives
	//
	//	n = N z^2 cv^2 / (N e^2 + z^2 cv^2)
	//
	// where e is the target relative half-width. The target is tightened to
	// e/(1+e) so the guarantee is on the error relative to the true value
	// rather than relative to the estimate -- the two differ by exactly the
	// interval being placed.
	mean, sd := meanStdDev(counts)
	if mean <= 0 {
		return CatalogSize{}, fmt.Errorf("pilot found no apps in %d shards", len(counts))
	}
	e := opts.Precision / (1 + opts.Precision)
	cv := sd / mean
	z := 1.96
	need := int(math.Ceil(float64(total) * z * z * cv * cv / (float64(total)*e*e + z*z*cv*cv)))
	need = min(need, total)

	// Bounded below by the pilot as well as above by the catalog: counts is
	// short of pilot whenever a shard failed to load, so a need that lands
	// between the two would slice order backwards and panic.
	if need > len(counts) && need > pilot {
		more, err := c.countShards(ctx, shards, order[pilot:min(need, total)], "sample", total, opts)
		if err != nil {
			return CatalogSize{}, err
		}
		counts = append(counts, more...)
	}

	out := summarise(gen, counts, total)
	out.Target = opts.Precision
	return out, nil
}

// countShards reads the given shards and returns one app count per shard.
func (c *Client) countShards(ctx context.Context, shards []string, idx []int, stage string, total int, opts SizeOptions) ([]float64, error) {
	if len(idx) == 0 {
		return nil, nil
	}
	// Sorted so the requests walk the index in order, which is kinder to the
	// CDN and makes a run's request log readable.
	pick := slices.Clone(idx)
	slices.Sort(pick)

	var counts []float64
	var apps int
	for sh, err := range c.CatalogShardSeq(ctx, CatalogOptions{
		ShardURLs: shards, Shards: pick, Concurrency: opts.Concurrency,
	}) {
		if err != nil {
			return nil, err
		}
		if sh.Err != nil {
			// A shard that cannot be read is not a shard with no apps in it.
			// Counting it as zero would drag the estimate down silently, so
			// it is dropped from the sample instead -- which is sound because
			// which shards fail is independent of what is in them.
			continue
		}
		counts = append(counts, float64(len(sh.Packages)))
		apps += len(sh.Packages)
		if opts.Progress != nil {
			opts.Progress(SizeProgress{Stage: stage, ShardsRead: len(counts), Total: total, Apps: apps})
		}
	}
	return counts, nil
}

// summarise turns per-shard counts into an estimate with its interval.
//
// This is the Horvitz-Thompson estimator for a simple random sample of
// clusters drawn without replacement (Horvitz & Thompson 1952, JASA
// 47:663-685; Cochran, Sampling Techniques, 3rd ed., ch. 9-11):
//
//	T = (N/n) sum(T_i)          Var(T) = N^2 ((N-n)/(N-1)) s^2 / n
//
// The (N-n)/(N-1) factor is the finite population correction, and it is what
// makes a census exact rather than merely very precise: at n=N it is zero.
// Textbooks give it both ways -- (1-n/N) pairs with an n-1 divisor in s^2,
// (N-n)/(N-1) with the population form -- and they differ by N/(N-1), which
// at 83,445 shards is six parts in a million. This is the one the code
// computes; the comment used to name the other.
func summarise(gen Generation, counts []float64, total int) CatalogSize {
	n := float64(len(counts))
	N := float64(total)
	mean, sd := meanStdDev(counts)

	fpc := math.Sqrt((N - n) / (N - 1))
	halfWidth := studentT95(len(counts)-1) * sd / math.Sqrt(n) * fpc * N

	// Dispersion: under a uniform hash the counts are Poisson, so (n-1)s^2/mean
	// is chi-square with n-1 degrees of freedom. Reported as a normal deviate
	// via the standard large-df approximation.
	var dispersion, zscore float64
	if mean > 0 {
		dispersion = sd * sd / mean
		chi := (n - 1) * dispersion
		zscore = (chi - (n - 1)) / math.Sqrt(2*(n-1))
	}

	return CatalogSize{
		Generation: gen, Apps: int(math.Round(mean * N)),
		HalfWidth: halfWidth, ShardsRead: len(counts), ShardsTotal: total,
		MeanPerShard: mean, Dispersion: dispersion, DispersionZ: zscore,
	}
}

// studentT is the two-sided 95% quantile of Student's t for a given degrees of
// freedom, falling back to the normal quantile once the difference stops
// mattering.
//
// The estimator used 1.96 whatever the sample size. At the default pilot of
// 200 that is right to three decimals, but -pilot is a caller's flag with no
// floor: at n=2 the correct multiplier is 12.71, so a two-shard run reported
// an interval six times narrower than the truth -- and two such intervals,
// both labelled 95%, did not overlap.
//
// A table rather than an approximation: thirty numbers are cheaper and exact,
// and this package takes no dependencies.
func studentT95(df int) float64 {
	table := [...]float64{
		12.706, 4.303, 3.182, 2.776, 2.571, 2.447, 2.365, 2.306, 2.262, 2.228,
		2.201, 2.179, 2.160, 2.145, 2.131, 2.120, 2.110, 2.101, 2.093, 2.086,
		2.080, 2.074, 2.069, 2.064, 2.060, 2.056, 2.052, 2.048, 2.045, 2.042,
	}
	switch {
	case df < 1:
		return table[0]
	case df <= len(table):
		return table[df-1]
	default:
		// Beyond thirty the t and the normal agree to about two percent, and
		// the sample-size arithmetic is nowhere near that precise.
		return 1.96
	}
}

func meanStdDev(a []float64) (float64, float64) {
	if len(a) == 0 {
		return 0, 0
	}
	var sum float64
	for _, x := range a {
		sum += x
	}
	mean := sum / float64(len(a))
	if len(a) < 2 {
		return mean, 0
	}
	var ss float64
	for _, x := range a {
		ss += (x - mean) * (x - mean)
	}
	return mean, math.Sqrt(ss / float64(len(a)-1))
}
