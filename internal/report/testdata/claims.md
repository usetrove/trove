# Token-saving tools: their claims next to Trove's measurements

Generated 2026-10-29 by `trove report claims --markdown`. Method: [benchmark methodology, section 11](https://github.com/usetrove/trove/blob/main/docs/benchmark-methodology.md#11-the-claims-table).

| Tool | Made by | Its claim | Effect on the weekly limit per session | Tokens or cost per session | Pass-rate change | Sessions (on / off) | Dates | Method |
|---|---|---|---|---|---|---|---|---|
| rtk | third-party | [reduces LLM token consumption by 60-90% on common dev commands](https://github.com/rtk-ai/rtk) (claimed) | -40.0% (95% range -41.7% to -38.3%); -0.013 points of the weekly limit per session (95% range -0.014 to -0.013); lower, estimated from API prices | tokens per session -40.0% (95% range -41.6% to -38.3%); lower, measured | pass rate -25.0 points (95% range -37.5 to -12.5; 40 and 40 sessions that ran tests), measured | 40 / 40 | 2026-10-17 to 2026-10-27 | [method](https://github.com/usetrove/trove/blob/main/docs/benchmark-methodology.md#11-the-claims-table) |
| hook_repeat_read | trove | none: Trove published no estimate for it before the test | -0.0% (95% range -2.8% to +2.9%); +0.00000 points of the weekly limit per session (95% range -0.00092 to +0.00099); no clear effect, estimated from API prices | tokens per session +0.0% (95% range -2.7% to +3.0%); no clear effect, measured | pass rate +0.0 points (95% range +0.0 to +0.0; 40 and 40 sessions that ran tests), measured | 40 / 40 | 2026-10-17 to 2026-10-27 | [method](https://github.com/usetrove/trove/blob/main/docs/benchmark-methodology.md#11-the-claims-table) |

- Every row is one randomized session test on this machine: each new session was given the tool or optimizer, or not, by a hash of its key. Sessions are counted under the arm they were given, whether or not it acted.
- Weekly-limit figures are estimates for one account, from this machine's meter readings (docs/limit-fit.md). They are not official weights.
- Ranges are 95% bootstrap ranges. "No clear effect" means the range includes no change; that is a result, not a gap.
- A tool's claim is quoted from its own documentation and is not checked by Trove. Trove's own claim is its shadow estimate, made before the test.
- Tools Trove looked at and did not run, and why: https://github.com/usetrove/trove/blob/main/docs/external.md#considered-and-not-run.
