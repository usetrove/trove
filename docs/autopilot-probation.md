# Autopilot probation (design, not built)

Status: **design for review, 2026-11-13. Nothing here is implemented.** It is gate G7 in
[autopilot-eligibility.md](autopilot-eligibility.md). Facts below about today's code were read from the code on that
date; the ones I could not confirm are in "Check before building".

## The problem

Autopilot promotes from shadow evidence. A shadow run shows a technique would remove tokens; it cannot show that the
agent still gets its work done once they are gone. That is fine for a lossless technique (the model receives the same
information). It is not fine for `hook_repeat_read`, `hook_quiet_commands`, `output_trimmer` and `hook_read_limit`,
which are recoverable: the model sees less, and can get it back at a cost. Two of them (`hook_quiet_commands`,
`hook_large_output`) have no shadow saving estimate at all, so shadow evidence cannot judge them even on tokens.

Probation is a step between "shadow evidence looks good" and "on for everyone". The technique is on for about half
of new sessions and off for the rest, and only a clean comparison promotes it.

## What already exists (and is reused, not rebuilt)

| Piece | Where | What it gives probation |
|---|---|---|
| Balanced, randomised, recorded per-session arms, never changed mid-session | `experiment.Assigner` (blocks of 4, table `experiment_assignments`) | the arm for each session; rule 5 holds |
| The gateway and the hooks both ask it | `measure.Gate.State`, `cli.pluginResolver` | the "off" arm runs the technique in shadow, so shadow evidence keeps flowing |
| Analysis with confidence ranges, strata, bootstrap | `experiment.Analyze` (cost, turns, tool errors, abandoned) | `Saves`, `CostsMore`, `WorseOutcomes`, `NoClearEffect`, `NotEnoughData` |
| Automatic rollback on a measured loss | `experiment.Step` | the safety net, already running |
| Per-technique guardrails | `measure.EvaluateHooks` (`hook_reread`, `hook_rerun`, `hook_fetch_back`) | fail-fast on the specific failure of each hook |
| Session pins | `session_pins` | which arm a session really ran, so mislabelled sessions are excluded |

So probation is mostly a new Autopilot state and a policy, not a new statistics engine.

## What is missing

1. **Autopilot has no probation state.** Today it goes shadow → candidate → on. Nothing tells the assigner "this
   technique is in probation", because the assigner only acts on optimizers the user has set to `on` in
   `config.toml`.
2. **Measuring a user with no gateway.** `experiment.Analyze` reads the `sessions` table, which needs priced
   requests and a roll-up (`outcome_signals`). A subscription user has hook events but no gateway requests, so
   today they would get `NotEnoughData` forever. This is the biggest gap and decides whether probation works for
   the people the plugin is for. See "Measuring without a gateway".
3. **Consent.** A user who agreed to `lossless-v1` agreed to lossless changes. Running a recoverable technique on
   half their sessions is a different promise.

## The states

```
shadow ──(shadow evidence passes; G2/G3/G5 hold)──▶ candidate ──▶ probation ──(clean comparison)──▶ on
   ▲                                                                  │
   └──────────(worse, costs more, or no benefit by the deadline; cooldown, reason shown)◀──┘
```

- **candidate → probation** only if the technique is recoverable (not lossless), the user is on `lossless-v2` or
  later, A/B comparison is enabled (`[experiment] enabled`, default true), and nothing else is in probation.
- **probation**: the technique is `on` in the state file, and the assigner decides each new session's arm. Sessions
  already running keep their pin. The "off" arm runs shadow.
- **probation → on** when `experiment.Analyze` says `Saves` (whole 95% range below zero on cost) and no outcome
  metric is `WorseOutcomes`, with at least `ProbationMinSessionsPerArm` sessions on each side. Then it is on for
  everyone from the next session (rule 5), and the existing guardrails and `Step` keep watching it.
- **probation → shadow** on `CostsMore`, `WorseOutcomes`, a guardrail trip, or `NoClearEffect` at the deadline
  (`ProbationMaxDays`), with the reason shown. A cooldown and `MaxAttempts` apply as today. "No clear effect" is an
  honest outcome, not a failure: it says Trove cannot show a benefit on this user's work.
- **A user's own `config.toml` setting always wins**, and `autopilot pause` or `off` end probation at the next
  session boundary.

## One at a time

Only one technique is in probation at once. Arms are randomised per technique, so two overlapping probations would
be statistically valid in principle, but interactions (both change what a Bash result looks like) would make the
result hard to attribute, and G8 already asks that each hook be attributable on its own. Queue order: the technique
with the best shadow evidence first; ties by registry ID. Cost: a longer wait for the second technique.

## How long it takes (stated plainly)

`MinSessionsPerArm` is 20 today, so a comparison needs at least 40 finished sessions. At 3 sessions a day that is
two weeks, per technique, and a noisy cost metric may still say `NoClearEffect`. The deadline stops probation
running forever. These numbers are placeholders until a benchmark sets them; they are labelled unvalidated wherever
shown. Probation is slow by design and cannot be made fast without making it unsafe.

## Measuring without a gateway

The same session key joins hooks, the gateway and history (`session.ClaudeCodeKey`), and the history scan writes
priced requests and sessions through the same writer. So a hooks-only user's sessions can reach the `sessions` table
after the fact, from their own Claude Code logs. Three consequences:

- **Delay.** History is read when `trove` scans it, not live, so a result lags by the scan interval. Probation
  decisions are made at session boundaries anyway.
- **Cost is a lower bound** (`logs.md`: history spend came out 1 to 8% below Claude Code's own totals, once 23%).
  Comparing two arms priced the same way cancels most of that, but the label must say the figures are from history
  and priced at list rates.
- **A subscription user does not pay per token**, so dollars are the wrong headline for them. The comparison for a
  hooks-only user should be tokens in the context per session (input including cache reads), with outcome signals
  beside it. Dollars stay for API-key users.

If these cannot be made to work, probation is an API-key feature and a subscription user stays on shadow for
recoverable techniques. That is an acceptable, honest outcome and must be shown as such, not hidden.

## Consent

`lossless-v2` is a new policy version with its own consent text, per the rule that widening changes what the user
agreed to. It must say, in plain English: some sessions will run with the change and some without, chosen at random,
so Trove can compare; Trove keeps it only if the comparison is clean; the user can stop at any time. A user on `v1`
keeps exactly the `v1` list until they agree. `config.AutopilotPolicy` is already compared on load.

## Privacy and the other rules

- Only metadata is stored: the assignment table, counts and the roll-up signals already in use (rule 7).
- All failures lower, never raise: if the database cannot be read the session runs shadow (rule 4).
- Every figure is labelled measured or estimated, and says which kind of data it came from (rule 6, rule 9).
- The technique's declared overhead is already inside the measured cost, so it is not subtracted twice
  (`Result.OverheadTokensPerSession` says so today).

## Check before building (unverified)

1. Do hooks write `session_pins` for the sessions they pin, in a form `experiment.load` accepts? Without pins every
   session is excluded as "not run as assigned".
2. Does `RollupSessions` write `outcome_signals` for sessions that came only from the history scan, and are tool
   errors and abandonment derivable for them? If not, the outcome metrics for hooks-only users need a source.
3. Whether hook-side outcome counts (`hook_events`) can stand in for gateway tool-signal counts, and which of the
   four metrics survive without the gateway.
4. Whether `Experiment.Enabled = false` (the user opted out of A/B) should block probation (recommended: yes, and
   say so) or fall back to shadow-only forever for those techniques.

## Build plan (each step a small commit with tests)

1. **Verify the four items above** against the code and a real hooks-only install; write the findings here.
2. **`Policy` fields** `ProbationMinSessionsPerArm`, `ProbationMaxDays`, and the `lossless-v2` policy version with
   the recoverable techniques listed but not yet promotable.
3. **The probation state** in `autopilot.json` and `Decide`: transitions, the one-at-a-time rule, deadlines,
   cooldowns, and tests for every edge (table test).
4. **The assigner handoff**: a probation technique is assigned even if the user's file says nothing, and only then;
   the engine and hooks pins are unchanged. Test that an Autopilot "on" outside probation never calls the assigner.
5. **Reading the result**: `Advance` calls `experiment.Analyze` for the technique in probation and maps the status
   to a transition.
6. **The hooks-only measurement path** from items 2 and 3 above, or a documented "gateway users only" fallback.
7. **Consent text, `trove autopilot` and `/trove:status` display** of "in probation: N of M sessions per side".
8. **A simulated probation** in the stress suite (known effect planted in the data) to show the decision is right in
   both directions, then one real run on the benchmark (single-hook arms, G8) before any technique is promoted.

## Decisions needed from you

- One technique in probation at a time (recommended) or several?
- For a user with no gateway: tokens per session as the headline (recommended), or dollars priced from history?
- If the user has opted out of A/B comparison: block probation (recommended) or ask once?
- Minimum sessions per arm for probation: keep 20 (two weeks at 3 sessions a day) or raise it?
