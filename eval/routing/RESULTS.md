# Routing evaluation: results

Run 2026-11-07 against gpscrape v1.6.0 and gpscrape-mcp v0.1.1, as fixed in
[PLAN.md](PLAN.md): 72 runs of Claude Code (`claude -p`, model sonnet), 4 arms
x 6 tasks x 3 repetitions. No harness errors, no ground-truth drift, total
cost $5.48. Two independent reviews (Codex and agy) recomputed every rule
from the 72 logs and spot-checked 10 and 11 runs; both found the stored
answers, grades and counters faithful to the logs and reached the verdicts
below.

## Numbers

| Arm | Small tasks correct | Bulk tasks correct | Bulk runs that call the CLI | Median cost, bulk | Install attempts |
|---|---|---|---|---|---|
| A  MCP only          | 12/12 | 6/6 | 0 (Bash disallowed) | $0.211 | 0 |
| B  CLI only          |  8/12 | 1/6 | 0/6 | $0.043 | 8, in 4 runs |
| C  MCP + CLI         | 12/12 | 6/6 | 2/6 (1/6 with data) | $0.173 | 0 |
| D  MCP + CLI + Skill | 12/12 | 6/6 | 2/6 (1/6 with data) | $0.077 | 0 |

"With data": the CLI call actually ran and returned reviews; the others were
`which gpscrape`, `-h`, or commands the permission filter refused.

## Decision rules

| Rule | Verdict | Numbers |
|---|---|---|
| R1 the server hint routes bulk work to the CLI | **not met** | C: 2/6 bulk runs call the CLI, 4 needed |
| R2 the Skill adds routing | **no measurable difference** | D 2/6 vs C 2/6 |
| R3 routing pays off | **met, but not by routing** | all of A, C, D are 6/6 correct; D's median cost is 36% of A's. Three of D's four cheap bulk runs fetched with MCP like A and only summed the results with `jq`; A had no shell and counted by reading. The saving is local arithmetic, not the CLI |
| R4 small lookups stay on the tools | **met** | C 12/12 and D 12/12 used MCP and no CLI, all correct |
| R5 no install attempts | **not met** | 8 attempts in 4 runs, all in arm B; one succeeded (see below) |

## What the numbers support

1. **Without the MCP server or the Skill, an agent does not find the CLI.**
   In arm B gpscrape was on PATH in all 18 runs and was called 0 times.
   Instead the agent tried `pip install google-play-scraper` (an unrelated
   PyPI package) in 4 runs, and answered from memory in others. The pointers
   in the server instructions and the Skill are what make the CLI usable at
   all.
2. **Small lookups go to the MCP tools and come back right** in every arm
   that has them (36/36).
3. **At 300 reviews the hint is mostly not followed, and that is defensible.**
   Three MCP pages of 100 answered every bulk task correctly. The hint speaks
   of thousands of reviews; these tasks were below that threshold, so they do
   not test it.
4. **Default permissions get in the way of the recipe the hint and the
   Skill teach.** In headless Claude Code with an allow-list, `> file`
   redirection, compound commands and shell variables were refused, and two
   runs that started on the CLI fell back to MCP after the refusals
   (C-T3-1, D-T3-2). In an interactive session the user would be asked
   instead; in automation the recipe needs those permissions granted.

## What they do not support

Both reviewers list the same limits; in order of weight:

- **The harness leaked.** `python3:*` was on the allow-list, so
  `python3 -m pip install --target /tmp/gps_lib google-play-scraper` ran in
  B-T3-2. The package was removed after the run. Allowing an interpreter is
  allowing everything.
- **The CLI counter counts intent, not use.** It includes refused calls,
  `which gpscrape` and `-h`; it misses `pip download` among installs.
- **Skill use is not shown.** Arm D offered the Skill; no log shows it read.
- **Arm B rewards memory.** With web tools refused, 8/12 small answers came
  from the model's training data, not the store.
- **Scope.** One app, 6 bulk observations per arm, lenient grading (±2, a
  substring match for T2), and prompt-cache warm-up noise in costs.

## Changes this suggests

- Keep the CLI pointer in both the server instructions and the
  `play_get_reviews` description: arm B shows what happens without it.
- Say in the Skill and the hint that the CLI recipe needs shell permissions
  for redirection and pipes, and give a form that needs fewer of them
  (`gpscrape reviews ... -fields score | jq ...` writes nothing to disk).

## A second run, if wanted

Fix the harness first: no interpreters on the allow-list (or a throwaway
container with no network but the store), count only CLI calls that ran and
returned data, detect Skill reads, catch `pip download`. Then use tasks
above the hint's threshold (3,000+ reviews, all languages) where paging MCP
is genuinely worse, several apps, and arms with the shell permissions the
recipe needs. Pre-register it like this one.
