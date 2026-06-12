# lightfeed

Browser-driven **deep pagination** for the Google Play category/cluster feed,
implementing `googleplayscraper.FeedPaginator` (the `FeedBrowser` mode of the
root scraper).

It lives in a **separate Go module** so the root
[`google-play-scraper`](../) package stays **zero-dependency**. Only callers who
opt into `FeedBrowser` pull in the browser driver
([`chromedp`](https://github.com/chromedp/chromedp)) and run a headless browser.

## Why

The Play Store ships only the first ~18 apps of a category page in the initial
HTML; the rest stream in as you scroll an infinite feed backed by the private
`qnKhOb` RPC. The root scraper's stateless `FeedLightweight` mode replays that
RPC and reaches ~77 apps on `GAME_ACTION`. To go deeper, `lightfeed` drives a
real headless browser to actually scroll the page and read every app link the
live feed renders.

Measured on `/store/apps/category/GAME_ACTION` (verified live 2026-06-12):

| mode                       | apps  | requests / driver        |
| -------------------------- | ----- | ------------------------ |
| initial grid only          | ~18   | 1 GET                    |
| `FeedLightweight`          | ~77   | +1 per recommendation    |
| `FeedBrowser` (this module)| ~130  | headless browser scroll  |

## The browser: Lightpanda

[Lightpanda](https://lightpanda.io) is a ~64 MB static headless browser
(V8 + NetSurf, no Chromium) that speaks a subset of the Chrome DevTools
Protocol. `chromedp.NewRemoteAllocator` connects to it over CDP; we restrict
ourselves to `Page.navigate` + `Runtime.evaluate`, which Lightpanda implements.

Download a nightly build (Apple Silicon macOS shown; `linux-x86_64` and
`linux-aarch64` are published under the same tag):

```sh
curl -sL -o lightpanda \
  https://github.com/lightpanda-io/browser/releases/download/nightly/lightpanda-aarch64-macos
chmod +x lightpanda
./lightpanda version
```

## Usage

### Autostart (lightfeed manages the process)

```go
p, err := lightfeed.New(lightfeed.WithLightpandaPath("./lightpanda"))
if err != nil {
    log.Fatal(err)
}
defer p.Close()

apps, err := client.Cluster(ctx, googleplayscraper.ClusterOptions{
    Path:          "/store/apps/category/GAME_ACTION",
    FeedMode:      googleplayscraper.FeedBrowser,
    FeedPaginator: p,
})
```

### External endpoint (you run the browser)

```sh
lightpanda serve --host 127.0.0.1 --port 9222
```

```go
p, err := lightfeed.New(lightfeed.WithCDPEndpoint("ws://127.0.0.1:9222"))
```

### Options

| option                    | default | meaning                                    |
| ------------------------- | ------- | ------------------------------------------ |
| `WithLightpandaPath(p)`   | —       | autostart a managed browser from binary `p`|
| `WithCDPEndpoint(ws)`     | —       | connect to a browser you run yourself      |
| `WithScrollRounds(n)`     | 40      | max scroll iterations per page             |
| `WithThrottle(d)`         | 600ms   | pause after each scroll for the feed to load|
| `WithTimeout(d)`          | 90s     | total budget per page                      |

Provide **exactly one** of `WithLightpandaPath` / `WithCDPEndpoint`.

## Result shape

`PaginateFeed` returns **thin** `SearchResult`s — `AppID`, `URL`, and (when the
anchor exposes them) `Title` and `Icon`. A browser scroll harvests app *links*,
not the rich grid payload. The root scraper merges them against the rich initial
grid (preferring the grid's fields); enrich the rest via `App()` if you need full
detail.

## CoverageOptions integration

`CategoryApps` can union a deep scroll into its sweep:

```go
res, err := client.CategoryApps(ctx, googleplayscraper.CoverageOptions{
    Category:             googleplayscraper.CategoryGameAction,
    ClusterFeedMode:      googleplayscraper.FeedBrowser,
    ClusterFeedPaginator: p,
})
```

## Example

```sh
go run ./examples/deep-cluster -lightpanda ./lightpanda -category GAME_ACTION
# or
go run ./examples/deep-cluster -cdp ws://127.0.0.1:9222 -category GAME_ACTION
```

## Tests

Offline tests (config validation, link-set dedup, interface satisfaction) run
under `go test ./...`. The live deep-pagination canary skips unless both
`LIGHTPANDA_PATH` and `LIGHTPANDA_CDP` are set:

```sh
lightpanda serve --host 127.0.0.1 --port 9222 &
LIGHTPANDA_PATH=./lightpanda LIGHTPANDA_CDP=ws://127.0.0.1:9222 go test -run TestLive ./...
```

## Disclaimer

Not affiliated with or endorsed by Google. The `qnKhOb` feed is an undocumented
internal API and may change or break at any time. Scrape responsibly: keep (or
raise) the default throttle, cap your scroll rounds, and respect Google's Terms
of Service and any applicable rate limits. You are responsible for how you use
it.
