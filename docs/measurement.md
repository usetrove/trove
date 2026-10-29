# How Trove measures savings, and when it backs out

This page explains, in plain terms, how Trove decides an optimizer is worth
turning on, how it counts what an optimizer saved, how it watches for harm, and
what it cannot know. If a number in a report seems too good, this is where to
check how it was made. Code: `internal/measure`.

Every figure Trove shows carries a label and a sample size:

- **measured**: counted straight from provider-reported usage or recorded requests.
- **estimated**: worked out with an assumption, a model, or a "what would have happened" calculation.

## 1. The lifecycle of an optimizer

1. **Baseline (shadow only).** From install, until **both** 7 days have passed
   **and** 20 sessions have been seen (`[measure] baseline_days`,
   `baseline_sessions`), every optimizer runs in shadow mode: it works out what
   it *would* remove, and changes nothing. Even if you set an optimizer to `on`
   in `config.toml`, it stays in shadow until the baseline ends. Both limits are
   needed: a week with two sessions, or 20 sessions in one hour, is not a baseline.
   These sessions are also the "before" picture the guardrails compare against.
2. **Offer.** After the baseline, if an optimizer's shadow estimate passes a
   threshold (at least 10 sessions and at least $1.00 estimated over the window),
   the report shows the estimate and the exact line to add under `[optimizers]`.
   **Nothing turns on without your action.**
3. **On.** From the next session after you enable it, savings are counted from
   real requests (section 2) and the guardrails watch it (section 4).
4. **Optional interleaving** (`[measure] interleave = true`, off by default).
   Whole sessions alternate between on and off, decided by a fixed function of
   the session id and optimizer name, never mid-session. The off sessions run in
   shadow, so you still get estimates. **Trade-off:** about half your sessions
   forgo the saving, in exchange for a cleaner comparison, because on and off
   sessions then happen at the same time on the same projects, instead of
   comparing against last week.

Optimizer states are fixed when a session starts. Switching one on or off
mid-session would change the request bytes and invalidate the provider's cache,
so a change (baseline ending, a rollback, your config edit) only affects sessions
that start afterwards.

## 2. How savings are counted

For each request an optimizer changed:

```
saving = cost of the request had the removed tokens still been sent
         − what the request actually cost
```

- The **actual cost** comes from the provider's reported usage (input, cache
  reads, cache writes by lifetime, output) priced with Trove's pricing table.
- The **counterfactual** uses the pricing engine's `CostWithAdded`. The removed
  tokens are spread across input, cache reads and each cache-write lifetime in the
  same proportion the request's prompt was billed, so a token that would have been
  a cache read is valued at the cache-read price, not the (much higher) input
  price. Long-context pricing tiers are re-evaluated on the larger prompt.
- A request whose cost cannot be priced exactly is **left out and counted** as
  "unpriced", never guessed.
- Arithmetic per request is exact decimal arithmetic; tests reproduce
  hand-calculated values to the last digit (`savings_test.go`). Totals read back
  from the database are rounded to a millionth of a dollar.

**Why dollar savings are labeled "estimated".** The provider reports how many
tokens it received, but not how many the optimizer removed. Trove knows how many
*bytes* it removed and converts with a bytes-per-token ratio (3.5 by default). If
several optimizers changed one request, its saving is shared between them in
proportion to the tokens each removed. Both steps are approximations.

**What earns the label "measured".** When interleaving gives you both on and off
sessions, Trove compares the *provider-reported* prompt tokens per request of the
two groups (a plain difference of means with a standard-error check, at roughly
95% one-sided confidence). That comparison is labeled **measured** and says
whether the on sessions really sent fewer tokens. The dollar figure beside it
stays **estimated**. The comparison needs at least 20 sessions on each side;
below that it says "not enough data yet".

## 3. Outcome signals (what Trove watches for harm)

All computed locally, from metadata, and stored as counts only: no prompt text,
code, file names, tool names, tool inputs or tool output are stored. (Tool names
and inputs are looked at in memory for a moment so identical calls can be
recognised; only a number comes out.)

Per session:

| Signal | Meaning |
| --- | --- |
| turns, tokens, duration | Requests, tokens and wall-clock time. |
| repeated tool calls | Tool calls identical (same tool, same input) to an earlier call in the session. |
| tool errors | Tool results marked as errors by the agent. |
| collapsed results | Results the duplicate-result collapser replaced with a note. |
| re-fetches | A call repeated *after* its result had been collapsed: the agent went and fetched again what an optimizer removed. |
| abandoned after action | The session made at most 2 more requests, and ended within 5 minutes, after the first request an optimizer changed. A heuristic: a finished task looks the same. |
| commits (optional) | See below. |

Sessions are rolled up once they have been idle longer than `gateway.idle_minutes`.

**Git commits** (`[measure] git_signals = true`, off by default; needs
`git_repo`). Trove runs the read-only `git rev-list --count` over your repo for
the time of the session plus 30 minutes, and stores the **number**. It never reads
messages, file names or diffs, and takes no locks. You name the repo because the
gateway deliberately never learns file paths. The count is recorded for the
report; no guardrail depends on it.

## 4. Guardrails and automatic rollback

An optimizer's "on" sessions are compared with a reference: **off sessions**
(sessions after it was first on where it was not, e.g. interleaving or after a
rollback) if there are enough, otherwise the **baseline** (sessions before it was
ever on).

| Rule | Trips when |
| --- | --- |
| retries | Repeated identical tool calls per session are up by at least 30% (`retry_increase_percent`) **and** the rise is larger than chance would explain. |
| tool errors | Tool errors per session are up by the same margin and are beyond chance. |
| re-fetch | Of sessions where something was collapsed, more than 25% (`refetch_rate_percent`) re-fetched, with even the low end of a 95% interval above 25%. |

If the reference average is zero, a relative rise is meaningless, so the rule
needs a rise of at least half a call (or error) per session instead.

**Minimum sample.** No rule runs, and no conclusion is shown, until each side has
at least `min_sessions` (default 20) finished sessions. Below that the report says
**"not enough data yet"**, not "looks fine" and not "looks bad".

**On a trip**, Trove records the event (rule, measured value, threshold, both
sample sizes, which reference) and the reason in plain English, and sets that
optimizer back to **shadow for new sessions**. Running sessions are not touched.
`config.toml` is never edited. The rollback shows in the report. You decide when to
re-enable (clearing it starts the count afresh; old sessions are not held against
the next attempt).

**What Trove will and will not say.** With one user's data, Trove cannot show
that output quality is unchanged, and never claims it. The strongest statement
is: *"no regression detected locally; output quality was verified on benchmarks
(Prompt 8), not on your data."* "No regression detected" means these specific
signals did not get worse by more than the limits, with this much data.

## 5. Limits you should know about

- **Sessions are inferred.** Without a Claude Code session id, a session is a
  hash of the system prompt plus first message, split on idle gaps
  (`internal/gateway/session`). Two runs with the same opening look like one.
- **Savings are not a controlled experiment.** Unless you interleave, "on" is
  compared with "earlier", and the work you do changes week to week.
- **Even interleaved, sessions differ** in size and task. The comparison is
  evidence, not proof, and small effects need many sessions to show up.
- **The signals are proxies.** More repeated calls or errors can signal confusion,
  but not every change in them is harm, and harm that does not show up in them is
  invisible here.
- **Dollar savings are list-price estimates.** Discounts, credits and rate limits
  are not modelled.
- **Anthropic Messages only for now.** Other traffic is recorded but has no tool
  signals, so it is left out of tool-based rules.
- **The output trimmer** stays shadow-only until the fetch tool exists, so it is
  never measured as "on".
- **Thresholds are defaults, not laws.** They are configurable under `[measure]`.
