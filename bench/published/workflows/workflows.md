# Scripted workflows: what the optimizers do on Claude Code sessions

Model claude-sonnet-5-5, list prices as of 2026-11-08, seed 20261108. Scripted sessions, not real users: see the limits at the end.

## The numbers

1. **Estimated saving, everyday sessions: median 3.7% of list-price cost across 7 workflows (range 0.0% to 8.6%).** Estimated.
2. **Estimated saving, heavy-output sessions: median 19.3% across 3 workflows (range 2.5% to 50.7%).** Estimated. These sessions are dominated by large tool output (verbose test runs, build logs, incident logs) and were chosen because waste is high there. A ceiling for that kind of work, not a typical day.
2b. **Estimated saving, custom agents that send no cache markers: median 72.0% across 7 workflows (range 51.6% to 79.8%).** Estimated. Applies only to a script or home-grown agent that does not cache its prompt, where the cache_breakpoints optimizer adds the markers. Claude Code manages its own caching, so this does not apply to it.
3. **Nothing lost, and cache safe: 698 of 698 failure and error lines in the original output are still visible to the model, trimmed output is recoverable byte for byte, 400 of 400 requests kept the cached prefix byte-identical, 0 broken invariants, repeat runs byte-identical: true.** Measured by the checkers in this run.
4. **Engine time per request: p50 2.54 ms, p99 17.09 ms.** Measured in-process on this machine; excludes the gateway's forwarding time.

## By workflow

| workflow | tier | turns | input tokens (est.) | tokens removed (est.) | outputs trimmed | cost off (est.) | cost on (est.) | saving (est.) | key lines kept | prefix kept | violations |
|---|---|---|---|---|---|---|---|---|---|---|---|
| quick-question | everyday | 3 | 60084 | 0 | 0 | $0.0635 | $0.0635 | 0.0% | 0/0 | 3/3 | 0 |
| fix-failing-test | everyday | 12 | 333151 | 0 | 0 | $0.1418 | $0.1418 | 0.0% | 10/10 | 12/12 | 0 |
| debug-with-logs | everyday | 12 | 350233 | 12976 | 0 | $0.2730 | $0.2628 | 3.7% | 70/70 | 12/12 | 0 |
| dependency-upgrade | everyday | 10 | 227105 | 10823 | 0 | $0.1594 | $0.1513 | 5.1% | 10/10 | 10/10 | 0 |
| rename-across-codebase | everyday | 28 | 1084741 | 38 | 0 | $0.5567 | $0.5566 | 0.0% | 8/8 | 28/28 | 0 |
| explore-and-review | everyday | 20 | 701600 | 45299 | 0 | $0.4750 | $0.4416 | 7.0% | 0/0 | 20/20 | 0 |
| long-feature-session | everyday | 94 | 8215584 | 784877 | 0 | $3.5912 | $3.2806 | 8.6% | 80/80 | 94/94 | 0 |
| ci-failure-triage | heavy | 15 | 914425 | 471952 | 32 | $0.4420 | $0.2181 | 50.7% | 4/4 | 15/15 | 0 |
| container-build-debug | heavy | 14 | 465410 | 95980 | 27 | $0.2675 | $0.2159 | 19.3% | 66/66 | 14/14 | 0 |
| incident-log-forensics | heavy | 13 | 917063 | 22029 | 0 | $0.4405 | $0.4295 | 2.5% | 276/276 | 13/13 | 0 |
| quick-question (custom agent, no caching) | custom-agent | 3 | 59597 | 0 | 0 | $0.1234 | $0.0597 | 51.6% | 0/0 | 3/3 | 0 |
| fix-failing-test (custom agent, no caching) | custom-agent | 12 | 329117 | 0 | 0 | $0.6819 | $0.1986 | 70.9% | 10/10 | 12/12 | 0 |
| debug-with-logs (custom agent, no caching) | custom-agent | 12 | 349418 | 12553 | 0 | $0.7231 | $0.1517 | 79.0% | 57/57 | 12/12 | 0 |
| dependency-upgrade (custom agent, no caching) | custom-agent | 10 | 228229 | 10512 | 0 | $0.4826 | $0.1577 | 67.3% | 10/10 | 10/10 | 0 |
| rename-across-codebase (custom agent, no caching) | custom-agent | 28 | 1079606 | 0 | 0 | $2.2186 | $0.4814 | 78.3% | 8/8 | 28/28 | 0 |
| explore-and-review (custom agent, no caching) | custom-agent | 20 | 706688 | 44561 | 0 | $1.4643 | $0.4097 | 72.0% | 0/0 | 20/20 | 0 |
| long-feature-session (custom agent, no caching) | custom-agent | 94 | 8121897 | 781265 | 0 | $16.4598 | $3.3321 | 79.8% | 89/89 | 94/94 | 0 |

- **quick-question** (everyday): Three turns: read a file, search, answer. The case where little can help.
- **fix-failing-test** (everyday): pytest fails, read the test and source, edit, rerun twice until green.
- **debug-with-logs** (everyday): Run the app, read noisy colored logs, search them, fix, restart and read the same startup noise again.
- **dependency-upgrade** (everyday): npm install with progress noise, build warnings that repeat, CRLF test output, fix, rebuild.
- **rename-across-codebase** (everyday): Search every usage, read and edit many files, type-check, re-read, fix, type-check again.
- **explore-and-review** (everyday): Orient in an unfamiliar repo: many reads, repeated status and diff, a second pass over key files.
- **long-feature-session** (everyday): About 90 turns: explore, implement across files, test, debug, refactor. A normal long day.
- **ci-failure-triage** (heavy): Monorepo with a verbose test reporter (about 500 tests per run): four full runs while fixing failures one by one.
- **container-build-debug** (heavy): A failing container build: long build logs rerun after each Dockerfile fix, plus container logs.
- **incident-log-forensics** (heavy): Pull production logs repeatedly: long logs with the same stack trace over and over, and polling the same cluster status.
- **quick-question (custom agent, no caching)** (custom-agent): Three turns: read a file, search, answer. The case where little can help.
- **fix-failing-test (custom agent, no caching)** (custom-agent): pytest fails, read the test and source, edit, rerun twice until green.
- **debug-with-logs (custom agent, no caching)** (custom-agent): Run the app, read noisy colored logs, search them, fix, restart and read the same startup noise again.
- **dependency-upgrade (custom agent, no caching)** (custom-agent): npm install with progress noise, build warnings that repeat, CRLF test output, fix, rebuild.
- **rename-across-codebase (custom agent, no caching)** (custom-agent): Search every usage, read and edit many files, type-check, re-read, fix, type-check again.
- **explore-and-review (custom agent, no caching)** (custom-agent): Orient in an unfamiliar repo: many reads, repeated status and diff, a second pass over key files.
- **long-feature-session (custom agent, no caching)** (custom-agent): About 90 turns: explore, implement across files, test, debug, refactor. A normal long day.

## Limits (say these when you quote the numbers)

- The workflows are scripted from typical Claude Code patterns. How often a file is re-read, how noisy a shell is and how long a session runs are assumptions written in internal/stress/workflows.go, not measurements of real users.
- Costs are estimates at list price on the pinned model and the pricing date shown. Token counts use the engine's bytes-per-token ratio, not provider counts. Declared optimizer overhead is charged to the optimized arm.
- The cache model is the realism layer's: the cached prefix is what matches the previous request byte for byte, and each turn has a 10% chance of a cold cache in both arms.
- Engine timings are measured in-process on this machine and exclude the gateway's own forwarding time.
- Savings from these sessions say what the optimizers do on these patterns. They are not a forecast of any user's bill.
- The heavy tier was written to be waste-rich. Its saving is where large, repetitive tool output dominates the session; do not quote it as what a typical session saves, and never average it with the everyday tier.
- A saving only counts if nothing was lost: every failure or error line in the original output must still be visible to the model, and every output the engine trimmed must be recoverable from the recovery store byte for byte. The report counts both.
