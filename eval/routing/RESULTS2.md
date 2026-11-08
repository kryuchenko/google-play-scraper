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

## Run 2b

Pre-registered in [PLAN2b.md](PLAN2b.md) before it ran; results below once
it has.
