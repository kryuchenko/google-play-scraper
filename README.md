# google-play-scraper

[![Tests](https://github.com/kryuchenko/google-play-scraper/actions/workflows/test.yml/badge.svg)](https://github.com/kryuchenko/google-play-scraper/actions/workflows/test.yml)
![Coverage](https://img.shields.io/badge/coverage-82.6%25-brightgreen)
[![Go Report Card](https://goreportcard.com/badge/github.com/kryuchenko/google-play-scraper)](https://goreportcard.com/report/github.com/kryuchenko/google-play-scraper)
[![Go Reference](https://pkg.go.dev/badge/github.com/kryuchenko/google-play-scraper.svg)](https://pkg.go.dev/github.com/kryuchenko/google-play-scraper)

Go library for scraping Google Play Store app data — **no external dependencies**.

Inspired by [facundoolano/google-play-scraper](https://github.com/facundoolano/google-play-scraper) (Node.js).

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
)
```

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

`AppID`, `Title`, `Summary`, `Description`, `DescriptionHTML`, `Developer`, `DeveloperID`, `DeveloperEmail`, `DeveloperWebsite`, `DeveloperAddress`, `Icon`, `Score`, `ScoreText`, `Ratings`, `Reviews`, `Histogram`, `Price`, `PriceText`, `Currency`, `Free`, `Installs`, `MinInstalls`, `MaxInstalls`, `Genre`, `GenreID`, `Categories`, `Version`, `AndroidVersion`, `ContentRating`, `Released`, `Updated`, `URL`, `Screenshots`, `Video`, `VideoImage`, `HeaderImage`, `PrivacyPolicy`, `Available`

</details>

---

### Search

Search for apps on Google Play.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| term | string | *required* | Search term |
| num | int | `20` | Number of results (max 250) |
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
| age | Age | `""` | Age rating filter |
| num | int | `50` | Number of results |
| lang | string | `"en"` | Language code |
| country | string | `"us"` | Country code |
| fullDetail | bool | `false` | Fetch full details for each app |

```go
apps, err := client.List(ctx, googleplayscraper.ListOptions{
    Collection: googleplayscraper.CollectionTopFree,
    Category:   googleplayscraper.CategoryGame,
    Age:        googleplayscraper.AgeFive, // Ages 5 and under
    Num:        50,
})
```

**Collections:** `CollectionTopFree`, `CollectionTopPaid`, `CollectionGrossing`, `CollectionTrending`, `CollectionNewFree`, `CollectionNewPaid`

**Age ratings:** `AgeFive` (5 and under), `AgeSix` (6-8), `AgeNine` (9-12)

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

## License

MIT
