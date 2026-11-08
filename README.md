# google-play-scraper

[![Tests](https://github.com/kryuchenko/google-play-scraper/actions/workflows/test.yml/badge.svg)](https://github.com/kryuchenko/google-play-scraper/actions/workflows/test.yml)
![Coverage](https://img.shields.io/badge/coverage-93.5%25-brightgreen)
[![Go Reference](https://pkg.go.dev/badge/github.com/kryuchenko/google-play-scraper.svg)](https://pkg.go.dev/github.com/kryuchenko/google-play-scraper)

Read public Google Play Store data from the shell or from Go: app details,
reviews, search, charts, permissions, data safety, per-country availability,
and the store's complete list of package ids.

The `gpscrape` command writes newline-delimited JSON for `jq` and other stream
readers. The Go package exposes the same operations with contexts, iterators
and batched lookups; its root module imports only the standard library, and a
test enforces that.

It reads the anonymous web interface of `play.google.com`, so it sees what a
browser sees. Lists and charts stop at about 200 apps; the whole store is
reachable only through the sitemaps. Read [Limitations](#limitations) before
building on either.

Requires Go 1.27. Inspired by
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
  "score": 4.35,
  "installs": "1,000,000,000+"
}
```

Each app is one JSON record in snake_case, scores rounded to two places;
`-raw` writes the library's own record instead, every field it parses. Paging
commands write records as they arrive; Ctrl-C stops a run and keeps what it
has written, and a second Ctrl-C exits at once:

```console
$ gpscrape reviews com.spotify.music -limit 2 | jq -c '{score, untrusted_text}'
{"score":3,"untrusted_text":"I will give these apps three star because I like it a little bit"}
{"score":5,"untrusted_text":"so far so good, better than Amazon music."}
```

`gpscrape -h` lists the commands, `gpscrape <command> -h` the flags each one
adds. [Command line](#command-line) covers the common jobs.

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
carry an HTTP status are `*StatusError`, so you can tell a missing app from a
transport failure. Set a throttle: the client is unthrottled by default, which
suits only one-off lookups. Signatures, option fields and per-method caveats
are on [pkg.go.dev](https://pkg.go.dev/github.com/kryuchenko/google-play-scraper).

## What you can read

- **App details**, for one app or for many in a single request.
- **Reviews**: one page, or every review an app has, in every language.
- **Search**, autocomplete suggestions, similar apps, and a developer's apps.
- **Charts**: top free, top paid, grossing, new and movers, per category.
- **Permissions** and the **data-safety** declaration.
- **Availability** across the 177 storefront country codes.
- **Google Play Games on PC**: whether a game has a Windows build, and the
  screenshots, trailer and rating of its PC listing.
- **The catalog**: every package id the store lists, counted in ninety seconds
  or swept in full, kept current as a snapshot, and indexed by genre.

## Command line

Commands that make requests take `-lang`, `-country`, `-throttle`,
`-concurrency` and `-timeout`. `-adaptive` turns `-throttle` into a floor and
lets the client find its own rate. Records go to stdout, one JSON object per
line; progress and errors go to stderr.

`app`, `search`, `list`, `reviews`, `similar`, `developer`, `permissions`,
`datasafety` and `availability` write the records of package
[`records`](https://pkg.go.dev/github.com/kryuchenko/google-play-scraper/records):
snake_case fields, third-party text in `untrusted_*`. The [MCP server](mcp/)
returns the same objects, so one jq filter works on both. `-raw` writes the
library's own types with every field instead, `-fields app_id,score` keeps only
the keys you name, and `gpscrape schema <command>` lists a command's flags and
fields as JSON.

### Errors and exit codes

When stderr is not a terminal, a failed run writes
`{"error":{"code","message","retryable"}}` to it (`GPSCRAPE_ERRORS=json|text`
overrides); the codes are the MCP server's. Exit status: 2 bad arguments or a
missing confirmation, 3 not found, 4 rate limited, 5 timeout, 130 interrupted,
1 otherwise. `catalog sweep` and an unlimited `catalog ids` need `-yes` when
no terminal is attached.

### Running inside Claude Code's sandbox on macOS

The sandbox blocks the system trust service, so Go cannot verify TLS and every
request fails with `x509: OSStatus -26276`; the error carries a `hint` with the
fix. Set `"sandbox": {"enableWeakerNetworkIsolation": true}` (with
`play.google.com` an allowed domain), or add `"gpscrape *"` to
`sandbox.excludedCommands`.

### One app, or many

```bash
gpscrape app com.spotify.music | jq .score
gpscrape app com.spotify.music com.whatsapp com.duolingo   # all three in one request
gpscrape permissions com.spotify.music com.whatsapp        # likewise
gpscrape datasafety com.spotify.music
gpscrape availability com.spotify.music -countries us,de,jp
```

Several ids ride in one `batchexecute` request, so a list of apps costs one
throttle interval, not one per app. In a batch, an id that fails is still a
line of output in its position, `{"app_id":"…","code":"not_found","error":"…"}`
(`-fields` keeps `code` and `error` on it), so stdout holds exactly one record
per id and a pipeline never needs stderr to line them up. A single id that
fails writes nothing to stdout; the error and the exit code say why. Ids are
checked before any request: a name where a package id belongs is
`invalid_argument`.

`availability` lists the countries under `available`, `region_locked` and
`not_listed`, counts the conclusive answers in `checked`, and gives the reason
for any failed probe under `errors`. `-countries` takes Play Store country
codes only (there is no store in `cn`, for example). A run in which every
country failed exits with their error code, not 0. With `-raw` the record is
the library's: a status per country under `statuses`, and `globallyRemoved`.

### Google Play Games on PC

```bash
gpscrape pcapp com.mytona.cookingdiary.android | jq '{onPC, title, youTubeId, screenshots}'
gpscrape pcapp com.mytona.cookingdiary.android com.whatsapp -lang de
```

`pcapp` reads the PC store's listing, one page per app. A game with a PC build
returns `onPC: true` and the PC listing's own media: screenshots (a PC set or a
subset of the phone ones), the trailer for that language as `video` and
`youTubeId`, the PC players' rating, and the system requirements. An app
without a PC build returns `onPC: false` and nothing else, because Google
redirects it to the phone listing.

`onPC` reflects the public PC listing only. A game the Windows storefront
promotes before release (Dispatch, in November 2026) reads `onPC: false`, and
`availability` shows it region-locked everywhere.

`-country` does not tell you where the PC client runs: the PC store also
serves listings to storefronts the client does not support, Russia and Iran
among them. For the official region list use `PCRegions()` in Go, a static
copy of [Google's help page](https://support.google.com/googleplay/answer/11358071).

### Reviews, including all of them

```bash
gpscrape reviews com.spotify.music -limit 500 | jq -r .untrusted_text
gpscrape reviews com.spotify.music -sort rating -score 1 -limit 100
gpscrape reviews com.spotify.music -langs en,ru,de -limit 500   # three corpora
gpscrape reviews com.spotify.music -langs all -limit 500        # every measured corpus
```

Language partitions reviews rather than filtering them: each `hl` code serves
its own corpus, and the corpora do not overlap, so reading all of an app's
reviews means reading every language. With `-langs`, `-limit` applies per
language and each record names its corpus. There is no country filter because
a review has no country: `ru/kz` and `ru/us` return the same reviews, id for id.

### Search and charts

```bash
gpscrape search "podcast player" -num 50
gpscrape suggest spoti
gpscrape list -collection new_free GAME_PUZZLE      # what is new in a category
gpscrape list -collection top_free -full=false      # chart rows only, no extra requests
gpscrape categories -kind game                      # the genre ids list accepts
gpscrape similar com.spotify.music
gpscrape developer "Spotify AB"
```

`search`, `list`, `similar` and `developer` fetch each result's details in
batches of 32 by default, because the result rows lack the developer id, the
price (search, similar, developer) and the tagline (charts). `-full=false`
skips that; rows without a price then carry no `free` or `price` field rather
than a guess (with `-raw`, `"priceKnown": false`).

Each of these returns one page, about 200 apps at most; Google rejects the
continuation token for stateless clients. For more apps, combine sources
(categories, collections, locales, the similar graph).

### The catalog

`catalog` is a group of verbs. The right-hand column is what each costs in
requests to Google:

| Verb | What it does | Requests |
| --- | --- | --- |
| `check` | has Google republished the catalog? | 2 |
| `new` | apps the store lists as recently published | 17 |
| `size` | how many apps there are, give or take 1% | ~900 |
| `genres` | genre changes and removals, from a snapshot | ~3.3k, plus ~310 per `-confirm-gone` storefront |
| `sweep` | the complete id list, and the exact count | ~88k |
| `apps` | the id list by genre, from what is on disk | none |
| `diff` | compare two snapshots already on disk | none |
| `ids` | stream ids without keeping any state | as many as you ask for |

```console
$ gpscrape catalog check -dir ./catalog
{"generation":"2026-08-23_1787500934","built":"2026-08-23T16:02:14Z","age_hours":287,"shards":83445,"up_to_date":false}
$ gpscrape catalog size | jq -c '{apps, half_width, shards_read, shards_total, met_target}'
{"apps":3549110,"half_width":35366,"shards_read":866,"shards_total":83445,"met_target":true}
```

That is 3,549,110 apps give or take 35,366 at 95% confidence, from 866 of
83,445 shards, in about ninety seconds, on 2026-08-23. The store grows: on
2026-11-07 it had 87,679 shards and, by a 0.7% sample, about four million
apps. The record also carries the dispersion check behind the interval; the
exact count costs a full sweep.

A full sweep is one request per shard, about 88,000 on 2026-11-07, some five
hours at the default throttle. Keep a snapshot directory and let the tool decide when a sweep is
due: Google republishes the sitemaps every few days, and shard names carry the
generation that produced them.

```bash
gpscrape catalog sweep -dir ./catalog            # sweeps only if a new generation exists
gpscrape catalog sweep -dir ./catalog -keep 3    # and keeps the three newest snapshots
gpscrape catalog diff -dir ./catalog -changes    # one record per id added or removed
```

Shards are immutable within a generation, so an interrupted sweep resumes from
the shards it has not reached. A completed sweep writes a sorted snapshot, a
manifest with the generation id and a checksum, and a delta against the
previous snapshot, and prints the manifest as its one output record. If the
previous snapshot cannot be read, the manifest says why in `delta_error`.

`catalog sweep` and `catalog genres` default to `-adaptive`, because over hours
of requests a fixed interval turns one long `Retry-After` into a failed shard.
The adaptive client slows down and waits instead, and stops after three
refused probes. `-adaptive=false` restores the fixed interval.

After a sweep, run `catalog size -dir ./catalog` to check the snapshot against
an independent estimate (`snapshot_ids`, `snapshot_agrees`). It is the only
check that notices a shard that parsed short without an error, which would
leave the snapshot with fewer ids than the store has.

A snapshot directory takes one writer at a time. `sweep` and `genres` hold
`sweep.lock` while they run and refuse a second run against the same
directory; a lock left by a dead process is cleared on the next run. `check`,
`new`, `size`, `apps`, `diff` and `ids` take no lock; `new` only appends to its
own signal log.

The genre table turns a snapshot into an app list by category. Build it once,
then read it offline:

```bash
gpscrape catalog genres -dir ./catalog                        # ~3.3k requests, plus ~310 per -confirm-gone storefront
gpscrape catalog apps -dir ./catalog -genre 'GAME_*' -ids-only > games.txt
gpscrape catalog apps -dir ./catalog -genre GAME_PUZZLE
```

`genres` asks a thousand ids per request. Before calling an app gone it
re-checks it in the storefronts `-confirm-gone` names, because an app can be
listed in Brazil and invisible from the United States. A lookup that fails in
that confirm pass is reported as an `error`, not a removal, and the row is
kept. `-prune` drops rows for ids the snapshot no longer lists.

An interrupted `genres` run over the snapshot resumes. It checkpoints every
10,000 ids into `genres.tsv.merge` and `genres.tsv.resume.json`; a rerun on the
same snapshot, table, `-lang` and `-country` continues after the last
checkpoint, or goes straight to the confirm pass if the walk had finished. Any
other rerun, and every `-ids` run, starts clean. Records printed before the
interruption are not printed again, and the summary counts the whole pass.
After a hard kill (not Ctrl-C), records between the last checkpoint and the
kill may be missing from stdout or appear twice; the table is unaffected.

Without a snapshot, `catalog ids` streams ids straight from the sitemaps:

```bash
gpscrape catalog ids -shards 0-99 -ids-only > ids.txt
```

Shards are read in parallel and ids come out as each shard arrives, so the
order differs between runs; sort the output if you compare it, or sweep for a
sorted snapshot.

### Debugging a run

```bash
gpscrape reviews com.spotify.music -limit 50 -debug              # every request, to stderr
GPSCRAPE_DEBUG=1 gpscrape catalog check                          # the same, for a script
gpscrape catalog sweep -dir ./catalog -debug -log-file sweep.log -trace sweep.trace
```

`-debug` logs each request, response, retry and throttle change, with the
connection timings behind each request; the first line names the build and the
run's settings. Results stay on stdout, so a pipeline is unaffected:

```text
level=DEBUG msg=request method=GET url="https://play.google.com/store/apps/details?id=com.spotify.music&hl=en&gl=us" attempt=1 waited=0s
level=DEBUG-4 msg=connection dns=24.077208ms connect=19.079541ms tls=54.277042ms reused=false addr=216.58.204.174:443 ttfb=242.611416ms
level=DEBUG msg=response method=GET url="https://play.google.com/store/apps/details?id=com.spotify.music&hl=en&gl=us" attempt=1 status=200 took=242.692542ms bytes=1254486
```

Bodies and headers are never logged. `-trace` keeps a Go execution trace of
the run's last minutes in memory and writes it at exit, Ctrl-C included;
`go tool trace sweep.trace` then shows where the time went, throttle versus
Google.

The transport is Go's default, so `HTTPS_PROXY=http://127.0.0.1:8080` routes
every request through a proxy such as mitmproxy, and `GODEBUG=http2debug=1`
prints the HTTP/2 frames.

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
| `PCApp` | the Google Play Games on PC listing: whether there is one, its media, rating and requirements |
| `PCRegions` | the 141 regions where Google Play Games on PC is offered (a static snapshot; no request) |
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
`WithHooks`. Every request takes one path: cache seam, retry with full jitter,
throttle, HTTP.

### Batched lookups

`AppsMany`, `PermissionsMany` and `SuggestMany` pack up to 32 lookups into one
request and send the packs over `WithConcurrency` workers. The throttle meters
requests, not lookups: 32 apps took 9.76s as 32 page fetches and 146ms as one
batched request, at about 20KB per app instead of a megabyte of markup.

```go
for _, r := range client.AppsMany(ctx, appIDs, gps.AppOptions{}) {
	if r.Err != nil {
		log.Printf("%s: %v", r.AppID, r.Err)
		continue
	}
	fmt.Println(r.App.Title, r.App.Score)
}
```

Results are positional (`out[i]` describes `appIDs[i]` whatever order Google
answers in), and each carries its own error, so a failed chunk does not cost
the rest of the run.

### Iterators

`ReviewsSeq` and `CatalogSeq` are `iter.Seq2` values, so a caller can stop at
any point instead of waiting for a sweep to finish:

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

The client is silent by default and logs at Debug at most: requests,
responses, retries, throttle waits and adaptive interval changes. `LevelTrace`
adds the connection timings behind each request. `WithHooks` gives callbacks
for a metrics registry, `WithHTTPClient` accepts an instrumented transport such
as `otelhttp.NewTransport`, and every operation that makes a request opens a
`runtime/trace` task, so `go tool trace` attributes each request to its call.

## Limitations

- **Lists and charts cap at ~200 apps** per category and collection. Google
  rejects the continuation token behind `List` and `Search` for stateless
  clients; the Node and Python libraries hit the same server-side wall.
  `CategoryApps` combines sources instead (one `GAME_ACTION` run reached
  ~1,800 unique apps), but it cannot reach an app that never charts, never
  ranks in search and appears in no similarity graph.
- **The whole store is reachable only through the sitemaps**, and they carry
  package ids and nothing else: no titles, no ratings. Pair a sweep with
  `AppsMany` to resolve the ids you care about.
- **Rate limits differ by endpoint.** The sitemap CDN sustained 32 requests
  per second for 90 seconds with no failures. The details endpoint tolerated a
  short burst but not sustained load, and an availability sweep across 177
  countries lost several to fetch errors. Be conservative, or use
  `WithAdaptiveThrottle` and let the client find the rate.
- **Payload shapes change without notice.** A field that moves yields a zero
  value rather than failing a crawl. A canary suite (build tag `canary`) runs
  against the live store to catch drift.
- **No authenticated API.** Real pagination and bulk detail lookups exist only
  on Google Play's mobile protobuf API, which needs a Google account token, a
  device check-in and maintained reverse-engineered schemas. This library does
  not implement it; see [AuroraOSS/gplayapi](https://gitlab.com/AuroraOSS/gplayapi).

## Submodules

- [`apidoc/`](apidoc/): an auto-generated OpenAPI 3.1 description of the
  private, undocumented Google Play endpoints this library calls.
- [`lightfeed/`](lightfeed/): `FeedBrowser` support for `Cluster`. It drives
  [Lightpanda](https://lightpanda.io) over CDP to scroll a category page past
  the depth a stateless client reaches. `Cluster` returns
  `ErrFeedPaginatorRequired` unless you inject the paginator. The middle step,
  `FeedMode: FeedLightweight`, lives in the root module: it replays the page's
  own stateless feed tokens, one extra request per topic, and takes
  `GAME_ACTION` from ~18 apps to ~77; the browser reaches ~149.
- [`mcp/`](mcp/): `gpscrape-mcp`, an MCP server that gives AI agents
  (Claude Code, Codex CLI, OpenCode, ChatGPT and other MCP clients) the
  store's quick lookups as tools, over stdio or Streamable HTTP.

All three are separate Go modules; the root library stays stdlib-only.

[`skills/gpscrape/`](skills/gpscrape/SKILL.md) is a skill for Claude Code and
other agents that read `SKILL.md`: when to use the MCP tools and when the CLI,
and how to filter the CLI's output. Copy the folder to `~/.claude/skills/`.

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
