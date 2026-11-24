# Autopilot

Status: **first version built, thresholds not validated.** See "What is built" below for what exists and what does
not. Autopilot applies optimizations automatically after one consent, without giving up the rules in `PRINCIPLES.md`.
Every number named here is a placeholder to be set from a measured benchmark, not a claim.

## Why

Today every optimizer is `off` by default and the user moves each one to `shadow` and then `on` ([optimize.md](optimize.md)).
That is safe, but a new user sees no saving until they act. Autopilot keeps the same safety rules and removes the
manual steps: the user opts in once, to a named policy, and Trove does the rest.

## What the user consents to

One screen at setup, in plain English:

> Trove will watch your usage, test savings in the background without changing anything, and turn on only the
> ones that are proven safe for your sessions. It can undo everything with `trove undo`. Nothing leaves your machine.

Choices: **Turn on Autopilot**, or **Just show me reports** (today's behavior).

The consent record stores the policy name, the policy version, the date and the paths enabled. It stores no
prompt text and no credentials.

### Rule 6 wording

`PRINCIPLES.md` rule 6 now reads: every optimization is opt-in, one at a time or by agreeing once to a named policy.
Autopilot is that policy. The project owner approved the change on 2026-11-13.

## Paths by login type

Trove detects the path from config and history, never by reading credentials.

| User | What Autopilot may use | What it never does |
|---|---|---|
| API key or monthly API credits | Local gateway on 127.0.0.1 (opt-in through `trove setup --gateway`), plugin, hooks, `trove fix` | Read or store the key beyond passing it through in memory |
| Subscription login (any plan) | Plugin, hooks, MCP tools, settings writer, history audit | Point the agent at the gateway, touch the login or its traffic (rules 3 and 10) |
| Unknown | Reports and shadow estimates only | Turn anything on |

A subscription pass-through mode is **not** part of this design. It would need written clarification of the
provider's authentication terms first and a deliberate change to rules 3 and 10.

## State machine

Each optimizer has its own state. Autopilot moves it through these states; the user can override any of them.

```
off -> shadow -> candidate -> on -> (rolled_back -> shadow)
                    ^                      |
                    +----------------------+  (only after a cool-down)
```

| State | Request changed? | Meaning |
|---|---|---|
| `off` | No | Not running, not charged |
| `shadow` | No | Runs on a copy, records estimated net saving after its own overhead (rule 15) |
| `candidate` | No | Met the promotion rule; waiting for the next session boundary |
| `on` | Yes | Applied, measured against the shadow baseline |
| `rolled_back` | No | A guardrail or the net-saving rule tripped; held in shadow for a cool-down |

### Session boundaries (rule 5)

- A state change takes effect only when a new session starts. Never mid-session.
- Within a session, transforms are pure functions of the request. The same history gives byte-identical output.
- An optimizer that touches the cached prefix needs a stricter promotion rule than one that does not.

### Promotion rule (placeholders)

An optimizer moves from `shadow` to `candidate` only when all of these hold. Thresholds come from the benchmark, and
are shown here only to name them.

- at least `N_sessions` shadow sessions on this user's own traffic
- estimated net saving, after declared overhead, above `min_net_saving`
- no guardrail trips in the shadow window
- the optimizer's prerequisites are met (for example `output_trimmer` needs the MCP server and recovery store)
- it does not conflict with an optimizer that is already `on` (for example two that both move cache breakpoints)

### Rollback rule

At the next session boundary an `on` optimizer returns to `shadow` when any of these hold:

- measured net saving is negative, as in `measure.NetGuard`
- outcome signals worsen against the baseline (errors, retries, fetch-backs of trimmed output, task failures)
- an internal error rate above the limit
- the user pauses Autopilot or runs `trove undo`

On any internal error while optimizing, the original request is forwarded unmodified (rule 4).

## What the user sees

Nothing is required of them. Optional, content-free surfaces:

- **Status line:** state and last session's saving, for example `Trove: learning, est. 11% possible`. Every number
  is labeled "measured" or "estimated" (rule 6).
- **Session end:** one receipt line.
- **Weekly:** `trove report` summary: spend, savings, and anything rolled back and why.
- **Notifications** only when a decision is needed: a rollback, a price-data update, a capability change after an
  agent update.

Always available: `trove status`, `trove autopilot off`, `trove autopilot pause`, `trove undo`. All support `--json`.

## Trials (a second, separate yes)

The first agreement covers lossless changes only. `trove autopilot trials on` asks for a second one, to let Autopilot
try changes that are not lossless. Today (policy `lossless-v3`) there are five, tried one at a time in this order:
`hook_repeat_read`, `hook_quiet_commands`, `output_trimmer`, `hook_large_output` and `hook_read_limit`. Each has its own
guardrail that puts it back to shadow by itself (see [hooks.md](hooks.md)), and its cost in added text is declared and
charged. A user who agreed to the first two under `lossless-v2` keeps exactly those two until they run
`trove autopilot trials on` again and say yes to the new list; what v2 already kept stays. It is its own consent because
a trial changes what the model sees for half of the user's sessions. `trove setup`, `--yes` and `--autopilot` never
give it, and `trove autopilot trials off` withdraws it, which also removes anything a trial kept. Switching Autopilot
off ends trials as well, and switching it on again is a v1 agreement. The design, the gates and what each step
verified are in [autopilot-probation.md](autopilot-probation.md). A trial runs only when A/B comparison is on, never
beside a setting the user made, and for a user with no gateway only when reading local history is allowed.

## Rule-set trials (one yes per rule set)

`trove autopilot trial <rule set>` (`terse-rules`, `ponytail` or `caveman`) runs a per-user A/B of one output-style
rule set (T2, plan 6.6). The user agrees to each trial on its own; `--yes` agrees without a prompt and `--json` needs
it. The rule set must be installed with `trove external install` (which shows its license) and left off, and no other
rule set and no `verbosity_steer` may be on. At the start of each new Claude Code session the plugin's SessionStart hook
picks an arm from a hash of the session key, records it, and in the "on" arm adds the rule set's text to the session's
context (`hookSpecificOutput.additionalContext`, verified on code.claude.com/docs/en/hooks on 2026-11-17). It adds the
text again after a resume, `/clear` or compaction. The arm is pinned, so a running session keeps it when the trial
ends. Other agents are not split, because their SessionStart replies cannot carry context.

At each session boundary Autopilot reads the comparison from the gateway's record of a session or, failing that, the
agent's local history. The verdict reports four figures, each labeled measured or saying why there is none yet:
output tokens per turn, cost per turn (from sessions whose every request was priced), the share of sessions ending
with failing tests per arm, and turns. The test_fail_rate guardrail and a worse result stop the trial
(`rolled_back`). Switching the rule set on everywhere, or turning on another rule set, also stops it (`off`). A clean
result after enough sessions on each side ends the trial with the verdict in `trove autopilot`. Autopilot never turns a
rule set on, because T2 needs a benchmark verdict before Autopilot may; the user decides. One trial of any kind runs at
a time. `trove autopilot trial <rule set> --stop` ends it at once. The numbers (sessions per arm, days) are the trial
policy's placeholders.

### The routine steer's trial (added 2026-11-24)

`trove autopilot trial hook_routine_steer` trials Trove's own T2 technique B24 the same way. Its autopilot.json entry
is the optimizer's own name, not `ext_<tool>`. It starts only when all of these hold:
- the steer runs in `shadow` and has counted at least 20 routine prompts on Opus 5.5;
- `trove hooks verify --prompt-context` has passed;
- A/B is on;
- the user agreed to its own consent text.

The hooks assign the arms at SessionStart. The "on" arm is steered and the "off" arm only counted, so the trial
compares routine turn with routine turn.

It is judged on thinking tokens per routine turn (measured) and on that difference as a share of the limits
(estimated), with the sequential always-valid tests the rollback checks use. Beside the usual guardrails it stops on
re-prompts: the prompt after a routine turn being a correction more often than in the off arm, by more than 10
points. It ends if the steer is switched on everywhere or the live check stops passing. Like a rule-set trial it never
turns anything on. See [hooks.md](hooks.md#routine-steer).

## Settings policy (a third, separate yes; added 2026-11-24, plan 4.5)

`trove autopilot settings on` asks for agreement to `settings-v1`, the only policy that edits a file outside
`~/.trove`. It names one setting: `subagentPromptCacheTtl` in `~/.claude/settings.json`, the prompt cache lifetime of
subagents, workflows, compaction, session titles and the other requests outside the main conversation (technique C1,
T1 `cache_control`, fix kind `cache_ttl_subagents`). `--yes` agrees without a prompt and `--json` needs it. `trove setup`,
`--autopilot` and the other agreements never give it. `trove autopilot settings off` withdraws it.

**What it reads.** At a session end, at most every 6 hours, Trove reads the Claude Code history (up to 0.5 s of new
lines) and the rebuild ledger ([analyze.md](analyze.md#rebuild-ledger)). From the subagent requests only, it counts:

- the rebuilds caused by a pause of more than 5 and at most 60 minutes while the agent was on the five-minute lifetime,
  and their size (measured). A one-hour entry would have been read instead;
- the cache writes at each lifetime (measured), and, when the history ran on one hour, the reads after such a pause.

**The estimate.** The net of the one-hour lifetime is the rewrites it avoids minus the one-hour premium on every other
write. It assumes a lost entry is rewritten in full, so it is always labeled **estimated**. The basis is one of these:

- **estimated from your meter**: for a mostly-subscription history, when the plan-limit fit has weights per token
  type ([limit-fit.md](limit-fit.md)). The saving is a share of the 5-hour limit with its 90% interval. How a one-hour
  write counts toward the plan limits is not documented, so this fit is the only source for its weight;
- **estimated from API prices** otherwise: per-model list prices (a five-minute write 1.25x, a one-hour write 2x, a read
  0.1x or less of the base input price).

**When it sets `"1h"`.** All of these must hold: at least 100 subagent requests in 5 sessions over 30 days; at least 5
such rebuilds; at most 20% of writes without a recorded lifetime; prices known for most of the models. On API prices,
the avoided rewrites must exceed the premium by 25% and the net must be at least $1 (the `cache_ttl` suggestion's own
thresholds). On the meter, the whole 90% interval must be a saving. Then it writes the key, backed up, recorded in
`fixes` and reversible with `trove undo <id>`, only while no other agent session wrote its transcript in the last
`fixer.activity_minutes` (default 30). Otherwise it waits for the next session end.

**It never changes a value the user set.** It leaves the setting alone when any of these is set: `subagentPromptCacheTtl`
in the user, project or local settings; `CLAUDE_CODE_SUBAGENT_PROMPT_CACHE_TTL`, `FORCE_PROMPT_CACHING_5M`,
`ENABLE_PROMPT_CACHING_1H` or a `DISABLE_PROMPT_CACHING*` variable in the environment or a settings file's `env`; a
`cacheTtl` in the frontmatter of a subagent in `~/.claude/agents` or the project's `.claude/agents`. The setting would
override that frontmatter value. A value applied by hand with `trove fix` counts as the user's.

**Tripwire.** 14 days after the change, and at every check after that, it estimates the last 14 days the other way
round: the one-hour premium paid against the reads that only a one-hour entry served. If the net is below zero (point
estimate, at least 30 requests), it removes what it set, again only at a quiet session end. If the file was edited since,
only the key is deleted, and only while it still holds `"1h"`. Then it waits 30 days and tries at most twice. If the
user undoes the change, it never sets it again. If the policy is withdrawn, or Autopilot is turned off or paused, its
own change is taken back at the next quiet session end.

`trove autopilot` shows the state, the reason and the last estimate (`settings` in `--json`). The thresholds are
placeholders until a benchmark sets them.

Rules: 5 (written only between sessions; the docs do not say whether Claude Code re-reads these keys mid-session, so
Trove assumes it might), 6 (own consent, only the named setting, from history evidence, never over the user's value,
automatic rollback, every figure labeled), 7 (numbers and labels only in `autopilot.json`), 9 (no figure without
enough data), 15 (no tokens added). Verified on 2026-11-24: see S10 in [techniques.md](techniques.md#what-was-verified).

## What stays manual

- Optimizers that can change answers keep a stricter rule or an explicit choice. Output trimming is a trial candidate in v3 (with a guardrail half as tolerant as the rest); `cache_breakpoints` and `volatile_prefix_mover` (custom agents only, never Claude Code), `inflight_dedup` and `spend_cap` (shadow only) are never promoted by Autopilot, and the spend cap should never be automatic.
- Contributing data stays a separate `trove contribute enable` (rule 1).
- Anything that needs a new outbound call, a new credential path or a subscription proxy.

## Where a planner fits

Without a planner, Autopilot promotes optimizers from a fixed list in a fixed order. A planner would choose the
order, the compatible set and the parameters (see `tuning`) from content-free features of the user's own sessions
(`features`). It would follow the same state machine and could not change anything mid-session.

Build order:

1. Fixed-list Autopilot, shadow and promotion only.
2. A shadow planner that records what it would have chosen, and changes nothing.
3. An active planner only if a session-level A/B shows it beats the fixed list after its own overhead.

## What it takes to build

- Autopilot policy and consent record in `internal/config` and `internal/store` (new table needs a retention decision)
- promotion and rollback logic in `internal/optimize` and `internal/measure`, driven at session boundaries
- a scheduler hook for session start in the gateway and the Claude Code hooks
- first-run and weekly summaries in `internal/report`
- tests: promotion and rollback table tests, golden files for the status line and receipt, a test that no prompt
  text or credential reaches the database, and a stress case for rapid session starts
- a benchmark run to set `N_sessions`, `min_net_saving` and the guardrail limits

## Open questions

1. Rule 6 wording (above). Who approves the change, and does it also need a note in `PRIVACY.md`?
2. Which optimizers are eligible for automatic promotion on day one, and which stay manual?
3. How long is the shadow window for a user with very few sessions?
4. Does a subscription user on the plugin path get the same promotion logic, given that hook-side savings are
   estimated from sizes and cannot be measured from provider usage fields?
5. What does the user see when two optimizers conflict?

## Not in this design

- A hosted or team edition.
- TLS interception or installing a certificate.
- Any change to how credentials are handled.
- Savings claims. None may be made before a benchmark run (see [benchmark-methodology.md](benchmark-methodology.md)).

## What is built

Version 1 of the policy is named `lossless-v1`.

| Part | Where |
|---|---|
| State machine and promotion and rollback rules (pure, table-tested) | `internal/autopilot/autopilot.go` |
| Evaluation from shadow evidence, guardrail rollbacks and the baseline | `internal/autopilot/advance.go` (`Advance`) |
| Consent record and per-optimizer state, `autopilot.json` (0600, atomic) | `internal/config/autopilot.go` |
| The user's own `config.toml` setting always wins | `Config.OptimizerState` |
| New sessions pick up changes, running sessions never do | `Engine.current` and the session pin |
| When it runs | hourly in the gateway; at session end in the hooks (`hooks.Maintain`) |
| Commands | `trove autopilot [on\|off\|pause\|resume]`, and one more consent step in `trove setup` (`--autopilot` for scripts; `--yes` alone does not agree to it) |
| Status line | adds ` · autopilot learning` or ` · autopilot N on`; no saving figure |
| Short help | `trove --help` lists 8 commands; `trove advanced` lists the rest, which still work |
| Receipt line | `trove receipts` and the session-end notice name the optimizers that were on, from the session's pin (`Receipt.On`) |
| Weekly summary | `trove report` and `trove autopilot` show the last 7 days: what is on, what is collecting evidence, rollbacks and every change (`autopilot.Summarize`); `--json` has an `autopilot` object. No saving figure is stated |
| Notifications | a desktop notice, once each, when an optimizer is turned on or rolled back; only with `[notifications] desktop = true`, and old events are never replayed (`autopilot.Announce`) |
| Plugin | `/trove:setup` offers Autopilot with the consent points and runs `autopilot on --yes` only on a yes; `/trove:status` shows `autopilot` |
| Shadow planner | each check writes advice for every optimizer (`would turn on`, `needs your choice`, `collecting`, `not worth it`) into `autopilot.json`; it changes no state and no request (`internal/autopilot/planner.go`) |
| Planner advice in % of the plan limits | each item's estimated net saving per shadow session, as a share of the 5-hour and weekly limits from the per-user fit ([limit-fit.md](limit-fit.md)), with its interval and basis. Display only: no decision reads it (`annotateLimits` in `internal/autopilot/advance.go`) |
| Conflict check | before it promotes an optimizer, Autopilot asks the technique registry whether something already on (a built-in the user set, or a wrapped tool such as RTK) cannot run beside it; if so it is held back with the reason, and the planner says `blocked` (`internal/technique`, `autopilot.blockedBy`) |
| Prerequisite check | before it promotes an optimizer, and at every check after, Autopilot reads the user's real setup (channels used, MCP server registered, custom agents, the stored hook probe record) and asks the technique registry whether the optimizer can really act there. One that cannot is held back as "not available in your setup", and a promotion is undone (back to shadow) if what it needs goes away, for example the hook probe no longer covers it after a Claude Code update (`autopilot.Setup`, `technique.HookNeeds`, `internal/hookcaps`) |

Eligible on day one: `noise_normalizer` and `duplicate_result_collapser`, which are lossless and leave the cached
prefix alone. `output_trimmer`, `cache_breakpoints` and `volatile_prefix_mover` are not eligible.

The defaults are `MinSessions` 10, `MinNetTokens` 2000 over 30 days, cool-down 7 days, at most 2 tries. They are
placeholders. `trove autopilot` says so each time it shows them.

## Known gaps

- **Thresholds are not validated.** No benchmark has run. Do not describe them as tuned.
- **Hook-side states and rule 5 (checked, not a gap).** The hooks read a snapshot that is rewritten when any
  session ends, but `SessionStart` pins each session's states first, so a running session keeps what it started
  with (`TestStateIsPinnedForTheSession`, `TestAutopilotStatesReachTheSnapshot`). An earlier note here said
  otherwise; it was wrong. A session that began before the hooks were installed has no pin and stays off.
- **Fixed 2026-11-14: a user with no gateway used to get no promotions.** `Advance` read shadow figures from the gateway's request rows only, and counted baseline sessions from the gateway's table of sessions, so a hooks-only (subscription) user had no evidence and never left the baseline. It now also reads the hooks' own counts (`measure.HookShadowEstimates`, from `hook_events`) and counts hook sessions toward the baseline. Where both channels saw the same waste, it takes the larger figure, never the sum. An optimizer that needs a channel the user is not using (the duplicate collapser without a gateway) is held back as "not available in your setup" and shown as `unavailable` in the planner. Hook-side figures are estimates from sizes, not provider usage (open question 4). What each further technique needs is in [autopilot-eligibility.md](autopilot-eligibility.md).
- **The planner is advice only.** It records what it would choose. Making it active needs a session-level A/B that
  shows it beats the fixed list after its own overhead (build order, step 3); none has been run, so it does not
  choose anything. It does not look at features of the user's repo yet, only at shadow estimates.
- **Notifications need a desktop.** They use `osascript` on macOS and `notify-send` on Linux, and are off unless
  `[notifications] desktop = true`. Without a notifier the events are still recorded and shown in the report.
- **Receipts record names, not savings per optimizer.** `Receipt.On` lists what was on; the saving shown beside it
  is the existing estimated figure for the session.
- **Declared overhead comes from the technique registry.** The two eligible optimizers inject no text. A
  technique that does must declare its per-session and per-use cost in `internal/technique/registry.go`
  (`Overhead`, `UseOverhead`, `UseNetted`) before it can be marked for Autopilot; a test enforces it and another
  checks the figures against the real message sizes. Autopilot subtracts it from the shadow saving before it
  compares with the threshold (`Policy.NetOf`), and refuses to promote one whose cost is not declared as numbers.
