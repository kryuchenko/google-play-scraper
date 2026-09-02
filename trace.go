package googleplayscraper

import (
	"context"
	"runtime/trace"
)

// Execution tracing
//
// Every operation that issues more than one request opens a runtime/trace
// task, and the request path below it opens regions. The task id travels in
// the context, so it reaches the worker goroutines in parallelIndexed on its
// own and every request made on behalf of one call is attributable to it —
// the same property a distributed tracer gives you, without the dependency.
//
// Capture a trace from a program that uses this package:
//
//	f, _ := os.Create("scrape.trace")
//	trace.Start(f)
//	defer trace.Stop()
//	// ... call the client ...
//
// then read it with `go tool trace scrape.trace`. The Tasks view groups work
// by operation and shows the latency distribution; the per-task timeline shows
// where the time went, and how much of it was the throttle rather than Google.
//
// A full catalog sweep is far too long to trace continuously — 83k shards
// would produce a file nothing can open. Use runtime/trace.FlightRecorder
// instead: it keeps the last few seconds in a ring buffer and writes a
// snapshot when something interesting happens (a stall, a burst of 429s).
//
// All of this costs approximately nothing while tracing is off: StartRegion
// returns a no-op region and Log returns immediately. The one construct with a
// non-trivial cost even when disabled is NewTask, which derives a context, so
// tasks are opened once per public call and never in a loop.

// Trace region and task names. Kept together so the vocabulary stays
// consistent and greppable; traceRegion* values name spans within one request,
// traceTask* values name whole operations.
const (
	traceRegionThrottle = "throttle.wait"
	traceRegionHTTP     = "http.request"
	traceRegionBackoff  = "retry.backoff"

	traceTaskApp               = "App"
	traceTaskAvailability      = "Availability"
	traceTaskCategoryApps      = "CategoryApps"
	traceTaskCatalog           = "EnumerateCatalog"
	traceTaskReviewsCompr      = "ReviewsComprehensive"
	traceTaskSearch            = "Search"
	traceTaskCluster           = "Cluster"
	traceTaskClusterURLs       = "ClusterURLs"
	traceTaskSimilar           = "Similar"
	traceTaskDeveloper         = "Developer"
	traceTaskPermissions       = "Permissions"
	traceTaskDataSafety        = "DataSafety"
	traceTaskSuggest           = "Suggest"
	traceTaskCategories        = "Categories"
	traceTaskList              = "List"
	traceTaskCatalogSeq        = "CatalogSeq"
	traceTaskCatalogGeneration = "CatalogGeneration"
	traceTaskDigests           = "Digests"
	traceTaskReviewsSeq        = "ReviewsSeq"
)

// startTask opens a trace task for a top-level operation and returns the
// derived context along with the function that ends it. Call it once per
// public method, never inside a loop:
//
//	ctx, endTask := startTask(ctx, traceTaskAvailability)
//	defer endTask()
func startTask(ctx context.Context, name string) (context.Context, func()) {
	ctx, task := trace.NewTask(ctx, name)
	return ctx, task.End
}

// logTrace attaches a key/value pair to the enclosing task. The guard matters:
// callers pass values that are cheap to hand over but would not be free to
// build, and there is no reason to build them when nothing is recording.
func logTrace(ctx context.Context, category, message string) {
	if trace.IsEnabled() {
		trace.Log(ctx, category, message)
	}
}
