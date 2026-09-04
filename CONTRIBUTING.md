# Contributing

## Repository layout

Three Go modules live here, and the split is deliberate:

| Module | Go | Purpose |
| --- | --- | --- |
| `.` | 1.25 | the library — **zero third-party dependencies**, enforced by `TestRootIsZeroDependency` |
| `apidoc/` | 1.27 | OpenAPI spec for Google's endpoints plus its drift tests; pulls swaggo and libopenapi |
| `lightfeed/` | 1.27 | optional browser-driven feed paginator; pulls chromedp |

The root module's floor stays conservative because consumers import it. The
submodules are tooling and an opt-in extra, so they track the current Go
release.

## Local setup

```bash
git clone https://github.com/kryuchenko/google-play-scraper
cd google-play-scraper
go build ./...
```

The committed `go.work` wires the three modules together so that a change in
the root is visible to `apidoc` and `lightfeed` without publishing anything.
Without it, both submodules would resolve the root module from the proxy and
you would be testing the last release instead of your working tree.

The workspace also picks the toolchain. `go.work` says `go 1.27.0`, because
the submodules need it, so any command run from the root without `GOWORK=off`
— the `go build ./...` above included — needs Go 1.27 even though the library
itself builds on 1.25. To build or test the root module on its 1.25 floor, set
`GOWORK=off`; that is what CI does for the whole root job.

Two things follow from that, and both are load-bearing:

- **The submodules' `go.mod` files must not contain a `replace` directive.**
  A `replace` is ignored in any module that is not the main module, so
  `go get github.com/kryuchenko/google-play-scraper/lightfeed` would resolve
  the root module to whatever version the `require` names — and fail outright
  if that version was never tagged. The workspace is the local-development
  mechanism; `replace` is not.
- **Anything that inspects the module graph must set `GOWORK=off`.** In
  workspace mode `go list -m all` reports every workspace module's
  requirements, so `TestRootIsZeroDependency` would see 67 modules instead of
  one. The test sets it itself; CI sets it for the whole root job.

## Running tests

```bash
# the library, offline (what CI runs)
GOWORK=off go test -short -race ./...

# the submodules
cd apidoc    && go test ./...
cd lightfeed && go test ./...

# live contract tests against play.google.com — slow, rate-limited, not for CI
GOWORK=off go test -tags canary -run TestCanary -v -timeout 15m ./...

# just the CLI end-to-end, against production
GOWORK=off go test -tags canary -run TestCanaryCLI -v -timeout 15m ./cmd/gpscrape
```

`-short` skips the network-backed tests. The offline suite runs off the
fixtures in `testdata/`; regenerate them with `internal/fixturegen` rather than
by hand.

### Production checks, on demand

The canary suite is the production check. It is not part of CI's normal run —
CI passes `-short`, so nothing there ever talks to Google. It runs weekly on a
schedule and from the Actions tab via `workflow_dispatch`, and locally with the
commands above.

Two halves, and they fail for different reasons:

- `TestCanary` (root package) asserts concrete fields on freshly fetched data.
  A red subtest names the method and the field, which means Google changed a
  payload — not that this repo regressed. `TestCanary/Batched` additionally
  compares the batched methods against the one-at-a-time ones they mirror;
  that is what detects drift in the reverse-engineered `Ws7gDc` request, which
  would otherwise degrade silently rather than fail.
- `TestCanaryCLI` (`cmd/gpscrape`) builds the binary and runs every command
  against production. Flag parsing, positional handling, the NDJSON contract
  and exit codes live only in the binary, and all of them can break while every
  library test stays green.

### Mutation testing

Coverage says a line ran, not that a test would notice it changing. To check
the tests actually assert something:

```bash
go install github.com/go-gremlins/gremlins/cmd/gremlins@latest
GOWORK=off GOFLAGS=-short gremlins unleash --workers 2 --timeout-coefficient 6 .
```

`GOFLAGS=-short` matters twice over: without it every mutant runs the live
suite, which is both slow and a few hundred requests to Google per mutant.

Two cautions learnt from running it. Warm the build cache first
(`go test -count=1 .`) — against a cold cache most mutants are reported TIMED
OUT, which counts as killed and inflates efficacy to a meaningless 100%. And
watch disk: gremlins copies the working tree per mutant per worker, and a
full-module run with four workers filled a drive that had over ten gigabytes
free.

It earns its keep. On this release it found two live mutants where `len(frame)
< 3` became `<= 3` in both batchexecute decoders: every fixture and mock
happened to produce seven-element frames, so the shortest valid frame — tag,
rpcid, payload — was never exercised.

## Before opening a pull request

```bash
gofmt -l .
GOWORK=off go vet ./...
go fix ./...          # modernizers; run with the current toolchain
```

`go fix` is toolchain-sensitive: its set of modernizers changes between Go
releases, and it respects each module's `go` directive, so it will not
introduce APIs newer than the module's floor.

## What ships

The module a consumer downloads is not the repository. Two things keep it
small, and both work by the same rule -- the module zip omits any directory
containing its own `go.mod`:

- `apidoc/` and `lightfeed/` are real submodules, opted into separately.
- `testdata/` holds a `go.mod` that is not a real module at all. Its only job
  is exclusion. The recorded pages there are 14.8MB against 0.8MB of actual
  library, so without it every consumer of a zero-dependency library downloads
  fifteen megabytes of HTML they will never open. Measured from `git archive
  HEAD`: the zip goes from 3513KB to 292KB, a 12x reduction.

  Measure from `git archive`, not from the working tree — `modzip.CreateFromDir`
  does not respect `.gitignore`, so a worktree measurement quietly counts
  `.DS_Store` and local editor state that the published module never contains.

The go tool ignores `testdata/` when matching packages, so that file is
invisible to `build`, `vet` and `test`, and the fixtures load exactly as
before. If you add a large fixture, it costs nothing downstream.

Do not remove it without re-measuring.

## Releasing

Before tagging anything:

```bash
GOWORK=off go test -race ./...                        # live, not -short
GOWORK=off go test -tags canary -run TestCanary ./...  # production check
$(go env GOPATH)/bin/golangci-lint run ./...
(cd apidoc && GOWORK=off go test ./...)
(cd lightfeed && GOWORK=off go test ./...)
```

The changelog entry for the version being released is the release body — the
Release workflow reads it out of `CHANGELOG.md`. A version with no section
there ships with empty notes, so write it before tagging, not after.

Push the release branch and the tags. Do not push `backup/*`; those exist to
make a local rewrite recoverable and are noise in a shared repository.

Tag order matters, because the submodules require a published version of the
root module:

1. Tag the root: `v1.4.0`.
2. Bump `require github.com/kryuchenko/google-play-scraper` in `apidoc/go.mod`
   and `lightfeed/go.mod` to that version, and commit.
3. Tag the submodules: `apidoc/v0.1.0`, `lightfeed/v0.1.0`.

Doing this in the other order bakes an unresolvable `require` into the
submodule tags, which cannot be fixed without a new tag. The `Publish check`
workflow resolves all three modules from the proxy and is the gate on getting
this right — run it via `workflow_dispatch` before announcing a release.

### Releasing a new major

The same constraint, one turn tighter. A submodule cannot require
`.../v2 v2.0.0` before that version is fetchable: `go.work` maps the path to a
local directory for `go list`, but the build still resolves the required
version through the proxy, so it fails with `GOPROXY=off` inside the workspace.

1. Release the previous minor first — the deprecation notes in it are what
   name the major as the removal version.
2. Tag the root: `v2.0.0`. The submodules still point at v1 at this commit and
   still build.
3. Commit the submodule move: `require .../v2 v2.0.0`, then `go mod tidy`.
4. Re-tag the submodules.

The submodules keep their own paths throughout — a module at
`.../apidoc` may require `.../v2` without taking a `/v2` suffix itself.

Raising the root module's `go` directive breaks builds for anyone pinned to an
older toolchain with `GOTOOLCHAIN=local` (distribution packages, air-gapped
builds, some corporate CI). Ship it in a minor release with a CHANGELOG entry,
never in a patch.
