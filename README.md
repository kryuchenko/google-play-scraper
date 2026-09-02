# google-play-scraper

[![Tests](https://github.com/kryuchenko/google-play-scraper/actions/workflows/test.yml/badge.svg)](https://github.com/kryuchenko/google-play-scraper/actions/workflows/test.yml)
![Coverage](https://img.shields.io/badge/coverage-93.2%25-brightgreen)
[![Go Report Card](https://goreportcard.com/badge/github.com/kryuchenko/google-play-scraper)](https://goreportcard.com/report/github.com/kryuchenko/google-play-scraper)
[![Go Reference](https://pkg.go.dev/badge/github.com/kryuchenko/google-play-scraper.svg)](https://pkg.go.dev/github.com/kryuchenko/google-play-scraper)

Go library and CLI for reading public Google Play Store data — app details,
reviews, search, top charts, permissions, data safety, region availability and
the full catalog of package ids.

The root module imports nothing outside the standard library, and
`TestRootIsZeroDependency` fails if that ever stops being true. The two optional
pieces — a headless-browser feed paginator and an OpenAPI description of
Google's endpoints — live in separate nested modules, so neither reaches your
`go.sum`.

Requires Go 1.25. Inspired by
[facundoolano/google-play-scraper](https://github.com/facundoolano/google-play-scraper)
(Node.js).

## Install

```bash
go get github.com/kryuchenko/google-play-scraper
```

## Command line

```bash
go install github.com/kryuchenko/google-play-scraper/cmd/gpscrape@latest
```

`gpscrape` writes newline-delimited JSON to stdout, so it composes with `jq` and
anything else that reads a stream. Paging commands write as results arrive
rather than at the end, and SIGINT stops a run without discarding what it has
already written.

```bash
gpscrape app com.spotify.music | jq .score
gpscrape app com.spotify.music com.whatsapp com.duolingo   # all three in one request
gpscrape reviews com.spotify.music -limit 500 | jq -r .text
gpscrape reviews com.spotify.music -langs all -limit 500        # every corpus
gpscrape reviews kz.kaspi.mobile -langs kk,ru                   # no country filter exists
gpscrape availability com.spotify.music -countries us,de,jp
gpscrape catalog check                                            # 2 requests
gpscrape catalog size                                             # 3.5M +/- 1%, ~90s
gpscrape catalog sweep                                            # the exact count, ~4.6h
gpscrape catalog apps -genre 'GAME_*' -ids-only > games.txt        # the game index
gpscrape catalog ids -shards 0-99 > ids.txt
gpscrape list -collection new_free GAME_PUZZLE                    # what is new
```

Commands: `app`, `search`, `reviews`, `similar`, `developer`, `permissions`,
`datasafety`, `suggest`, `categories`, `availability`, `list`, `sync`, and
`catalog` with its own verbs (`check`, `new`, `size`, `genres`, `sweep`, `apps`,
`diff`, `ids`). Every command takes `-lang`, `-country`, `-throttle`, `-concurrency` and `-timeout`;
`-adaptive` turns `-throttle` into a floor and lets the client find its own
rate. `gpscrape <command> -h` lists the rest.

### gpscrape sync

A full catalog sweep is ~83k requests, so `sync` runs one only when Google has
actually regenerated its sitemaps — which is not daily. Shard filenames carry
the id of the generation that produced them, so noticing that nothing changed
costs three requests:

```bash
gpscrape sync -check -dir ./catalog   # "up to date" or "new generation available"
gpscrape sync -dir ./catalog          # sweep; resumable, writes snapshot + manifest + delta
```

Within a generation shards are immutable, which is what makes an interrupted
sweep resumable from the shards it has not reached. Each completed sweep writes
a sorted, deduplicated snapshot, a manifest with the generation id and a
checksum, and a delta against the previous snapshot. The delta is the point:
which apps appeared in the store since last time is a few hundred kilobytes
against a snapshot's tens of megabytes.

## Quick start

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
}
```

Every method takes a `context.Context` and honours cancellation. Errors that
carry an HTTP status are `*StatusError`, so a missing app is distinguishable
from a transport failure.

## Operations

Signatures, option fields and per-method caveats are on
[pkg.go.dev](https://pkg.go.dev/github.com/kryuchenko/google-play-scraper).

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
| `CatalogSeq` | every app id in the store, via Google's public sitemaps |

Most option structs take `Lang` (ISO 639-1) and `Country` (ISO 3166-1 alpha-2),
defaulting to `en`/`us`.

Client options: `WithThrottle`, `WithConcurrency`, `WithTimeout`,
`WithUserAgent`, `WithHTTPClient`, `WithRetry`, `WithAdaptiveThrottle`,
`WithHooks`. Set a throttle — the default is unthrottled, which suits one-off
lookups and nothing else.

`ReviewsAll` and `EnumerateCatalog` are deprecated as of 1.4.0 and will be
removed in v2; use `ReviewsSeq` and `CatalogSeq`, which let the caller stop
where it wants to.

## Batched lookups

`AppsMany`, `PermissionsMany` and `SuggestMany` pack up to 32 lookups into one
`batchexecute` request instead of issuing a request each. The throttle meters
requests rather than lookups, so this is the difference between 32 intervals
and one.

Measured on 32 apps at a 300ms interval: 9.76s over 32 page fetches against
146ms over one batched request. The transfer drops with it — about 20KB per app
instead of a megabyte of markup. On the pure RPC path, 64 calls at a 200ms
interval took 18.94s unpacked and 0.65s at 32 per request, with identical
payloads.

```go
for _, r := range client.AppsMany(ctx, appIDs, gps.AppOptions{}) {
	if r.Err != nil {
		log.Printf("%s: %v", r.AppID, r.Err)
		continue
	}
	fmt.Println(r.App.Title, r.App.Score)
}
```

Results are positional — `out[i]` describes `appIDs[i]`, whatever order Google
answers in — and each carries its own error, so a failed chunk does not cost the
rest of the run. `App` stays the reference for a single app: it reads the
rendered page, so a field carried only in the markup would not survive the
batched path.

## Coverage and limitations

This scrapes the anonymous public web interface of `play.google.com`, so it is
bound by what that interface exposes.

- **Lists and charts cap at ~200 apps** per category × collection. The
  continuation-token pagination behind `List` and `Search` (the `qnKhOb` RPC) is
  rejected for stateless clients, so both return roughly one page. This is
  server-side; the Node and Python libraries hit the same wall.
- **Breadth comes from multiplying sources**, not from paginating deeper:
  categories × collections × locales, each category's clusters, plus a
  `Similar`/`Developer` graph walk. `CategoryApps` automates exactly that — a
  single `GAME_ACTION` run reached ~1,800 unique apps, about 9× the
  single-request ceiling. It still collects only the commercially visible layer:
  an app with no ratings that never charts, never ranks in search and appears in
  no similarity graph is not reachable this way.
- **The whole store is reachable only through the sitemaps.** `robots.txt`
  advertises two sitemap indexes pointing at ~83k gzipped shards; keeping the
  `/store/apps/details?id=` locs yields on the order of 3 million package ids —
  and nothing else, no titles or ratings. Pair it with `AppsMany` to resolve the
  ids you care about. `SitemapIndexURLs`, `AllSitemapShards` and
  `SitemapShardPackages` are exported if you would rather drive the crawl
  yourself.
- **Rate limits are real and asymmetric.** The sitemap CDN sustained 32 rps for
  90 seconds with no failures; the details endpoint was clean to 12 rps in a
  48-request burst but not over several minutes, where a 177-country
  availability sweep lost 7-10 countries to fetch errors. Be conservative, or
  use `WithAdaptiveThrottle` and let the client find the rate.
- **Payload shapes change without notice.** Parsers are defensive by design: a
  field that moves yields a zero value rather than failing a crawl. A canary
  suite (build tag `canary`) runs against the live store to catch drift.

### What this library does not do

Batched detail lookups at a scale beyond `AppsMany`, and real `nextPageUrl`
pagination, exist on Google Play's mobile protobuf API — the
`android.clients.google.com/fdfe/` endpoints the Play Store app uses. This
library does not implement them: they need a Google account token (i.e.
authenticated access, a clearer ToS violation than anonymous scraping), device
check-in with a protobuf device profile, and ongoing maintenance of
reverse-engineered schemas and token rotation. The reference implementation is
[AuroraOSS/gplayapi](https://gitlab.com/AuroraOSS/gplayapi) (Kotlin; the GitHub
`whyorean/GPlayApi` mirror is outdated), as used by
[Aurora Store](https://github.com/whyorean/AuroraStore).

## Submodules

- [`apidoc/`](apidoc/) — an auto-generated OpenAPI 3.1 description of the
  private, undocumented Google Play endpoints this library calls. Regenerate
  with `cd apidoc && make gen`; the committed output is
  `apidoc/docs/swagger.{json,yaml}`.
- [`lightfeed/`](lightfeed/) — `FeedBrowser` support for `Cluster`, driving
  [Lightpanda](https://lightpanda.io) over CDP to scroll a category page past
  the depth a stateless client can reach. `Cluster` returns
  `ErrFeedPaginatorRequired` unless you inject the paginator, so the dependency
  is always explicit.

Both are separate Go modules; the root library stays stdlib-only.

## Links

- [API reference](https://pkg.go.dev/github.com/kryuchenko/google-play-scraper)
- [CHANGELOG.md](CHANGELOG.md)
- [CONTRIBUTING.md](CONTRIBUTING.md)
- [SECURITY.md](SECURITY.md)
- Runnable programs in [`examples/`](examples/): `app-availability`,
  `catalog-crawler`, `category-coverage`, `verify-game-genre`,
  `yandex-taxi-reviews`

## License

MIT
