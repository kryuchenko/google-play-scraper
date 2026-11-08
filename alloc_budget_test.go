//go:build !race

package googleplayscraper

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/metrics"
	"testing"
)

// Allocation and memory budgets.
//
// The benchmarks in bench_test.go measure; nothing compares their numbers
// across commits, and timings on shared CI runners swing by a fifth from run
// to run. Allocation counts do not: the same call on the same toolchain
// allocates the same number of objects to within a fraction of a percent. So
// these tests hold the parsers to a ceiling on allocations, and the streaming
// paths to a ceiling on memory, which turns "it got slower" into a failing
// test without timing anything.
//
// Built without -race: the race detector instruments allocations and the
// counts it reports are not the ones a normal build makes. CI runs these in a
// step of their own.
//
// Each budget is what the call allocated when it was set, plus 10%. A change
// that saves allocations passes and logs; lower the budget then, or the next
// regression hides in the slack. A budget is per toolchain: when Go moves,
// re-measure rather than read the difference as a regression.

func readBudgetFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func TestAllocBudget(t *testing.T) {
	appPage := readBudgetFixture(t, "app_page.html")
	reviews := readBudgetFixture(t, "reviews_batch.bin")
	search := readBudgetFixture(t, "search_page.html")
	shard := append(benchShardBody(), []byte(`</urlset>`)...)
	var nested any = "leaf"
	for range 12 {
		nested = []any{nil, nil, nested, nil}
	}

	// The calls are the ones the benchmark of the same name makes.
	cases := []struct {
		name   string
		budget float64
		fn     func()
	}{
		{"ParseAppPage", 2591, func() {
			if _, err := parseAppPage(appPage, "com.example.app", "https://example"); err != nil {
				t.Fatal(err)
			}
		}},
		{"ParseReviews", 21625, func() {
			if res, err := parseReviewsResponse(reviews, "com.example.app"); err != nil || len(res.Reviews) == 0 {
				t.Fatalf("parse reviews: %v", err)
			}
		}},
		// parseSearchPage's own body, so the budget does not depend on its
		// signature.
		{"ParseSearchPage", 39937, func() {
			if _, _, err := extractSearchResults(parseDataBlocks(search)); err != nil {
				t.Fatal(err)
			}
		}},
		{"SitemapShard", 290, func() {
			if len(shardPackages(shard)) == 0 {
				t.Fatal("no packages parsed")
			}
		}},
		{"GetPath", 0, func() {
			if getPath(nested, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2) != "leaf" {
				t.Fatal("getPath")
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := testing.AllocsPerRun(20, c.fn)
			switch {
			case got > c.budget:
				t.Errorf("%.0f allocs/op, budget %.0f", got, c.budget)
			case got < 0.8*c.budget:
				t.Logf("%.0f allocs/op, well under the budget of %.0f: lower it", got, c.budget)
			default:
				t.Logf("%.0f allocs/op, budget %.0f", got, c.budget)
			}
		})
	}
}

// The streamed shard read is the reason a catalog sweep fits in memory: it
// allocates a fixed few hundred kilobytes per 8MB shard, where reading the
// whole document first allocates the document. A change that quietly buffers
// again would still pass the allocation count above; the byte count does not.
func TestShardStreamBytesBudget(t *testing.T) {
	const budget = 300 << 10
	plain := benchRealisticShard()
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write(plain)
	_ = zw.Close()
	body := gz.Bytes()

	res := testing.Benchmark(func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			zr, err := gzip.NewReader(bytes.NewReader(body))
			if err != nil {
				b.Fatal(err)
			}
			pkgs, err := shardPackagesFrom(zr)
			if err != nil || len(pkgs) == 0 {
				b.Fatalf("shard: %v", err)
			}
			_ = zr.Close()
		}
	})
	got := res.AllocedBytesPerOp()
	if got > budget {
		t.Errorf("streamed shard allocates %d B/op, budget %d", got, budget)
	} else {
		t.Logf("streamed shard allocates %d B/op, budget %d", got, budget)
	}
}

// ReviewsSeq is how a caller reads tens of thousands of reviews without
// holding them. It streams page by page; this checks that what it keeps alive
// does not grow with the number of reviews read. Every page is the same
// captured 1000-review response with its continuation token, so the sequence
// never ends on its own and the loop stops at the count.
func TestHeapCeiling(t *testing.T) {
	const (
		total   = 50_000
		warmUp  = 5_000
		ceiling = 4 << 20
	)
	page := readBudgetFixture(t, "reviews_batch_1000.bin")
	c := newMockClient(t, func(req *http.Request) (mockResponse, bool) {
		return mockResponse{Body: page}, req.Method == http.MethodPost
	})

	n := 0
	var base uint64
	for r, err := range c.ReviewsSeq(context.Background(), "com.example.app", ReviewOptions{}) {
		if err != nil {
			t.Fatalf("after %d reviews: %v", n, err)
		}
		_ = r
		n++
		if n == warmUp {
			base = liveHeap()
		}
		if n == total {
			break
		}
	}
	if n < total {
		t.Fatalf("the sequence ended after %d reviews; the fixture should paginate for ever", n)
	}
	end := liveHeap()
	growth := int64(end) - int64(base)
	if growth > ceiling {
		t.Errorf("live heap grew %d bytes from review %d to %d (%d -> %d), ceiling %d",
			growth, warmUp, total, base, end, ceiling)
	} else {
		t.Logf("live heap %d bytes at review %d, %d at %d (growth %d, ceiling %d)",
			base, warmUp, end, total, growth, ceiling)
	}
}

// liveHeap collects and reports the bytes held by live heap objects.
func liveHeap() uint64 {
	runtime.GC()
	s := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
	metrics.Read(s)
	return s[0].Value.Uint64()
}
