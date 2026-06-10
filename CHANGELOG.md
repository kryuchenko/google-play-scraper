# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

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
