# Optimizer verification checklist

What still needs a human (or a real API key) after the automated tests pass.
Automated coverage is described in `docs/optimize.md`; this file lists only what
tests cannot prove. Tick items off and note the date.

Status: nothing below has been run yet (written 2026-10-29).

Use a throwaway data directory so your real `~/.trove` is untouched, for example
`C:\tmp\trove-test`. For the real-traffic parts follow the setup in
`docs/gateway-manual-test.md` (API key in `ANTHROPIC_API_KEY`, gateway in Window A,
Claude Code pointed at it). Cost: a few cents on Haiku.

## 1. Re-check the provider facts I only partly read

- [ ] Prompt caching page: https://platform.claude.com/docs/en/build-with-claude/prompt-caching
  I read only the first 100,000 of about 166,000 characters. Read the rest and
  confirm, against `minCacheTokens` in `internal/optimize/breakpoints.go`:
  - the minimum cacheable tokens per model (512 / 1,024 / 2,048 / 4,096),
  - 4 explicit breakpoints maximum, and that top-level `cache_control` counts as one,
  - nothing in the remaining pages says a top-level marker is unsupported for any
    model or upstream you use.
  Then update the "verified" date in the comment above `minCacheTokens`.
- [ ] Model ids in `minCacheTokens` and `effortDefaults` (`suggest/suggest.go`) match
  the ids your agents really send (for example dated snapshots). An id that is not
  listed is skipped on purpose, so a missing id means the optimizer silently does nothing.
- [ ] Batch discount (50%) and effort levels: re-read
  https://platform.claude.com/docs/en/build-with-claude/batch-processing and
  https://platform.claude.com/docs/en/build-with-claude/effort if more than a few
  weeks have passed.

## 2. Shadow mode with real Claude Code

Goal: the numbers are plausible and nothing is changed.

1. In `C:\tmp\trove-test\config.toml`:
   ```toml
   [optimizers]
   noise_normalizer = "shadow"
   duplicate_result_collapser = "shadow"
   output_trimmer = "shadow"
   ```
2. Start the gateway, run a Claude Code session that runs a build or tests with
   noisy output and reads the same file twice.
3. Check:
   - [ ] `trove optimize status` lists the three as `shadow` and the rest `off`.
   - [ ] `requests.optimizations_applied` has entries with `"state":"shadow"` and
     non-zero `tokens_removed`; `requests.tokens_removed` is `0` (shadow removes nothing).
   - [ ] Claude Code behaves exactly as it does without Trove (no errors, same answers).
   - [ ] The shadow `tokens_removed` is the same order of magnitude as the analyzer's
     `shadow_savings` for the same request. They use different estimates, so they
     will not match exactly; a factor of 10 apart means something is wrong.

## 3. `on` mode with real Claude Code

1. Change `noise_normalizer` and `duplicate_result_collapser` to `"on"`, then
   **start a new Claude Code session** (states are pinned per session, so the
   running one will not change).
2. Check:
   - [ ] `trove optimize status` shows them `on`.
   - [ ] Rows show `"state":"on"`, `tokens_removed > 0`, and `cost_diff_usd` filled
     for requests with no cache writes (requests with cache writes may stay unpriced).
   - [ ] Claude Code still completes the task correctly after a tool result was
     cleaned or replaced by a "[Trove: this tool result is identical to ...]" note.
   - [ ] **Cache behavior (the important one).** Over 10+ turns, `cache_read_tokens`
     keeps growing the way it did with optimizers off. A cache read that drops to
     near zero on turns after the first means a prefix is changing: stop and report it.
   - [ ] Compare a session with the optimizers on and one with them off on the same
     task: total input tokens should be lower with them on.
3. Is the "estimated" label honest? Compare the recorded `tokens_removed` with the
   difference in billed input tokens between the on and off runs. Large
   disagreement means the 3.5 bytes-per-token ratio needs calibrating.

## 4. Pinning and the kill switch (needs a running gateway)

- [ ] **Mid-session config edit.** With a session running and optimizers `on`, set
  them to `off` in `config.toml`. The running session must keep being optimized; a
  new session must not be.
- [ ] **Restart mid-session.** Stop and restart the gateway during a session. The
  session keeps its optimizer states (pin reloaded from `session_pins`) and
  `cache_read_tokens` does not collapse.
- [ ] **Kill switch.** `trove optimize off` -> the very next request is forwarded
  unchanged (check `optimizations_applied` is empty). Expect one cache re-write on
  that turn; that is documented. `trove optimize resume` turns it back on. Also try
  `$env:TROVE_OPTIMIZE = "off"` before starting the gateway.

## 5. Custom-agent optimizers (needs a non-Claude-Code client)

`cache_breakpoints` and `volatile_prefix_mover` never touch `claude_code` or
`codex`, so they need a script or SDK client that sends requests without any
`cache_control`.

1. Add `[optimize] custom_agents = ["sdk"]` (or `"other"`, whichever label
   `trove gateway` records for your client; check the `agent` column).
2. Set `cache_breakpoints = "on"` (and optionally `volatile_prefix_mover = "on"`),
   start a new session, send 3+ requests with a system prompt of at least
   ~2,100 characters (so it passes the model's minimum) and a growing history.
3. Check:
   - [ ] Anthropic accepts the request (HTTP 200). A 400 mentioning `cache_control`
     means the top-level marker or the 4-breakpoint count is wrong: report it.
   - [ ] `cache_write_tokens` is non-zero on the first request and `cache_read_tokens`
     is non-zero on the next ones.
   - [ ] With a system prompt that contains `Today's date: <date>`, and
     `volatile_prefix_mover = "on"`, the cache still hits after the date line changes
     (change the date between runs).
   - [ ] A request that already has `cache_control` is forwarded unchanged.
   - [ ] The agent label `claude_code` with `custom_agents = ["claude_code"]` is
     rejected at startup with a plain-English message.

## 6. Things I could not test

- [ ] `go test -race ./internal/...` on a machine with cgo. It could not run here.
- [ ] macOS and Linux (`go test ./...`). Everything was run on Windows only.
- [ ] Streaming requests through the optimizer on `on` (the request body is
  rewritten but the response must still stream unbuffered). Send one streamed
  request and watch that tokens arrive progressively.
- [ ] Very large histories (2 MB+ bodies, ~21 ms per request in the benchmark):
  confirm the added latency is acceptable for your real sessions, using
  `latency_ms` / `ttft_ms` with optimizers on versus off.
- [ ] Re-run the latency check on your own machine:
  `go test ./internal/optimize -run Budget -v` (p50 must be under 5 ms at ~217 KB)
  and `go test ./internal/optimize -bench . -run xxx`.

## 7. Judgement calls to review

These were my choices; change them if you disagree.

- [ ] Duplicate-collapser note wording and the 200-byte minimum (`dedupe.go`).
- [ ] `noise_normalizer` collapses runs of 3 or more identical lines
  (`minRun` in `internal/noise/noise.go`). The analyzer's detector uses 5.
- [ ] Volatile-line patterns (dates, UUIDs, 10/13-digit epochs, four phrases) in
  `volatile.go`. A line of yours that matches by accident will be moved.
- [ ] `output_trimmer` heuristics (what counts as test output, how much context is kept)
  in `trimmer.go`. It is shadow-only, so this only affects estimates for now.
- [ ] Time budget default of 25 ms (`[optimize] budget_ms`). Over it, your request
  is forwarded unchanged; check `optimizations_applied` for `"failed":"timeout"` on
  big histories.
- [ ] Suggestion thresholds (20 requests minimum, output share 40%) in `suggest/suggest.go`.
  Run `trove optimize suggest --days 30` after some real use and judge whether the
  output is useful or noisy.

## 8. Cleanup

- `trove undo --data-dir C:\tmp\trove-test` restores `~/.claude/settings.json`.
- Delete `C:\tmp\trove-test` when done.
- If anything misbehaves, `trove optimize off` stops every optimizer immediately.
