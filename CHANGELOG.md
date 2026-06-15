# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Internal

- CI now lints the generated OpenAPI spec with Redocly (recommended ruleset) and
  Spectral (`spectral:oas`, failing on warnings) on the stable leg, pinned for
  reproducibility, so a future change cannot reintroduce a deprecation or lint
  warning unnoticed (the Go drift test only guards the schema `example`
  keyword). Ships the `apidoc/.spectral.yaml` ruleset.

## [1.3.1] - 2026-06-15

### Internal

- The generated OpenAPI 3.1 spec (`apidoc/`) now uses the JSON Schema 2020-12
  `examples` array for schema-level examples instead of the deprecated singular
  `example` keyword that swag v2 emits. A deterministic post-processor
  (`apidoc/internal/specfix`, run by `gen.sh` and shared with the freshness
  test) rewrites every `example` under `components.schemas` to `examples`,
  leaving the still-valid Parameter Object `example` fields under `paths`
  untouched. A drift test asserts no deprecated schema `example` survives. This
  is a docs-only artifact change; the Go library API is unaffected.
- The generated spec now lints clean under both Redocly (recommended ruleset)
  and Spectral (`spectral:oas`): global `tags` and an `info.contact` were added
  to the generator, and the Redocly config was renamed to `redocly.yaml` (with a
  quoted `"off"` severity) so it is auto-discovered. No deprecation warnings
  remain.

## [1.3.0] - 2026-06-15

### Added

- Full-catalog enumeration via Google Play's public sitemaps — the one
  anonymous channel that lists the *entire* store rather than the
  commercially-visible top of it. `EnumerateCatalog(ctx, emit, CatalogOptions)`
  sweeps every shard and calls `emit` once per app package id (on the order of 3
  million). `robots.txt` advertises two sitemap indexes covering ~80,945 gzipped
  shards; each shard is a `<urlset>` of whole-store URLs (books/movies/music and
  apps interleaved) from which only the `/store/apps/details?id=` locs are kept.
  The lower-level steps are exported too — `SitemapIndexURLs`,
  `SitemapShards`/`AllSitemapShards`, and `SitemapShardPackages` — so callers can
  drive or resume the crawl themselves. `CatalogOptions` exposes `Concurrency`, a
  `Shards` subset (sampling/resume), and serialized `OnShardDone`/`OnShardError`
  callbacks. The sweep reuses the client throttle (no new `Client` field), is
  context-cancellable into a partial result, and skips a failed shard rather than
  aborting. Ships with the `examples/catalog-crawler` CLI (writes ids to a file,
  optional `-resolve` sanity check) and `apidoc` stubs for `/robots.txt`,
  `/sitemaps/sitemaps-index-{n}.xml`, and `/sitemaps/{shard}.xml.gz`. Zero new
  dependencies (`encoding/xml` + `compress/gzip`).

### Internal

- Locked the unified row decoder's byte-equivalence in CI: golden tests assert
  the full `SearchResult` for each of the four row layouts
  (cluster/list/search-grid/qnKhOb) plus the `requireAppID` and
  no-price-path edge rules, so a future path-map or candidate-priority change is
  caught offline rather than only by the live canary.

## [1.2.0] - 2026-06-13

### Added

- `ClusterOptions.FeedMode` (and `CoverageOptions.ClusterFeedMode`) selects how a
  category/cluster page's recommendation feed is followed: `FeedNone` (initial
  grid only), `FeedLightweight` (the stateless +1 page, what `FollowFeed: true`
  did — kept), or `FeedBrowser` (browser-driven deep scroll). `FollowFeed bool`
  is deprecated and maps to `FeedLightweight`.
- `FeedBrowser` reaches depth a stateless client cannot — the feed's deeper
  continuation cursor is computed by the page's JS from render state and is not
  recoverable from the network responses (verified exhaustively). It is driven
  through the optional `lightfeed` submodule, which runs **Lightpanda** (a 64 MB
  headless browser, no Chromium) over CDP: a GAME_ACTION category page yields
  ~130–149 apps vs ~77 with `FeedLightweight`. In a full `CategoryApps` sweep it
  adds ~16–24 genuinely new apps beyond the core/cluster/search phases.
- The `lightfeed` submodule keeps its `chromedp` dependency out of the root: the
  root module stays zero-dependency, and `FeedBrowser` requires the caller to
  inject a `FeedPaginator` (else `ErrFeedPaginatorRequired`) — no silent
  fallback, so the chosen strategy is always explicit.
- `Availability(ctx, appID, opts)` and `AvailableCountries(ctx, appID, opts)`:
  probe an app's region availability across many Google Play countries and
  report a per-country `Status` (`StatusAvailable`, `StatusNotInRegion`,
  `StatusNotFound`, `StatusFetchError`). Each probe is lightweight — it fetches
  the listing and reads only the availability node, skipping the full parse —
  and runs through the shared throttle and an opt-in worker pool. A single
  country's failure never aborts the sweep; only context cancellation does,
  returning the partial result. The result's `GloballyRemoved` flag is set when
  every conclusively-probed country returned 404. Ships with `AllCountries` (a
  snapshot of Play country codes) and the `examples/app-availability` CLI.
- `CategoryApps` coverage orchestrator: unions many independent slices of a
  category (collections × locales × age buckets × a search-term dictionary × a
  `Similar`/`Developer` graph walk), deduplicating by `AppID` with a saturation
  stop, to collect far more than the ~200-app single-request ceiling. A measured
  `GAME_ACTION` run reached ~1,800 unique apps. Ships with `CoverageLocales`, a
  per-category search-term dictionary, and the `examples/category-coverage` CLI.
  This maximizes coverage of the commercially visible layer of a category; it
  does not (and anonymously cannot) enumerate the full catalog.
- 15 new `App` fields parsed from the detail page: monetization (`AdSupported`,
  `OffersIAP`, `IAPRange`, `OriginalPrice`, `DiscountEndDate`), distribution
  (`IsAvailableInPlayPass`, `Preregister`, `EarlyAccessEnabled`), media
  (`Video`, `VideoImage`, `HeaderImage`, `PreviewVideo`), changelog
  (`RecentChanges`), and EU-DSA trader info (`DeveloperLegal*`).
- `WithHTTPClient`, `WithConcurrency`, and a typed `StatusError{Code}` (branch on
  404 vs 429 via `errors.As`); offline parser fixtures so `go test -short` runs
  fully without network.

### Fixed

- `App.Histogram` was decoded with a wrong index mapping: the 5-star bucket (the
  largest for most apps) was always 0 and the 1–4-star counts were reversed and
  mislabeled, so the buckets did not sum to `Ratings`. It now correctly maps the
  `[null, 1★, 2★, 3★, 4★, 5★]` node to `hist[0]=1-star .. hist[4]=5-star`;
  verified live that the buckets sum to `Ratings`.
- `Search` now picks the data array with the most app entries instead of the
  first depth-first match, fixing queries that returned a single garbage result.

### Behavior change (soft break)

- `App.Available` was hardcoded to `true` and never reflected reality. It is now
  derived from the listing's availability node (`[18][0]`): `true` only when the
  app is installable in the requested country, `false` for a region-locked
  listing or a pre-registration (unreleased) entry. Callers that read
  `App.Available` will now see `false` where they previously always saw `true` —
  the field compiles unchanged but its value is no longer a constant.

### Changed

- `App.Released` changed type from `string` to `int64`. It previously held a
  stringified float in scientific notation (e.g. `"1.401196337e+09"`); it is now
  a Unix epoch in seconds (e.g. `1401196337`), `0` when absent — symmetric with
  `App.Updated`. This is a typed break: callers that read `Released` as a string
  must update to the integer field. The JSON `released` value is now a number
  rather than an exponential string.

### Internal

- `AvailabilityResult` now serializes its `Errors` map as `country → string`
  (via `err.Error()`) instead of empty `{}` objects, making the JSON output and
  the OpenAPI `object,string` schema truthful. The Go-side `Errors` field stays
  `map[string]error`, so typed errors remain available to Go callers.
- The four RPC row parsers (cluster/list/search-grid/qnKhOb) now share one
  table-driven `decodeResultRow`, each declaring its own field→path map, so a
  Google layout shift is fixed in one place. The request throttle was reworked
  into a fair, context-cancellable token reservation. The `apidoc` submodule
  publishes an OpenAPI 3.1 spec of Google's private endpoints (validated in CI);
  PR CI runs `-short` (deterministic offline) with live drift checks in a
  scheduled canary workflow.

## [1.1.0] - 2026-06-10

### Behavior change (soft break)

- `Search` no longer returns an error for `Num` > 250; the value is clamped to
  250, matching the clamping behavior already used by `List` (660) and
  `Cluster` (5000). Callers that relied on the error to detect oversized
  requests should check their own bounds before calling. This only *removes* a
  previously returned error, so it is released as a minor version rather than a
  full `/v2` module migration; the only affected callers are those matching the
  old `"can't exceed 250"` error string.

### Added

- `WithConcurrency(n)` client option to fetch `App()` details in parallel when
  a listing is requested with `FullDetail: true`. Default is 1 (sequential), so
  existing behavior is unchanged unless opted in. Result order is preserved and
  the `WithThrottle` interval still bounds the request rate across workers.
- `WithHTTPClient(*http.Client)` client option for supplying a custom HTTP
  client (proxy pools, custom transports, tests). Must follow redirects and
  keep cookies like the default.
- `StatusError{Code int}` returned by failed requests so callers can branch on
  the HTTP status (e.g. 404 vs 429) via `errors.As`. The error string is
  unchanged (`unexpected status: %d`) for backward compatibility.
- Offline parser unit tests backed by saved Google responses in `testdata/`;
  all live-network tests are now gated behind `testing.Short()`, so
  `go test -short` runs fully offline.

### Changed

- The request throttle now reserves start slots on a monotonic schedule under a
  short lock instead of sleeping while holding the lock. Concurrent callers no
  longer serialize on a shared sleep; the minimum interval between request
  starts is enforced fairly and remains cancellable via context.
- `Search` result extraction now picks the data array with the most app entries
  instead of the first depth-first match, fixing queries that previously
  returned a single garbage result.
- Internal: the duplicated `AF_initDataCallback` parsing block is extracted into
  a single `parseDataBlocks` helper (no behavior change).
