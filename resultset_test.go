package googleplayscraper

import (
	"reflect"
	"testing"
)

func TestResultSetAddBatchDedup(t *testing.T) {
	rs := newResultSet()

	first := rs.addBatch("a", []SearchResult{
		{AppID: "com.a", Title: "A"},
		{AppID: "com.b", Title: "B"},
		{AppID: ""}, // skipped: no AppID
	})
	if first != 2 {
		t.Fatalf("first batch new count = %d, want 2", first)
	}

	// Overlapping batch: only com.c is new.
	second := rs.addBatch("b", []SearchResult{
		{AppID: "com.a", Title: "A"},
		{AppID: "com.c", Title: "C"},
	})
	if second != 1 {
		t.Fatalf("second batch new count = %d, want 1", second)
	}

	if got := rs.len(); got != 3 {
		t.Fatalf("total unique = %d, want 3", got)
	}

	per := rs.perSourceSnapshot()
	if per["a"] != 2 || per["b"] != 1 {
		t.Fatalf("perSource = %v, want a:2 b:1", per)
	}
}

func TestResultSetMergeFillsEmptyKeepsNonEmpty(t *testing.T) {
	rs := newResultSet()

	// Search-style result: Free defaulted true, no price/summary/currency.
	rs.addBatch("search", []SearchResult{
		{AppID: "com.app", Title: "App", Free: true},
	})
	// List-style result: supplies price, currency, summary; different Title
	// must NOT overwrite the existing one.
	rs.addBatch("list", []SearchResult{
		{
			AppID:    "com.app",
			Title:    "Different Title",
			Summary:  "A great app",
			Currency: "USD",
			Price:    2.99,
			Free:     false,
		},
	})

	got := rs.byID["com.app"]
	if got.Title != "App" {
		t.Errorf("Title overwritten: got %q, want %q", got.Title, "App")
	}
	if got.Summary != "A great app" {
		t.Errorf("Summary not filled: got %q", got.Summary)
	}
	if got.Currency != "USD" {
		t.Errorf("Currency not filled: got %q", got.Currency)
	}
	if got.Price != 2.99 {
		t.Errorf("Price not filled: got %v", got.Price)
	}
	if got.Free {
		t.Error("Free should flip to false once a concrete paid price arrives")
	}
}

func TestResultSetMergeDoesNotRevertPaidToFree(t *testing.T) {
	rs := newResultSet()

	// Authoritative paid record arrives first.
	rs.addBatch("list", []SearchResult{
		{AppID: "com.paid", Title: "Paid", Price: 4.99, Free: false},
	})
	// A later listing source stamps the default Free=true with no price; the
	// known paid status must survive.
	rs.addBatch("search", []SearchResult{
		{AppID: "com.paid", Free: true},
	})

	got := rs.byID["com.paid"]
	if got.Free {
		t.Error("paid app reverted to Free=true after a default-free source")
	}
	if got.Price != 4.99 {
		t.Errorf("Price changed: got %v, want 4.99", got.Price)
	}
}

func TestResultSetSortedResultsDeterministic(t *testing.T) {
	rs := newResultSet()
	rs.addBatch("s", []SearchResult{
		{AppID: "com.zebra"},
		{AppID: "com.alpha"},
		{AppID: "com.mango"},
	})

	got := rs.sortedResults()
	want := []string{"com.alpha", "com.mango", "com.zebra"}
	gotIDs := make([]string, len(got))
	for i, r := range got {
		gotIDs[i] = r.AppID
	}
	if !reflect.DeepEqual(gotIDs, want) {
		t.Errorf("sortedResults order = %v, want %v", gotIDs, want)
	}
}

// TestCoverageSaturation exercises the sliding-window saturation latch on
// synthetic batches, with no network involved.
func TestCoverageSaturation(t *testing.T) {
	run := &coverageRun{
		opts: CoverageOptions{
			SaturationWindow:    3,
			SaturationThreshold: 0.1,
		},
		results: newResultSet(),
	}

	// Three productive sources (ratio 1.0): far above threshold, not saturated.
	for range 3 {
		run.satSamples = append(run.satSamples, 1.0)
	}
	run.checkSaturation()
	if run.saturated {
		t.Fatal("should not be saturated while window is full of new apps")
	}

	// Three barren sources (ratio 0.0) push the window average below threshold.
	for range 3 {
		run.satSamples = append(run.satSamples, 0.0)
	}
	run.checkSaturation()
	if !run.saturated {
		t.Fatal("should be saturated after a full window below threshold")
	}
}

func TestCoverageSaturationNeedsFullWindow(t *testing.T) {
	run := &coverageRun{
		opts: CoverageOptions{
			SaturationWindow:    4,
			SaturationThreshold: 0.5,
		},
		results: newResultSet(),
	}
	// Only two samples, both zero: window not yet full, must not latch.
	run.satSamples = []float64{0.0, 0.0}
	run.checkSaturation()
	if run.saturated {
		t.Fatal("saturation must not trigger before the window is full")
	}
}

func TestTermQueueDedupAndGuard(t *testing.T) {
	q := newTermQueue([]string{"Shooter", "shooter", " FPS ", "fps"})

	var got []string
	for {
		t, ok := q.next()
		if !ok {
			break
		}
		got = append(got, t)
	}
	// Case/whitespace-insensitive dedup keeps the first spelling of each term.
	want := []string{"Shooter", " FPS "}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("queue order = %v, want %v", got, want)
	}
}
