# google-play-scraper

[![Tests](https://github.com/kryuchenko/google-play-scraper/actions/workflows/test.yml/badge.svg)](https://github.com/kryuchenko/google-play-scraper/actions/workflows/test.yml)
![Coverage](https://img.shields.io/badge/coverage-94.4%25-brightgreen)
[![Go Reference](https://pkg.go.dev/badge/github.com/kryuchenko/google-play-scraper.svg)](https://pkg.go.dev/github.com/kryuchenko/google-play-scraper)
[![Go Report Card](https://goreportcard.com/badge/github.com/kryuchenko/google-play-scraper)](https://goreportcard.com/report/github.com/kryuchenko/google-play-scraper)

Read public Google Play Store data from the shell or from Go: app details,
reviews, search, charts, permissions, data safety, per-country availability,
and the store's complete list of package ids.

One engine, two surfaces. The `gpscrape` command writes newline-delimited JSON,
so it composes with `jq` and anything else that reads a stream. The Go package
exposes the same operations with contexts, iterators and batched lookups. The
root module imports nothing outside the standard library, and a test fails if
that ever changes.

It reads the anonymous web interface of `play.google.com`, so it sees what a
browser sees. Lists and charts stop at about 200 apps and cannot be paged
deeper; the whole store is reachable only through the sitemaps. See
[Limitations](#limitations) before building on either.

Requires Go 1.25. Inspired by
[facundoolano/google-play-scraper](https://github.com/facundoolano/google-play-scraper)
(Node.js).

## Quick start: command line

```bash
go install github.com/kryuchenko/google-play-scraper/cmd/gpscrape@latest
```

```console
$ gpscrape app com.spotify.music | jq '{title, developer, score, installs}'
{
  "title": "Spotify: Music and Podcasts",
  "developer": "Spotify AB",
  "score": 4.344856,
  "installs": "1,000,000,000+"
}
```

One request, one JSON record per app, with every field the store page shows.
Paging commands write records as they arrive. SIGINT stops a run without
discarding what it has already written, and a second Ctrl-C exits at once:

```console
$ gpscrape reviews com.spotify.music -limit 2 | jq -c '{score, text}'
{"score":3,"text":"I will give these apps three star because I like it a little bit"}
{"score":5,"text":"so far so good, better than Amazon music."}
```

`gpscrape -h` lists the commands, `gpscrape <command> -h` the flags each one
adds. The [Command line](#command-line) section below walks through the common
jobs.

## Quick start: Go

```bash
go get github.com/kryuchenko/google-play-scraper
```

```go
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	gps "github.com/kryuchenko/google-play-scraper"
)

func main() {
	client := gps.NewClient(gps.WithThrottle(200 * time.Millisecond))

	app, err := client.App(context.Background(), "com.spotify.music", gps.AppOptions{})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(app.Title, app.Score, app.Installs)
	// Spotify: Music and Podcasts 4.344856 1,000,000,000+
}
```

Every method takes a `context.Context` and honours cancellation. Errors that
carry an HTTP status are `*StatusError`, so a missing app is distinguishable
from a transport failure. Set a throttle: the default is unthrottled, which
suits one-off lookups and nothing else. Signatures, option fields and
per-method caveats are on
[pkg.go.dev](https://pkg.go.dev/github.com/kryuchenko/google-play-scraper).

## What you can read

- **App details**, for one app or for many in a single request.
- **Reviews**: one page, or every review an app has, in every language.
- **Search**, autocomplete suggestions, similar apps, and a developer's apps.
- **Charts**: top free, top paid, grossing, new and movers, per category.
- **Permissions** and the **data-safety** declaration.
- **Availability** across the 177 storefront country codes.
- **The catalog**: every package id the store lists, counted in ninety seconds
  or swept in full, kept current as a snapshot, and indexed by genre.

## Command line

Every command takes `-lang`, `-country`, `-throttle`, `-concurrency` and
`-timeout`. `-adaptive` turns `-throttle` into a floor and lets the client find
its own rate. Output is one JSON record per line on stdout; progress and errors
go to stderr.

### One app, or many

```bash
gpscrape app com.spotify.music | jq .score
gpscrape app com.spotify.music com.whatsapp com.duolingo   # all three in one request
gpscrape permissions com.spotify.music com.whatsapp        # likewise
gpscrape datasafety com.spotify.music
gpscrape availability com.spotify.music -countries us,de,cn
```

Several ids ride in one `batchexecute` request, so a list of apps costs one
throttle interval rather than one per app. An id that fails in a batched run
is still one line of output, in its position — `{"appId":"…","error":"…"}` —
so the stream has one record per id asked for and a pipeline never has to
reconcile stdout with stderr.

`availability` reports one status per country by name under `statuses`:
`available`, `not_in_region`, `not_found` or `error`, with the reason for an
`error` under `errors`. `globallyRemoved` is true only when every country
answered and none of them lists the app.

### Reviews, including all of them

```bash
gpscrape reviews com.spotify.music -limit 500 | jq -r .text
gpscrape reviews com.spotify.music -sort rating -score 1 -limit 100
gpscrape reviews com.spotify.music -langs en,ru,de -limit 500   # three corpora
gpscrape reviews com.spotify.music -langs all -limit 500        # every measured corpus
```

Language does not filter reviews, it partitions them: each `hl` code is served
its own corpus and the corpora do not overlap, so reading all of an app's
reviews means reading every language. `-limit` applies per language when
`-langs` is given, and records carry the corpus they came from. There is no
country filter, because a review carries no country: `ru/kz` and `ru/us` return
the same reviews id for id.

### Search and charts

```bash
gpscrape search "podcast player" -num 50
gpscrape suggest spoti
gpscrape list -collection new_free GAME_PUZZLE      # what is new in a category
gpscrape list -collection top_free -full            # details for each app, batched
gpscrape categories -kind game                      # the genre ids list accepts
gpscrape similar com.spotify.music
gpscrape developer "Spotify AB"
```

Google returns roughly one page for any of these, about 200 apps at most, and
rejects the continuation token for stateless clients. Breadth comes from
multiplying sources (categories, collections, locales, the similar graph), not
from paging deeper.

### The catalog

`catalog` is a group of verbs. The right-hand column is what each costs in
requests against Google:

| Verb | What it does | Requests |
| --- | --- | --- |
| `check` | has Google republished the catalog? | 2 |
| `new` | apps the store lists as recently published | 17 |
| `size` | how many apps there are, give or take 1% | ~900 |
| `genres` | genre changes and removals, from a snapshot | ~3.4k, plus ~310 per `-confirm-gone` storefront |
| `sweep` | the complete id list, and the exact count | 83k |
| `apps` | the id list by genre, from what is on disk | none |
| `diff` | compare two snapshots already on disk | none |
| `ids` | stream ids without keeping any state | as many as you ask for |

```console
$ gpscrape catalog check -dir ./catalog
{"generation":"2026-08-23_1787500934","built":"2026-08-23T16:02:14Z","ageHours":287,"shards":83445,"upToDate":false}
$ gpscrape catalog size | jq -c '{apps, halfWidth, shardsRead, shardsTotal, metTarget}'
{"apps":3549110,"halfWidth":35366,"shardsRead":866,"shardsTotal":83445,"metTarget":true}
```

That is 3,549,110 apps give or take 35,366 at 95% confidence, from 866 of
83,445 shards, in about ninety seconds. The record also carries the dispersion
check that justifies the interval; the exact count costs a full sweep.

A full sweep is ~83k requests, about four and a half hours at the default
throttle, so keep a snapshot directory and let the tool decide when a sweep is
due. Google republishes its sitemaps every few days, not daily, and shard names
carry the generation that produced them:

```bash
gpscrape catalog sweep -dir ./catalog            # sweeps only if a new generation exists
gpscrape catalog sweep -dir ./catalog -keep 3    # and keeps the three newest snapshots
gpscrape catalog diff -dir ./catalog -changes    # one record per id added or removed
```

Within a generation shards are immutable, so an interrupted sweep resumes from
the shards it has not reached. Each completed sweep writes a sorted snapshot,
a manifest with the generation id and a checksum, and a delta against the
previous snapshot, and prints the manifest as its one output record.

A snapshot directory takes one writer at a time. `sweep` and `genres` hold a
`sweep.lock` in it while they run, a second run against the same directory is
refused, and a lock left behind by a process that has died is cleared on the
next run. `check`, `new`, `size`, `apps`, `diff` and `ids` do not take it;
`new` only appends to its own signal log.

The genre table turns a snapshot into an app list by category. Build it once,
then read it with no network at all:

```bash
gpscrape catalog genres -dir ./catalog                        # ~3.4k requests, plus ~310 per -confirm-gone storefront
gpscrape catalog apps -dir ./catalog -genre 'GAME_*' -ids-only > games.txt
gpscrape catalog apps -dir ./catalog -genre GAME_PUZZLE
```

`genres` asks a thousand ids per request and re-checks apparent removals in
other storefronts before calling an app gone, because an app can be listed in
Brazil and invisible from the United States. `-confirm-gone` names those
storefronts. A lookup that fails in that confirm pass is reported as an
`error`, not a removal, and the row is kept. `-prune` drops rows for ids the
snapshot no longer lists.

Without a snapshot, `catalog ids` streams ids straight from the sitemaps:

```bash
gpscrape catalog ids -shards 0-99 -ids-only > ids.txt
```

### Debugging a run

```bash
gpscrape reviews com.spotify.music -limit 50 -debug              # every request, to stderr
GPSCRAPE_DEBUG=1 gpscrape catalog check                          # the same, for a script
gpscrape catalog sweep -dir ./catalog -debug -log-file sweep.log -trace sweep.trace
```

`-debug` writes one line per request, response, retry and throttle change,
and the connection timings behind each request. The first line names the
build and the settings the run used. Results stay on stdout, so a pipeline
is unaffected:

```text
level=DEBUG msg=request method=GET url="https://play.google.com/store/apps/details?id=com.spotify.music&hl=en&gl=us" attempt=1 waited=0s
level=DEBUG-4 msg=connection dns=24.077208ms connect=19.079541ms tls=54.277042ms reused=false addr=216.58.204.174:443 ttfb=242.611416ms
level=DEBUG msg=response method=GET url="https://play.google.com/store/apps/details?id=com.spotify.music&hl=en&gl=us" attempt=1 status=200 took=242.692542ms bytes=1254486
```

Bodies and headers are never logged. `-trace` keeps a Go execution trace of
the last minutes of the run in memory and writes it at exit, after Ctrl-C
too; `go tool trace sweep.trace` then shows where the time went, and how much
of it was the throttle rather than Google.

Two things need no flag. The transport is Go's default, so
`HTTPS_PROXY=http://127.0.0.1:8080` routes every request through mitmproxy,
and `GODEBUG=http2debug=1` prints the HTTP/2 frames.

## Go package

| Method | Returns |
| --- | --- |
| `App` / `AppsMany` | full details for one app / for many, batched |
| `Search` | search results, optionally price-filtered and detail-enriched |
| `Reviews` / `ReviewsSeq` | one page of reviews / an iterator that keeps paginating |
| `Developer` | a developer's apps, by name or numeric id |
| `Similar` | apps Google lists as similar |
| `Permissions` / `PermissionsMany` | requested permissions with their group label |
| `DataSafety` | the data-safety declaration and privacy policy URL |
| `Suggest` / `SuggestMany` | search autocomplete terms |
| `List` | a store collection for a category: top free/paid/grossing, newest, movers |
| `ClusterURLs` / `Cluster` | the clusters on a category page, and one cluster's apps |
| `Categories` | the 54 store categories (a static list; no request) |
| `Availability` / `AvailableCountries` | per-country status across the 177 codes in `AllCountries` |
| `CategoryApps` | many slices of one category, unioned and deduplicated |
| `CatalogSeq` / `CatalogSize` | every app id in the store via the sitemaps / the estimated count |
| `CatalogShardSeq` | the same sweep with the shard as its unit, so a run can resume from the URLs it did not reach |
| `DigestsSeq` | a few fields (genre, and whether the app is listed) for very many apps, a thousand per request |
| `SitemapGeneration` / `Generation` | which build of the catalog Google is serving, in two requests, with its build time and ordering |
| `ReviewLanguages` | the 71 language codes that each serve their own review corpus (a static list; no request) |
| `CurrentThrottle` | the interval between requests the client is using now, which is what the adaptive controller moves |

Most option structs take `Lang` (ISO 639-1) and `Country` (ISO 3166-1
alpha-2), defaulting to `en`/`us`.

Client options: `WithThrottle`, `WithConcurrency`, `WithTimeout`,
`WithUserAgent`, `WithHTTPClient`, `WithRetry`, `WithAdaptiveThrottle`,
`WithHooks`. Every request goes through one path: cache seam, retry with full
jitter, throttle, HTTP.

### Batched lookups

`AppsMany`, `PermissionsMany` and `SuggestMany` pack up to 32 lookups into one
request, and the packs go out over `WithConcurrency` workers. The throttle
meters requests rather than lookups, so this is the difference between 32
intervals and one: 32 apps took 9.76s over 32 page
fetches and 146ms over one batched request, at about 20KB per app instead of a
megabyte of markup.

```go
for _, r := range client.AppsMany(ctx, appIDs, gps.AppOptions{}) {
	if r.Err != nil {
		log.Printf("%s: %v", r.AppID, r.Err)
		continue
	}
	fmt.Println(r.App.Title, r.App.Score)
}
```

Results are positional, `out[i]` describes `appIDs[i]` whatever order Google
answers in, and each carries its own error, so a failed chunk does not cost the
rest of the run.

### Iterators

`ReviewsSeq` and `CatalogSeq` are `iter.Seq2` values, so a caller stops where
it wants to instead of waiting for a sweep to finish:

```go
for pkg, err := range client.CatalogSeq(ctx, opts) {
	if err != nil {
		return err
	}
	if seen(pkg) {
		break
	}
}
```

`ReviewsAll` and `EnumerateCatalog` are deprecated as of 1.4.0 and will be
removed in v2.

### Logging and tracing

```go
logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: gps.LevelTrace}))
client := gps.NewClient(gps.WithThrottle(200*time.Millisecond), gps.WithLogger(logger))
```

The client is silent by default and never logs above Debug: requests,
responses, retries, throttle waits and adaptive interval changes. `LevelTrace`
adds the connection timings behind each request. For anything else,
`WithHooks` gives callbacks for a metrics registry, `WithHTTPClient` accepts
an instrumented transport such as `otelhttp.NewTransport`, and every
operation that makes a request opens a `runtime/trace` task, so
`go tool trace` attributes each request to the call that made it.

## Limitations

- **Lists and charts cap at ~200 apps** per category and collection. The
  continuation-token pagination behind `List` and `Search` is rejected for
  stateless clients. This is server-side; the Node and Python libraries hit
  the same wall. `CategoryApps` multiplies sources instead, and a single
  `GAME_ACTION` run reached ~1,800 unique apps, but an app that never charts,
  never ranks in search and appears in no similarity graph is not reachable
  this way.
- **The whole store is reachable only through the sitemaps**, and they carry
  package ids and nothing else: no titles, no ratings. Pair a sweep with
  `AppsMany` to resolve the ids you care about.
- **Rate limits are real and asymmetric.** The sitemap CDN sustained 32
  requests per second for 90 seconds with no failures. The details endpoint
  tolerated a short burst but not sustained load, and an availability sweep
  across 177 countries lost several to fetch errors. Be conservative, or use
  `WithAdaptiveThrottle` and let the client find the rate.
- **Payload shapes change without notice.** Parsers are defensive by design: a
  field that moves yields a zero value rather than failing a crawl. A canary
  suite (build tag `canary`) runs against the live store to catch drift.
- **No authenticated API.** Real pagination and bulk detail lookups exist on
  Google Play's mobile protobuf API, which needs a Google account token, a
  device check-in and ongoing maintenance of reverse-engineered schemas. This
  library does not implement it. The reference implementation is
  [AuroraOSS/gplayapi](https://gitlab.com/AuroraOSS/gplayapi).

## Submodules

- [`apidoc/`](apidoc/): an auto-generated OpenAPI 3.1 description of the
  private, undocumented Google Play endpoints this library calls.
- [`lightfeed/`](lightfeed/): `FeedBrowser` support for `Cluster`, driving
  [Lightpanda](https://lightpanda.io) over CDP to scroll a category page past
  the depth a stateless client can reach. `Cluster` returns
  `ErrFeedPaginatorRequired` unless you inject the paginator, so the
  dependency is always explicit. `FeedMode: FeedLightweight` is the middle
  step and lives in the root module: it replays the page's own stateless feed
  tokens, one extra request per topic, and takes `GAME_ACTION` from ~18 apps
  to ~77 where the browser reaches ~149.

Both are separate Go modules; the root library stays stdlib-only.

## Documentation

- [API reference](https://pkg.go.dev/github.com/kryuchenko/google-play-scraper)
- [CHANGELOG.md](CHANGELOG.md): what each release changed, and what was
  measured and rejected along the way
- Runnable programs in [`examples/`](examples/): `app-availability`,
  `catalog-crawler`, `category-coverage`, `verify-game-genre`,
  `yandex-taxi-reviews`
- [CONTRIBUTING.md](CONTRIBUTING.md) for layout, tests and the release process
- [SECURITY.md](SECURITY.md) for reporting a vulnerability

## Development

```bash
GOWORK=off go test -short -race ./...                  # the library, offline; what CI runs
go test -tags canary -run TestCanary -v -timeout 15m ./...   # live contract tests, slow
```

## License

MIT
