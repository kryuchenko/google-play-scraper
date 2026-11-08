# Second run: results

Plan: [PLAN2.md](PLAN2.md), amended before the rerun by [PLAN2b.md](PLAN2b.md).
Both runs are kept: [run2a/results.jsonl](run2a/results.jsonl),
[run2b/results.jsonl](run2b/results.jsonl). Two independent reviewers checked
the numbers below against the data (one also against the transcripts); their corrections are
applied, and the points that change how to read the results are in
"Review" at the end.

## Run 2a (2026-11-08): the CLI could not reach the store

27 runs, no harness errors, truth unchanged before and after (T7 1158 of
3000, T8 3.77 over 7,100 reviews, T9 7.2%).

Every gpscrape request in arms C and D failed with

    tls: failed to verify certificate: x509: OSStatus -26276

Go on macOS verifies certificates through the system trust service, which
Claude Code's Seatbelt sandbox blocks. Claude Code documents this ("Go-based
CLIs fail TLS verification on macOS") and two fixes:
`sandbox.enableWeakerNetworkIsolation` or `excludedCommands`. The pre-run
probe used curl, which verifies certificates itself, so it missed this.

PLAN2 has no rule for excluding a run that the environment broke. Read
literally, run 2a fails R6, R7 and R8: no C or D run retrieved data from the
CLI, C was correct 3/9 against A's 4/9, and C's median cost was $0.39
against A's $0.16. I treat it as a broken set-up rather than a measurement
of routing (the agents chose the CLI, and the CLI could not work), but that
exclusion was decided after seeing the failure; it is a judgement, not a
pre-registered rule.

Arm A had no CLI and was unaffected:

| Task | A correct | MCP calls per run |
|---|---|---|
| T7 3,000 ru reviews, count 1-star | 0/3 | 1, 2, 0 |
| T8 100 per language, every language | 1/3 | 7, 0, 3 |
| T9 2,000 en reviews, word share | 3/3 | 21, 21, 23 |

The correct T8 run did not use the MCP tools for the bulk of the work: it
wrote Python scripts and called the store's endpoint directly from the
sandbox (play.google.com was the allowed domain).

## Run 2b (2026-11-08)

Same plan; the sandbox adds `enableWeakerNetworkIsolation` (PLAN2b, written
before this run). All 27 runs repeated, arm A included. No harness errors, no
TLS errors in any transcript, truth unchanged before and after.

| Arm | Correct | Runs that retrieved data via the CLI | Skill read | Median cost | Median wall |
|---|---|---|---|---|---|
| A: MCP, no CLI | 2/9 | -- | -- | $0.28 | 32 s |
| C: MCP + CLI | 9/9 | 9 | -- | $0.08 | 23 s |
| D: MCP + CLI + Skill | 9/9 | 9 | 7 | $0.07 | 15 s |

"Retrieved data" counts a gpscrape data subcommand (not `-h`) whose output
had no error object; each C and D run has at least one, recounted from the
transcripts. C and D made no MCP calls, and every C and D answer matched the
reference exactly (1158, 3.77, 7.2). Two D runs first tried `-sort helpful`,
got `invalid_argument`, and retried with `helpfulness`.

Arm A: T7 0/3 and T8 0/3, answering "unknown" -- each of these six runs
named the gpscrape command that would do it and asked the user, rather than
installing anything; T9 2/3, paging 20 to 22 times, with one run stopped at
the 40-turn cap.

| Rule | As registered | Result | |
|---|---|---|---|
| R6 | C: CLI data call in at least 6/9 | 9/9 | met |
| R7 | D: CLI data call in at least 3 more of 9 than C | 9 vs 9 | not met |
| R8 | C at least 3 more correct than A, or median cost under half of A's without being correct less often | 9 vs 2 correct; $0.08 vs $0.28 (30%) | met, both ways |
| R9 | zero install attempts reaching a network; zero writes outside work/temp | none observed | met as far as observed |

## Reading it

- Above the threshold, the tool descriptions and server instructions were
  enough: with the CLI on PATH an agent used it in every run, Skill or not.
- R7's null is a ceiling: C was already 9/9, so the Skill had nothing to
  add on this measure. D's runs were faster (median 15 s against 23 s), but
  the plan set no rule for speed and two D runs never read the Skill, so
  this is an observation, not an effect of the Skill.
- Without the CLI, the hint still reached the agent: every failed T7 and T8
  run in arm A named the CLI.
- One model (Sonnet), three tasks, three runs per cell. The registered rules
  are counts, not significance tests.

## Review

Points from the independent reviews that bound the claims above:

1. The harness's own `cli_data_ok` counted help calls and treated a Bash
   call as successful even when a later command in it hid gpscrape's error.
   In run 2a this made every C and D run look like a CLI success. The
   counts in this file are recounted from the transcripts; `run2.py` is
   fixed for later runs.
2. The grader marked correctness from the extracted answer alone. No run
   was mis-graded (the one capped run had no answer), but a capped or
   timed-out run now counts as wrong in `run2.py`, as PLAN2 says, also when
   a drifted truth forces a regrade. The answer parser still takes the
   first number on the `ANSWER:` line, so a refusal that quotes a number
   could pass; none did here.
3. R9: the harness records neither network reachability nor a write audit.
   No install attempt and no write outside the working and temp directories
   was observed; the sandbox confines Bash, and the run also allowed the
   native `Edit` tool, which the sandbox does not cover.
4. T8's reference is the CLI's list of review languages, which the library
   does not claim is exhaustive. "Correct" means agreement with that
   reference, not proof that every language Google Play serves is covered.
5. PLAN2b is an outcome-informed change to the set-up that loosens network
   isolation for Go. Repeating every arm and not pooling with 2a limits the
   bias, but it is an amended run, not PLAN2 run unchanged.
6. Arm A is not a pure "MCP only" arm: with a shell and play.google.com
   allowed, an agent can write its own scraper, as run 2a's correct T8 run
   did. That makes A noisy (4/9 in 2a, 2/9 in 2b) and, if anything, makes it
   stronger than tools alone.
