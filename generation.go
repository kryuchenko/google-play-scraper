package googleplayscraper

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"regexp"
	"strconv"
	"time"
)

// Generation identifies one publication of Google's sitemap.
//
// The catalog is not served as a stream of changes but as a set of files
// rebuilt from scratch every so often, and the filename carries which build it
// belongs to:
//
//	play_sitemaps_2026-08-23_1787500934-00000-of-83445.xml.gz
//	              ^date      ^run        ^index  ^count
//
// Within a generation the shards never change, so noticing that the generation
// has not moved is enough to know the catalog has not been republished -- and
// that costs two requests instead of eighty-three thousand.
//
// The shards are hash-partitioned rather than appended, verified two ways: any
// shard spans nearly the whole alphabet with the same app-to-book ratio as any
// other, and release dates in the first and last shard have the same
// distribution. A new app therefore lands in an arbitrary shard, so when the
// generation does roll there is no tail to fetch -- only a full sweep finds
// what was added.
type Generation struct {
	Date   string `json:"date"`   // "2026-08-23", as it appears in the filename
	Run    string `json:"run"`    // the run id every shard of the build shares
	Shards int    `json:"shards"` // the -of-NNNNN count
}

// generationRe matches the shard filename Google publishes.
var generationRe = regexp.MustCompile(`play_sitemaps_(\d{4}-\d{2}-\d{2})_(\d+)-(\d+)-of-(\d+)\.xml`)

// ParseGeneration reads the generation out of a shard URL.
func ParseGeneration(shardURL string) (Generation, error) {
	m := generationRe.FindStringSubmatch(shardURL)
	if m == nil {
		return Generation{}, fmt.Errorf("not a sitemap shard URL: %s", shardURL)
	}
	shards, err := strconv.Atoi(m[4])
	if err != nil {
		return Generation{}, fmt.Errorf("shard count in %s: %w", shardURL, err)
	}
	return Generation{Date: m[1], Run: m[2], Shards: shards}, nil
}

// GenerationOf reads the generation from a list of shard URLs and insists they
// agree.
//
// Disagreement means the URLs were collected while Google was republishing, so
// the list is half of one build and half of another. Sweeping that produces a
// catalog that never existed at any moment, which is worse than an error and
// impossible to notice afterwards.
func GenerationOf(shardURLs []string) (Generation, error) {
	if len(shardURLs) == 0 {
		return Generation{}, fmt.Errorf("no shard URLs")
	}
	first, err := ParseGeneration(shardURLs[0])
	if err != nil {
		return Generation{}, err
	}
	for _, u := range shardURLs[1:] {
		g, err := ParseGeneration(u)
		if err != nil {
			return Generation{}, err
		}
		if g.ID() != first.ID() {
			return Generation{}, fmt.Errorf(
				"shard list spans two generations (%s and %s); it was read while Google was republishing",
				first, g)
		}
	}
	return first, nil
}

// ID is the generation's identity: date and run together.
func (g Generation) ID() string { return g.Date + "_" + g.Run }

func (g Generation) String() string {
	return fmt.Sprintf("%s (run %s, %d shards)", g.Date, g.Run, g.Shards)
}

// Compare orders two generations, oldest first. It reports a negative number
// when g is older than other, zero when they are the same build, and a
// positive number when g is newer.
//
// The run id is compared numerically, not lexicographically: it grows without
// padding, so "1787500934" and "999999999" sort the wrong way as text.
func (g Generation) Compare(other Generation) int {
	if c := compareString(g.Date, other.Date); c != 0 {
		return c
	}
	a, aerr := strconv.ParseInt(g.Run, 10, 64)
	b, berr := strconv.ParseInt(other.Run, 10, 64)
	if aerr == nil && berr == nil {
		switch {
		case a < b:
			return -1
		case a > b:
			return 1
		}
		return 0
	}
	return compareString(g.Run, other.Run)
}

func compareString(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// Built reports when Google assembled this generation, and whether that could
// be determined.
//
// The run id has so far been a Unix timestamp -- run 1787500934 is
// 2026-08-23 16:02:14 UTC, matching both the date in the same filename and the
// Last-Modified of its shards. That is an observation about an undocumented
// scheme, not a guarantee, so a run that is not a plausible timestamp reports
// false rather than quietly becoming a moment in 1970.
func (g Generation) Built() (time.Time, bool) {
	secs, err := strconv.ParseInt(g.Run, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	t := time.Unix(secs, 0).UTC()
	if t.Year() < 2010 || t.Year() > 2100 {
		return time.Time{}, false
	}
	return t, true
}

// SitemapGeneration reports the generation Google is currently publishing.
//
// Two requests: robots.txt for the index URLs, then the first index for a
// shard name to read the generation out of. It does not read the second index,
// because both belong to the same build and one shard name is enough.
func (c *Client) SitemapGeneration(ctx context.Context) (Generation, error) {
	ctx, endTask := startTask(ctx, traceTaskCatalogGeneration)
	defer endTask()

	indexes, err := c.SitemapIndexURLs(ctx)
	if err != nil {
		return Generation{}, err
	}
	if len(indexes) == 0 {
		return Generation{}, fmt.Errorf("robots.txt advertises no sitemap index")
	}
	shards, err := c.SitemapShards(ctx, indexes[0])
	if err != nil {
		return Generation{}, err
	}
	// Checking that every shard in the index agrees costs nothing here -- the
	// list is already in memory -- and catches a roll observed mid-index.
	return GenerationOf(shards)
}

// SampleSeed derives a deterministic seed from the build id.
//
// A sample that cannot be reproduced cannot be compared with anything,
// including itself: re-running it would draw different shards and any
// difference in the answer would be indistinguishable from a change in the
// catalog. Deriving the seed from the generation gives both properties at
// once -- the same build always draws the same shards, and a new build draws
// fresh ones rather than inheriting a partition that has stopped meaning
// anything.
func (g Generation) SampleSeed() uint64 {
	h := sha256.Sum256([]byte(g.ID()))
	return binary.LittleEndian.Uint64(h[:8])
}
