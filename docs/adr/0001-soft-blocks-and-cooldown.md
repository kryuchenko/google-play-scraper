# ADR 0001: Treat soft blocks as refusals, pause the client after a refusal, and budget retries

- Status: Accepted
- Date: 2026-10-27
- Reviewed by: two independent reviews of the first draft; their findings are folded in below,
  and the points where this ADR departs from a reviewer are listed under "Dissent".

## Context

A batch job matched 344 game titles to Google Play package ids: one search page and one
batched details request per title, 4 workers, no throttle. After roughly 300 to 400 requests,
search pages kept coming back with no results. The job recorded neither status codes nor
bodies, so it is not known whether Google answered 429, redirected to `google.com/sorry`, or
served a 200 page without the data block. A rerun with 2 workers and a 1-second pause per
search page completed the remaining 246 titles.

The library already does most of what the literature recommends:

| Recommendation | Where it is in the library |
|---|---|
| One pacing point per host, not per worker | `waitThrottle`: one interval between request starts per `Client` |
| AIMD on explicit overload (Chiu and Jain 1989) | `WithAdaptiveThrottle`: speed up ×0.9 after 20 clean responses, slow down ×2 on 429/503 |
| Latency only as corroboration | `SlowStreak`, windowed minimum RTT |
| Exponential backoff with full jitter (Brooker 2015) | `RetryPolicy`, `backoff` |
| Honour `Retry-After` (RFC 6585, RFC 9110) | `RespectRetryAfter`, `retryAfterHint` |
| Retries consume rate | the throttle sits inside the retry loop |

These gaps remain.

1. **The controller judges a response before reading it.** `attempt` calls `observe` and
   `OnResponse` as soon as the status line arrives (`pipeline.go`), before the body is read.
   Google can answer a blocked client with a 200: a redirect chain ending on
   `https://www.google.com/sorry/...` (documented in `apidoc/google_endpoints.go`) or a page
   without the `AF_initDataCallback` blocks the parsers read. Such a response counts towards the
   next speed-up, is never retried, and reaches a parser that returns an empty slice with a nil
   error. The caller cannot tell "no results" from "blocked". `batchexecute` responses are
   decoded after `do` returns, so a block page there is also recorded as a clean 200.

2. **A refusal slows the client, it does not stop it.** After a 429 the adaptive interval
   doubles up to `Max`, and requests keep going. Slots reserved before the refusal still fire.
   `retryAfterHint` clamps the server's delay to `Max`, and `retryDelay` gives up outright when
   `Retry-After` exceeds `MaxDelay`, so a 60-second `Retry-After` under the CLI defaults ends the
   request instead of waiting. Crawler practice (Mercator, IRLbot, Olston and Najork 2010) and
   the circuit-breaker pattern (Nygard 2007) stop the host for a while and resume with one
   request.

3. **Retries are unbounded in aggregate.** `MaxAttempts` bounds one request. With concurrency N,
   a block turns every in-flight request into up to `MaxAttempts` attempts. The Google SRE book
   recommends a client-wide retry budget of about 10%.

4. **Callers swallow the errors that would explain a block.** `Search` stops paginating on any
   error and returns what it has with a nil error; `enrichSearchResults` skips failed lookups.

The literature does not offer a method that learns an undocumented limit without crossing it:
with accept/reject feedback only, two thresholds above the highest rate tried cannot be told
apart without trying higher. For a batch job, the expected cost of a long block outweighs the
time a faster rate saves. This ADR does not estimate the limit. It makes the existing
controller see the signals it misses and react to them firmly.

## Decision

### 1. Classify every response before the controller sees it

Each attempt produces exactly one verdict, and `observe` and `OnResponse` run once, after the
body has been read and classified. `OnResponse` receives the classification error, so a hook
never sees a soft block as a clean 200.

```go
type Verdict int
const (
	VerdictOK          // the expected data is there
	VerdictEmpty       // a page that positively says it has no data
	VerdictBlocked     // the server refused: google.com/sorry, a challenge, an HTML page where an RPC envelope belongs
	VerdictInterstitial // consent or sign-in page: waiting will not help
	VerdictMalformed   // a store page, but not the shape the parser needs
)

type SoftBlockError struct {
	Verdict Verdict
	Reason  string // short machine-readable cause, e.g. "sorry-redirect", "no-ds4"
	URL     string // final URL after redirects
}
```

Classification happens in three steps:

1. **Final URL, before the status branch.** `resp.Request.URL` is the last request of the
   redirect chain. A host other than the requested one decides the verdict whatever the status:
   `www.google.com` with path `/sorry` or `/sorry/...` is `blocked`; `consent.google.com` and
   `accounts.google.com` are `interstitial`. Any other host is left to the body checks: a
   caller's transport may legitimately rewrite the host (a proxy, a test server), and the first
   implementation, which called every foreign host `malformed`, broke exactly those. Matching is
   on parsed host and path, never on substrings. A client whose redirect policy stops at a 3xx keeps
   today's behaviour (a `StatusError`).
2. **Status.** Non-200 statuses behave as today. 429 and 503 are refusals.
3. **Body, by an endpoint validator.** Validators are attached per call, not to `get` or `post`:
   sitemaps and `robots.txt` are not HTML and get no body check.

| Endpoint | ok | empty | blocked | malformed |
|---|---|---|---|---|
| Any HTML page (generic) | has `AF_initDataCallback` | — | no `AF_initDataCallback` at all | — |
| Search | the parser finds results | `ds:4` section token decodes to `zero_results_cluster` | generic | neither |
| App details | `ds:5` present (region-locked pages included) | — | generic | `ds:5` absent |
| Developer, Similar, Data safety | `ds:3` present | — | generic | `ds:3` absent |
| `batchexecute` RPC | anything the envelope decoder can try | — | body is HTML | — |

The search check uses the parser's own result instead of a block-name rule, so the `ds:3`/`ds:1`
fallback in `extractSearchResults` keeps working. The zero-results marker was checked on a live
page: section `[0][1][0][0][3][0]` of `ds:4` is a base64 token containing `zero_results_cluster`.
The visible "No results for …" text is localized and is not used. Fixtures cover a populated
search, a zero-result search, a sorry redirect and an HTML page served to an RPC.

Frame-level `wrb.er` errors inside a valid envelope are not classified here. Their meaning is
not established, and the envelope decoder already reports them per item.

### 2. Controller reaction by verdict

| Signal | AIMD interval | Breaker | Retried by default |
|---|---|---|---|
| 429, 503, `blocked` | ×`Slowdown` | opens | yes, after the pause |
| transport error, other 5xx | ×`SoftSlowdown` | no | yes, within budget |
| `malformed` | ×`SoftSlowdown` | no | yes, within budget |
| `interstitial` | unchanged | no | no |
| 200 `ok`, 200 `empty`, 404 | clean | closes a half-open breaker | — |

`malformed` deliberately gets the soft treatment: a markup change at Google must not stall a
healthy client for minutes. Only positive evidence of refusal opens the breaker.

`Retry-After` is parsed in `attempt` and passed into the same observation, under the same lock,
so the pause and the interval are decided together. With the breaker off, `retryAfterHint`
keeps its current job of raising the interval, so that configuration behaves as before.

### 3. Pause the whole client after a refusal

New `AdaptivePolicy` fields, off by default:

```go
Cooldown    time.Duration // pause after a refusal; zero keeps today's behaviour
MaxCooldown time.Duration // cap on the escalating pause; zero means 10 × Cooldown
MaxProbes   int           // failed probes before waiting requests fail; zero means 3, negative means never
```

```go
type BreakerState int
const (BreakerClosed, BreakerOpen, BreakerHalfOpen, BreakerStopped)
```

**Admission.** `waitThrottle` becomes an admission step that returns a ticket carrying the
breaker generation it was admitted under, and whether it is the half-open probe.

- **Closed:** reserve a start slot as today. After sleeping to the slot, re-check the
  generation; if a pause began meanwhile, start admission again rather than fire.
- **Open:** wait until the pause ends, on a timer that also selects on a broadcast channel
  (closed and replaced on every state change) and on `ctx.Done()`.
- **Half-open:** the first arrival takes the probe and goes at once; everyone else waits on the
  broadcast channel. The probe ticket is released on every exit path: a server verdict resolves
  it, and cancellation, a request-construction error or a transport error release it without a
  verdict, so the next waiter can probe.
- **Stopped:** admission fails at once with an error wrapping `ErrBlocked` and the last refusal.

**Completion.** Only a ticket from the current generation can move the breaker.

- A refusal from a current closed ticket opens the breaker: generation +1, pause until
  `now + max(Retry-After, U(C/2, C))`, with `C = min(MaxCooldown, Cooldown × 2^k)`.
- A refusal from a stale ticket (sent before the pause began) never escalates and never counts
  as a failed probe. It can only lengthen the current pause to honour its `Retry-After`. Without
  this rule, four requests in flight when the block hits would count as three failed probes.
- A refusal on the probe increments `k` and reopens. When `k` reaches `MaxProbes`, the breaker
  goes to **stopped** for `MaxCooldown`: every waiter and every new request fails fast with
  `ErrBlocked`. After `MaxCooldown`, the next request becomes a probe. The client is never
  bricked; a long-lived service recovers by itself.
- A `Retry-After` longer than `MaxCooldown` goes straight to stopped for the server's period:
  the polite answer to "come back in an hour" is not to hold requests for an hour.
- A non-refusal response to the probe closes the breaker and resets the start schedule to
  `now`. The interval keeps the AIMD decrease. `k` resets only after a full clean `Window`.

**Retries.** When a retryable refusal happens with the breaker on, `do` does not compute a
backoff or apply `MaxDelay`: the next admission waits out the pause. `MaxAttempts` still bounds
the request.

### 4. Client-wide retry budget

```go
Budget float64 // retries allowed per first attempt; zero keeps today's unbounded behaviour
Burst  int     // retries available at start and the cap; zero means 10 when Budget > 0
```

A token bucket shared by the client: it starts with `Burst` tokens; every admitted first
attempt adds `Budget`, capped at `Burst`; a retry takes one token, atomically, or is not made
and the request returns its last error. A retry that follows a refusal while the breaker is on
is free: it waits out a pause, which already is the rate control. Cancelled waits refund
nothing because they took nothing.

### 5. Propagation

- `Search` returns the error instead of a partial result when pagination or `FullDetail`
  enrichment fails with `ErrBlocked` or a refusal. Other errors keep today's partial-result
  behaviour.
- Fan-out operations (`Availability`, `AppsMany`, catalog sweeps) keep per-item errors. When the
  breaker is stopped they fail fast without network traffic. The CLI cancels the run on the
  first transition to stopped and says why.

### 6. Observability

- `Hooks.OnBreaker func(BreakerEvent)` with from/to state, until, reason and probe count.
- `Client.Breaker() (BreakerState, time.Time)`.
- The library keeps its logging contract: Debug and Trace only. Each attempt's debug record
  gains `verdict` and `final_url`; breaker transitions log at Debug.
- The CLI wires `OnBreaker` to one stderr line per transition, with or without `-debug`.

### 7. CLI defaults under `-adaptive`

`Cooldown: 30s`, `MaxProbes: 3`, `Budget: 0.1`. Without `-adaptive`, nothing changes except
classification.

## Consequences

- Blocked runs stop and say why, instead of returning silently empty results.
- A run that hits a limit takes longer: at least half of `Cooldown` per refusal.
- **Behaviour change without opting in:** classification is always on. A response that used to
  come back as an empty result with a nil error now returns a `*SoftBlockError`. That is the
  bug being fixed, but it is visible to callers, and the changelog says so.
- Adding fields to exported structs breaks callers that use unkeyed struct literals for
  `AdaptivePolicy`, `RetryPolicy` or `Hooks`. Keyed literals are unaffected.
- A wrong validator can stall a healthy run. Mitigation: validators check only the structure
  their parser already depends on, `malformed` never opens the breaker, and fixtures pin ok,
  empty and blocked pages per validated endpoint.

## Also fixed

- `request.go` says the default client persists cookies. It has no cookie jar. The comment is
  corrected; adding a jar is a separate decision (it would change consent behaviour).
- `cmd/gpscrape` says `-adaptive` starts at two seconds. It starts at `-throttle`, by the design
  documented on `AdaptivePolicy.Start`. The comment is corrected.

## Dissent

- One review proposed removing the `ds:3`/`ds:1` fallback in `extractSearchResults`. Kept: the
  search validator judges the parser's outcome, so the fallback is harmless, and removing it is
  a separate change with its own risk.
- One review proposed exempting every half-open probe from the retry budget. Narrowed to
  retries that follow a refusal while the breaker is on; a probe triggered by other traffic is a
  first attempt and is not a retry at all.
- One review proposed typed validation of `wrb.er` frames. Deferred until their meaning is
  established on captured responses.
- Endpoint-specific validators for cluster pages and list pages are deferred: their parsers
  scan for a grid rather than read a fixed block, and the generic HTML check covers the block
  page case.
- Both reviews flagged `Search` pagination as swallowing errors. Implementing that showed a
  separate bug: pagination does not work at all. The token is read from a stale path and the
  legacy qnKhOb payload returns nothing, so `Num > 20` yields one page. That needs a fresh
  browser payload, as `Cluster` got, and is tracked in issue #16. The error propagation is in
  place for when it works; `FullDetail` propagation is covered by tests now.

## Alternatives considered

- **Estimate the limit online** (token-bucket identification, thresholding or safe bandits).
  Needs violations or richer feedback than Google gives, and the limit is not stationary: the
  `adaptive.go` notes record a details-page limit that differed between a 48-request probe and
  a 700-request run. Rejected for now.
- **Google SRE adaptive throttling.** Designed for a server fleet shedding load. A batch client
  is better served by queueing behind a pause. Rejected.
- **Delay-based control** (Vegas, BBR, Netflix Gradient2). Already rejected in `adaptive.go`.
- **Proxy rotation or fingerprint changes.** Out of scope: the goal is to respect the limit.

## References

- Chiu, Jain. Analysis of the Increase and Decrease Algorithms for Congestion Avoidance. 1989.
- Heydon, Najork. Mercator. WWW Journal, 1999. Najork, Wiener. WWW 2001 (the 10·t delay).
- Lee, Leonard, Wang, Loguinov. IRLbot. ACM TWEB, 2009.
- Olston, Najork. Web Crawling. Foundations and Trends in IR, 2010.
- Brooker. Exponential Backoff and Jitter. AWS Architecture Blog, 2015.
- Forero Cuervo. Handling Overload. Site Reliability Engineering, ch. 21, 2016.
- Nygard. Release It!, 2007 (circuit breaker).
- RFC 6585 §4 (429), RFC 9110 §10.2.3 (Retry-After).
- Farkiani, Liu, Crowley. Rethinking HTTP API Rate Limiting: A Client-Side Approach. arXiv:2510.04516, 2025.
- Amani, Alizadeh, Thrampoulidis. Linear Stochastic Bandits Under Safety Constraints. NeurIPS 2019.
- Gundelach, Mühlhauser, Herrmann. Detecting Bot Detection. ARES 2026.
