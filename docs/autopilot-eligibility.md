# Widening what Autopilot may promote

Status: **a plan, not built.** It says what each technique needs before it can join Autopilot's eligible list,
in what order, and what has to change in the code first. Written 2026-11-14 from the code in this checkout. Where a
claim rests on something not verified, it says so.

## Where things stand

Autopilot (see [autopilot.md](autopilot.md)) may promote two optimizers on its own: `noise_normalizer` and
`duplicate_result_collapser`. The list is `Policy.Eligible` in `internal/autopilot/autopilot.go`, and the registry
marks the same two `Auto` ([techniques.md](techniques.md)). A test keeps the two lists equal. The registry also
refuses to mark a technique automatic unless it is built, lossless, leaves the cached prompt alone and adds no text.

That is a deliberate floor, not a limit of the design. Widening it is a change to what the user agreed to, so it comes
with a new policy version and a new consent (see "Policy versions", below).

## A gap found while writing this (fixed 2026-11-14, see "What was built first")

`autopilot.Advance` reads its evidence from `measure.ShadowEstimates`, which sums the shadow figures stored on the
**gateway's** request rows. The hooks write their own counts to a different table (`hook_events`, with kinds such as
`would_block` and `would_rewrite` and a token estimate from sizes). Autopilot never reads that table.

So today **a user with no gateway (every subscription user) gets no promotions at all**, not even for
`noise_normalizer`, which does run in the hooks. Earlier notes in this project said a hooks-only user could have
`noise_normalizer` promoted; that was wrong, and `autopilot.md` is corrected alongside this document. Fixing it is
step one of widening anything, because every hook-side technique needs the same thing.

## What every technique needs (the gates)

A technique joins the list only when all of these hold. Each is a check someone can run and a test that fails if it
is skipped.

| # | Gate | Where it lives today | Missing for most candidates |
|---|---|---|---|
| G1 | **Built, with a shadow mode** that records, from real use and without content, what it would have saved. | optimizers and hooks | gateway behaviors (dedup, spend cap) record shadow but have no active mode |
| G2 | **An evidence source Autopilot reads.** Sessions that ran it in shadow and the estimated net tokens, as `Evidence`. | `Advance` reads gateway rows only | hook techniques: an adapter over `hook_events` (`would_*` events, distinct sessions, token sums) |
| G3 | **Declared overhead**, charged against its saving (rule 15). Text it adds is counted. | `Policy.NetOf`, registry `Overhead`, `UseOverhead`, `UseNetted` | **Built (2026-11-13).** The registry allows `AddsText` in `Auto` only when both figures are numbers; Autopilot charges per session and per event unless the optimizer's own estimate already subtracts it; B6, B7 and B8 declare 68, 42 and 54 tokens per use, checked against the real message text. `output_trimmer` still declares no numbers, so Autopilot refuses it |
| G4 | **A guardrail that can see how it fails**, rolling it back to shadow by itself (rule 6). | `measure.EvaluateHooks` (`hook_reread`, `hook_rerun`, `hook_fetch_back`) and gateway rules; hook rollbacks use the same recorder as the gateway's, so `Advance` already sees them | `hook_read_limit` has none; `hook_large_output` has none beyond adoption counts |
| G5 | **Prerequisites checked at promotion time, in the user's real setup**: login, gateway, MCP server registered, a live hook probe for the exact agent version, a custom agent. Otherwise the state says `on` while it runs as shadow. | `technique.Check` with a context | `Advance` uses a permissive context for conflicts only; it needs the real one, and a "hook probe passed" capability that does not exist yet |
| G6 | **No conflict with what is already on** (registry claims), and the open question about `hook_quiet_commands` beside RTK decided. | `technique.CanAdd`, wired into `Advance` | the RTK decision |
| G7 | **A tier rule.** Lossless: shadow evidence is enough, as today. Recoverable or behavior-changing: shadow evidence is not enough, because a shadow run cannot show harm that only appears when it acts. These need a **probation** step (below). Cached-prompt-touching: probation plus proof the cached prefix is stable. | not built | the whole tier rule |
| G8 | **Thresholds validated per technique** from a pre-registered study, using the existing ship rule (cost change at least 5% with the whole interval below it, success not lower by more than the margin). Placeholders are not enough. | `bench/`, ship rule | the benchmark has gateway conditions per optimizer, but its `hooks` condition switches on four hook optimizers together, so the benefit of each cannot be told apart |
| G9 | **Applied at a session boundary, never mid-session.** | engine and hook pins | nothing missing |
| G10 | **The user stays in control**: their own `config.toml` setting wins, `autopilot off` and `pause` work, and the consent text matches what it now does. | `OptimizerState`, `autopilot` command | consent text per policy version |
| G11 | **Tests**: a table test of the new decision path, an `Advance` test with real evidence, a stress case, and the canary that no prompt text or credential reaches a row or a log. | existing patterns | per technique |

### Probation, for anything that is not lossless

A shadow run proves a technique would remove tokens. It cannot prove the agent still gets its work done once the
tokens are gone. For recoverable or behavior-changing techniques, promotion goes through a step in between:

1. Shadow evidence passes as today, then the technique enters **probation**: it is on for about half of new sessions
   and off for the rest, assigned when each session starts. The assigner already exists
   (`internal/experiment`, session-level, balanced, recorded, never changed mid-session) and the gate already lets
   it decide the arm.
2. The outcome counts the project already keeps (retries, tool errors, fetch-backs, abandoned sessions, tokens per
   request) are compared between the arms.
3. Only a clean comparison, with enough sessions per arm, promotes it for every session. Otherwise it goes back to
   shadow with the reason shown.

Probation costs time and some sessions without the saving. That is the price of not changing answers silently.

### Policy versions

The current policy is `lossless-v1`. Widening the list changes what a user consented to, so each widening is a new
policy version (`lossless-v2`, then others), with its own consent text. A user on `v1` keeps exactly the `v1` list
until they agree to the new one; `config.AutopilotPolicy` is already compared on load, so an older file is ignored by
a program that does not know it, and the reverse needs the same care.

## Technique by technique

"Eligible after" means the gates that still have to be met, in order. IDs are from the registry.

### 1. `hook_repeat_read` (B6). First in line.
Denies a repeat read of an unchanged file once, naming the earlier prompt. **Recoverable** in tier (corrected
2026-11-13: this page and the registry said lossless, the code said recoverable, and a test now keeps them equal): the
model is told no, and the agent has to repeat the Read to get it anyway. The hook clears its ledger on compaction.
- Has: shadow mode (`would_block`, with token estimates), a guardrail (`hook_reread`, rolls back at 25% retried),
  session pinning.
- Needs: **G2** (the `hook_events` adapter, which also unlocks hooks-only users for `noise_normalizer`), **G5** (a
  "hook probe passed for this agent version" capability; hooks treat an unprobed agent as unable to act), **G3** (a
  denial message replaces a whole read, so its overhead is netted inside the tokens it removes; the registry
  invariant has to allow that), **G8** (a benchmark arm for this hook on its own).
- Verdict: eligible after G2, G5, G3, G8 **and G7 (probation)**. G2, G5 and G3 are built.

### 2. `noise_normalizer` for users with no gateway (B1, already eligible).
Not a new technique, but the first thing G2 delivers: it is eligible today and cannot be promoted for a subscription
user because there is no evidence source for them.

### 3. `hook_quiet_commands` (B15).
Adds one quieting flag to an allowlisted command (`go test`, `npm test` and similar).
- Has: shadow mode, a guardrail (`hook_rerun`, counts the agent running the command again as first written).
- Needs: **G2**, **G5** (it rewrites the tool input, so it needs the `updatedInput` capability from the probe),
  **G6** (decide whether it may run beside RTK: both rewrite the same Bash input, and `trove external` has never
  refused the pair), **G8**.
- **Its shadow mode has no saving estimate, and cannot honestly have one.** How many tokens a flag removes depends on
  the command and on what it would have printed, which a shadow run (it runs the command as written) never sees. A
  fixed per-rule figure would be invented, so none is recorded (rule 9). The registry marks it `NoSavingEstimate`,
  Autopilot refuses to judge it from shadow evidence and the planner says so. It can only be judged by measuring
  sessions with it on, which is the probation step.
- Verdict: eligible after G2, G5, G6, G8 **and G7 (probation)**. Recoverable in tier (the agent can rerun the full
  command), corrected from "lossless" on 2026-11-13.

### 4. `hook_read_limit` (B7).
Adds `limit: 400` to a read of a file of 40 KB or more and tells the model the read was partial.
- Has: shadow mode and the rewrite capability checks.
- Needs: **G4 first**: there is no guardrail. Reading the rest with `offset` and `limit` is what the note asks the
  agent to do, so the existing counters cannot tell help from harm. A usable signal would be extra reads of the same
  file after a limited read and the turns they add. **G7**: it changes what the model sees, so probation. **G3**: the
  note is added text. **G2**, **G5**, **G8**.
- Verdict: later. Eligible after a new guardrail, then probation.

### 5. `hook_large_output` (B8).
Hints at `trove_run` for known-noisy commands. Only works if the Trove MCP server is registered.
- Needs: **G5** (the MCP server must already be registered; Autopilot must not install it, because that is a
  separate consent), **G3** (the hint is text, and the MCP tool definitions it relies on are B9's overhead, charged
  to it), **G4** (adoption is counted: `advised` against `trove_run` use), **G2**, **G8**.
- Verdict: later. The saving depends on the agent following the hint, so it needs the most careful netting of
  overhead against benefit.

### 6. `output_trimmer` (B3).
Keeps failures and the first and last lines of long test and build output; the full text is stored and can be
fetched back.
- Has: shadow mode, a guardrail (`hook_fetch_back`), the recovery store.
- Needs: **G5** (the MCP server and a working recovery store), **G7 probation** (it is recoverable, not lossless),
  **G3** (the marker text and the MCP definitions), a stricter threshold than the lossless ones, and **G8**.
  Shadow evidence cannot show the fetch-back rate, because nothing is trimmed in shadow; only probation can.
- Verdict: eligible after probation exists. The registry currently keeps it out on purpose.

### 7. `volatile_prefix_mover` and `cache_breakpoints` (B4, B5).
They apply only to custom agents, never to Claude Code or Codex, and they change the cached prompt layout.
- Needs, beyond the above: proof that the cached prefix stays byte-stable across a session (the tests exist for the
  optimizers; the evidence has to exist per agent), probation, and a custom agent marked in `optimize.custom_agents`.
- Verdict: not for Claude Code users, ever. For SDK users, last, after everything else, and only with the strictest
  rules.

### 8. Gateway behaviors: `inflight_dedup` (C3) and `spend_cap` (C5).
Both are shadow only. Neither has an active mode, so neither can be promoted.
- Needs: the active mode itself, built and reviewed against its design document
  ([inflight-dedup.md](inflight-dedup.md), [spend-cap.md](spend-cap.md)), real shadow data showing it matters, and for
  both a consent that names what changes (answers are shared; requests may be refused).
- Verdict: `spend_cap` should probably **never** be automatic: refusing requests is the user's decision. Dedup only
  after the active mode exists.

### 9. Settings and config techniques: C1, C6, C7, C9, C10, C11, B10, B13.
These are changes to the agent's own settings files, not optimizer states. Autopilot as built holds states in
`autopilot.json`; it does not edit files.
- Widening to them is a different feature: "Autopilot may apply a class of settings changes". It needs a consent
  per class, a diff and a byte-exact undo for each change (the fixer already does this), application at a session
  start, per-setting evidence, and the benchmark rule. Today no profile ships a number for any of them for lack of
  evidence.
- Closest to ready: **C1 (cache lifetime)**, because the per-user estimate already exists and only recommends a
  change when the other lifetime is at least 25% cheaper. It would also need a measured check after the change.
- Never automatic: **C6 (effort level)** and **C7 (model per subagent)** lower capability or change who does the
  work. They stay user decisions, offered as suggestions.

### 10. Not eligible, and why.
- **Wrapped tools** (RTK, token-savior, terse-rules): installing another project's software needs the user to read
  its licence and approve. Autopilot does not install anything.
- **`trove_mcp` (B9), scout and cascade (B11):** installed or written with their own consent flows.
- **Candidates not built** (fan-out, response cache, model swap, tool-output rewrite for more tools, terse replies):
  they cannot be eligible before they exist. Fan-out was verified and is not recommended
  ([fan-out.md](fan-out.md)).

## What was built first (2026-11-14)

The evidence adapter (gate G2) and the part of G5 that says where a technique can run:

- `measure.HookShadowEstimates`, `measure.HookSessions` and `measure.TrafficSeen` read the hooks' side:
  `would_rewrite` and `would_block` events with their token estimates, the distinct sessions the hooks have seen,
  and which channels (gateway, hooks) recorded anything.
- `autopilot.Advance` combines the gateway's and the hooks' figures per optimizer. It takes the **larger** of each,
  not the sum, because in shadow both channels look at the same waste. It counts hook sessions toward the baseline
  so a person with no gateway can leave it.
- `Evidence.Unavailable` and the planner verdict `unavailable`: an optimizer that needs a channel the user is not
  using is held back with a plain reason, using the technique registry's account of where each technique runs. With
  no traffic recorded yet nothing is called unavailable.
- Tests tie the hook side to the real hook: a noisy command output goes through the real hook in shadow mode, the
  real spool and a real database, and is read back by the adapter.

What this changes today: `noise_normalizer` can now be promoted for a user with no gateway, and the planner says
why `duplicate_result_collapser` cannot. It widens nothing else: `Policy.Eligible` is still the same two.

### Then the prerequisite checks (G5), 2026-11-14

Before promoting, and at every check after, Autopilot asks whether the optimizer can really act in the user's setup.
Otherwise the state file says `on` while the program quietly runs it as shadow, and the report claims a saving that
is not happening.

- **Setup detection** (`autopilot.Setup`): which channels recorded anything, whether the MCP server is registered
  (`install.Registered`), the custom agents in the configuration, and the stored hook probe record. It reads Trove's
  own files and database only. It does not run the agent. The plugin already replaces the probe record with a
  docs-only one when Claude Code is updated, so reading the file is reading the current fact.
- **One list of what each technique needs.** The registry gained `HookNeeds` (for example `Bash:output`). They are
  required only when the technique is not running through the gateway: one with a request layer is satisfied by the
  gateway being in use. `technique.Check` evaluates them against the setup. A test checks the registry against what
  the hooks really rewrite (`hooks.RewriteKindOf`), so the two cannot drift.
- **`internal/hookcaps`**: the probe record's types and queries moved to a small leaf package, because `internal/hooks`
  imports Autopilot and so Autopilot could not import it. `hooks` re-exports the same names, so nothing else changed,
  and a test checks that what the hooks write is what Autopilot reads.
- **What a probe has to say.** A record counts only if a live probe or a verified session saw that tool and kind
  work. A probe that saw it fail, one for another tool or kind, and a docs-only record (for Claude Code) all do not.
- **A promotion is undone** if the prerequisite goes away: when the probe no longer covers it, the optimizer goes back
  to shadow with "no longer available in your setup" and earns its way back after a re-test. This is not a guardrail
  rollback and has no cool-down.
- With **no traffic recorded** on either channel, prerequisites are not checked: the optimizer is still collecting.

Still to do from G5: the registry carries `Requires` for the MCP server and the recovery store, and Autopilot detects
the MCP server, but no optimizer that needs it is eligible yet; a check that the recovery store itself opens, not
only that the MCP server is registered; and an agent-version match for installs that do not use the plugin (the
plugin's start check handles the update case; `trove doctor` warns otherwise).

## Suggested order

1. **The evidence adapter for hooks (G2).** One piece of work that fixes hooks-only users for `noise_normalizer` and is
   a prerequisite for every hook technique. Includes the real-setup context for promotion checks (G5) and a "hook
   probe passed" capability.
2. ~~**Relax the registry's `AddsText` rule** to "overhead declared and netted", with a test.~~ Done.
3. **Benchmark arms for single hooks (G8).** Add conditions that switch on one hook optimizer at a time, so each is
   attributable.
4. **The probation step (G7).** Both `hook_repeat_read` and `hook_quiet_commands` are recoverable, so neither can
   join before it exists. Nothing recoverable can.
5. **`hook_repeat_read`, then `hook_quiet_commands`** (after deciding the RTK pairing), with new consent text. Then
   `output_trimmer`, then `hook_read_limit` (after its guardrail) and `hook_large_output`.
6. **Everything else** only on evidence from real use, one at a time.

## The admission checklist (copy for each technique)

```
Technique:                          Registry ID:
[ ] G1  shadow mode records an estimate from real use, content-free
[ ] G2  Autopilot reads its evidence (sessions, net tokens)
[ ] G3  overhead declared and netted (rule 15)
[ ] G4  a guardrail sees how it fails, and rolls it back
[ ] G5  prerequisites checked in the user's real setup at promotion time
[ ] G6  no conflict with what is on; open conflict questions decided
[ ] G7  tier rule met (lossless: evidence; otherwise probation passed)
[ ] G8  thresholds from a pre-registered study; ship rule met
[ ] G9  applied only at a session start
[ ] G10 user setting wins; pause and off work; consent text updated; policy version bumped
[ ] G11 tests: decision table, Advance with evidence, stress, no content or credential in rows or logs
[ ] Registry: Auto set; Policy.Eligible updated; docs/autopilot.md and this file updated
```
