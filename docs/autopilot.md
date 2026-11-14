# Autopilot

Status: **first version built, thresholds not validated.** See "What is built" below for what exists and what does
not. Autopilot applies optimizations automatically after one consent, without giving up the rules in `CLAUDE.md`.
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

`CLAUDE.md` rule 6 now reads: every optimization is opt-in, one at a time or by agreeing once to a named policy.
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

## What stays manual

- Optimizers that can change answers (for example output trimming) keep a stricter rule or an explicit choice.
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
