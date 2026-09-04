# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

## [1.4.0] - 2026-09-05

### Added

- `AppsMany`, which fetches details for many apps in a handful of requests.

  `App` reads the `ds:5` script block out of a ~1MB HTML page, one page per
  app. That block is not authored by the page: it is the response of an RPC the
  page issues, and every app page carries an `AF_dataServiceRequests` map that
  names both the RPC — `Ws7gDc` — and the exact request body used to produce
  it. Calling that RPC directly returns a payload structurally identical to
  `ds:5`, so the existing extractor parses it unchanged.

  Measured on 32 apps at a 300ms interval: 9.76s over 32 page fetches against
  146ms over one batched request, the same 29 apps resolving either way. The
  transfer drops with it — about 20KB per app instead of a megabyte of markup.
  Against the live store every static field matches the page exactly; only
  rating and review counters differ, by the few units they genuinely moved in
  the seconds between the two requests.

  Because the request body is published in every app page, drift is detectable
  rather than something to discover from empty results:
  `TestWs7gDcTemplateStillMatchesThePage` re-extracts it live and fails if
  Google changes it.

  `App` remains the reference for a single app: it reads a rendered page, and a
  field that only ever appeared in the markup rather than in `ds:5` would not
  survive the batched path.

- `SuggestMany`, the same packing for search suggestions. This is the operation
  the batching was measured on: 64 terms took 18.94s one at a time and 0.65s at
  32 per request.

- `PermissionsMany`, which fetches permissions for many apps in a handful of
  requests instead of one request per app.

  A `batchexecute` POST has always been able to carry several RPCs -- the
  `f.req` body is an array of call tuples -- but every method here sent exactly
  one. That matters because the throttle meters requests, not RPCs: 64 apps at
  a 200ms interval is 64 intervals one way and 2 the other. Measured against
  the live endpoint, 64 calls took 18.94s unpacked and 0.65s at 32 per request,
  with the returned payloads byte-identical to the one-at-a-time fetch.

  The endpoint accepts at least 64 calls per request and will mix different
  rpcids in one POST; the pack size here is 32, deliberately short of what was
  seen to work. What is *not* established is how Google's own rate limiting
  counts a batched request. The wall-clock saving is arithmetic and certain --
  there really are fewer requests -- but the quota question was not measured,
  and this release already learnt once that an endpoint tolerating a burst is
  not an endpoint tolerating sustained load.

  Answers come back in whatever order Google finishes them: a request sending
  indices 7 and 9 was observed returning 9 then 7. Pairing responses to calls
  positionally would hand back another app's data, silently and only sometimes,
  so `decodeBatchFrames` keys frames by the index echoed in the request and
  `batchCall` restores the caller's order.

  `gpscrape permissions` now takes several app ids and packs them the same way.

- `cmd/gpscrape`, a command-line interface. Newline-delimited JSON on stdout,
  so the output composes with `jq` and anything else that reads a stream; the
  paging commands write as they go rather than at the end. SIGINT is a
  first-class stop rather than an abort — a catalog sweep is 83k requests, and
  what was already written stays valid.

      gpscrape app com.spotify.music
      gpscrape reviews com.spotify.music -limit 500 | jq -r .text
      gpscrape availability com.spotify.music -countries us,de,jp
      gpscrape catalog ids -shards 0-99 -ids-only > ids.txt

  `gpscrape catalog sweep` maintains a catalog snapshot directory as a batch
  job rather than a call. Shard filenames carry the id of the generation that
  produced them, so noticing that nothing has changed costs two requests
  instead of 83k — and Google does not regenerate daily. Within a generation
  shards are immutable, which is what makes a sweep resumable: an interrupted
  run continues from the shards it has not reached, and a 404 mid-sweep means
  the generation rolled and the run restarts.

  Each completed sweep writes a sorted, deduplicated snapshot, a manifest with
  the generation id and a checksum, and a delta against the previous snapshot.
  The delta is the point: "which apps appeared in the store since last time" is
  a few hundred kilobytes against a snapshot's tens of megabytes.

  The final phase of a sweep -- reading the collected ids, sorting,
  deduplicating, compressing and diffing -- is where the person who ran a
  one-shot crawl is actually sitting and watching. Measured at 3.7 million ids
  it was 3.1s and peaked at 535MB; it is now around 1s and peaks at about half
  that. Compression is split across cores by concatenating independent gzip
  members -- legal per RFC 1952 and read as one stream by both stdlib and the
  system gunzip -- which is 5.4x faster at exactly the same 18.0MB, rather than
  buying speed by lowering the compression level. Reading the partial file whole and slicing it avoids 3.7 million
  per-line allocations; the delta streams the previous snapshot instead of
  loading it; and the sort is an in-place parallel American Flag Sort.

  The sort is worth a note because the obvious answer was wrong. Radix sorting
  measured *slower* than `slices.Sort` single-threaded on this data (903ms
  against 845ms) -- package ids share only three or four leading bytes, not
  enough to repay the bucket setup. What it wins is that its top-level buckets
  are disjoint ranges of the same array, so they sort concurrently with no
  merge step: 300ms and **zero allocation**, against 290ms and 178MB for a
  parallel merge sort.

  Those three figures were measured on the real catalog and none of them can be
  re-run from this repository: there is no benchmark in `cmd/gpscrape`, and the
  merge sort they were compared against was never committed. Treat the
  single-threaded comparison in particular as data-dependent rather than
  settled -- against synthetic ids of the same shape it comes out 13% the other
  way. The claim that survives without the numbers is the structural one, and
  it is the reason for the choice: disjoint buckets parallelise without a merge,
  and `TestSortStringsDoesNotAllocate` and `TestSortStringsMatchesSlicesSort`
  hold the implementation to that.

  stdlib-only, and inside the root module, so it ships and versions with the
  library and does not disturb the zero-dependency invariant.
- `WithRetry(RetryPolicy)`. Nothing retried before: a caller who wanted to
  survive a transient 503 wrote the loop themselves, and that loop almost
  certainly did not re-enter the throttle — which made the *next* failure more
  likely rather than less. Exponential backoff with full jitter, optional
  `Retry-After`, and a `Retryable` predicate that by default repeats transport
  errors, 429 and 5xx but never a 404, because a 404 is an answer.

  Retry sits outside the throttle and inside the caller: every attempt reserves
  its own slot, so a run of failures spreads out instead of punching through
  the rate limit exactly when the server is asking for less.

- `WithAdaptiveThrottle(AdaptivePolicy)`, which finds a safe request rate
  instead of asking the caller to guess one.

  The guess mattered more than it looked. A live measurement against Google
  found the sitemap CDN accepting **2,880 requests at a sustained 32 rps for 90
  seconds with no failures and p95 flat at ~100ms**. The 200ms this package
  used as its example is six times more conservative than that, which turns a
  catalog sweep into four and a half hours of mostly waiting.

  The details endpoint is a different matter, and finding out how different was
  worth more than the speedup. A 48-request burst was clean to 12 rps; seven
  hundred requests over a few minutes were not, costing seven to ten countries
  of a 177-country sweep to fetch errors and driving the controller back to its
  ceiling. A sweep that took 16 seconds on a fresh quota took over three
  minutes on a used one. Both numbers are real; only one of them is a limit.

  Additive increase, multiplicative decrease, driven by what the server says.
  An explicit 429 or 503 gets the full decrease; an unexplained 5xx or a
  transport error gets a gentler one, since it is as likely to be a fault as
  capacity feedback; latency gets the gentlest, and only after a streak.

  That grading is the correction of a mistake. Delay-based control assumes the
  queue it measures is one the sender created -- LEDBAT is built on exactly
  that premise -- and a crawler is one of thousands of clients of a large CDN,
  where an inflated round-trip time is more likely somebody else's traffic, a
  route change or a cache miss. The first version treated one slow response as
  congestion and throttled itself from 1.06s to 1.91s against Google with no
  rejection anywhere in the run.

  `Retry-After` overrides the controller's estimate, but only upwards -- a
  shorter one must never speed a client up. Bounds are the caller's and `Max`
  is never exceeded.

  It starts from whatever `WithThrottle` set rather than from `Max`. That was
  not the first design, and the first design was worse than useless: beginning
  at the cautious end, it finished a 300-shard run **5x slower** than the fixed
  interval it was meant to improve on, because climbing back took more clean
  responses than the run contained. Started from the caller's number instead,
  the same run takes 19s against 60s -- **3.2x** -- and settles at the floor.

  `gpscrape -adaptive` turns it on for the CLI, where `-throttle` becomes the
  floor rather than the interval.

- `WithHooks(Hooks)` — `OnRequest`, `OnResponse`, `OnRetry`. Enough to hang
  OpenTelemetry, a metrics registry or a structured logger off the client
  without this package depending on any of them.

- `WithLogger(*slog.Logger)` and `LevelTrace`. The client is silent unless
  given a logger and never writes above Debug: one record per request,
  response and retry, the throttle wait behind each attempt, and every change
  the adaptive controller makes to the interval, with the signal that caused
  it. At `LevelTrace`, one level below Debug, each request also reports its
  `net/http/httptrace` timings -- DNS, connect, TLS, first byte, and whether
  the connection was reused -- which is what tells a slow server from a slow
  network. Bodies and headers are never logged: a details page is a megabyte
  and a reviews page carries people's names. Attributes are built only after
  the handler has said it wants the record, so a logger left installed at
  Info costs one `Enabled` call per request.

  `gpscrape -debug`, or `GPSCRAPE_DEBUG=1` for a script, turns all of it on
  to stderr; `-log-file` sends it to a file instead. `-trace FILE` keeps a
  `runtime/trace` flight recording of the run's last minutes in memory and
  writes it at exit, after SIGINT too, for `go tool trace`. A continuous
  trace of a four-hour sweep would be a file nothing can open; the recording
  is bounded at 64MB and holds what happened before the run stopped.

- `Client.ReviewsSeq` and `Client.CatalogSeq`: iterator forms of reviews
  pagination and the catalog sweep. Both let the caller stop where it actually
  wants to, which the shapes they replace could not express — a callback
  cannot end a sweep, and a slice has to be sized in advance.

      for pkg, err := range c.CatalogSeq(ctx, opts) {
          if err != nil {
              return err
          }
          if seen(pkg) {
              break
          }
      }

  The error slot carries terminal errors only. Per-shard failures still go to
  `opts.OnShardError` and do not end the sweep; the two kinds are different and
  collapsing them would lose information.
- Execution tracing through `runtime/trace`. Operations that issue more than
  one request open a task and the request path opens regions, so `go tool
  trace` can show how much of an operation was the client's own throttle rather
  than Google. The task id travels in the context and so reaches the worker
  goroutines, which makes every request attributable to the call that caused
  it. Costs nothing while tracing is off, and adds no dependency.
- Package documentation. `go doc` and pkg.go.dev previously showed nothing
  above the symbol index.
- `Client.CatalogSize` and `gpscrape catalog size` — how many apps the store
  lists, to a stated error, in ninety seconds rather than four and a half
  hours: 3,549,110 +/- 35,366 at 95% from 866 of 83,445 shards.

  Nothing cheaper than a full pass is exact, and that is a theorem rather than
  a limitation: Charikar, Chaudhuri, Motwani and Narasayya (PODS 2000) show any
  estimator reading r of n rows suffers ratio error at least
  sqrt((n-r)/2r log(1/d)) on some input. This catalog is the easy corner of
  that result — an app is listed in exactly one shard, checked over 63,305 ids
  with zero duplicates — so the Horvitz-Thompson estimator applies and its
  variance is computable rather than merely bounded.

  The assumption underneath is measured rather than asserted. Shards are
  hash-partitioned, so per-shard counts should be Poisson and their variance
  should equal their mean; over 900 shards, variance/mean = 0.938. The result
  carries that ratio and declines to vouch for an estimate when it drifts.

  There is no `-exact` flag: exactness costs a full pass however it is spelled,
  and `catalog sweep` already makes that pass and keeps the ids as well.
- `gpscrape catalog apps` — the app list by genre, read from the genre table
  with no network at all. `catalog apps -genre 'GAME_*' -ids-only` is the game
  index; the pipeline is sweep for the ids, `genres` to resolve them, `apps` to
  read the answer, and only the last step is free.
- `gpscrape reviews -langs en,ru,de` and `-langs all`, and `ReviewLanguages`,
  a list of 71 language codes each of which was exercised against the live
  store.

  Reviews are the one place where the language parameter does not filter but
  *partitions*: hl selects which corpus is served and the corpora do not
  overlap. Fifty codes returned 1,991 reviews of which 1,991 were distinct,
  with no id under two codes. Reading one language reads one slice of an app's
  reviews; the union is how to read all of them. Every record now carries the
  corpus it came from, and ids are deduplicated because some codes are aliases
  — tg and tk are served the Russian corpus verbatim, 30 of 30 identical.

  The country parameter does nothing here, which is worth stating plainly
  because the opposite is widely assumed. Checked on kz.kaspi.mobile, a bank
  used almost entirely from Kazakhstan: ru/kz, ru/ru and ru/us return the same
  reviews id for id, and there is no country anywhere in a review's seventeen
  fields. Looping over country codes and labelling each review with the country
  it was "fetched from" — which several published datasets do — invents that
  column. The ratings histogram *is* per-country and strongly so, which is
  probably where the belief comes from.

- `Generation` in the library, and `Client.SitemapGeneration` — two requests to
  learn whether Google has republished the catalog at all. Parsing a shard URL,
  ordering two builds and reading the build time out of the run id are
  knowledge about Google's naming, not about how anybody stores snapshots, so
  `cmd/gpscrape` no longer keeps its own copy of them.

  Ordering compares the run id numerically. It grows without padding, so as
  text "999999999" sorts after "1787500934" and the newest snapshot silently
  stops being recognised as newest. `Built` reads the run id as a Unix
  timestamp — run 1787500934 is 2026-08-23 16:02:14 UTC, matching both the date
  in the same filename and the shards' `Last-Modified` — and reports failure
  rather than quietly returning a moment in 1970 when it is not one.

  `GenerationOf` refuses a shard list that spans two builds, which is what a
  list read during a republish looks like. Sweeping that produces a catalog
  that existed at no moment.

- `Client.CatalogShardSeq` and `CatalogOptions.ShardURLs`, which make the shard
  the unit of a sweep rather than the package.

  The unit of *resumability* was always the shard: a sweep is 83,445 requests
  over hours, and an interrupted one has to restart from the URLs it did not
  reach, which cannot be worked out from a flat stream of ids. `cmd/gpscrape`
  had to bypass this package and drive `SitemapShardPackages` itself to get
  that, and so would any other batch consumer. A failed shard now arrives in
  the stream carrying its URL, rather than only through a callback, because a
  caller that means to retry it needs it where the rest of its bookkeeping is.

  `CatalogSeq` is a flattening of it, so there is one sweep engine rather than
  two. `ShardURLs` skips discovery entirely: an index names a shard only within
  one generation, so anything resumable records URLs.

- `gpscrape catalog sweep -sample PCT`, which sweeps a fraction of the shards.

  Shards are hash-partitioned — any one has the same mix of apps, books and
  films, and the same spread of release dates, as any other — so a uniform
  subset is an unbiased sample of the catalog. That makes "how big is it, what
  share are games, how many ids are already dead, how fast is it growing"
  answerable for 834 requests instead of 83,445. Measured: a 0.01% sweep took
  eight shards and two seconds, and its 350 ids extrapolate to 3.65M against
  the 3.67M a full sweep finds.

  The seed is recorded in the manifest and derived from the generation when not
  given, so a re-run samples the same shards and a new build samples afresh. An
  unreproducible sample cannot be compared with anything, including itself.

  `catalog diff` refuses two snapshots swept at different coverage. They are the
  same shape — a sorted list of ids — so nothing about the files stops the
  comparison, and its answer, "99% of the store was removed", is
  indistinguishable from a real catastrophe.

- `Client.DigestsSeq`, which resolves a few fields for very many apps.

  The details RPC carries a field selector, and trimming it to the genre alone
  returns 178-243 bytes per app against 15,880 for the whole record — so a
  genre pass over 3.36M apps moves from 62GB to 0.8GB, and from 6.4 hours to
  about fourteen minutes at eight workers. Which field carries the genre was
  found by asking for each of the 49 the app page requests, one at a time.

  It takes an `iter.Seq[string]` rather than a slice: the intended input is the
  whole catalog, and 3.36M ids is 160MB before any work starts. Fed from a file
  or a cursor, peak memory is one pack per worker whatever the catalog's size.

  `Listed` is a field rather than an error, because an app the store will not
  serve is an answer and not a failure. Absence becomes a *removal* only
  against a previous snapshot, which is the caller's business. A frame that
  never arrived is an error, though: at a thousand lookups per request,
  reporting that as absence would announce a thousand deletions at once.

- `gpscrape catalog` became a group of verbs, since keeping a local copy of the
  store's app list is not one job but several that differ by two orders of
  magnitude in cost:

      gpscrape catalog check     2 requests       has Google republished?
      gpscrape catalog new       17 requests      what the store lists as new
      gpscrape catalog size      ~900 requests    how many apps, to a stated error
      gpscrape catalog genres    ~3.4k requests   genre changes and removals
      gpscrape catalog sweep     83,445 requests  the complete list
      gpscrape catalog apps      none             the id list by genre, from disk
      gpscrape catalog diff      none             compare two snapshots
      gpscrape catalog ids       as asked         stream ids, keep no state

  `genres` re-reads apps already known, so it finds removals and genre changes
  within a day for a few minutes' work — about 3,400 requests over a full
  snapshot, plus roughly 310 per extra `-confirm-gone` storefront; it cannot
  find an app never heard of. `new` finds those, but reads ranked lists, so an
  app with no traction never appears. Only `sweep` is complete. `diff` prints
  how much of what the sweep found the cheap signal had already reported — the
  recall figure that decides how often the expensive pass has to run, printed
  unasked because a metric behind a flag is a metric nobody has.

- Three more collections: `CollectionNewFree`, `CollectionNewPaid` and
  `CollectionMoversShakers`, plus a `gpscrape list` command, which the CLI had
  no equivalent of at all — top charts were reachable from Go and from nowhere
  else.

  `NEW_FREE` is the useful one. Across the seventeen `GAME_*` categories it
  returns 1,230 distinct apps in 17 requests and 14 seconds, of which 99% were
  published within thirty days and 12% within seven. For anyone tracking what
  has appeared in the store, that is a signal costing seventeen requests
  against the 83,445 of a full catalog sweep — though it is ranked, so an app
  with no traction never enters it. High precision, unmeasured recall.

  The cluster identifiers are undocumented and were found by asking the
  endpoint: it answers to `topselling_new_free`, `topselling_new_paid` and
  `movers_shakers`, and returns nothing for `new_free`, `topselling_trending`,
  `topselling_rising` or `topselling_new_grossing`. A retired name would not
  error, it would return an empty list, so `TestCanary/Collections` exercises
  every one of them weekly.

- A snapshot directory takes one writer at a time. Two `catalog sweep` runs
  against the same `-dir` used to interleave their writes to the resume state,
  the partial file and the genre table, and nothing said so. `sweep` and
  `genres` now hold a `sweep.lock` in the directory -- `{"pid","host","started"}`
  -- for as long as they run; a second run is refused with the holder's pid,
  host and start time, and a lock left behind by a process that has died on
  this host is cleared on the next run with a line on stderr. `check`, `new`,
  `size`, `apps`, `diff` and `ids` do not take it; `new` only appends to its
  own signal log.

- A second Ctrl-C exits at once. The first cancels the run and lets it unwind,
  which is what keeps everything already written valid; a run that is stuck is
  exactly when a person presses it again, and that used to be absorbed. The
  second prints `gpscrape: interrupted again, exiting now`, exits 130 without
  unwinding, and deliberately leaves `sweep.lock` behind for the next run to
  clear.

### Changed

- Page parsing is **7 to 36 times faster** and allocates 51–96% less.

  Two parsers were pathologically slow rather than inherently expensive. The
  script-block extractor was a regular expression with two lazy quantifiers
  backtracking across a megabyte of HTML, and it copied the page into a string
  to feed it and each payload back out again. The sitemap shard parser built a
  document tree of ~400 entries to read the one field in eight that is an app.
  Both are substring scans now.

  | | before | after | | allocated |
  | --- | ---: | ---: | ---: | ---: |
  | app details page | 9.72 ms | 0.27 ms | 36x | -96% |
  | search page | 20.89 ms | 2.86 ms | 7.3x | -58% |
  | top charts page | 56.11 ms | 6.92 ms | 8.1x | -51% |
  | sitemap shard | 0.60 ms | 0.048 ms | 12.6x | -90% |

  Both columns are reproducible from the repository, which the first published
  version of this table was not: `BenchmarkOracle*` in `bench_test.go` runs the
  replaced implementations -- the regex and the XML document tree kept as fuzz
  oracles -- against the same fixtures as the current ones.

      go test -run '^$' -bench 'Oracle|ParseAppPage|ParseSearchPage|ParseListPage|SitemapShard' -benchtime 300x .

  That matters because the earlier numbers were not reproducible and one of
  them had silently stopped being true: the top charts row read 3.69 ms, which
  was correct for a fixture regenerated in this same release. Against the page
  that actually ships, 53% larger, it is 6.92 ms.

  This does not make much difference to wall-clock time, and saying so is more
  useful than the multipliers: the library is network-bound at every step, and
  parsing was 5% of the time spent fetching one page and 0.3% of a catalog
  sweep. What it does buy is the memory, the garbage collector, and the
  removal of a backtracking failure mode that a page of unusual shape could
  have made far worse than the fixtures suggest.

  Locating a block and decoding it are now separate, so a caller that wants one
  `ds:N` no longer builds trees for the other twelve. That is 4.8x faster and
  allocates 7.6x less on the path an `Availability` sweep repeats once per
  country: over the default 177 countries, 228ms of parsing becomes 47ms and
  107MB of garbage becomes 14MB.

  `App` was moved onto it too, and should have been from the start. It reads
  `ds:5` and nothing else -- `extractAppData` touches no other key -- yet
  `parseAppPage` decoded all thirteen blocks and threw twelve away, on the
  highest-volume operation in the library. Fixing that is the 36x row in the
  table above; on its own, separately from the scanner, it is 4.7x faster and
  allocates 6.7x less. `TestParseAppPageNeedsOnlyDS5` pins the results as
  identical so the shortcut cannot silently start dropping a field.

  `Search` genuinely needs every block, and the cluster walkers iterate them by
  design.

  Both replacements are held against the implementations they replaced by
  differential fuzz tests. That is not ceremony: the fuzzer found a real
  divergence within a minute -- the scanner accepted any quoted key where the
  expression required `ds:N`, so a malformed page would have produced a block
  under an empty key.

- Minimum Go version is 1.25 (was 1.24). Upstream supports the two most recent
  releases; `GOTOOLCHAIN=auto`, the default, fetches a newer toolchain
  automatically, so only `GOTOOLCHAIN=local` builds are affected.
- `ReviewsComprehensive` streams each rating instead of collecting it, since
  the cross-rating de-dup discards most of what each returns anyway.
- Modernised with the `go fix` modernizers under the Go 1.27 toolchain: mostly
  `interface{}` to `any` across the payload parsers, plus `strings.SplitSeq`
  in loops that never needed the intermediate slice.

- The sitemap index parser was the last one still building a document tree.
  An index is 6MB of XML holding 50,000 `<loc>` entries and nothing else worth
  reading, so unmarshalling it allocated **94MB to produce 7.2MB of strings**,
  paid before a sweep fetches its first shard — 81% of the total cost of a
  ten-shard sampled run. It is a substring scan now, the same shape as the
  shard parser, and held against the tree it replaced by
  `FuzzIndexScanMatchesXML`: 35MB, and 1.6 million fuzz executions with no
  divergence.

- Sitemap shards are scanned as a stream instead of being decompressed into a
  buffer first. A shard decompresses to about 7.5MB, of which the `<loc>`
  elements are 0.6%: 46KB of URLs wrapped in hreflang alternates, roughly 43
  apps among 400 locs. Reading it as a stream took allocation from 15.34MB to
  0.73MB per shard, measured live, at identical wall time — decompression
  dominates and is unchanged. Over a full sweep that is 60GB of garbage
  against 1250GB.

  It is held against the buffered scan it replaced by differential fuzzing
  over 2.4 million executions, by readers that hand out one byte at a time so
  every marker is split across a read, and live on 20 real gzipped shards with
  byte-identical id lists.

- `gpscrape` buffers its NDJSON output when writing to a pipe or a file.
  `json.Encoder` issues one `Write` per value, so writing straight to stdout
  cost a syscall per record: measured over 100,000 records, 74ms against 12ms
  buffered, and a catalog sweep emits 3.7 million of them.

  Terminals stay unbuffered. What makes the paging commands watchable is a line
  appearing when it is produced, and a 64KB buffer would hold thousands back; a
  pipe has no such expectation and wants the throughput.

- Review parsing builds each review's URL by concatenation instead of
  `fmt.Sprintf`. One call per review, and at a thousand reviews a page the
  formatter was 262k allocations in the profile for a string whose shape never
  varies. Worth 2.4% of the parse's allocations and 1.6% of its time -- small,
  but free.

  Two larger ideas were profiled and rejected. Decoding through a
  `json.Decoder` to skip `Unmarshal`'s separate validation pass -- 25% of parse
  CPU -- makes it *worse*: the reader's buffering took allocated bytes from
  6.9MB to 10.3MB. And Go 1.27's json v2, re-tested here on a payload six times
  larger than the benchmark that prompted the earlier retraction, is 0.4%
  different with allocation counts identical to the unit (82,511 either way).
  The retraction stands.

  What remains is inherent: half the allocations are `arrayInterface`, the cost
  of decoding Google's positional arrays into `any` trees. A month of one app's
  reviews spends 93ms parsing against 4s of network.

- Review pagination asks for 1000 per request instead of 150. The 150 carried
  the comment "Google Play limit per request" and was not one: asked for 200,
  300, 500, 1000, 2000 and 3000, the endpoint returns exactly that many.

  It cost a request every 150 reviews, and the operation is throttle-bound --
  a month of one busy app's reviews is 5,785 of them, which was **39 requests
  and 19.3s, and is now 6 requests and 4.3s**. Compared by id across both runs:
  nothing missing in either direction, and no shared review differing by a
  character.

  Not set higher because of how it fails. 3000 works; 5000 and 10000 come back
  as a null payload, which is also Google's "no more reviews" signal -- so
  overshooting does not error, it silently truncates the sweep. 1000 keeps a
  third of the last size known to work. `Reviews`, where the page size is
  visible to the caller in the slice they get back, still defaults to 150.

- `Availability` reads the RPC the details page is built from instead of the
  page. A sweep is still one request per country -- the country is a query
  parameter so it cannot be batched, and nothing in the payload enumerates
  countries -- so **this does not make it faster**, and saying so is the point:
  under a throttle the wall clock is `countries x interval` and no amount of
  concurrency or lighter requests moves it. Measured over 24 countries at
  300ms, 7.07s became 6.98s.

  What changes is weight. Over 20 countries, 25.6MB of markup becomes 400KB,
  so the default 177-country sweep drops from about 227MB to 3.5MB. Round-trip
  time roughly halves (162ms to 101ms), which shows up only when the throttle
  is not the binding constraint -- at one worker the same sweep is 1.5x faster,
  at four it is identical.

  The three outcomes map exactly onto what the page returned, verified live on
  each, including an embargoed country (`kp`), where page and RPC agree on
  `StatusNotFound`. `TestCanary/AvailabilityClasses` holds that mapping, since
  a collapse of two signals into one would silently reclassify every unknown
  app rather than fail.

- `genres.tsv` is cumulative: "everything ever resolved, minus what was seen to
  disappear", not "what the last run resolved". The merge is what stops a
  transient error deleting an app, and it means an id that falls out of the
  sitemap without ever being observed as gone keeps its row until `-prune`
  removes it — that is the removal path, and it is not automatic. To spot the
  drift between runs that did not prune, compare `wc -l genres.tsv` against
  `ids` in the manifest.

- `Availability.Status` marshals as its name. `gpscrape availability` wrote
  `"statuses":{"cn":2,"de":1}`, which asks the reader to know the order of a
  const block; it now writes `{"cn":"not_in_region","de":"available"}`, the
  words `String()` has always returned — `available`, `not_in_region`,
  `not_found`, `error`, `unknown`. A record written with the numbers still
  reads back: `UnmarshalJSON` accepts both spellings.

- `catalog sweep -check` is removed in favour of `catalog check`. It was a
  second surface over the same question with a poorer answer — three requests
  instead of two, and no `built`, `ageHours` or `shards` — which is the
  argument that already removed `sync`. The flag now fails with a message
  naming `gpscrape catalog check`.

- `catalog sweep` with nothing to do emits the manifest of the snapshot that is
  already current, instead of exit 0 and an empty stdout. A sweep exiting 0
  now always means exactly one manifest record describing the snapshot on
  disk; `completedAt` tells "swept just now" from "already had it".

- `AppsMany`, `PermissionsMany` and `SuggestMany` send their packs over
  `WithConcurrency` workers instead of one after another. Results stay
  positional and a failed pack still costs only its own items; under a fixed
  throttle this changes little, and for a hundred thousand ids it stops one
  request's latency from setting the floor.

- `ReviewLanguages` is a function returning a copy, not an exported slice. It
  was added in this release, so nothing published depends on the variable, and
  a caller can no longer corrupt the list for the whole process.

- An availability probe asks the details RPC for the one field it reads
  instead of all forty-nine. Measured live: 268 bytes per app in place of
  about 20KB, with the same answer for an available app, one not offered in a
  country, and one that does not exist. The one-field response is a
  positional array of nulls with the field in place, which is not how the
  digest's one-field request comes back (a map keyed by field number); both
  readers go through the same decoder, which knows both shapes.

### Deprecated

Both of these will be **removed in v2**. They keep working for the whole of the
v1 line; nothing breaks until you change the import path, and that is a
deliberate act rather than something `go get -u` does to you.

- `Client.ReviewsAll` in favour of `Client.ReviewsSeq`. It buffers the whole
  run before returning anything, and stops at 500 by default — a ceiling the
  caller can neither see nor raise except by guessing a larger number.
- `Client.EnumerateCatalog` in favour of `Client.CatalogSeq`. A full sweep is
  83k requests, so being unable to walk away early is not a small thing.

  Neither is automated by `go fix`: that requires the old API to be expressible
  as a single call to the new one, and both migrations turn a result into a
  loop. Both remain and keep working.

### Removed

- `PermissionsOptions.Short`. Documented as "return only permission names"
  since 1.0.0, threaded through to the parser and never read — a promise with
  no implementation. The return type is `[]Permission`, so the only thing it
  could have meant is the same slice with `Type` blanked, which is one line for
  a caller to write. Deleted rather than implemented.

### Fixed

- The legacy HTML listing path answered any collection it did not recognise
  with the top-free chart and a nil error. The page lays out the three original
  charts in a fixed order and says nothing about the others, and the section
  was chosen by a switch with an implicit default — so a collection outside
  those three read section 0. `List` falls back to that path whenever the RPC
  errors *or returns nothing*, so a single transient failure on "what is new"
  returned the most popular apps, labelled as new releases, with no error.

  That is the worst shape a failure can take for a caller tracking new
  releases: it looks like data. The path now refuses a collection it cannot
  locate, before spending the request, and `TestCanary/Collections` asserts
  that distinct collections return distinct lists rather than merely non-empty
  ones — a check that would have caught this.

- A sampled snapshot was treated as the generation it sampled. Four separate
  places ask "do we already have this build?" and only `catalog diff` consulted
  the manifest, so after a `catalog sweep -sample 0.001` the full sweep never
  ran and nothing said so, `catalog check` reported `upToDate: true`, and
  `catalog genres` rewrote its table from the sample. `manifest.complete()` now
  exists and every reader of a snapshot directory asks it, including
  `catalog apps`; a test walks them rather than naming the ones that existed
  when it was written.
- Resuming a sweep onto a different sample panicked. The resume state was keyed
  to the generation alone, but a shard list is a function of the generation and
  the sampling together: an interrupted run followed by a narrower `-sample`
  sized the pending slice `len(shards)-len(finished)`, which went negative.
  When it did not panic it produced a manifest claiming a coverage its snapshot
  did not have. The state now carries the sampling and refuses a mismatch,
  which makes the existing clean-restart path do the right thing.
- `catalog ids 0-9` swept all 83,445 shards. The argument parsed cleanly and
  was discarded, leaving `-shards` empty — four hours instead of ten shards.
  Every catalog verb that takes no positional arguments now refuses them.
- `catalog check` defaulted `-dir` to `""` while every sibling defaults to
  `catalog`, so it answered `upToDate: false` unconditionally and a consumer
  following the usage text ran 83,445 requests a day instead of one per
  generation.
- `catalog genres` rebuilt its table from what one run saw, so a transient 503
  dropped an app from it and `-ids` over a subset shrank it to that subset;
  either way the app returned as `first_seen` — a change that did not happen.
- `reviews -langs ALL` scraped one nonexistent corpus instead of seventy-one.
  The keyword was matched before the normalisation every real language code
  goes through, so it was sent as `hl=all`: one record, exit 0, no warning.
- `-throttle` did not apply between languages. The client was rebuilt on every
  call and throttle state lives in the client, so each language's first request
  fired immediately and `-langs all` was 71 requests back to back at any
  throttle setting. One client per run now.
- `reviews -score 6` returned unfiltered data under a flag that said it was
  filtered. Out-of-range ratings are refused.
- `-sample 100` wrote a manifest saying the catalog held no apps: the sampler
  declines to sample everything and returned an empty shard list, which the
  sweep took as its work. 100 now means the whole catalog, as 0 always has;
  anything else outside (0,100] is refused, and a sweep that collected nothing
  no longer publishes a manifest at all.
- `-shards " "` and `-shards ","` swept all 83,445 shards. Every part parsed to
  nothing, the result was an empty slice, and an empty slice means "every
  shard". A `-shards` that names none is now an error. Ranges are bounded, so
  `-shards 0-9999999999` is refused rather than allocating its way to an OOM,
  and repeats are collapsed — `-shards 3,3,3` fetched shard 3 three times.
- `catalog apps -genre game_puzzle` and `catalog new -categories NOT_A_CATEGORY`
  produced empty output and exit 0, which a pipeline cannot tell from "there
  are none". Both are validated now, as `-collection` already was.
- `catalog diff` guarded on the filename rather than the manifest, so a sampled
  snapshot renamed to a well-formed generation id read as a complete one and
  diffed against a real sweep reported the whole catalog as removed.
- `catalog size -precision NaN` passed the range check — NaN fails every
  comparison — and returned a real-looking measurement against a target nobody
  could name.
- `catalog genres` published an empty table and exited 0 when every lookup
  failed. Found by running the game-index pipeline end to end while rate
  limited: all 1,740 lookups errored, the summary still read "1740 apps", and
  the table was overwritten with nothing. A run in which nothing resolved is
  refused, and one in which some lookups failed says how many.
- `gpscrape sync`, which pre-release builds offered as a second name for
  `catalog sweep`, does not ship. It was not an alias, it was the same function
  under two names -- `catalog sweep -h` printed `Usage of sync:` -- so the group
  had two surfaces and one implementation. No released version ever had it.
- `catalog sweep` printed nothing to stdout. The flagship command was the only
  one with no machine-readable answer: a consumer had to parse stderr or go
  reading files out of a directory whose layout this tool asks nobody to
  depend on. It now emits its manifest as one NDJSON record.
- `catalog ids` emitted bare ids while the tool documents newline-delimited
  JSON, so `gpscrape catalog ids | jq .` failed. It emits records now, with
  `-ids-only` for the bare form, matching `catalog apps`. Asking for shards
  that do not exist is an error rather than an empty stream at exit 0.
- `catalog genres -prune` drops rows for ids the snapshot no longer lists.
  The merge that stops a transient error deleting an app also meant nothing
  ever removed a row for an app that left the sitemap without being seen to
  go, so the table grew monotonically away from the catalog it describes.
  Refused with `-ids`: a list you chose cannot tell you what is gone.
- `signalStats.precision` divided matches from one generation window by every
  id the signal log had ever held, so it fell as the log aged whatever the
  signal was doing -- a ratio with its two halves measured over different
  periods. Each record already carried the time it was seen; the window now
  uses it.
- `catalog diff` reported that ids had changed without saying which. A summary
  is not something a database can act on, and keeping a local copy current is
  the whole point of the verb; the ids existed in `delta-<generation>.json` but
  reaching them meant computing a filename inside a directory whose layout this
  tool calls its own business. `-changes` emits one record per changed id.
- `catalog genres` called an app gone when one storefront could not see it.
  "Not listed in the country this run used" is not "removed from the store":
  probing 200 ids the pipeline had classified as dead found two alive
  elsewhere -- one listed only in Russia, and one listed in Brazil, Germany,
  India, Japan, Kazakhstan, Russia and Turkey, every market checked except the
  United States it had been run from. Across 316,400 apparently dead ids that
  is thousands of live listings buried.

  Apparent removals are now re-asked in other storefronts before being
  believed, and an app that answers anywhere keeps its genre from wherever it
  answered. `-confirm-gone` names the storefronts and can be emptied to trust
  `-country` alone. It costs only the ids that came back absent, and the
  digest packs a thousand per request: about 310 requests per extra storefront
  against the 3,400 the main pass costs.

  A `gone` record now names the storefronts that answered, so the claim
  carries its own scope rather than implying the whole store.
- `catalog genres` held two copies of the genre table. It cloned the table so
  a failed lookup would leave the old value in place -- correct, and at catalog
  scale two maps of 3.2M entries live at once, measured at 766MB resident for
  the command that runs daily. A delta says the same thing in the space the
  changes take, which on an ordinary day is a few thousand entries: 609MB. The
  remaining 307MB is the table itself, which this design needs in order to
  name a change; walking the sorted snapshot and the sorted table together
  would remove it, and is left for after the release.
- `catalog apps` held the whole genre table in memory to filter it. Measured on
  the real catalog: 422MB resident to turn a 115MB table into 9MB of ids, and
  the table only grows. It now streams the file and keeps only what matches --
  347,340 rows instead of 3,209,964, and 83MB instead of 422MB, with output
  byte-identical to the old path.
- Nothing ever deleted resume state from a generation that had rolled. A done
  log names shard URLs, and republishing replaces every one of them, so the
  files could never be read again -- but only the current generation's were
  cleaned, so an interrupted sweep left about 99MB behind on every roll, on
  the order of 9GB a year. A sweep now removes them and says how much it
  freed. Snapshots are the caller's data and go only when asked: `-keep N`
  keeps the newest N and deletes older ones.
- `catalog apps` refused a sampled table with no way to say "I know". Reading a
  sample as a sample is how the share of the catalog that is games was
  measured, and the guard had made it impossible; `-allow-sample` does it, and
  reports the coverage on every run.
- `catalog size` used 1.96 as its multiplier whatever the sample size, so a
  small `-pilot` reported an interval far narrower than the truth: at `-pilot 2`
  it claimed 4.2% where the correct figure is 27%. Student's t is used below
  thirty degrees of freedom. The command also now reports whether it met the
  precision it was asked for; missing is an ordinary outcome, because the
  sample size is solved from the pilot's spread and the pilot's spread is
  itself an estimate.
- `FullDetail` fetched one HTML page per result. `Search`, `List`, `Similar`
  and `Developer` all enrich through the same path, and all four were an N+1:
  a listing of 32 cost 33 requests and about thirty megabytes of markup, and
  `List` accepts `Num` up to 660. Enrichment now goes through the batched RPC
  the details page is itself built from.

  Measured against the live store at a 300ms throttle, a 32-result listing with
  `FullDetail` went from **9.73s over 33 requests to 0.44s over 2**. This was
  the batching work of this release not being applied to its largest caller.

  `WithConcurrency` no longer affects enrichment: the work is two requests, and
  under a fixed throttle concurrency cannot make requests start faster anyway.

- A missing app reported success from `Permissions` and `PermissionsMany`: an
  empty payload returned `(nil, nil)`, so a caller auditing a list of ids could
  not tell "this app declares no permissions" from "this app does not exist" —
  and an app legitimately can declare none. It is an error now, matching the
  sibling parser for app details, which always treated it as one.

- `AppsMany`, `PermissionsMany` and `SuggestMany` sent an empty id or term to
  Google instead of rejecting it, spending an RPC slot to get back an error
  naming nothing. The singular forms had always validated up front.

- A cancelled context produced different errors for the same action depending
  on when the cancellation landed — `context.Canceled` if it arrived during a
  retry backoff, `unexpected status: NNN` if it arrived during the request.
  Both causes are wrapped now, so `errors.Is` finds the cancellation and the
  last status stays reachable.

- `gpscrape permissions` changed its JSON shape with the number of arguments:
  bare permission objects for one app, per-app records for several. A pipeline
  written against one id silently produced nulls the day someone passed two.
  It is one record per app now, matching `gpscrape app`.

- `catalog sweep -check` created the snapshot directory as a side effect of
  asking a question, and printed a sentence to stdout while its sibling branch
  printed to stderr. That was fixed, and then the flag was removed altogether:
  it duplicated `catalog check` with a poorer record and one more request (see
  Changed).

- The `lightfeed` and `apidoc` submodules could not be resolved from outside
  this repository at all — both required the root module at a version that was
  never tagged, and papered over it with a `replace`, which is ignored outside
  the module that declares it. A CI job now resolves all three from the proxy
  the way a consumer does.
- The catalog crawler ignored both `Flush` and `Close` on its output file, so
  a full disk at the end of an hours-long sweep would have produced a silently
  truncated result indistinguishable from a smaller catalog.
- `parseTimestamp` discarded the error from parsing the sub-second part.
- Three parser tests contained an `if err != nil` whose body was only a
  comment: they counted toward coverage while asserting nothing.

- `DigestsSeq` hung a caller that stopped early. The producer's only escape
  from a full output buffer was the context, and the iterator never derived a
  cancellable one, so a `break` parked the consumer, the workers and the
  producer permanently — reachable from `catalog genres` on any emit error.
  The iterator now cancels its own context before waiting for its workers, the
  way `CatalogShardSeq` already did.

- A `batchexecute` frame that never arrived was read as an answer.
  `Availability` turned it into `StatusNotFound`, and since `GloballyRemoved`
  is "not found everywhere", dropped frames could report a live app delisted;
  `Suggest` and `SuggestMany` reported it as "no suggestions", and
  `Permissions` described it as the app's own empty answer. All of them now go
  through the frame-aware path `DigestsSeq` already used: a missing frame is
  an error (`StatusFetchError`, and the country lands in `Result.Errors`), and
  only a frame that arrived empty is ever interpreted as data.

- `WithAdaptiveThrottle` promised that `Max` is never exceeded "regardless of
  option order" and did not enforce it at construction: `WithThrottle(5s)`
  after a policy with `Max: 2s` started at 5s, and a `Start` outside
  `[Min, Max]` was never clamped. Both are clamped once in `NewClient`, after
  every option has run, so the bounds hold from the first request rather than
  from the first adjustment.

- `ReviewsAll` fetched a 1000-review page to return ten. It delegates to
  `ReviewsSeq`, which asks for the largest page there is because for it a
  page is purely a request count; `ReviewsAll` has a ceiling and now pages at
  `min(Count, 1000)`, so `Count: 10` is one small request rather than a
  megabyte of transfer.

- `ReviewsComprehensive` could only return a nil error: five failed rating
  passes gave an empty slice and no explanation. A partial result is still
  success — four ratings out of five is a useful answer — but nothing
  collected and every pass failed now returns the last error.

- `List` returned `(nil, nil)` for `NEW_FREE`, `NEW_PAID` and `MOVERS_SHAKERS`
  when the batch path came back empty and the HTML fallback refused the
  collection, which it has no section for. The refusal is returned as the
  error it is; the one remaining empty-and-nil outcome is a genuinely empty
  collection.

- `WithTimeout` set `Timeout` on the `*http.Client` a caller passed in through
  `WithHTTPClient`, changing every other request that program makes with it.
  The client is shallow-copied first; `Transport`, `Jar` and `CheckRedirect`
  are still shared, as they are meant to be.

- A `-sample` sweep after a complete one wrote a delta calling every id outside
  the sample removed — 3,260 of 5,000 in the repro, about 3.5M in production.
  The delta was gated on the generation id alone; it now applies the two
  refusals `catalog diff` already makes (different coverage, or either side a
  sample) and says on stderr why no delta was written. A `delta-*.json` now
  exists only between two complete snapshots of two different generations.

- `catalog genres` treated a failed request in the confirm pass as proof of
  removal. An id whose lookup errored in every extra storefront was reported
  `"change":"gone"` and its row deleted — a live app struck from the table on
  the strength of a 503. Such an id is now `"change":"error"`, keeps its row,
  and counts toward the guard that refuses a run in which nothing resolved. A
  genuine `gone` record's `country` lists the primary storefront once and then
  only the storefronts that answered; it used to list `-country` twice and
  storefronts that never replied.

- `catalog new` exited 0 with an empty stdout when every category failed,
  indistinguishable from "the store published nothing new". One failed
  category is an ordinary day; all of them is a run that observed nothing,
  and it now exits 1 naming the count and the last error.

- `-keep` never deleted a delta. The sweep writes `delta-<from>-to-<to>.json`
  and the pruner looked for `delta-<generation>.json`, a name that has never
  existed, so snapshots and manifests went and every delta stayed for good;
  the test fixture had been written to the pruner's spelling and agreed with
  it. One name builder serves both, and the pruner removes every delta that
  names the generation at either end.

- The snapshot was the one file written without tmp + fsync + rename, and the
  manifest carrying its sha256 was written after it. A crash between the two
  left a manifest describing hours of work as a file that might be truncated.
  Both snapshot writers now go through the durable tail the manifest already
  used; the bytes and the hash are unchanged for a given id list. The genre
  table's writer, which had the rename, no longer leaves a `.tmp` behind on
  failure and syncs the directory too.

- A write failure inside a sweep was logged per shard and the sweep carried
  on. `bufio` errors are sticky, so after the disk filled every remaining
  shard — up to 83,000 requests — was fetched, parsed and thrown away, and
  the run reported the error hours later at the final flush. The first write
  failure now cancels the sweep; it exits 1 naming the file, keeps its done
  log, and is not mistaken for an interrupt.

- `-sample 0` means the whole catalog, as the flag's help has always said, but
  the range error said "above 0 and at most 100" and this changelog said
  `(0,100]`. The error now says `0` or `(0,100]`, and a `-sample 0` typed on
  purpose is announced on stderr as sweeping every shard, because to at least
  one reader it looked like a request for a tiny sample. A sweep resuming a
  partial snapshot also said "sweeping it in full" when it was itself sampled;
  it now says at what coverage.

- `catalog apps` and `catalog diff` accepted `-lang`, `-country`, `-throttle`,
  `-concurrency`, `-timeout` and `-adaptive` and ignored all six: both verbs
  read files already on disk and make no requests, so `-country de` returned
  the same table with no warning. The flags are no longer registered for
  those two verbs, so `-h` tells the truth and a stray one is an error.

- `gpscrape version` printed plain text on stdout, the one line of output in
  the tool that was not newline-delimited JSON, and printed `devel` for anyone
  who had installed a tagged release with `go install ...@latest`, because the
  version is stamped only by the release workflow's ldflags. It emits
  `{"version":"v1.4.0"}` and falls back to the module version the toolchain
  recorded in the binary.

- Two help strings. `-trace`'s usage line read `-trace go tool trace`, because
  `flag` takes the first backquoted word of a usage string as the operand
  name; it reads `-trace FILE` now. `search -full` said "one request each" for
  an enrichment that has been batched since the `FullDetail` fix above.

- `app` with several ids dropped a failed id from stdout and mentioned it on
  stderr only, while `permissions` emitted `{"appId":…,"error":…}` in its
  place. A caller diffing the ids asked for against the ids returned should
  not have to reconcile two streams; both commands now emit the inline error
  record, in position, and a batched run always produces one line per id
  asked for. A single `gpscrape app ID` still reads the details page, so its
  fields are unchanged.

- The differential fuzz tests could fail on an input Google never serves.
  `<sitemapindex><loc>0</loc></sitemapindex>` parses as XML, the oracle reads
  only a `<loc>` inside its `<sitemap>` (or `<url>`) wrapper, and the scanners
  read every `<loc>` — so CI hit a divergence once before the tag, on an input
  that was never committed. The scanners are deliberately not element-aware:
  tracking the enclosing element would add state to a streaming hot path that
  has to survive chunk boundaries, to change the answer for documents that do
  not occur. So the contract is stated on `indexShards` and `shardPackages`,
  an unwrapped `<loc>` is excluded from the property the way an unterminated
  tail already was, and the input is committed under `testdata/fuzz/` as a
  seed for both targets.

### Security

- Six vulnerabilities with call traces into this code were reachable through
  the previous toolchain. `govulncheck` now runs in CI.

### Internal

- Every operation that makes a request now opens its own `runtime/trace`
  task: `Reviews`, `CatalogSize` and the sitemap helpers open theirs, and the
  batched `AppsMany`, `PermissionsMany` and `SuggestMany` are named apart from
  their singular forms. `CatalogSize`'s ~900 requests used to be attributed to
  "CatalogSeq", so a size run and a sweep looked alike in a trace. A test now
  runs a trace against the mock transport and checks the task name each public
  method opens.

- `Reviews` rides the shared batch layer -- `rpcCall`, `buildFReq`,
  `decodeBatchFrames` -- instead of building its own `batchexecute` URL,
  escaping its own `f.req` and walking the envelope by hand. It was the last
  request path off that layer. Held identical live: 1,500 reviews by
  helpfulness and 600 across two languages came back id for id before and
  after, and the payload-level parser with its lazy skip is unchanged.

- A non-200 response body is drained before the connection is released, up to
  32KB: enough for HTTP/1.1 to reuse the connection, and a ceiling so a
  rate-limit page cannot cost bandwidth at exactly the wrong moment. Over
  HTTP/2, which Google negotiates, it changes nothing.

- The in-place radix sort falls back to `slices.Sort` past a shared prefix of
  64 bytes rather than recursing once per byte without bound, and the partial
  file reader refuses a line over 1MB -- the limit every `bufio.Scanner` in the
  tool already applied -- naming the file and line. Both are guards against
  corrupt input the tool did not write; ids from Google's own sitemap never
  reach either.

- GitHub Actions moved to their Node 24 majors: `codeql-action` v4,
  `setup-node` v7 and `upload-artifact` v7. The v3 CodeQL action and the
  Node 20 runtime it targets are deprecated, and every run said so.

- Benchmarks over the recorded fixtures, and fuzz targets for every parser.

  An earlier draft of this entry claimed Go 1.27's json v2 was worth +21%
  throughput on the batchexecute paths at the cost of +31% allocation count,
  measured against `GOEXPERIMENT=nojsonv2`. **That does not reproduce and is
  withdrawn.** Re-run six times each way at `-benchtime 400x`, the two settle
  at 2.2504 ms and 2.2526 ms -- a 0.1% difference, inside the noise -- with
  **identical** allocation counts (14,319 either way) and bytes agreeing to
  four significant figures. The experiment does take effect; the difference
  simply is not there on this code. No baseline was ever committed, which is
  why a number that "the benchmarks settle" survived without any benchmark
  settling it.

  They do locate the cost: parsing a details page is 0.27ms, of which the
  scan and decode of the one block that is read dominates and extracting the
  fields from the result is 3.7µs.

  Benchmark numbers are comparable within one toolchain only. Coverage
  instrumentation already moved by six points between 1.26 and 1.27 on
  identical code; the allocator and collector changed at least as much. A
  baseline gets rewritten on a toolchain bump rather than read as a regression.

  The fuzz targets pin one narrow contract: no input may panic. Everything
  here is parsed from an undocumented third-party format via chains of type
  assertions, and a panic takes down a caller that is usually sweeping
  thousands of pages where one bad response should cost one page. 2.4 million
  executions across six targets found none. CI runs 30 seconds per target.
- The catalog sweep moved into an unexported engine that both entry points
  share. `CatalogSeq` calls it directly rather than going through the
  deprecated `EnumerateCatalog`: building the supported API on top of the
  deprecated one would mean the deprecated path could not be removed later
  without rewriting the supported one.
- The package example and the live integration and canary tests exercise the
  iterators. The deprecated methods are thin wrappers now and keep their own
  offline coverage.
- `lightfeed` is now built, vetted and tested in CI. No workflow touched it
  before.
- Dependabot on all three modules and on the workflows, with `go-openapi`/
  `pb33f`/`swaggo` and the `chromedp`/`cdproto` pair grouped — they move as
  families, and separate PRs per module would be noise nobody reads.
- CodeQL with the `security-and-quality` queries, built explicitly rather than
  by autobuild, which would either miss the submodules or trip over the
  committed `go.work`.
- A release workflow that cross-compiles `gpscrape` for linux, darwin and
  windows on amd64 and arm64, stamps the version, and publishes checksums.
  stdlib-only and `CGO_ENABLED=0`, so this is a loop over GOOS/GOARCH rather
  than a build matrix.
- `SECURITY.md`, including what is explicitly *not* a vulnerability here:
  Google rate-limiting a caller, and fields going missing after an upstream
  payload change.
- `.golangci.yml` with no exclusions — everything the default set reported was
  either fixed or made explicit at the call site. Both `golangci-lint` and
  `staticcheck -checks=all` are clean.
- The coverage badge measures the library rather than the demo binaries. Their
  statement counts move between Go releases far more than the library's: on one
  commit, go1.26.4 reports 74.3% over the full profile and go1.27.0 reports
  68.5%, purely because coverage instrumentation changed.
- A committed `go.work` wires the three modules together for local
  development; `TestRootIsZeroDependency` forces `GOWORK=off` so the invariant
  is about this module rather than the workspace.
- `CONTRIBUTING.md` documents the layout, why `replace` is not an option here,
  and the tag order a release must follow.

- CI now lints the generated OpenAPI spec with Redocly (recommended ruleset) and
  Spectral (`spectral:oas`, failing on warnings) on the stable leg, pinned for
  reproducibility, so a future change cannot reintroduce a deprecation or lint
  warning unnoticed (the Go drift test only guards the schema `example`
  keyword). Ships the `apidoc/.spectral.yaml` ruleset.

- Three functions the batching migration left behind — `parseSuggestResponse`,
  `parsePermissionsResponse` and `buildURL` — were alive only through their
  own tests. `buildURL` also iterated a map, so its parameter order was
  nondeterministic, and did no percent-encoding. Deleted with those tests.

- Four small ones in `cmd/gpscrape`: a resumed sweep re-appended URLs already
  in its failed list, so the retry pass fetched them twice and the "remaining"
  count could go negative; the partial file was leaked when opening the done
  log failed; a panic in a command skipped the exit hooks, so the flight
  recorder wrote nothing and the log file was never closed; and `catalog new`
  returned from an emit failure without flushing the signal log's buffer.

- CI. The workflow token is read-only everywhere except the job that pushes
  the coverage badge; it used to run golangci-lint, govulncheck and two `npx`
  linters with a write-scoped token on disk. `FuzzShardStreamMatchesBuffer`
  joins the fuzz list, which had run nine of the ten targets. And `apidoc`
  gets the same second pass against this branch that `lightfeed` had: with
  `GOWORK=off` its drift test regenerated the spec from the previous release's
  types and could not see a change made here.

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
  million). `robots.txt` advertises two sitemap indexes covering ~83k gzipped
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
