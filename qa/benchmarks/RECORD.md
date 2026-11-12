# Benchmark record (Trove beta)

Created before any measurement, so the format cannot be shaped by the results.
Every number is labelled **measured** (observed on the machine named below) or **estimated**.
Nothing is filled in from memory or from a list price.

## Machine and build

| Field | Value |
|---|---|
| Commit | 2b2054f (hooks-rewrite) |
| Go | go1.27.3 windows/amd64 |
| OS, CPU, cores | Windows 11 Home; AMD Ryzen 5 5500U, 12 logical threads; laptop, on battery and shared with the load generator |
| Build flags | gateway and engine: default `go build` (the dev binary); load test: `CGO_ENABLED=0` |
| Upstream | `benchfake` mock on loopback, no provider call |
| Runs | one run per row, 2026-11-08; no warm-up beyond the first request |

## B1. Gateway overhead under concurrency (measured, zero spend)

Target: p50 added latency under the 5 ms budget (CLAUDE.md). Requests go through the real `trove gateway start`
to the local fake upstream. Overhead = gateway p50 minus direct p50. Each row is 20 requests per concurrent session.
Source: `qa/results/2026-11-08/overhead.tsv`.

| Concurrent sessions | Requests | p50 via gateway (ms) | p99 via gateway (ms) | p50 direct (ms) | p50 added by gateway (ms) | Errors | Status |
|---|---|---|---|---|---|---|---|
| 1 | 20 | 1.12 | 2.59 | 0.57 | 0.55 | 0 | measured |
| 10 | 200 | 4.37 | 19.30 | 0.74 | 3.63 | 0 | measured |
| 50 | 1000 | 10.10 | 82.45 | 2.74 | 7.36 | 0 | measured |

Reading: at 1 and 10 concurrent sessions the added p50 is inside the 5 ms budget. At 50 it is not (7.4 ms), on this
laptop, with the load generator on the same CPU. Treat the 50-session row as a finding to reproduce on a quiet
machine, not as a settled number. A single run per row is not a distribution.

Not run yet: sustained load (the plan's 10-minute soak), memory and goroutine counts, and 100 or 500 sessions.

## B2. Engine cost per request, all optimizers on (measured, zero spend)

Source: `qa/results/2026-11-08/engine-bench.txt`, `go test -bench BenchmarkEngineAllOn -benchtime=2s -count=5`.
The benchmark's own body sizes are 63 KB, 217 KB and 2067 KB. They differ from the 1 KB, 100 KB, 1 MB and 16 MB
sizes planned in RECORD's first draft, so no 1 KB or 16 MB number exists yet.

| Body size | Mean (ms) | Range over 5 runs (ms) | Allocations | Status |
|---|---|---|---|---|
| 63 KB | 1.15 | 1.13 to 1.20 | 1,115 | measured |
| 217 KB | 2.97 | 2.90 to 3.25 | 3,268 | measured |
| 2067 KB | 22.4 | 21.1 to 23.7 | 29,438 | measured |

Reading: the largest body is close to the 25 ms per-request budget (`budget_ms`). Past the budget the original request
is forwarded, so the optimizer's output is lost for that request, not the request. This is the number to watch.

## B3. Provider-side savings (needs real agent runs; not run)

Not run. Needs paid runs through the offline lab or a real agent, with `bench/` pinned models, paired units, and at
least the minimum sample per arm. `bench/results/smoke-real*` predate this record: their comparison files have
`n_pairs = 0` and verdict `need_more_data`, so they give **no** usable savings number. Do not cite them.

## Result log

Each run appends one line here: date, commit, command, and the numbers it produced.

- 2026-11-08, 2b2054f, `PER=20 bash qa/benchmarks/run_overhead.sh ./.dev/bin/trove.exe qa/results/2026-11-08`:
  B1 rows above. The first attempt sent direct requests to the wrong path (100% errors); it was fixed and rerun. The
  reported rows are from the fixed run only.
- 2026-11-08, 2b2054f, `go test -run '^$' -bench BenchmarkEngineAllOn -benchtime=2s -count=5 ./internal/optimize/`:
  B2 rows above.

## B4. Request-level optimizers on real Claude Code history (measured bytes, estimated tokens; zero spend)

Source: `qa/results/2026-11-08/replay.tsv`, produced by `qa/replay` over the 117 sessions in the owner's
`~/.claude/projects` (204 log files). Each sampled request is rebuilt from the log (every message before the assistant
turn) and run through the real engine, one optimizer at a time. Eight requests per session, evenly spaced: 801 requests.
Message text is never written out; only counts and byte totals.

| Optimizer | Mode | Requests changed | Request bytes removed | Estimated tokens removed |
|---|---|---|---|---|
| noise_normalizer | on | 60 of 801 (7.5%) | 30,283 of 157.9 MB (0.02%) | 8,652 |
| duplicate_result_collapser | on | 35 of 801 (4.4%) | 8,475 of 157.9 MB (0.01%) | 2,428 |
| output_trimmer | shadow (estimate only) | not counted: shadow changes nothing (see note) | 36,396 of 157.9 MB (0.02%) | 10,398 |

Note on the trimmer: its shadow estimate counts tokens that would be removed; nothing is changed. Its threshold is
6,000 bytes and 60 lines per tool result. The logs have 1,210 tool results that meet it, so the estimate here is from
the sampled requests only.

Limits, which change what these numbers mean:
- The logs do not contain the system prompt or the tool definitions. The real requests are larger, so the percentages
  above are overstated, not understated, relative to the real request.
- Requests are sampled (8 per session), not all of them. Long sessions are sampled the same as short ones.
- Tokens are the engine's estimate from bytes. Provider counts, and whether cache reads change, need paid runs (B3).
- The savings here are on request bytes, not dollars. Dollars need the provider's usage and the cache rules.

Reading: on real Claude Code history the three request-level optimizers together remove about 0.03% of request bytes
(about 21,000 estimated tokens over 801 requests). This does not support any savings claim for these optimizers as
configured for Claude Code. Do not state a savings figure from this table in the beta.
- 2026-11-08, 2b2054f, `go build -o qa/bin/replay.exe ./qa/replay && qa/bin/replay.exe -dir ~/.claude/projects -per 8`:
  B4 rows above. The first run reported zero for the trimmer because the engine's TokensRemoved counts only the on
  state; it was fixed to read shadow estimates from the applied reports, and the reported rows are from the fixed run.
