# trove bench workflows

Replays seven hand-scripted, everyday Claude Code sessions through the real optimizer engine, offline. No provider
call, no spend, no Docker. `bash bench/scripts/95_workflows.sh` builds the binary, runs it against a scratch data folder
(never your real `~/.trove`), and writes `workflows.md` and `workflows.json`.

The sessions: a quick question, fix a failing test, debug with logs, a dependency upgrade, a rename across a codebase,
explore and review a repo, and a ~90-turn feature session. All tool output is generated text shaped like the real thing
(pytest and tsc output, noisy colored logs, npm progress with CR/LF, repeated `git status` and `git diff`, files re-read
with and without an edit in between). No real code, paths or secrets.

## The numbers

| number | label | meaning |
|---|---|---|
| median and range of the saving across the workflows | **estimated** | list-price cost, off vs on, same cache model in both arms, declared optimizer overhead charged to the "on" arm |
| requests that kept the cached prefix byte-identical, broken invariants, repeat-run determinism | **measured** | the stress suite's checkers, run on every request |
| engine time per request, p50 and p99 | **measured** | in-process; the gateway adds its own forwarding time |

## Two tiers, never averaged

- **everyday**: a quick question, fix a failing test, debug with logs, a dependency upgrade, a rename across a codebase, explore
  and review, and a ~90-turn feature session.
- **heavy**: sessions dominated by large tool output, chosen because waste is high there: a monorepo test loop with a verbose
  reporter (about 500 tests per run), a failing container build rerun after each fix, and incident log pulls. Quote the heavy
  tier as a ceiling for output-heavy work, never as a typical day, and never average it with the everyday tier.

- **custom agent, no caching**: the everyday sessions again, run as a script or home-grown agent that sends no cache markers.
  The original request is billed as plain input every turn; the optimized one is billed with the cache only if the
  `cache_breakpoints` optimizer really added a marker. This saving is Anthropic's prompt caching, which Trove switches on for agents
  that do not use it. It is zero for an agent that already caches, and it does not apply to Claude Code, which manages its own caching.
  It assumes a 5-minute cache lifetime and a 10% chance of a cold cache on any turn.

## What limits the saving (`--analyze`, or `ANALYZE=1` in the script)

For Claude Code sessions the saving is capped by structure, and the analysis shows where:

1. **Ceiling.** What an oracle that deleted every tool output would save. Most of what is left is the system prompt, the model's own
   replies and the files it read, none of which a safe optimizer may remove. The gap between "saving now" and the ceiling is mostly
   file reads and search results.
2. **Size.** Making every output twice or four times as large does not raise the everyday saving much, because most of the growth is
   file content. The output trimmer never touches a file the agent read.
3. **Trimmer thresholds.** The trimmer acts only on output that looks like a test or build run (at least 10 recognizable lines) and is at
   least 6,000 bytes. Lowering the size thresholds to their allowed minimums changes nothing on these sessions, because the signature gate is the
   binding one and it is not tunable.
4. **A modelled optimizer.** Sending a re-read of a changed file as only its changed lines would add under one point to the everyday median,
   so it was not built. Measuring a feature in the model first is cheaper than shipping one that does not pay.

The size and threshold rows are what-ifs, not claims, and nothing in them changes a shipped default.

## Nothing lost

A saving only counts if nothing was lost. The run checks that every failure or error line in the original tool output is still
visible to the model, that every output the engine trimmed is stored in the recovery store byte for byte, that the cached prefix
stays byte-identical, and that a repeat run produces identical bytes. Any miss makes the command exit non-zero.

## What this does not show

The shape of each session (how often a file is re-read, how noisy a shell is, how long a session runs) is an assumption
written in `internal/stress/workflows.go`. It is not a measurement of real users, so the saving says what the optimizers
do on these patterns. It is not a forecast of anyone's bill, and the report says so. For real sessions use
`REPLAY=1 bash bench/scripts/95_workflows.sh`, which replays your own Claude Code logs read-only (aggregates only, nothing stored).
For a bill comparison use the paired study (`docs/benchmark-methodology.md`).
