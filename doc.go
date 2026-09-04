// Package googleplayscraper reads public Google Play Store data — app
// listings, reviews, search results, category catalogs and region
// availability — using only the standard library.
//
// There is no official public API for any of this. Everything here is built on
// the same endpoints the Play Store web client uses: server-rendered pages
// with app data embedded in AF_initDataCallback script blocks, and the
// batchexecute RPC that the client calls for pagination. Those payloads are
// undocumented positional arrays, and their shape changes without notice. The
// parsers are therefore defensive by design: a field that moves or disappears
// yields a zero value rather than an error, so one upstream change does not
// fail an entire crawl. The canary test suite (build tag "canary") runs
// against the live store on a schedule to catch that drift.
//
// # Client
//
// All operations hang off a [Client], configured with functional options and
// safe for concurrent use:
//
//	c := googleplayscraper.NewClient(
//		googleplayscraper.WithThrottle(200*time.Millisecond),
//		googleplayscraper.WithConcurrency(4),
//	)
//
//	app, err := c.App(ctx, "com.spotify.music", googleplayscraper.AppOptions{
//		Lang:    "en",
//		Country: "us",
//	})
//
// Every method takes a [context.Context] and honours cancellation. Errors that
// carry an HTTP status are reported as [StatusError], so a missing app is
// distinguishable from a transport failure:
//
//	var se *googleplayscraper.StatusError
//	if errors.As(err, &se) && se.Code == http.StatusNotFound {
//		// app does not exist in this region
//	}
//
// # Rate limiting
//
// Google throttles aggressively and the library does not retry on your behalf.
// [WithThrottle] enforces a minimum interval between the starts of consecutive
// requests, reserving slots on a monotonic schedule so that N concurrent
// callers spread out to one start per interval instead of firing together.
// Set it. The default is unthrottled, which is appropriate only for one-off
// lookups.
//
// # Beyond a single app
//
// Several operations exist because the obvious ones are capped. A category
// request returns roughly 200 apps no matter how you ask; [Client.CategoryApps]
// unions many cluster slices of the same category to get past that. The store
// exposes no complete listing at all; [Client.CatalogSeq] walks Google's
// public sitemaps instead, which yields on the order of three million package
// ids — a batch job measured in tens of gigabytes, not an interactive call.
// [Client.Availability] probes a package across the 177 countries in
// [AllCountries] to map where it is published.
//
// # Zero dependencies
//
// The root module imports nothing outside the standard library, and a test
// enforces it. Browser-driven feed pagination, which needs chromedp, lives in
// the lightfeed submodule; the OpenAPI description of Google's endpoints lives
// in apidoc. Neither is required to use this package.
package googleplayscraper
