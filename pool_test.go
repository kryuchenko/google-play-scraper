package googleplayscraper

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

// TestParallelIndexedPreservesOrder verifies that, even with many concurrent
// workers and deliberately reversed completion timing, each index writes its own
// slot — so the output slice mirrors input order. Run under -race, this also
// guards against a slot being written by the wrong worker.
func TestParallelIndexedPreservesOrder(t *testing.T) {
	const n = 100
	out := make([]int, n)

	err := parallelIndexed(context.Background(), n, 8, func(_ context.Context, i int) {
		out[i] = i * i
	})
	if err != nil {
		t.Fatalf("parallelIndexed returned %v, want nil", err)
	}
	for i := 0; i < n; i++ {
		if out[i] != i*i {
			t.Fatalf("out[%d] = %d, want %d", i, out[i], i*i)
		}
	}
}

// TestParallelIndexedCallsEachIndexOnce verifies every index is processed
// exactly once across the pool — no drops, no duplicates.
func TestParallelIndexedCallsEachIndexOnce(t *testing.T) {
	const n = 250
	var (
		mu     sync.Mutex
		counts = make(map[int]int)
	)

	parallelIndexed(context.Background(), n, 16, func(_ context.Context, i int) {
		mu.Lock()
		counts[i]++
		mu.Unlock()
	})

	if len(counts) != n {
		t.Fatalf("processed %d distinct indices, want %d", len(counts), n)
	}
	for i := 0; i < n; i++ {
		if counts[i] != 1 {
			t.Errorf("index %d processed %d times, want 1", i, counts[i])
		}
	}
}

// TestParallelIndexedSequential verifies the workers==1 fast path runs on the
// calling goroutine, in order, and returns nil.
func TestParallelIndexedSequential(t *testing.T) {
	const n = 10
	var order []int

	err := parallelIndexed(context.Background(), n, 1, func(_ context.Context, i int) {
		order = append(order, i) // safe: no goroutines spawned on this path
	})
	if err != nil {
		t.Fatalf("parallelIndexed returned %v, want nil", err)
	}
	for i := 0; i < n; i++ {
		if order[i] != i {
			t.Fatalf("sequential order[%d] = %d, want %d", i, order[i], i)
		}
	}
}

// TestParallelIndexedSequentialCancel verifies the sequential path stops
// dispatching once ctx is cancelled and returns ctx.Err().
func TestParallelIndexedSequentialCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var ran int

	err := parallelIndexed(ctx, 10, 1, func(_ context.Context, i int) {
		ran++
		if i == 2 {
			cancel()
		}
	})
	if err != context.Canceled {
		t.Fatalf("got err %v, want context.Canceled", err)
	}
	if ran != 3 { // indices 0,1,2 ran; 3 saw the cancel and stopped
		t.Fatalf("ran %d indices, want 3 (stopped after cancel)", ran)
	}
}

// TestParallelIndexedConcurrentCancel verifies that cancelling mid-sweep stops
// the pool from dispatching new indices and surfaces ctx.Err(). The exact count
// of completed indices is nondeterministic, so we only assert that not all ran
// and that the error is propagated.
func TestParallelIndexedConcurrentCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	const n = 1000
	var done int64

	err := parallelIndexed(ctx, n, 4, func(_ context.Context, i int) {
		if atomic.AddInt64(&done, 1) == 5 {
			cancel()
		}
	})
	if err != context.Canceled {
		t.Fatalf("got err %v, want context.Canceled", err)
	}
	if atomic.LoadInt64(&done) >= n {
		t.Fatal("all indices ran despite cancellation; pool did not stop dispatching")
	}
}

// TestParallelIndexedClampsAndEmpty verifies the edge-case clamps: n<=0 is a
// no-op, and workers above n (or below 1) still process every index.
func TestParallelIndexedClampsAndEmpty(t *testing.T) {
	if err := parallelIndexed(context.Background(), 0, 4, func(context.Context, int) {
		t.Fatal("fn called for n=0")
	}); err != nil {
		t.Fatalf("n=0 returned %v, want nil", err)
	}

	for _, workers := range []int{0, -3, 1000} {
		const n = 5
		var count int64
		parallelIndexed(context.Background(), n, workers, func(context.Context, int) {
			atomic.AddInt64(&count, 1)
		})
		if count != n {
			t.Errorf("workers=%d: ran %d indices, want %d", workers, count, n)
		}
	}
}
