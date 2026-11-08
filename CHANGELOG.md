# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Security

- Responses are capped at 64MB (`ErrBodyTooLarge`, not retried) and an inflated
  sitemap shard at 256MB (`ErrInflatedTooLarge`), so a proxy that never ends a
  body, or a small gzip bomb, can no longer take all the memory. The largest
  real bodies are about 3MB, a shard about 8MB inflated.

### Added

- `-adaptive-min` lets the adaptive throttle probe faster than `-throttle`,
  for finding the rate Google actually allows. The controller remembers the
  interval of each refusal for 30 minutes and settles at 80% of the median
  refused rate, and probes past the caller's interval five times more slowly
  than it recovers to it (`AdaptivePolicy.ProbeWindow`, `CutMemory`,
  `Headroom`).
- `gpscrape catalog size -dir` checks its estimate against the complete
  snapshot of the same generation (`snapshot_ids`, `snapshot_agrees`, and a
  warning when they disagree). It is the one check that notices ids lost
  without an error.
- `gpscrape catalog genres` resumes an interrupted run. It checkpoints every
  10,000 ids (`genres.tsv.merge`, `genres.tsv.resume.json`); a rerun on the
  same snapshot, table, `-lang` and `-country` continues after the last
  checkpoint, or goes straight to the confirm pass if the walk had finished,
  and publishes the same table byte for byte. `-ids` runs, and runs in which
  every lookup failed, do not resume.
- A sweep that cannot diff against the previous snapshot records why in the
  manifest's `delta_error`.
- `gpscrape`: inside Claude Code's sandbox on macOS, where every request fails
  with `x509: OSStatus -26276`, the error carries a `hint` naming the setting
  that fixes it and is no longer marked retryable.

### Changed

- **Breaking, records:** `AppSummary.Free` is a `*bool`, nil when the price
  is unknown, so the JSON field `free` can be absent (see Fixed).
- **Breaking, `gpscrape catalog`:** every catalog record is snake_case like the
  rest of the CLI's output (`app_id`, `genre_id`, `age_hours`, `up_to_date`,
  `sample_pct`, `completed_at`, `half_width`, `shards_read` and the rest).
  Catalog files an older version wrote (manifests, resumable sweep state, the
  `catalog new` signal log) still read; new ones are written snake_case.
- `gpscrape catalog sweep` and `catalog genres` default to `-adaptive`, because
  over hours of requests a fixed interval turned a `Retry-After` beyond the
  retry policy into a failed shard. `-adaptive=false` restores the fixed
  interval.
- `gpscrape catalog genres` uses far less memory: at four million ids the
  table work peaks at about 45MB instead of 700MB+ (more when many ids are
  absent from the first storefront). `-ids` lists are sorted and deduplicated,
  so each id is looked up once. A genre table not sorted by app id is now an
  error rather than silently re-sorted.
- `gpscrape catalog diff` reads both snapshots side by side instead of whole:
  538MB to 4MB at catalog scale.
- `lightfeed`: the port probe and readiness wait honour the caller's context.

### Fixed

- `catalog genres` stopped mid-pack (Ctrl-C, `-timeout`) lost rows on
  resume: the ids in flight came back with the stop's error and were counted
  as answered, so the resume skipped them and apps new to the table never got
  a row. A stop now saves progress up to the last real answer.
- `Search` with a negative `Num` panicked; a negative `Num` or `Count` now
  means the default in `Search`, `Developer` and `Reviews`. The CLI refuses
  any negative count flag (`-num`, `-limit`, `-concurrency` and the rest) as
  `invalid_argument`; `reviews -limit -1` used to download every review.
- Search, list, similar and developer records claimed `"free": true` for
  paid apps when the row carried no price (`-full=false`); `free` and `price`
  are now absent when the price is unknown.
- `gpscrape` checks arguments before any request: a name where a package id
  belongs, or an empty search or suggest term, is `invalid_argument` (store
  links are taken for their `id=`); unknown `-fields` names are refused up
  front.
- A batch in which every id failed the same way exits with that code (every
  id not found: `not_found`, exit 3) instead of a retryable `store_error`.
  Missing local state (no snapshot, no genre table, one snapshot for `diff`)
  is `not_found`.
- `-fields` keeps `code` and `error` on a failed batch entry, and `pcapp`'s
  error entries carry `code`.
- `availability -countries` accepts Play Store country codes only (`zz` was
  reported available), refuses an empty list, and a run in which every
  country failed exits with their error code instead of 0.
- `reviews` of an unknown app exits `not_found` instead of 0 with nothing
  printed.
- The `-debug` banner reports the build version, the sweep confirmation
  states the generation's shard count, and `gpscrape-mcp`'s HTTP refusal
  names `-token-file`.
- The adaptive throttle no longer slows down on steady latency. Its baseline
  was the minimum of recent responses; Google's sitemap CDN answers some from
  cache in ~22ms and the rest in ~48ms, so ordinary responses read as twice
  the baseline and a live sweep climbed from 200ms to the 2s ceiling with
  every response a 200. The baseline is now the median.
- Adaptive throttle, settling below a remembered refusal: a clean window
  stopped one step short of the floor instead of settling on it, and when
  the floor was slower than the current interval it moved up to it. After
  refusals at `Max` the floor lies above `Max`, so the first clean window
  after an outage took the interval past `Max`. A clean window now never
  slows the client down.
- `WithThrottle` applied after `WithAdaptiveThrottle` now counts as the
  caller's interval: the controller probes past it on `ProbeWindow`, as it
  does with the options in the other order, instead of on `Window`.
  `AdaptivePolicy.Start` wins over `WithThrottle` in either order, as
  documented; before, a later `WithThrottle` replaced it.
- The adaptive throttle no longer stays at `Max` for `CutMemory` after an
  outage. Every refusal was remembered as a rate limit; now only the first
  429 or soft block after an answer is, below `Max`, with the breaker
  closed. Further refusals, refusals at `Max`, refused probes and 503s still
  slow the client but leave no cut.
- A refusal from a request sent before the breaker's pause no longer
  doubles the interval again or leaves a cut; the breaker already ignored
  it.
- One slow response no longer ends the adaptive throttle's clean window;
  only a streak does. With a latency tail past twice the median every few
  dozen responses, the window of 100 needed to probe past the caller's
  interval was almost never completed.
- `catalog sweep` resumes correctly after a kill at any point:
  - a torn last line in the partial id file or the done log is cut back
    before appending, so `com.c` + `com.d` no longer becomes `com.com.d` in the
    snapshot; the torn shard is fetched again whole;
  - a shard's ids are flushed before its done line, so a resume no longer
    skips ids that were never written;
  - the state file is written before the first shard, so a run killed early no
    longer has its work deleted by the next run;
  - the manifest is written last, after the delta, so a kill between them no
    longer leaves a generation that is never diffed;
  - a run killed after its manifest but before cleanup no longer leaves about
    100MB of resume files behind; the next run removes them;
  - `-force` over a generation on disk removes its manifest before replacing
    the snapshot, so a kill cannot leave a sampled snapshot marked complete.
- `catalog sweep` no longer refuses to publish ids it already has: a shard that
  succeeded on resume leaves the failed list, and the done log wins over a
  stale failure in the state file.
- `catalog sweep` stops when a checkpoint cannot be written, instead of
  fetching for hours with nothing to resume from.
- A sitemap shard whose document ends before `</urlset>` fails with
  `ErrShardTruncated` and is retried, instead of passing as a shard with fewer
  apps whose missing ids later surfaced as removals. The end check also covers
  a last `<loc>` past the scan window and any trailing whitespace.
- `catalog genres` stops on a read error in its id list (a corrupt gzip stream
  or an overlong line) before publishing, and for `-ids` before any request;
  before, the list read as a shorter catalog and `-prune` deleted every row
  past the break.
- `catalog genres -prune` against a sampled snapshot is refused, as with
  `-ids`; it used to delete every row outside the sample.
- `gpscrape search -num` above 50 warns that it is clamped instead of silently
  returning at most 50.
- `gpscrape-mcp`: `play_get_apps` no longer panics on a batch entry whose
  error wraps an already-explained error; the entry keeps its code.
- `catalog sweep` and `catalog genres` refuse to publish a file whose name was
  taken by another file while they wrote it. The partial id file replaced
  mid-sweep became the snapshot; a replaced `.tmp` was renamed over the
  snapshot, manifest, delta or genre table; and a replaced
  `genres.tsv.merge` failed every rerun with `unknown mark`. The run now
  fails naming the file, and the rerun starts that work over.
- A failed write of `state.json`, a manifest, a delta or
  `genres.tsv.resume.json` no longer leaves its `.tmp` behind, and a sweep
  whose last checkpoint fails closes its id file and done log.
- A `catalog sweep` resume whose partial id file was removed, emptied or cut
  short since the last run skipped the shards the done log listed, and the
  snapshot lacked their ids. It now sweeps the generation again from the
  start when the file holds fewer ids than the checkpoint recorded, or none
  while the done log lists shards.
- The lock file is fsynced, with its directory, so a power loss can no longer
  leave an empty `sweep.lock` that every later run refuses. A failed
  directory fsync after a rename now names the file it was publishing.
- `gpscrape`: a bad flag value (`-num abc`, an unknown flag) is an
  `invalid_argument` error object on stderr, exit 2, instead of the flag
  package's prose and the whole usage. `-h` still exits 0.
- `gpscrape` refuses as `invalid_argument` a negative `-throttle`, `-timeout`
  or `-adaptive-min` (no throttle, no deadline), `-adaptive-min` without
  `-adaptive` or above `-throttle` (both silently ignored), a `-country` that
  is not two letters, and an empty argument where none is checked yet (a
  developer, a category).
- `-langs all,en`, `catalog sweep -sample` out of range, `catalog size
  -exact` and `catalog sweep -check` are `invalid_argument`, exit 2; they
  were `store_error`, exit 1.
- `permissions -raw` error entries carry `code`.
- `App`, `Similar`, `DataSafety` and `PCApp` escape the app id in the URL: an
  id holding `&` rewrote the query (`x&gl=de` read the German storefront).
- `gpscrape`: a panic exited 2, the status for bad arguments, with no error
  object. It is reported as `internal` and exits 1; the stack still goes to
  stderr.
- `Search` and `Cluster` results after the featured card had no price
  (`priceKnown` false) and no summary, though each grid row carries its offer
  and description: 1 in 20 of a live search had either. Both are read now;
  the summary is the description's first paragraph, as for `List`.
- README lists every error code with its exit status; it named only some
  statuses and none of `not_listed`, `confirmation_required`, `cancelled` or
  `internal`.
- `gpscrape` integer flags are decimal: `-limit 010` is 10, not 8 (the
  flag package read a leading zero as octal), and `0x10`, `0b1` and `1_000`
  are refused as `invalid_argument`.
- `catalog sweep` resume: each done-log line carries its shard's id count
  (`<url>\t<ids>`), so ids lost from the partial file after the last
  checkpoint now restart the generation too; older done logs still read.
  The partial file and the done log are created with their directory
  fsynced.
- Adaptive throttle without a breaker: refusals from requests sent before an
  earlier refusal slowed the client are that episode, and slow it once,
  not once each.
- `suggest` and `categories` refuse `-fields` as `invalid_argument`: they
  print bare strings, and the flag was silently ignored.

### Internal

- A write-fault matrix tests every file `catalog sweep` and `catalog genres`
  write against no space, a short write, a failing fsync or close, a refused
  rename, a read-only directory, the file being replaced mid-run and a
  failing directory fsync, including the torn-line repair on resume: each
  run fails with `store_error` naming the file, leaves the published files
  alone and the lock free, and the rerun matches a clean run byte for byte.

- A kill-and-resume test (`-tags killtest`) kills `catalog sweep` as a real
  process (SIGKILL, SIGINT, SIGTERM, at many points) and compares the rerun's
  snapshot, manifest and delta with an uninterrupted run; `GPSCRAPE_KILLTEST=1`
  runs the long set.
- State-model tests for the adaptive throttle and the breaker
  (`adaptive_model_test.go`): seeded random plans of 10,000 events (clean,
  sporadic refusals, slowdowns, outages, idle gaps, a hard rate ceiling) run
  under `synctest`, with the interval, cut floor and breaker transitions
  checked after every event, and a run that must recover from an outage.
  Three seeds with `-short`, 64 without;
  `-adaptive.seed=N` or `ADAPTIVE_SEED=N` replays one.
- Parsers have allocation budgets and `ReviewsSeq` a memory ceiling, checked
  in CI.
- CI: govulncheck on all four modules on every pull request and weekly; CodeQL
  builds `mcp`; pull requests fuzz each target for 10 s and a weekly job for
  10 minutes; a benchstat report on every pull request (reports, does not
  gate); new linters (noctx, unparam, errorlint, nilerr, wastedassign,
  makezero, mirror) and deadcode, with their findings fixed; `apidoc` is
  linted like the other modules.
- The weekly canary runs the whole-catalog games pipeline (`catalog sweep`,
  `catalog genres`, `catalog apps -genre 'GAME_*'`) on a 0.01% sample, and the
  live CLI canary checks the shared records instead of v1.5's camelCase
  fields. `TestSearchPagination` no longer fails when Google returns one page
  or fewer.
- `gpscrape`: an all-pairs test of the command line (command, `-fields`,
  `-full`, `-lang`, `-country`, `-raw`, `-adaptive`, the id and how many)
  with its own greedy generator, and a checklist of edge values over every
  numeric and duration flag, read from the commands' flag sets. Both run the
  binary against an offline store and check that stdout is JSON lines, that
  the exit status matches the error code, and that nothing panics.
- Error-code traceability: `TestErrorCodeMatrix` in `cmd/gpscrape` runs the
  test binary as `gpscrape` against a fake store and reads each code and exit
  status back from the process; the one in `mcp` reads them from tool errors,
  and a cancelled call's from the wire. Codes come from `records/errors.go`,
  so a new one fails both until a scenario produces it and the READMEs list
  it. `-v` prints the matrix.
- Live tests (integration, the library canary) build their client with
  `LiveClient`/`Evidence`: a failing test writes the last 20 responses --
  URL, status, headers, first 4 KB of body, time -- and the hook events
  around them to `$GPSCRAPE_EVIDENCE_DIR`, or a temporary directory it logs.
  The CLI canary keeps each run's `-debug` log, stderr and stdout head. The
  canary workflow uploads the directory when it fails.
- The canary compares per-endpoint fill rates of key fields and median
  latencies with `testdata/canary_baseline.json` (from a live run;
  `UPDATE_CANARY_BASELINE=1` rewrites it): a fill rate down more than 0.25 or
  under its floor fails, 3x the latency is logged. The baseline is from a
  live run after the grid price and summary fix.

## gpscrape-mcp 0.1.1 (tag `mcp/v0.1.1`) - 2026-11-07

### Changed

- `gpscrape-mcp` returns the shared `records` objects that `gpscrape` writes:
  a failed `play_get_apps` entry gains a `code` field, and tool errors use
  `records.Code`. Other field names are unchanged. `apidoc`, `lightfeed` and
  `mcp` require v1.6.0.

## [1.6.0] - 2026-11-07

### Added

- Package `records`: the output contract the CLI and the MCP server share,
  with snake_case fields, third-party text in `untrusted_*`, and the common
  error codes (`invalid_argument`, `not_found`, `not_listed`, `rate_limited`,
  `timeout`, `busy`, `cancelled`, `confirmation_required`, `store_error`,
  `internal`).
- `gpscrape schema [command]` writes each command's summary, flags and output
  fields as JSON.
- `gpscrape -fields app_id,score` keeps only the named keys; a list that names
  none of a record's keys is an error listing the real ones.
- `gpscrape mcp` starts an installed `gpscrape-mcp` (next to `gpscrape` or on
  `PATH`); it never downloads it.
- `-yes` on `catalog sweep` and an unlimited `catalog ids`: without a
  terminal, these refuse to start their 83,000 requests unless it is given.
- `skills/gpscrape/SKILL.md`: a skill telling agents when to use the MCP
  tools and when the CLI.
- `gpscrape-mcp` releases (`mcp/v*` tags) ship prebuilt binaries, an MCPB
  bundle (Linux arm64 included) and the image `ghcr.io/kryuchenko/gpscrape-mcp`,
  and are listed in the MCP Registry as `io.github.kryuchenko/gpscrape`.
  Release artifacts carry signed build provenance (`gh attestation verify`).
- `gpscrape-mcp`: every tool error starts with a stable code, so a program
  can branch on it.
- `gpscrape-mcp`: `play_check_availability` sends progress notifications to a
  client that passes a `progressToken` (stdio).
- `gpscrape-mcp`: `-token-file` reads the bearer token from a file.
- `gpscrape-mcp` has an icon.

### Changed

- **Breaking, `gpscrape`:** `app`, `search`, `list`, `similar`, `developer`,
  `reviews`, `permissions`, `datasafety` and `availability` write the shared
  `records`, the objects the MCP tools return. `-raw` writes the library's
  types as before. A failed id in a batch is `{"app_id","code","error"}`.
- **Breaking, `gpscrape`:** when stderr is not a terminal (or with
  `GPSCRAPE_ERRORS=json`; `=text` forces a line), a failed run writes
  `{"error":{"code","message","retryable"}}` to stderr. Exit status follows
  the code: 2 bad arguments or confirmation needed, 3 not found, 4 rate
  limited, 5 timeout, 130 interrupted, 1 other.
- **Breaking, `gpscrape-mcp`:** tools are renamed to `play_` + verb + object:
  `search` → `play_search_apps`, `app` → `play_get_app`, `apps` →
  `play_get_apps`, `list` → `play_get_chart`, `reviews` → `play_get_reviews`,
  `similar` → `play_get_similar_apps`, `developer` →
  `play_get_developer_apps`, `permissions` → `play_get_permissions`,
  `datasafety` → `play_get_data_safety`, `suggest` → `play_suggest_queries`,
  `categories` → `play_list_categories`, `availability` →
  `play_check_availability`.
- **Breaking, `gpscrape-mcp`:** the secret path `/mcp/<token>` (for ChatGPT)
  is off unless `-secret-path` is given, because a token in a URL leaks into
  logs and history.
- **Breaking, `gpscrape-mcp`:** `-token` is removed, since a command-line
  secret is visible in `ps`. Use `$GPSCRAPE_MCP_TOKEN` or `-token-file`.
- The library requires Go 1.27 (was 1.25, which no longer gets security
  fixes).
- `gpscrape -h` opens with examples and documents the output, error format
  and exit statuses.
- `gpscrape-mcp`: every tool publishes an output schema and the server checks
  each result against it; `tools/list` grows from about 9.8 KB to 17.5 KB.
- `gpscrape-mcp`: `tools/list` and `server/discover` report a `ttlMs` of an
  hour instead of 0.
- `gpscrape-mcp`: a cancelled call returns no result even if its work
  finished.
- `gpscrape-mcp`: at most four calls per concurrency slot wait; the next is
  refused at once as `busy`. String arguments have a maximum length.
- `gpscrape-mcp` points agents that can run shell commands at the installed
  `gpscrape` CLI for bulk review jobs (`-limit`, `-langs all`), and tells them
  to ask before installing it. The server instructions are shorter and open
  with the untrusted-data warning; every tool description stays under 1000
  bytes, where Codex truncates.

### Fixed

- The CLI no longer takes `/dev/null` for a terminal, so commands started with
  `< /dev/null` (agents, cron) are treated as non-interactive.
- `gpscrape-mcp` over stdio skips a line that is JSON but not JSON-RPC
  (`{}`, `null`, `"jsonrpc":"1.0"`) instead of ending the session.
- `gpscrape-mcp` over HTTP checks `Origin` on every method; a foreign or
  malformed `Origin` gets 403.
- `gpscrape-mcp`: `play_list_categories` no longer claims `openWorldHint`.
- `gpscrape-mcp`: a call cancelled while waiting for a slot is logged as a
  cancellation, not "server busy".
- `gpscrape-mcp`: the first 512 bytes of the server instructions end at a
  paragraph boundary instead of mid-sentence.
- `-lang` is validated by every command: a list or `all` (which Google
  answered in English, exit 0) is refused and `reviews` points at `-langs`.
  Values are trimmed, so `-lang " ru "` means `ru`.
- `gpscrape reviews -langs` rejects malformed entries and `all` mixed with
  individual codes.
- `gpscrape catalog -h` prints the catalog usage instead of failing.

### Internal

- CI lints and vulnerability-scans the `mcp` module, dependabot watches its
  dependencies and the container's digest-pinned base images, and workflows
  pin actions to commit SHAs. A weekly workflow checks that Claude Code and
  Codex still pass the server instructions to the model. A test pins every
  `records` field name, and a CLI test no longer fails on a large `go test`
  log.

## [1.5.0] - 2026-11-05

### Added

- `mcp/`, a separate module with `gpscrape-mcp`: an MCP server exposing the
  store's quick lookups as twelve read-only tools, over stdio or stateless
  Streamable HTTP with a bearer token (or the token as the last path segment,
  for ChatGPT). Store text comes back in `untrusted_*` fields. See
  `mcp/README.md`.
- `Client.PCApp` and `gpscrape pcapp` read an app's Google Play Games on PC
  listing: PC screenshots, trailer (`Video`, `YouTubeID`, `PreviewVideo`),
  icon, header image, PC rating and system requirements. An app without a PC
  build returns `OnPC: false` and a nil error.
- `PCRegions()`: the 141 regions where Google Play Games on PC is offered, a
  snapshot of Google's help page from 2026-10-28; the PC store itself serves
  listings to unsupported regions.
- `SearchResult.PriceKnown`: false on search, similar and developer rows,
  which carry no price and used to report every app as free, unless
  `FullDetail` is set.
- `App.DeveloperNumericID`: Google's numeric developer id, the stable field
  to key on.
- `ErrNoDataSafety` and `ErrNoReviewsPage`, to tell "nothing to read" from
  data with `errors.Is`.
- Soft-block detection: a `google.com/sorry` redirect, a page without data
  blocks, or HTML where a `batchexecute` envelope belongs returns a
  `*SoftBlockError`; consent and sign-in pages (`VerdictInterstitial`) are not
  retried. See `docs/adr/0001-soft-blocks-and-cooldown.md`.
- A breaker on the adaptive throttle (`AdaptivePolicy.Cooldown`,
  `MaxCooldown`, `MaxProbes`): after a refusal the client pauses and probes;
  repeated refusals stop it with `ErrBlocked`. `Client.Breaker` reports the
  state and `Hooks.OnBreaker` each change.
- A client-wide retry budget: `RetryPolicy.Budget` and `Burst`.
- `gpscrape -adaptive` turns on both (30s cooldown, one retry per ten
  requests) and reports breaker changes on stderr.

### Changed

- `gpscrape search`, `list`, `similar` and `developer` fetch full details by
  default (one extra batched request per 32 apps), so prices, `developerId`
  and taglines are filled; `-full=false` restores the old output.
- `DataSafety` returns `ErrNoDataSafety` instead of an empty `*DataSafety`
  (which read as "collects nothing") or `nil, nil`.
- An empty reviews payload returns `ErrNoReviewsPage`.
- A block page served with status 200 returns a `*SoftBlockError` instead of
  an empty result. `Hooks.OnResponse` fires after the body is classified.
- `Search` returns an error when `FullDetail` lookups are refused, instead of
  un-enriched results.

### Fixed

- `Search` returns up to 50 results again (`Num` above 50 is clamped; was
  250); a `Price` filter returns about 20. (#16)
- `Search` returns the featured card for an exact match first, so
  "minecraft" returns Minecraft. (#13)
- `AppsMany` no longer reports an app missing because Google dropped it from
  one pack; such items are asked again, and a 404 returns `ErrNotFound`. (#18)
- `App.Categories` is filled with the genre and the store's tags.
- `App.Summary` and `SearchResult.Summary` decode HTML entities.
- `List` rows' `Summary` is the description's first paragraph as plain text,
  not the whole HTML description.
- `DataSafety` for an unlisted id returns `ErrNoDataSafety` wrapping
  `ErrNotFound` instead of a block that paused an adaptive client.
- `App.VideoImage` is the trailer thumbnail again, not the header image.
- `App.DeveloperID` holds the decoded developer id (a number or a name) that
  `Developer` accepts, not the listing path; search and feed results match.
- `Developer` sends 19-digit numeric ids to the numeric listing.

## [1.4.0] - 2026-09-05

### Added

- `cmd/gpscrape`, a command-line interface that writes newline-delimited JSON.
  Paging commands write as they go, and Ctrl-C stops a run without discarding
  what it has written.

      gpscrape app com.spotify.music
      gpscrape reviews com.spotify.music -limit 500 | jq -r .text
      gpscrape availability com.spotify.music -countries us,de,jp
      gpscrape catalog ids -shards 0-99 -ids-only > ids.txt

- `gpscrape catalog`, a group of verbs for keeping a local copy of the store's
  app list:

      gpscrape catalog check     2 requests       has Google republished?
      gpscrape catalog new       17 requests      what the store lists as new
      gpscrape catalog size      ~900 requests    how many apps, to a stated error
      gpscrape catalog genres    ~3.4k requests   genre changes and removals
      gpscrape catalog sweep     83,445 requests  the complete list
      gpscrape catalog apps      none             the id list by genre, from disk
      gpscrape catalog diff      none             compare two snapshots
      gpscrape catalog ids       as asked         stream ids, keep no state

  `genres` costs about 3,400 requests over a full snapshot, plus roughly 310
  per extra `-confirm-gone` storefront, and finds removals and genre changes
  but not unknown apps. `new` reads ranked lists, so it misses apps with no
  traction; only `sweep` is complete. `diff` prints how much of the sweep the
  cheap signal had already reported.
- `catalog sweep` maintains a snapshot directory. It skips the sweep when the
  generation has not changed (two requests), resumes an interrupted run from
  the shards it has not reached, and restarts if the generation rolls
  mid-sweep. Each completed sweep writes a sorted, deduplicated snapshot, a
  manifest with the generation id and a checksum, and a delta against the
  previous snapshot.
- `catalog sweep -sample PCT` sweeps a uniform fraction of the shards, an
  unbiased sample of the catalog (a 0.01% sweep: eight shards, two seconds).
  The seed is recorded in the manifest and derived from the generation by
  default, so a rerun samples the same shards. `catalog diff` refuses two
  snapshots swept at different coverage.
- `Client.CatalogSize` and `gpscrape catalog size`: the app count to a stated
  error in about ninety seconds (3,549,110 +/- 35,366 at 95% from 866 of 83,445
  shards). The result carries the variance/mean check behind the interval and
  declines to vouch for an estimate when it drifts. An exact count needs
  `catalog sweep`.
- `gpscrape catalog apps`: the app list by genre from the genre table, offline;
  `catalog apps -genre 'GAME_*' -ids-only` is the game index.
- A snapshot directory takes one writer at a time: `sweep` and `genres` hold
  `sweep.lock` (`{"pid","host","started"}`), a second run is refused with the
  holder's pid, host and start time, and a lock left by a dead process on this
  host is cleared on the next run. Other catalog verbs take no lock.
- A second Ctrl-C exits at once with status 130, printing
  `gpscrape: interrupted again, exiting now`, and leaves `sweep.lock` for the
  next run to clear.
- `AppsMany`, `PermissionsMany` and `SuggestMany` pack up to 32 lookups into one
  request. The throttle meters requests, so 32 apps take 146ms instead of 9.76s,
  at about 20KB per app instead of a megabyte of markup. Results are returned
  in the caller's order whatever order Google answers in. `App` remains the
  reference for a single app. `gpscrape permissions` takes several app ids.
- `WithRetry(RetryPolicy)`: exponential backoff with full jitter, optional
  `Retry-After`, and a `Retryable` predicate that by default retries transport
  errors, 429 and 5xx but never 404. Every attempt goes back through the
  throttle.
- `WithAdaptiveThrottle(AdaptivePolicy)` finds a safe request rate: additive
  increase, multiplicative decrease, with the full decrease for 429/503, a
  gentler one for other 5xx and transport errors, and the gentlest for latency
  after a streak. It starts from the `WithThrottle` interval, never exceeds
  `Max`, and lets `Retry-After` only slow it down. `gpscrape -adaptive` turns
  it on, with `-throttle` as the floor.
- `WithHooks(Hooks)` (`OnRequest`, `OnResponse`, `OnRetry`), for metrics or
  tracing without a dependency.
- `WithLogger(*slog.Logger)` and `LevelTrace`. The client is silent by default
  and logs at Debug at most: requests, responses, retries, throttle waits and
  adaptive interval changes; `LevelTrace` adds `httptrace` timings. Bodies and
  headers are never logged. `gpscrape -debug` (or `GPSCRAPE_DEBUG=1`) logs to
  stderr, `-log-file` to a file, and `-trace FILE` writes a bounded (64MB)
  flight recording of the run's last minutes at exit, for `go tool trace`.
- `runtime/trace` tasks and regions, so `go tool trace` attributes each request
  to its call and separates throttle time from Google's. Free while tracing is
  off.
- `Client.ReviewsSeq` and `Client.CatalogSeq`, iterators that let a caller stop
  early. The error slot carries terminal errors only; per-shard failures still
  go to `opts.OnShardError`.
- `Client.CatalogShardSeq` and `CatalogOptions.ShardURLs`: a sweep with the shard
  as its unit, so a caller can resume from the URLs it did not reach. A failed
  shard arrives in the stream with its URL.
- `Client.DigestsSeq` resolves a few fields (genre, and whether the app is
  `Listed`) for very many apps, a thousand per request at 178 to 243 bytes per
  app; it takes an `iter.Seq[string]`, so memory stays flat. A frame that never
  arrived is an error, not an absence.
- `Generation` and `Client.SitemapGeneration`: whether Google has republished
  the catalog, in two requests. Builds order numerically, `Built` reads the
  build time from the run id, and `GenerationOf` refuses a shard list that
  spans two builds.
- `gpscrape reviews -langs en,ru,de` and `-langs all`, and `ReviewLanguages`
  (71 codes). Each language serves its own review corpus and the corpora do not
  overlap, so reading all reviews means reading every language; records carry
  their corpus and ids are deduplicated across aliases. The country parameter
  does not affect reviews.
- `CollectionNewFree`, `CollectionNewPaid` and `CollectionMoversShakers`, and a
  `gpscrape list` command for collections. `NEW_FREE` across the seventeen
  `GAME_*` categories returns about 1,230 apps in 17 requests, 99% published
  within thirty days.
- Package documentation for `go doc` and pkg.go.dev.

### Changed

- **Breaking:** `Availability.Status` marshals as its name (`available`,
  `not_in_region`, `not_found`, `error`, `unknown`) instead of a number, so
  `gpscrape availability` writes `{"cn":"not_in_region"}`. `UnmarshalJSON`
  still reads the numbers.
- **Breaking:** `catalog sweep -check` is removed in favour of `catalog check`;
  the flag fails with a message naming it.
- Minimum Go version is 1.25 (was 1.24); only `GOTOOLCHAIN=local` builds are
  affected.
- Page parsing is 7 to 36 times faster and allocates 51 to 96% less (app page
  9.72 to 0.27 ms, search 20.89 to 2.86 ms, charts 56.11 to 6.92 ms, sitemap
  shard 0.60 to 0.048 ms). Wall-clock gains are small because the library is
  network-bound; the gain is memory and GC load. `App` decodes only `ds:5`.
- Catalog sweeps use far less memory: the sitemap index is scanned instead of
  unmarshalled (94MB of allocation down to the 7.2MB of strings it holds), and
  shards stream instead of being buffered (15.34MB to 0.73MB per shard). The
  final phase of a sweep at 3.7 million ids runs in about 1s instead of 3.1s at
  half the peak memory.
- Review pagination asks for 1000 reviews per request instead of 150: a busy
  app's month of reviews takes 6 requests and 4.3s instead of 39 and 19.3s.
  `Reviews` still defaults to 150.
- `Availability` reads the details RPC instead of the page and asks for the one
  field it needs: a 177-country sweep moves about 3.5MB instead of 227MB.
  It is not faster under a throttle, which stays one request per country.
- `AppsMany`, `PermissionsMany` and `SuggestMany` send their packs over
  `WithConcurrency` workers.
- `gpscrape` buffers NDJSON output to a pipe or file; terminals stay
  unbuffered so lines appear as they are produced.
- `catalog sweep` with nothing to do prints the current snapshot's manifest
  instead of nothing; `completedAt` tells a fresh sweep from an existing one.
- `genres.tsv` is cumulative: an id that leaves the sitemap without being seen
  gone keeps its row until `-prune`. Compare `wc -l genres.tsv` with `ids` in
  the manifest to spot drift.
- `ReviewsComprehensive` streams each rating instead of collecting it.
- `ReviewLanguages` is a function returning a copy, not an exported slice.

### Deprecated

Both will be removed in v2 and keep working throughout v1; `go fix` does not
migrate them.

- `Client.ReviewsAll`, in favour of `Client.ReviewsSeq`. It buffers the whole run
  and stops at 500 by default.
- `Client.EnumerateCatalog`, in favour of `Client.CatalogSeq`, which can stop
  early.

### Removed

- **Breaking:** `PermissionsOptions.Short`. It was never implemented.

### Fixed

- `List` no longer returns the top-free chart, with no error, for a collection
  the HTML fallback cannot locate. A transient RPC failure on "what is new"
  used to return the most popular apps labelled as new releases; the fallback
  now refuses such a collection before spending the request, and for
  `NEW_FREE`, `NEW_PAID` and `MOVERS_SHAKERS` returns that refusal as the error
  instead of `(nil, nil)`.
- Sampled snapshots are no longer mistaken for complete ones. After
  `catalog sweep -sample`, the full sweep never ran, `catalog check` reported
  up to date, `catalog genres` rebuilt its table from the sample, and
  `catalog diff` trusted a renamed sample's filename. Every reader of a
  snapshot directory, `catalog apps` included, now checks the manifest.
- `catalog sweep`:
  - resuming onto a different `-sample` panicked or wrote a manifest claiming
    coverage it lacked; the resume state carries the sampling and restarts
    cleanly on a mismatch;
  - `-sample 100` wrote a manifest saying the catalog held no apps; 100 now
    means the whole catalog, as 0 does, other values outside (0,100] are
    refused, and a sweep that collected nothing publishes no manifest. The
    range error now says `0` or `(0,100]`, `-sample 0` is announced as
    sweeping every shard, and a sampled resume states its coverage;
  - a `-sample` sweep after a complete one wrote a delta calling every id
    outside the sample removed (about 3.5M in production); a delta is now
    written only between two complete snapshots of different generations,
    and stderr says why when it is not;
  - the first write failure (a full disk) now stops the sweep with exit 1
    naming the file, instead of fetching up to 83,000 more shards for nothing;
  - the snapshot is written durably (tmp, fsync, rename) like the manifest, so
    a crash cannot leave a manifest describing a truncated file; the genre
    table's writer no longer leaves a `.tmp` behind and syncs the directory;
  - resume state from rolled generations (about 99MB per roll) is removed, and
    the sweep reports how much it freed; `-keep N` keeps the newest N
    snapshots, and now also deletes their deltas, which it never matched;
  - it prints its manifest as one NDJSON record instead of nothing on stdout;
  - a resumed sweep no longer fetches failed shards twice or reports a negative
    remaining count, a panic no longer skips the exit hooks (trace and log
    file), and the partial file is no longer leaked when the done log fails
    to open.
- `catalog ids`: a positional argument (`catalog ids 0-9`) was ignored and all
  83,445 shards swept; catalog verbs without positional arguments now refuse
  them. It emits JSON records (`-ids-only` for bare ids), and asking for
  shards that do not exist is an error.
- `-shards " "` or `-shards ","` swept every shard; a `-shards` that names
  none is an error, ranges are bounded (`-shards 0-9999999999` is refused),
  and repeats are collapsed.
- `catalog check` defaulted `-dir` to `""` instead of `catalog`, so it always
  answered not up to date and a consumer swept daily.
- `catalog genres`:
  - a transient error or an `-ids` subset dropped apps from the table, which
    then came back as `first_seen`; rows now persist, and `-prune` (refused
    with `-ids`) drops rows the snapshot no longer lists;
  - an app invisible from one storefront was called gone; apparent removals
    are re-checked in the `-confirm-gone` storefronts (about 310 requests per
    storefront, against 3,400 for the main pass; empty trusts `-country`
    alone), and a `gone` record names the storefronts that answered;
  - a lookup that failed in every confirm storefront was reported `gone` and
    its row deleted; it is now `"change":"error"` and keeps its row;
  - a run in which every lookup failed published an empty table at exit 0; it
    is refused, and a partly failed run says how many lookups failed;
  - memory at catalog scale fell from 766MB to 609MB by keeping a delta
    instead of a second copy of the table.
- `catalog apps` streams the genre table instead of loading it: 83MB instead of
  422MB on the real catalog, same output. `-allow-sample` reads a sampled
  table and reports its coverage.
- `catalog apps` and `catalog diff` no longer accept the network flags
  (`-lang`, `-country`, `-throttle`, `-concurrency`, `-timeout`, `-adaptive`)
  they ignored; a stray one is an error.
- `catalog apps -genre`, `catalog new -categories` and `reviews -score` reject
  invalid values instead of returning empty or unfiltered output at exit 0.
- `catalog new` exits 1 when every category failed, instead of an empty stdout
  that looked like "nothing new".
- `catalog diff -changes` emits one record per changed id; before, it reported
  only that ids had changed.
- `catalog size`: `-precision NaN` is refused; small samples use Student's t
  instead of 1.96 (at `-pilot 2` it claimed 4.2% where the true figure is
  27%); the record says whether it met the requested precision.
- `catalog new`'s signal precision is computed over one generation window
  rather than the whole log, so it no longer falls as the log ages; an emit
  failure flushes the signal log.
- `reviews -langs ALL` sent `hl=all` and read one empty corpus; it now reads
  all 71. `-throttle` now applies between languages (one client per run).
- `gpscrape sync`, a pre-release second name for `catalog sweep`, is not
  shipped.
- `FullDetail` in `Search`, `List`, `Similar` and `Developer` uses the batched
  RPC instead of one page per result: a 32-result listing went from 9.73s over
  33 requests to 0.44s over 2. `WithConcurrency` no longer affects enrichment.
- `Permissions` and `PermissionsMany` return an error for a missing app
  instead of `(nil, nil)`, which looked like an app declaring no permissions.
- `AppsMany`, `PermissionsMany` and `SuggestMany` reject an empty id or term
  up front, as the singular forms do.
- A `batchexecute` frame that never arrived is an error (`StatusFetchError`,
  the country in `Result.Errors`) rather than an empty answer, so dropped
  frames no longer report a live app as delisted (`Availability`,
  `GloballyRemoved`), no suggestions, or no permissions.
- A cancelled context wraps both the cancellation and the last status, so
  `errors.Is(err, context.Canceled)` works whenever it lands.
- `DigestsSeq` no longer hangs a caller that breaks out early.
- `WithAdaptiveThrottle` bounds are enforced from the first request: a
  `WithThrottle` above `Max` or a `Start` outside `[Min, Max]` is clamped in
  `NewClient` whatever the option order.
- `WithTimeout` no longer changes the `*http.Client` passed to
  `WithHTTPClient`; the client is shallow-copied first.
- `ReviewsAll` pages at `min(Count, 1000)`, so `Count: 10` is one small
  request. `ReviewsComprehensive` returns the last error when every rating
  pass failed and nothing was collected.
- `gpscrape app` and `gpscrape permissions` with several ids emit one line per
  id, in position, with an inline error record for a failure;
  `gpscrape permissions` is one record per app even for a single id. A single
  `gpscrape app ID` keeps its fields.
- `catalog sweep -check` is removed (see Changed); it duplicated
  `catalog check`.
- `gpscrape version` emits `{"version":"v1.4.0"}` and reports the module
  version for `go install ...@latest` builds instead of `devel`.
- Help text: `-trace FILE` instead of `-trace go tool trace`, and `search -full`
  no longer says "one request each".
- The `lightfeed` and `apidoc` submodules can be resolved from outside this
  repository; they required an untagged root version.
- The catalog crawler checks `Flush` and `Close` on its output file, so a full disk no
  longer yields a silently truncated result.
- `parseTimestamp` no longer discards the sub-second parse error.

### Security

- Six vulnerabilities with call traces into this code were reachable through
  the previous toolchain. `govulncheck` now runs in CI.

### Internal

- Every request-making operation opens its own `runtime/trace` task, so a
  trace tells `CatalogSize` from a sweep and batched calls from singular ones.
- `Reviews` uses the shared batch layer (identical results live); non-200
  bodies are drained up to 32KB for HTTP/1.1 connection reuse; the radix sort
  and partial file reader guard against corrupt input (64-byte prefix, 1MB
  line).
- Benchmarks over recorded fixtures and fuzz targets for every parser (no
  panics in 2.4 million executions). An earlier claim that json v2 gave +21%
  throughput did not reproduce and is withdrawn: the difference is 0.1%,
  inside the noise.
- Tooling: a release workflow cross-compiling `gpscrape` for linux, darwin
  and windows on amd64 and arm64 with checksums; CodeQL; Dependabot;
  `.golangci.yml` with no exclusions; Redocly and Spectral lint of the
  generated OpenAPI spec (`apidoc/.spectral.yaml`); `lightfeed` built and
  tested in CI; read-only workflow tokens except the coverage badge job;
  GitHub Actions on their Node 24 majors.
- `SECURITY.md` (including what is not a vulnerability: rate limiting, fields
  lost to upstream changes) and `CONTRIBUTING.md`; a committed `go.work` for
  local development; the coverage badge measures the library only.
- The catalog sweep shares one engine between `CatalogSeq` and the deprecated
  `EnumerateCatalog`; dead helpers left by the batching migration are deleted;
  parser tests that asserted nothing are fixed; the differential fuzz tests
  no longer fail on an unwrapped `<loc>` Google never serves.
- Live drift and canary checks for the batched details request and the new collections and availability classes; differential fuzz tests hold each new scanner against the parser it replaced.
- Benchmarks (`BenchmarkOracle*`) reproduce the parsing table; source modernised with `go fix` (`any`, `strings.SplitSeq`); review URLs built by concatenation.

## [1.3.1] - 2026-06-15

### Internal

- The generated OpenAPI spec (`apidoc/`) uses the JSON Schema 2020-12
  `examples` array instead of the deprecated `example`, and lints clean under
  Redocly and Spectral. The Go API is unaffected.

## [1.3.0] - 2026-06-15

### Added

- Full-catalog enumeration through Google Play's public sitemaps, the only
  anonymous channel that lists the whole store (about 3 million apps in ~83k
  gzipped shards). `EnumerateCatalog(ctx, emit, CatalogOptions)` calls `emit`
  once per package id; it uses the client throttle, skips a failed shard
  instead of aborting, and returns a partial result on cancellation.
  `CatalogOptions` takes `Concurrency`, a `Shards` subset for sampling or
  resuming, and `OnShardDone`/`OnShardError` callbacks. The lower-level steps
  are exported (`SitemapIndexURLs`, `SitemapShards`/`AllSitemapShards`,
  `SitemapShardPackages`), and `examples/catalog-crawler` writes the ids to a
  file. No new dependencies.

## [1.2.0] - 2026-06-13

### Added

- `ClusterOptions.FeedMode` (and `CoverageOptions.ClusterFeedMode`) chooses how
  a category page's feed is followed: `FeedNone` (initial grid only),
  `FeedLightweight` (one stateless extra page, what `FollowFeed: true` did) or
  `FeedBrowser` (deep scroll in a browser). `FollowFeed` is deprecated and maps
  to `FeedLightweight`.
- `FeedBrowser` runs through the optional `lightfeed` submodule, which drives
  Lightpanda over CDP and reaches ~130 to ~149 `GAME_ACTION` apps where
  `FeedLightweight` reaches ~77. The root module stays dependency-free:
  `FeedBrowser` needs an injected `FeedPaginator`, otherwise
  `ErrFeedPaginatorRequired`.
- `Availability` and `AvailableCountries` report a per-country `Status`
  (`StatusAvailable`, `StatusNotInRegion`, `StatusNotFound`, `StatusFetchError`)
  across `AllCountries`. A failed country does not abort the run; cancellation
  returns the partial result. `GloballyRemoved` is set when every conclusively
  probed country returned 404. Example: `examples/app-availability`.
- `CategoryApps` unions many slices of a category (collections, locales, age
  buckets, search terms, the `Similar`/`Developer` graph) to get past the
  ~200-app ceiling; one `GAME_ACTION` run reached ~1,800 unique apps. It covers
  the commercially visible layer, not the full catalog. Ships with
  `CoverageLocales` and `examples/category-coverage`.
- 15 new `App` fields: `AdSupported`, `OffersIAP`, `IAPRange`, `OriginalPrice`,
  `DiscountEndDate`, `IsAvailableInPlayPass`, `Preregister`,
  `EarlyAccessEnabled`, `Video`, `VideoImage`, `HeaderImage`, `PreviewVideo`,
  `RecentChanges` and the EU DSA trader fields `DeveloperLegal*`.

### Changed

- **Breaking:** `App.Released` is an `int64` Unix time in seconds (`0` when
  absent), like `App.Updated`, instead of a string such as
  `"1.401196337e+09"`. In JSON, `released` is a number.
- **Behavior change:** `App.Available` was always `true`; it now reflects the
  listing, and is `false` for a region-locked or pre-registration app.
- `AvailabilityResult.Errors` serializes as `country → message` instead of
  empty objects; in Go it stays `map[string]error`.

### Fixed

- `App.Histogram` was mis-indexed: the 5-star bucket was always 0 and the
  1-to-4-star counts were reversed. `hist[0]` is now 1 star through `hist[4]`
  5 stars, and the buckets sum to `Ratings`.

### Internal

- One table-driven decoder for the four RPC row layouts; the `apidoc` OpenAPI
  spec of Google's private endpoints, validated in CI; pull-request CI runs
  offline with `-short`, and a scheduled canary checks the live store.

## [1.1.0] - 2026-06-10

### Added

- `WithConcurrency(n)` fetches `App()` details in parallel for listings with
  `FullDetail: true`. The default is 1; order is preserved and `WithThrottle`
  still bounds the rate.
- `WithHTTPClient(*http.Client)` for proxies, custom transports and tests. The
  client must follow redirects and keep cookies.
- Failed requests return `StatusError{Code int}`, so callers can tell 404 from
  429 with `errors.As`. The error string is unchanged.
- `go test -short` runs fully offline against saved responses in `testdata/`.

### Changed

- **Behavior change:** `Search` clamps `Num` above 250 to 250, like `List`
  (660) and `Cluster` (5000), instead of returning an error. Callers that
  matched the `"can't exceed 250"` error should check bounds themselves.
- The throttle no longer sleeps while holding its lock, so concurrent callers
  do not serialize; the minimum interval is enforced fairly and stays
  cancellable.
- `Search` picks the data array with the most apps instead of the first match,
  fixing queries that returned a single garbage result.
