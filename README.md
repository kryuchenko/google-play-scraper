# google-play-scraper

[![Tests](https://github.com/kryuchenko/google-play-scraper/actions/workflows/test.yml/badge.svg)](https://github.com/kryuchenko/google-play-scraper/actions/workflows/test.yml)
![Coverage](https://img.shields.io/badge/coverage-76.0%25-brightgreen)
[![Go Report Card](https://goreportcard.com/badge/github.com/kryuchenko/google-play-scraper)](https://goreportcard.com/report/github.com/kryuchenko/google-play-scraper)
[![Go Reference](https://pkg.go.dev/badge/github.com/kryuchenko/google-play-scraper.svg)](https://pkg.go.dev/github.com/kryuchenko/google-play-scraper)

Go library for scraping Google Play Store app data — **no external dependencies**.

Inspired by [facundoolano/google-play-scraper](https://github.com/facundoolano/google-play-scraper) (Node.js).

## Features

- **App Details** — full app info: description, rating, reviews count, screenshots, version, etc.
- **Availability** — map an app's region availability across many countries (available / not-in-region / not-found)
- **Search** — search apps with price filtering (free/paid) and full details
- **Reviews** — fetch reviews with sorting, filtering by rating, and pagination
- **Developer Apps** — list all apps by a developer (by name or ID)
- **Similar Apps** — find apps similar to a given app
- **Permissions** — get app permissions list
- **Data Safety** — get privacy/data collection info
- **Suggestions** — get search autocomplete suggestions
- **Top Charts** — get top free/paid/grossing apps by category
- **Categories** — list all Play Store categories
- **Localization** — support for 50+ languages and countries
- **Rate Limiting** — built-in throttling to avoid blocks

## Installation

```bash
go get github.com/kryuchenko/google-play-scraper
```

## Quick Start

```go
import "github.com/kryuchenko/google-play-scraper"

client := googleplayscraper.NewClient()
ctx := context.Background()

app, _ := client.App(ctx, "com.spotify.music", googleplayscraper.AppOptions{})
fmt.Println(app.Title, app.Score) // "Spotify" 4.3
```

## Client Options

```go
client := googleplayscraper.NewClient(
    googleplayscraper.WithThrottle(500 * time.Millisecond), // Rate limiting
    googleplayscraper.WithTimeout(60 * time.Second),        // Request timeout
    googleplayscraper.WithUserAgent("MyApp/1.0"),           // Custom User-Agent
    googleplayscraper.WithConcurrency(5),                   // Parallel FullDetail fetches (default 1)
)
```

`WithConcurrency(n)` controls how many `App()` detail lookups run in parallel
when a listing is requested with `FullDetail: true` (Search, List, Developer,
Similar). The default is `1` (sequential), so parallelism is opt-in;
the configured `WithThrottle` interval still bounds the request rate across
workers. Result order is always preserved.

## API

### App

Retrieves full details of an application.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| appId | string | *required* | App ID (e.g., `com.google.android.apps.maps`) |
| lang | string | `"en"` | Language code (ISO 639-1) |
| country | string | `"us"` | Country code (ISO 3166-1) |

```go
app, err := client.App(ctx, "com.google.android.apps.maps", googleplayscraper.AppOptions{
    Lang:    "en",
    Country: "us",
})
```

<details>
<summary>Available fields</summary>

`AppID`, `Title`, `Summary`, `Description`, `DescriptionHTML`, `Developer`, `DeveloperID`, `DeveloperEmail`, `DeveloperWebsite`, `DeveloperAddress`, `Icon`, `Score`, `ScoreText`, `Ratings`, `Reviews`, `Histogram`, `Price`, `PriceText`, `Currency`, `Free`, `Installs`, `MinInstalls`, `MaxInstalls`, `Genre`, `GenreID`, `Categories`, `Version`, `AndroidVersion`, `ContentRating`, `Released`, `Updated`, `URL`, `Screenshots`, `Video`, `VideoImage`, `HeaderImage`, `PreviewVideo`, `PrivacyPolicy`, `Available`

Monetization: `AdSupported`, `OffersIAP`, `IAPRange`, `OriginalPrice`, `DiscountEndDate`

Distribution: `IsAvailableInPlayPass`, `Preregister`, `EarlyAccessEnabled`

Content & changelog: `RecentChanges`, `ContentRatingDescription`

Developer (EU DSA trader info, empty for non-EU traders): `DeveloperInternalID`, `DeveloperLegalName`, `DeveloperLegalEmail`, `DeveloperLegalAddress`, `DeveloperLegalPhoneNumber`

</details>

`Available` reflects whether the app is installable in the requested `country`:
it is `false` for a region-locked listing or a pre-registration (unreleased)
entry. (Previously it was hardcoded `true`.) To map availability across many
countries at once, use `Availability` below.

---

### Availability

Probes an app's region availability across many countries and returns a
per-country status. Each probe fetches the listing and reads only the
availability node, so it is much cheaper than a full `App` call per country.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| appId | string | *required* | App ID to probe |
| Countries | []string | `AllCountries` | Country codes to probe (lowercased and deduplicated) |
| Lang | string | `"en"` | Language code |
| Concurrency | int | client's `WithConcurrency` (1) | Countries probed in parallel |
| Progress | func | nil | Called once per probed country |

Each country resolves to one of four statuses:

| Status | Meaning |
|--------|---------|
| `StatusAvailable` | Installable in the country |
| `StatusNotInRegion` | Listing exists but the app is not offered there (region-locked, or a pre-registration entry) |
| `StatusNotFound` | Google returned 404 — no listing for the app in that country |
| `StatusFetchError` | Transport/HTTP error other than 404; availability is unknown (the error is recorded in `Result.Errors`) |

```go
result, err := client.Availability(ctx, "com.spotify.music", googleplayscraper.AvailabilityOptions{
    Countries: []string{"us", "de", "cn"},
})
// result.Statuses["cn"] == googleplayscraper.StatusNotInRegion

// Or just the installable countries:
countries, err := client.AvailableCountries(ctx, "com.spotify.music", googleplayscraper.AvailabilityOptions{})
```

A single country's 404 or transport error never aborts the sweep; only context
cancellation does, in which case the partial result is returned alongside
`ctx.Err()`. `result.GloballyRemoved` is `true` only when every conclusively
probed country returned 404 — it is meaningful only on a full `AllCountries`
sweep, not on a narrow `Countries` subset.

**Cost & etiquette.** A full sweep issues roughly one request per country
(`AllCountries` is ~177 codes), so at a 500ms throttle a complete sweep takes
~90s. This is *active probing*, not an official availability feed: keep the
throttle gentle to avoid rate limiting, and prefer a targeted `Countries` list
when you don't need the whole world. See `examples/app-availability`.

---

### Search

Search for apps on Google Play.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| term | string | *required* | Search term |
| num | int | `20` | Number of results; values above 250 are clamped to 250 |
| lang | string | `"en"` | Language code |
| country | string | `"us"` | Country code |
| price | string | `"all"` | `"free"`, `"paid"`, or `"all"` |
| fullDetail | bool | `false` | Fetch full details for each app |

```go
results, err := client.Search(ctx, googleplayscraper.SearchOptions{
    Term:  "weather",
    Num:   20,
    Price: "free",
})
```

---

### Reviews

Fetch app reviews with filtering and pagination.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| appId | string | *required* | App ID |
| lang | string | `"en"` | Language code |
| country | string | `"us"` | Country code |
| sort | Sort | `SortNewest` | `SortNewest`, `SortRating`, `SortHelpfulness` |
| count | int | `150` | Number of reviews per request (max 150) |
| filterScore | int | `0` | Filter by rating: 1-5, or 0 for all |
| nextToken | string | `""` | Pagination token |

```go
result, err := client.Reviews(ctx, "com.instagram.android", googleplayscraper.ReviewOptions{
    Sort:        googleplayscraper.SortNewest,
    Count:       100,
    FilterScore: 1, // Only 1-star reviews
})

// Pagination
nextPage, _ := client.Reviews(ctx, appID, googleplayscraper.ReviewOptions{
    NextToken: result.NextToken,
})
```

---

### ReviewsAll

Fetch multiple pages of reviews automatically.

```go
reviews, err := client.ReviewsAll(ctx, "com.instagram.android", googleplayscraper.ReviewOptions{
    Count:       500, // Total reviews to fetch
    FilterScore: 5,   // Only 5-star reviews
})
```

---

### Developer

List apps by a developer.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| devId | string | *required* | Developer name or numeric ID |
| num | int | `60` | Number of results |
| lang | string | `"en"` | Language code |
| country | string | `"us"` | Country code |
| fullDetail | bool | `false` | Fetch full details for each app |

```go
apps, err := client.Developer(ctx, googleplayscraper.DeveloperOptions{
    DevID: "Google LLC",
    Num:   20,
})
```

---

### Similar

Find similar apps.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| appId | string | *required* | App ID |
| lang | string | `"en"` | Language code |
| country | string | `"us"` | Country code |
| fullDetail | bool | `false` | Fetch full details for each app |

```go
similar, err := client.Similar(ctx, googleplayscraper.SimilarOptions{
    AppID: "com.google.android.apps.maps",
})
```

---

### Permissions

Get app permissions.

```go
perms, err := client.Permissions(ctx, googleplayscraper.PermissionsOptions{
    AppID: "com.instagram.android",
})

for _, p := range perms {
    fmt.Println(p.Type, p.Permission)
}
```

---

### DataSafety

Get data safety information.

```go
safety, err := client.DataSafety(ctx, googleplayscraper.DataSafetyOptions{
    AppID: "com.instagram.android",
})

fmt.Println("Collected:", len(safety.CollectedData))
fmt.Println("Shared:", len(safety.SharedData))
fmt.Println("Privacy Policy:", safety.PrivacyPolicyURL)
```

---

### Suggest

Get search suggestions.

```go
suggestions, err := client.Suggest(ctx, googleplayscraper.SuggestOptions{
    Term: "weath",
})
// ["weather", "weather app", "weather forecast", ...]
```

---

### List

Get top apps by collection and category.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| collection | Collection | `CollectionTopFree` | App collection |
| category | Category | `""` | App category |
| age | Age | `""` | Age rating filter (see caveat below) |
| num | int | `500` | Number of results; values >660 are clamped to 660. Google returns ~200 max per collection in practice |
| lang | string | `"en"` | Language code |
| country | string | `"us"` | Country code |
| fullDetail | bool | `false` | Fetch full details for each app |

```go
apps, err := client.List(ctx, googleplayscraper.ListOptions{
    Collection: googleplayscraper.CollectionTopFree,
    Category:   googleplayscraper.CategoryGame,
    Num:        500,
})
```

**Collections:** `CollectionTopFree`, `CollectionTopPaid`, `CollectionGrossing`

**Age ratings:** `AgeFive` (5 and under), `AgeSix` (6-8), `AgeNine` (9-12)

> **Age filter caveat:** `Age` is currently a no-op on the primary list path.
> The vyAe2 batchexecute endpoint reads filters from the request body, not the
> URL, and ignores the `age` query parameter, so filtered and unfiltered lists
> come back identical (verified for both `CategoryFamily` and `CategoryGame`).
> The parameter is still sent for parity with the reference implementation and
> is honoured only by the legacy HTML fallback.

---

### ClusterURLs / Cluster

Discover the app clusters ("Popular apps", "New releases", …) on a category or
top-charts page with `ClusterURLs`, then fetch a cluster's apps with `Cluster`.

```go
clusters, err := client.ClusterURLs(ctx, googleplayscraper.ClusterURLsOptions{
    Category: googleplayscraper.CategoryGame,
})
// clusters[i].Title, clusters[i].URL

apps, err := client.Cluster(ctx, googleplayscraper.ClusterOptions{
    Path: clusters[0].URL,
    Num:  100,
})
```

> **Known limitation:** continuation-token pagination (the `qnKhOb` RPC) is
> currently rejected by Google, so `Cluster` returns only the first page
> (~20–50 apps). For the same reason `Search` is limited to its first page plus
> inline results (a pre-existing limitation).

---

### Categories

Get all available categories.

```go
categories, err := client.Categories(ctx, googleplayscraper.CategoriesOptions{})
```

Returns 54 categories including: `GAME_ACTION`, `GAME_PUZZLE`, `BUSINESS`, `SOCIAL`, `COMMUNICATION`, etc.

---

## Localization

All methods support language and country parameters:

- **Language**: ISO 639-1 code (`"en"`, `"es"`, `"ru"`, `"ja"`, `"de"`, `"fr"`, ...)
- **Country**: ISO 3166-1 alpha-2 code (`"us"`, `"es"`, `"ru"`, `"jp"`, `"de"`, `"fr"`, ...)

```go
// Spanish results from Spain
app, _ := client.App(ctx, appID, googleplayscraper.AppOptions{
    Lang:    "es",
    Country: "es",
})
```

## API documentation

The [`apidoc/`](apidoc/) directory holds an auto-generated **OpenAPI 3.1**
specification of the *private, undocumented* Google Play HTTP endpoints this
library calls (the `/store/...` HTML pages and the `batchexecute` RPCs). It lives
in a **separate nested Go module** so the root library stays
**zero-dependency** — only `apidoc/` pulls in [swaggo](https://github.com/swaggo/swag).

Regenerate the spec with `cd apidoc && make gen` (or `./gen.sh`); the committed
output is `apidoc/docs/swagger.{json,yaml}`. See [`apidoc/README.md`](apidoc/README.md)
for details and the Terms-of-Service disclaimer — these endpoints are not a
public Google API and may change without notice.

## Coverage & limitations

This library scrapes the **anonymous public web** interface of Google Play
(`play.google.com`), so it is bound by what that interface exposes:

- **Lists/charts cap at ~200 apps** per category × collection. Google does not
  serve a full catalog, and continuation-token pagination (the `qnKhOb` RPC) is
  currently rejected for lists/search — see the `Cluster` note above. This is a
  server-side limit, not a library bug; the reference Node library
  (`facundoolano/google-play-scraper`) and the Python libraries hit the same
  wall.
- **Get broad coverage by multiplying sources**, not by paginating deeper:
  iterate categories × collections × countries/languages, fetch each category's
  clusters, and crawl the app graph via `Similar` and `Developer`. Filter to
  games by `GenreID` starting with `GAME`. The `CategoryApps` method below
  automates exactly this.

### CategoryApps — automated category coverage

`CategoryApps` unions many independent slices of one category (collections ×
locales × age buckets × a search-term dictionary × a `Similar`/`Developer`
graph walk), deduplicating by `AppID`, with a saturation stop on the expensive
phases. It is the practical answer to "give me everything in this category"
within the anonymous web limits.

```go
result, err := client.CategoryApps(ctx, googleplayscraper.CoverageOptions{
    Category:    googleplayscraper.CategoryGameAction,
    Locales:     googleplayscraper.CoverageLocales[:3], // us/en, gb/en, in/en, …
    GraphDepth:  1,                                     // Similar/Developer BFS
    MaxApps:     5000,
})
// result.Apps, result.PerSourceNew, result.RequestsMade, result.Saturated
```

**What to expect (measured):** a single `GAME_ACTION` run reached ~1,800 unique
apps — roughly 9× the ~200 single-request ceiling — with zero duplicates. The
**search-term dictionary contributes far more unique apps than adding locales**,
so widen `SearchTerms` before adding countries. The example CLI
(`examples/category-coverage`) exposes `-locales`, `-graph-depth`, `-suggest`,
and `-no-search` to control the request budget.

**Honest boundary:** this collects the *commercially visible* layer of a
category — typically thousands of apps, **not the full catalog**. Apps with no
ratings that never surface in any chart, search, or similarity graph are not
reachable through the anonymous web at all (see the FDFE note below). Full
pagination of a category remains impossible anonymously.

### Need deeper access? The mobile protobuf API (FDFE)

If you need true deep pagination or batched detail lookups, the only known
option beyond the web interface is Google Play's **mobile protobuf API** — the
same `android.clients.google.com/fdfe/` endpoints the Play Store app uses. It
supports real `nextPageUrl` pagination and `bulkDetails` (hundreds of package
names per request, ideal for fast catalog verification).

This library does **not** implement it, because it requires a different (and
heavier) setup with a different risk profile:

- a Google account token (or an anonymous one from a token dispenser) — i.e.
  **authenticated** access, a clearer ToS violation than anonymous scraping;
- device check-in / registration with a protobuf device profile;
- ongoing maintenance of reverse-engineered protobuf schemas and token rotation
  (anonymous accounts get banned periodically).

The reference implementation is **[AuroraOSS/gplayapi](https://gitlab.com/AuroraOSS/gplayapi)**
(Kotlin, on GitLab — the GitHub `whyorean/GPlayApi` repo is an outdated mirror),
as used by the [Aurora Store](https://github.com/whyorean/AuroraStore) client.
For a fully managed option, commercial APIs (SerpApi, 42matters) wrap the same
data behind their own pagination.

## License

MIT
