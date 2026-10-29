# internal/measure: status, gaps and test checklist

Written 2026-10-29. Method and limits are in `docs/measurement.md`. The detailed manual
steps are in `docs/measurement-verification.md`. This file tracks what is left.

## 0. Build and tests
- The build was broken by a typo in `internal/store/writer.go:36` (`stringoka`). It is fixed.
- `go build ./...` and `go test ./...` pass (run on Windows only, 2026-10-29).
- Several packages reported `(cached)`, so rerun with `go test -count=1 ./...` before trusting them.

## 1. Built, with automated tests
| Spec item | Code | Tests |
|---|---|---|
| 1 Lifecycle: baseline, offer, on, interleave | gate.go, report_data.go (Offers) | lifecycle_test.go, report_data_test.go |
| 2 Savings math, cache effects, labels | savings.go | savings_test.go |
| 3 Signals (counts only), session rollup | signals.go, rollup.go | signals_test.go, report_data_test.go |
| 4 Guardrails, rollback, not-enough-data | guardrail.go, stats.go | guardrail_test.go |
| 5 Report data functions | report_data.go, savings.go, guardrail.go | report_data_test.go |
| Docs | docs/measurement.md | n/a |

## 2. Not built
- [x] `trove report` is built (`internal/report`, `docs/report.md`, updated 2026-10-29). It calls
  TotalsBy, WasteByCategory, WasteTotalIn, SavingsByOptimizer, GuardrailEvents, ShadowEstimates,
  Offers and LoadStatus, and supports `--json`.
  Still open: `Check` is used only through SavingsByOptimizer's corroboration, not for a
  per-rule guardrail summary, and no command exposes ClearOverride yet.
- [ ] `trove optimize status` does not show baseline progress or rollbacks (they appear in
  `trove report` under Optimizers).
- [ ] The benchmark claim ("verified on benchmarks, Prompt 8") is text only. Do not ship it
  before Prompt 8 exists.
- [ ] OpenAI sessions have no tool signals (Anthropic Messages only).
- [ ] No abandonment guardrail (recorded as a signal only).

## 3. Built but untested
- [ ] `git.go` (CommitCount): no test file. Not tried on macOS or Linux.
- [ ] `measure.Run` loop and its wiring in `internal/cli/gateway.go`.
- [ ] Gateway integration: signals written to `requests.signals`, and Gate keeps shadow in
  the real request path.
- [ ] Migration `0004_measure.sql` applied to an existing (upgraded) database.
- [ ] `stats.go` edge cases directly: Wilson interval, mean comparison, zero variance, tiny n.
- [ ] Acceptance check: rollback never fires on clean fixtures and fires on regression
  fixtures. The test exists; confirm it passes uncached.

## 4. Decisions to confirm
- Dollar savings are labeled "estimated". Only the on-vs-off prompt-token comparison is
  "measured". Keep this, or promote the dollar figure?
- Decided 2026-10-29: spend in dollars is also "estimated" (list prices, not an invoice); the
  token counts behind it are "measured". `TotalsRow.CostUSD` now carries the estimated label.
- Defaults: baseline 7 days AND 20 sessions (the spec said "or"), min_sessions 20, retries
  and tool errors +30%, re-fetch 25%, offer at 10 sessions and $1.00.

## 5. Manual test checklist
Use a throwaway data dir and `docs/gateway-manual-test.md`.
- [ ] Baseline: new installs run optimizers in shadow; upstream requests are unchanged.
- [ ] After 8 days and 20 sessions, a NEW session pins `on`; running sessions keep `shadow`.
- [ ] Interleave: about half the sessions on, half shadow; a session never switches.
- [ ] `requests.signals` holds only integers. Grep the DB for a tool name and a file path: absent.
- [ ] `sessions.outcome_signals` is filled after the idle gap (5 minute loop).
- [ ] Re-fetch counts only repeats after a collapsed result. Abandonment does not fire on
  sessions you simply finished.
- [ ] Optional git signals: `commits` is a number only.
- [ ] Rollback end to end: a regression history creates a `guardrail_events` row and sets the
  optimizer to `shadow` with a `guardrail:` reason. New sessions pin shadow, config.toml is
  untouched, and ClearOverride forgives old sessions.
- [ ] Thresholds do not fire on weeks of your own normal data.

The report layer's own checklist (report, budget, receipts, status line, share card, setup) is in
`docs/report-verification.md`.

## 6. Order of work
1. Add the missing tests (git, stats, gateway signals path, migration).
2. Build `trove report` and the `optimize status` changes.
3. Run section 5 by hand.
4. Commit in small commits.
