# Routing evaluation: MCP tools, CLI, both, both with the Skill

Pre-registered before any run. Written 2026-11-07; the results go in
RESULTS.md next to this file, and nothing below changes after the first run.

## Question

gpscrape-mcp tells agents to use the gpscrape CLI for bulk jobs, and the
gpscrape Skill says the same in more detail. Does an agent actually route
bulk jobs to the CLI and small lookups to the tools, and does that pay off in
correct answers and cost?

## Set-up

- Client: Claude Code, headless (`claude -p`), model `sonnet`, isolated:
  `--setting-sources project --strict-mcp-config`, a fresh empty working
  directory per run, no session persistence, `--max-turns 25`,
  `--max-budget-usd 1`, an 8-minute timeout.
- Binaries: gpscrape v1.6.0 and gpscrape-mcp v0.1.1, both released.
- Store: the live Google Play Store. Ground truth is computed with the CLI
  immediately before each run and again after it; a task whose truth moved
  in between is flagged and graded against either value.

## Arms

| Arm | MCP server | Bash + gpscrape on PATH | Skill |
|---|---|---|---|
| A  mcp    | yes | no (Bash disallowed) | no |
| B  cli    | no  | yes | no |
| C  both   | yes | yes | no |
| D  skill  | yes | yes | yes (project .claude/skills/gpscrape) |

## Tasks

Every prompt ends: "End your reply with one line `ANSWER: <value>`."

| Id | Kind | Prompt (abridged) | Truth | Tolerance |
|---|---|---|---|---|
| T1 | small | installs bucket of com.spotify.music in the US store | `app` installs | exact |
| T2 | small | which rates higher in the US: com.spotify.music or com.pandora.android | `app` score | exact id |
| T3 | bulk  | of the 300 most helpful Russian-language reviews of com.spotify.music, how many are 1-star | `reviews -lang ru -sort helpfulness -limit 300` | ±2 |
| T4 | bulk  | average stars over the 100 most helpful reviews in each of en, de, fr (300) for com.spotify.music, one decimal | `reviews -langs en,de,fr -sort helpfulness -limit 100` | ±0.1 |
| T5 | small | in which of us, de, ru, kp, br can com.spotify.music be installed | `availability` | exact set |
| T6 | small | how many apps Spotify AB publishes in the US store | `developer` row count | exact |

3 repetitions per arm and task: 72 runs, interleaved in a random order with
a fixed seed (20261107).

## Measures per run

- correct (graded by script from the ANSWER line against the truth);
- interface: number of `mcp__gpscrape__*` tool calls, number of Bash calls
  that run `gpscrape`;
- install attempts: any Bash command containing `go install`, `brew install`,
  `pip install`, `npm i`, `curl ... | sh`;
- cost (total_cost_usd), turns, wall time.

## Decision rules (fixed now)

The sample is small, so every result is descriptive; a difference counts only
when it is large.

- R1 hint routes bulk work: in arm C, at least 4 of the 6 bulk runs (T3, T4)
  call the CLI.
- R2 the Skill adds routing: arm D calls the CLI in at least 3 more of the 6
  bulk runs than arm C. Fewer than 3 more is "no measurable difference".
- R3 routing pays off: on bulk tasks, C or D is correct in at least 3 more of
  6 runs than A, or costs less than half of A's median, without being
  correct less often.
- R4 small lookups stay on the tools: in arms C and D, at least 9 of the 12
  small runs each (T1, T2, T5, T6) answer with MCP tools and no CLI call.
- R5 safety: zero install attempts in any run. One attempt is a finding to
  fix, whatever the other rules say.

Before conclusions are written, the run logs and the grading go to two
independent reviewers (Codex and agy) without this plan's expectations.
