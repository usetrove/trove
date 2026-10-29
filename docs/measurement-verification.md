# Measurement verification checklist

What still needs a human (or real traffic) after the automated tests pass for
`internal/measure`. Method and limits are in `docs/measurement.md`; this file lists
only what the tests cannot prove. Tick items off and note the date.

Status: nothing below has been run yet (written 2026-10-29). Automated tests were
run on Windows only.

Use a throwaway data directory (for example `C:\tmp\trove-measure`) and the setup in
`docs/gateway-manual-test.md`.

## 1. Decisions to confirm

- [ ] **"Measured" label.** Dollar savings are always labeled "estimated" (removed
  tokens come from bytes). Only the on-vs-off comparison of provider-reported prompt
  tokens is labeled "measured" (`Corroborate` in `savings.go`). You asked for savings
  to be promoted to "measured" when corroborated; decide whether the dollar figure
  itself should be promoted, or whether the stricter behavior stays.
- [ ] **Defaults.** Baseline 7 days AND 20 sessions; `min_sessions` 20; retries and
  tool errors +30%; re-fetch 25%; offer at 10 sessions and $1.00. All are my
  choices, not measured values. Adjust under `[measure]` or in
  `DefaultOfferRules` (`report_data.go`).
- [ ] **Baseline rule.** The brief said "7 days or 20 sessions"; I used "both" so a
  quiet week or a busy hour is not a baseline. Confirm.
- [ ] **No abandonment guardrail.** Abandonment is recorded as a signal only: off
  sessions have no "optimizer acted" moment to compare against.

## 2. Baseline and gate with the real gateway

1. Fresh data dir, set `duplicate_result_collapser = "on"` in `config.toml`, start
   `trove gateway`, run Claude Code through it.
   - [ ] Requests show the optimizer in state `shadow`, not `on` (check
     `optimizations_applied` in the database), and the upstream body is unchanged.
2. Move `measure.install_at` back 8 days in the `settings` table and make sure 20
   sessions exist, then start a **new** session.
   - [ ] The new session's pin says `on`; sessions already running kept `shadow`.
3. Set `interleave = true`, run ten new sessions.
   - [ ] About half pin `on`, half `shadow`; a session never changes arm.

## 3. Signals on real traffic

- [ ] After a real session, `requests.signals` holds only integers
  (`SELECT signals FROM requests ORDER BY id DESC LIMIT 5`).
- [ ] `grep` the database file for a tool name and a file path you used; neither appears.
- [ ] After the idle gap, `sessions.outcome_signals` is filled (the gateway rolls up
  every 5 minutes). Counts match what you remember: repeated reads, a failing command.
- [ ] The re-fetch signal only counts a call repeated **after** a collapsed result.
  Check by hand on one session that triggered the collapser.
- [ ] "Abandoned after action" (at most 2 more requests, within 5 minutes) is not
  firing on sessions you simply finished. If it is, the heuristic is too loose.
- [ ] Git signals (optional): set `git_signals = true` and `git_repo`, make a commit
  during a session, confirm `commits` is a number. Not tested on macOS or Linux.

## 4. Guardrail rollback end to end

The tests use simulated sessions. With a throwaway database:

- [ ] Insert a regression history (30 baseline sessions, 25 "on" sessions with
  clearly more repeated calls), wait for the 5-minute loop or call `Evaluate`.
  A `guardrail_events` row appears and `optimizations` shows the optimizer as
  `shadow` with a `guardrail:` reason.
- [ ] A **new** session pins `shadow`; a session already running stays `on`.
- [ ] Nothing in `config.toml` changed.
- [ ] After `ClearOverride`, the old sessions are not held against the next attempt.
- [ ] Thresholds feel right on a few weeks of your own data: no rollback from
  ordinary variation. If it fires on clean data, raise `min_sessions` first.

## 5. Not built yet

- [ ] No CLI command shows these results. `trove report` (later) must call
  `TotalsBy`, `WasteByCategory`, `SavingsByOptimizer`, `GuardrailEvents`,
  `ShadowEstimates`, `Offers`, `Check`, and `ClearOverride`.
- [ ] `trove optimize status` does not mention baseline progress or rollbacks.
- [ ] The benchmark claim ("verified on benchmarks, Prompt 8") is text only until
  Prompt 8 exists. Do not ship that sentence before it does.
- [ ] Only Anthropic Messages traffic has tool signals; OpenAI sessions are left out
  of tool-based rules.
