# Autopilot probation (design, not built)

Status: **design, decisions made and the four open questions verified, 2026-11-13. Nothing here is implemented.** It is gate G7 in
[autopilot-eligibility.md](autopilot-eligibility.md). Facts below about the code were read from it on that
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
| The gateway and the OpenCode-style plugin engine ask it (the Claude Code hooks do not, see "Verified") | `measure.Gate.State`, `cli.pluginResolver` | the "off" arm runs the technique in shadow, so shadow evidence keeps flowing |
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

## Decisions (made 2026-11-13, the recommended options)

- One technique in probation at a time.
- A user with no gateway is judged on tokens per session, with outcome counts beside it, not dollars.
- A user who has opted out of A/B comparison (`[experiment] enabled = false`) is never put in probation. Their
  recoverable techniques stay in shadow and Autopilot says why.
- `ProbationMinSessionsPerArm` stays at 20 (placeholder, labelled unvalidated).

## Verified against the code (2026-11-13)

The first draft of this design assumed the existing A/B analysis would work for a user with no gateway. **It does
not.** Four checks, with what was found:

1. **Do the hooks write `session_pins`? No.** The Claude Code hooks pin a session's states into a per-session state
   file at `SessionStart` (`hooks/handlers.go`, `reset`), from a snapshot. They write nothing to `session_pins` and
   never ask the assigner for an arm. Only the gateway (`measure.Gate`) and the OpenCode-style plugin engine
   (`cli.pluginResolver`) do. `experiment.load` joins `session_pins`, so a hooks-only session is excluded from every
   comparison today.
2. **Do history-only sessions get roll-ups? No.** The history scan writes `history_requests` (tokens, cost, tool
   names, tool result bytes, error counts per request), a separate table from `requests` and `sessions`. It writes no
   `sessions` row and no `outcome_signals`, and `RollupSessions` only reads `sessions`. `experiment.Analyze` joins
   `sessions`, `session_pins` and `outcome_signals`, so history sessions never reach it. Two more facts: the
   history scan runs only when the user runs a `trove` command (`cli/logs.go`), not on a schedule, and the
   periodic loop that runs roll-ups and `experiment.Step` lives in the gateway process, which a hooks-only user does
   not run.
3. **Can hook events stand in? Partly.** `hook_events` records only what a hook did (`blocked`, `would_block`,
   `would_rewrite`, `rewritten`, `reissued`, `reread_anyway`, `fetched_back`), with no column for which arm a
   session was in, and nothing for a session where the hook never acted. So it can say whether the arm really ran
   (an "on" session with at least one real action), and give the per-hook failure counts the guardrails already use,
   but it cannot give a session count per arm or a cost.
4. **Does opting out of A/B stop it? Yes for the assigner.** Both the gateway and the plugin engine only create an
   assigner when `[experiment] enabled` is true, so with it off no arm is ever assigned. This matches the decision.

What this means: probation for a gateway user can reuse `experiment.Analyze` as designed. Probation for a hooks-only
user needs three new pieces, and is the larger half of the work.

## Probation for a user with no gateway (new work)

1. **Assignment in the hooks.** At `SessionStart` the hooks ask the same `experiment.Assigner` for the arm of the one
   technique in probation (the bookkeeping pass that already runs `autopilot.Advance` has the database open) and pin
   shadow for the "off" arm. The assignment is recorded in `experiment_assignments`, as for the gateway. If the
   database cannot be read, the session pins shadow (rule 4).
2. **A measurement source for sessions the gateway never saw.** Build per-session figures from `history_requests`
   joined on the shared session key (`session.ClaudeCodeKey`): tokens in the context per session (input, cache write
   and cache read), turns (request count), tool errors (the stored error counts). Abandonment has no source here and
   is left out rather than guessed. The arm comes from `experiment_assignments`, and a session counts as "on" only if
   the hook acted at least once in it (`hook_events`); an "on" session where it never acted is excluded, as the
   gateway path already excludes sessions not run as assigned.
3. **Something that runs it.** The history scan and the comparison have to run without the user typing a command.
   The cheapest place is the same bookkeeping pass at session boundaries, running an incremental scan (0.4 s on 119
   files, `logs.md`) in the background and never on the hook's own path. Decisions are made only there, so a
   probation result lags by about one session, which is fine because promotion happens at session boundaries anyway.
   A cost to check: a hook must stay fast, so the scan must not run inline.

The comparison reuses the existing statistics (the seeded bootstrap in `experiment`) on these per-session figures, and
keeps the same promote and roll-back thresholds. History token counts and any list-priced cost are a lower bound
(`logs.md`: 1 to 8% below Claude Code's own totals, once 23%); comparing two arms measured the same way cancels most
of that, and the label says so.

Step 3 was done as a bounded scan at session end (see the build plan, item 7), not as a background process, so
the hooks are not slowed beyond 700 ms at a point where the agent is already finished.

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

## Build plan (each step a small commit with tests)

1. ~~Verify the four open questions.~~ Done, above.
2. ~~**`Policy` fields** `ProbationMinSessionsPerArm`, `ProbationMaxDays`, and the `lossless-v2` policy version with
   the recoverable techniques listed but not yet promotable.~~ **Built.** `PolicyV2()` is v1 plus
   `hook_repeat_read` and `hook_quiet_commands` (in that order) on a probation list; 20 sessions per arm and 28 days
   are placeholders. `PolicyFor(name)` maps a consent record to a policy and falls back to v1 for a name it does not
   know. Nothing offers v2 to a user: `Enable` still writes v1, and consent for v2 is step 8.
3. ~~**The probation state** in `autopilot.json` and `Decide`.~~ **Built.** New state `probation`. A candidate that
   needs a trial starts one only if a trial can run (A/B comparison on, and gateway traffic seen, because only the
   gateway can assign arms today) and no other trial is running; tries are counted when a trial starts and limited
   by `MaxAttempts`. A trial ends in `on` only on a `saves` result with at least 20 sessions per side, in
   `rolled_back` on a worse result or a guardrail trip, and in `shadow` if it cannot continue or shows nothing by the
   deadline. A technique with no shadow saving estimate skips the net-token bar and is judged by the trial alone.
   The table test covers every transition.
4. ~~**The assigner handoff** for gateway users.~~ **Built, with a correction to the original wording.** The state
   file has two views: `States()` (used by the hooks and everything with no assigner) shows a trial as shadow, and
   `StatesWithArms(abEnabled)` (used only by the gateway) shows it as `on` when A/B comparison is enabled, so the
   assigner splits sessions, and as shadow when it is not. The earlier plan also said to test that an Autopilot "on"
   outside probation never calls the assigner. That was wrong: with A/B comparison enabled (the default) the
   gateway already runs *every* optimizer that is on through the assigner, including a lossless one Autopilot
   promoted, so such an optimizer runs on about half of a gateway user's sessions. Narrowing that is a behaviour
   change nobody asked for, so it is left alone and recorded here as a finding.
5. ~~**Reading the result** for gateway users.~~ **Built.** For an optimizer in probation, `Advance` calls
   `experiment.AnalyzeSince` (new: `Analyze` limited to sessions assigned after the trial began, so an earlier
   trial's sessions are never pooled in) with the user's own `[experiment]` thresholds and passes the status and the
   session counts to `Decide`. So a trial is judged by the same rule that decides "saves" and "costs more"
   everywhere else: for the optimizer to be kept the whole 95% range of the cost change must be below zero, no
   outcome (turns, tool errors, abandoned sessions) may be measurably worse, and each side needs at least 20
   sessions. A failure reading the comparison is returned as an error and the state is left as it was, so it can
   never keep an optimizer. Tests: a clean saving keeps it, a measured loss rolls it back, no difference waits and
   then gives up at the deadline, too few sessions decide nothing, sessions from before the trial are not counted,
   and a failed read keeps nothing. This makes probation work for API-key users; it is the first step that can turn
   a recoverable optimizer on, and it still cannot be reached by any user, because nothing offers `lossless-v2`.
6. ~~**Hooks assignment**.~~ **Built.** The snapshot the hooks read now lists the optimizers on trial. At
   `SessionStart` (only when a session is first pinned, so a resume is never reassigned) the hooks take a hash of the
   session key and the optimizer name (`measure.Arm`), pin the optimizer on for one arm and leave it shadow for the
   other, and append the arm to the spool. Ingesting the spool writes it to `experiment_assignments` (first record
   wins) and not to `hook_events`. Because the arm is a hash, no database is opened at session start; because it
   is spooled at the start, a session that never ends is still counted. A rollback or the kill switch still lowers
   an "on" arm to shadow, and the user's own `config.toml` setting and `[experiment] enabled = false` both keep an
   optimizer out of the trial list. A hash gives about, not exactly, half; the comparison does not need exact
   balance.
7. ~~**The history-based comparison** and its scheduling.~~ **Built.** `experiment.AnalyzeHookTrial`: tokens per
   session is the headline, with turns and tool errors beside it and abandoned sessions left out (only the
   gateway sees them). A session's figures come from the gateway's record when there is one, otherwise from
   `history_requests`; every assigned session counts under its assigned arm whether or not the hook acted (intention
   to treat), because dropping "on" sessions where the hook never acted would drop exactly the ones it could not
   have changed. A session counts once it has been idle for the gateway's idle window. At session end,
   while an optimizer that runs in the hooks is on trial, `Maintain` reads the agent's history first, with the same
   reader as `trove logs scan` (new package `internal/histscan`, shared so rows are priced and annotated
   identically), limited to files changed since the trial began and to 700 ms. Tests: planted savings and losses in
   both directions and no effect on history-only data, running and earlier sessions left out, a session in both
   sources counted once, arms assigned and recorded, a repeated record never changes an arm, the history scan runs
   only while a trial does, and the hooks' session key matches the history's.

   **Correction to the plan.** The gateway pins only the five request-level optimizers, so it can never split
   sessions for a hook technique. Steps 4 and 5 allowed a trial of `hook_repeat_read` only when the user had
   gateway traffic and read the gateway's comparison, which would have found no assignments. A hook technique now
   needs hook traffic and is judged by `AnalyzeHookTrial`; a request-level technique still needs the gateway.

   **Measured on this machine (2026-11-13):** a first scan of 30 days of Claude Code history into an empty database took
   3.9 s and a re-scan with nothing new 0.29 s. The 700 ms budget therefore fits an ordinary session's file but
   not a first read of a long trial window: that read finishes over several session ends, or sooner if the user
   runs any `trove` command. I did not measure a single very large transcript. If one file takes longer than the
   budget every time it will never be read by the hooks, the comparison will show "not enough data", and the
   trial will time out in shadow, which is safe but slow.
8. **Consent text, `trove autopilot` and `/trove:status` display** of "in probation: N of M sessions per side".
9. **One real run** on the benchmark (single-hook arms, G8) before any technique is promoted.

Steps 2 to 5 give API-key users probation and are the smaller, lower-risk half; 6 to 7 are what subscription users
need and may show it cannot be done fast enough, which would be a reportable result.
