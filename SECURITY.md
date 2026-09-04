# Security

## Reporting

Report vulnerabilities through [GitHub's private advisory
form](https://github.com/kryuchenko/google-play-scraper/security/advisories/new).
Please do not open a public issue first.

Expect an acknowledgement within a few days. This is a spare-time project, not
a product with an on-call rotation, and saying so is more useful than promising
an SLA that would not be met.

## What is in scope

The library parses undocumented payloads from a third party, so the attack
surface is almost entirely input handling:

- A crafted response that makes a parser panic, hang, or consume unbounded
  memory. Every parser is fuzzed against exactly this (`go test -fuzz`), so a
  reproducer is directly actionable — a failing input dropped into
  `testdata/fuzz/` is the ideal report.
- A path where data from Google reaches something that executes, writes
  outside the working directory, or is interpolated into a shell.
- Anything in `cmd/gpscrape` that mishandles a path or a file it writes.

## What is not

- **Rate limiting and blocking.** Google throttling or blocking a caller is the
  expected consequence of scraping too fast, not a vulnerability. `WithThrottle`
  and `WithRetry` exist to manage it.
- **Scraping being possible at all.** This reads pages that Google serves
  publicly without authentication. Whether a given use is permitted by
  Google's terms is a question for the person running it, not a defect here.
- **Wrong or missing fields after Google changes a payload.** The parsers are
  deliberately lenient and return zero values rather than failing, so that one
  upstream change does not abort a sweep of thousands of pages. Drift is
  tracked by the canary suite and reported as an ordinary bug.
- **Vulnerabilities in `apidoc` or `lightfeed` dependencies that this code does
  not reach.** `govulncheck` runs in CI and distinguishes reachable from
  merely present; a report is more useful with a call path.

## Supported versions

The most recent minor release of the current major line receives fixes. Older
majors do not, and the deprecation notes in the API name the release in which
something is removed, so an upgrade is never a surprise.

## What is done here already

- `govulncheck` on every push, gating on vulnerabilities that are actually
  reachable from this code rather than merely present in the module graph.
- CodeQL with the `security-and-quality` queries, weekly and on every change.
- Fuzzing of every parser in CI, seeded from recorded payloads.
- The root module has no third-party dependencies at all, enforced by a test.
  That is the largest single reduction in supply-chain surface available to a
  library, and it is why the browser-driven and OpenAPI parts live in separate
  modules that a caller opts into.
