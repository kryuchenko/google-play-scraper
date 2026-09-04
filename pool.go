package googleplayscraper

import (
	"context"
	"sync"
)

// parallelIndexed runs fn for every index i in [0, n) using a pool of at most
// workers goroutines, and returns once all dispatched work is done.
//
// Contract:
//   - fn(ctx, i) does the work for index i. It must be safe to call concurrently
//     for distinct i; the helper never calls it twice for the same i. Any
//     fan-in (writing a shared slice by index, recording under a mutex) is the
//     caller's responsibility, but is race-free as long as each i owns its slot.
//   - workers is clamped to [1, n]. With an effective worker count of 1 the work
//     runs sequentially on the calling goroutine — no goroutines are spawned —
//     which both callers rely on for their conc<=1 fast path.
//   - Cancellation is cooperative: once ctx is done, no further indices are
//     dispatched (already-running fn calls finish). The first index skipped due
//     to cancellation is *not* run. parallelIndexed returns ctx.Err() if ctx was
//     observed done before all indices were dispatched, and nil otherwise.
//
// fn itself returns no error: per-item failure handling (e.g. keeping an
// un-enriched fallback, recording a StatusFetchError) is encapsulated inside fn.
// The only error parallelIndexed surfaces is context cancellation, which lets a
// caller distinguish a complete sweep from a truncated one.
func parallelIndexed(ctx context.Context, n, workers int, fn func(ctx context.Context, i int)) error {
	if n <= 0 {
		return nil
	}
	if workers < 1 {
		workers = 1
	}
	if workers > n {
		workers = n
	}

	if workers == 1 {
		for i := range n {
			if err := ctx.Err(); err != nil {
				return err
			}
			fn(ctx, i)
		}
		return nil
	}

	indexes := make(chan int)
	var (
		wg       sync.WaitGroup
		once     sync.Once
		canceled error
	)

	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for i := range indexes {
				if err := ctx.Err(); err != nil {
					once.Do(func() { canceled = err })
					continue
				}
				fn(ctx, i)
			}
		}()
	}
	for i := range n {
		indexes <- i
	}
	close(indexes)
	wg.Wait()
	return canceled
}
