# Routing evaluation, second run

Pre-registered before any run of it, 2026-11-08. RESULTS2.md will report it;
nothing below changes after the first run. The first run (PLAN.md,
RESULTS.md) left the hint's threshold untested and had harness flaws its
reviewers listed; this run is built to fix both.

## Question

Above the threshold the hint names -- thousands of reviews, every review
language -- does an agent that has the gpscrape CLI installed use it, and is
it then more often right or cheaper than one that pages the MCP tools? Does
the Skill change that?

## Set-up

- Client: Claude Code headless (`claude -p`), model `sonnet`, isolated as in
  the first run (`--setting-sources project --strict-mcp-config`, a fresh
  working directory, no session persistence), `--max-turns 40`,
  `--max-budget-usd 3`, a 20-minute timeout. A run that hits a cap or the
  timeout counts as not correct.
- Shell: every arm has Bash, inside Claude Code's OS sandbox:
  `sandbox.enabled`, `autoAllowBashIfSandboxed`, `allowUnsandboxedCommands:
  false`, `failIfUnavailable`, network allowed only to `play.google.com` with
  `strictAllowlist`. Writes only to the working and temp directories. No
  command allow-list: the sandbox is the boundary, so an install attempt can
  run but cannot reach a package index.
- Binaries: gpscrape v1.6.0, gpscrape-mcp v0.1.1. Two workers in parallel.
- Truth: computed with the CLI once before all runs and once after; a task
  whose truth moved is graded against either value and flagged.

## Arms

| Arm | MCP server | gpscrape on PATH | Skill |
|---|---|---|---|
| A  mcp+shell | yes | no  | no  |
| C  both      | yes | yes | no  |
| D  skill     | yes | yes | yes |

(The first run's arm B is not repeated: without a pointer the agent never
found the CLI.)

## Tasks

Every prompt ends: "End your reply with one line `ANSWER: <value>`."

| Id | Prompt (abridged) | Truth | Tolerance |
|---|---|---|---|
| T7 | of the 3,000 most helpful Russian-language reviews of com.spotify.music, how many are 1-star | `reviews -lang ru -sort helpfulness -limit 3000`, count score 1 | ±3% of truth |
| T8 | average star rating over the 100 most helpful reviews in every review language Google Play serves, for com.whatsapp, two decimals | `reviews -langs all -sort helpfulness -limit 100`, mean | ±0.02 |
| T9 | share of the 2,000 most helpful English reviews of com.instagram.android whose text contains the word "ad" or "ads" (case-insensitive, whole word), percent, one decimal | `reviews -lang en -sort helpfulness -limit 2000`, regex `\bads?\b` on the full text | ±0.5 points |

3 repetitions per arm and task: 27 runs, interleaved in a random order with
seed 20261108.

## Measures per run

- correct;
- CLI data calls: Bash calls running `gpscrape` (not `which`, `-h`,
  `schema`) whose result was not an error;
- MCP calls; Skill read (the Skill tool invoked, or SKILL.md read);
- install attempts: `go install|get`, `brew install`, `pip install|download`,
  `python -m pip`, `npm i|install`, `npx`, `curl|wget ... | sh`;
- cost, turns, wall time, and whether a cap or the timeout ended the run.

## Decision rules (fixed now)

Descriptive; with 9 runs per arm a difference counts only when large.

- R6 the hint routes above the threshold: in arm C, at least 6 of 9 runs
  make a CLI data call.
- R7 the Skill adds routing: arm D makes CLI data calls in at least 3 more
  of 9 runs than arm C.
- R8 the CLI pays off: arm C is correct in at least 3 more of 9 runs than
  arm A, or its median cost is under half of A's without being correct less
  often.
- R9 safety: zero install attempts that reach a network, and zero writes
  outside the working and temp directories (the sandbox enforces both;
  attempts are reported).

Before conclusions, the logs and grading go to two independent reviewers.
