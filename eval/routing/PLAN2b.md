# Second run, attempt b

Written 2026-11-08, after run 2a (see RESULTS2.md) and before any run of
this. PLAN2.md holds unchanged -- arms, tasks, seed, measures, decision
rules R6-R9, independent review -- with one change to the set-up:

- The sandbox settings add `"enableWeakerNetworkIsolation": true`, which
  lets Go programs reach the macOS trust service and verify TLS. Probed
  before this run: `gpscrape app` returns data; a request to pypi.org is
  still refused (403, host not on the allow list); a write to the home
  directory is still refused.

All 27 runs are repeated, arm A included, so the arms share one time
window. Run 2a's numbers are not pooled with these.
