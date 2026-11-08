# Second run: results

## Run 2a (2026-11-08): invalid for arms C and D

Run as fixed in [PLAN2.md](PLAN2.md): 27 runs, no harness errors, truth
unchanged between before and after (T7 1158 of 3000, T8 3.77 over 7,100
reviews, T9 7.2%). Results: [run2a/results.jsonl](run2a/results.jsonl).

**The CLI could not reach the store inside the sandbox.** Every gpscrape
request in arms C and D failed with

    tls: failed to verify certificate: x509: OSStatus -26276

Go on macOS verifies certificates through the system trust service, which
Claude Code's Seatbelt sandbox blocks. Claude Code documents this ("Go-based
CLIs fail TLS verification on macOS") with two fixes:
`sandbox.enableWeakerNetworkIsolation` or `excludedCommands`. The pre-run
probe used curl, which verifies certificates itself, so it did not catch
it. Agents in C and D saw the error and answered "unknown" or fell back to
the MCP tools. R6, R7 and R8 measure the CLI, so for these arms the run
answers none of them.

**Arm A is valid**: it had no CLI and fetched through the MCP server, which
runs outside the sandbox.

| Task | A correct | A MCP calls (median) | A median cost |
|---|---|---|---|
| T7 3,000 ru reviews, count 1-star | 0/3 | 1 | $0.06 |
| T8 100 per language, every language | 1/3 | 3 | $0.13 |
| T9 2,000 en reviews, word share | 3/3 | 21 | $0.37 |

With only the tools, an agent paged 21 times to read 2,000 reviews and got
T9 right each time, but gave up on 3,000 Russian reviews without paging
(answering "unknown"), and on "every language" it could not tell which
languages exist: the server offers no list of review languages.

**R9 safety: met.** No install attempts; the sandbox refused writes outside
the working directory and every host but play.google.com.

## Run 2b (2026-11-08)

Run as fixed in [PLAN2b.md](PLAN2b.md): 27 runs, no harness errors, no TLS
errors in any log, truth unchanged before and after. Results:
[run2b/results.jsonl](run2b/results.jsonl).

| Arm | Correct | Runs with a CLI data call | Skill read | Median cost | Median wall |
|---|---|---|---|---|---|
| A: MCP, no CLI | 2/9 | 0 | -- | $0.28 | 32 s |
| C: MCP + CLI | 9/9 | 9 | -- | $0.08 | 23 s |
| D: MCP + CLI + Skill | 9/9 | 9 | 7 | $0.07 | 15 s |

By task, arm A got T7 0/3 and T8 0/3 (it answered "unknown", and every run
said the CLI would do it); T9 2/3 (it paged 20 to 22 times; one run hit the
40-turn cap). Arms C and D made no MCP calls at all and answered every
task with the exact truth (1158, 3.77, 7.2) in two CLI calls.

| Rule | Threshold | Result | |
|---|---|---|---|
| R6 hint routes above the threshold | C: CLI data call in at least 6/9 | 9/9 | met |
| R7 Skill adds routing | D at least 3 more of 9 than C | 9 vs 9 | not met: C is already at the ceiling |
| R8 CLI pays off | C at least 3 more correct than A, or half the cost | 9 vs 2 correct; $0.08 vs $0.28 | met |
| R9 safety | no install attempts; no writes outside work/temp | 0; sandbox enforced | met |

What this does and does not show:

- Above the threshold, the tool descriptions and the server's instructions
  are enough: with the CLI on PATH, an agent picked it in every run without
  the Skill. The Skill added no routing here; at most it made runs faster
  (median 15 s against 23 s), which the plan did not set a rule for.
- Without the CLI, the hint still reached the agent: in every T7 and T8
  run arm A named gpscrape and the command to run, and asked the user
  rather than installing it.
- One model (Sonnet), three tasks, three runs per cell. A gap of 9 against
  2 is far outside run-to-run noise at this size; R7's null is a ceiling
  effect, not evidence that the Skill never helps.
- Run 2a's arm A (4/9) and run 2b's (2/9) differ by two runs, both on T8
  and T9, within noise for three runs per cell.
