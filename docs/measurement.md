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

## 4a. A/B experiments: judging an optimizer by your real bill

This is the second kind of evidence. Keep the two kinds apart:

| Evidence | Label | What it is | What it cannot see |
| --- | --- | --- | --- |
| **Shadow estimate** | *estimated* | Tokens an optimizer would have removed, priced with caching effects. Nothing is changed. | **Behaviour changes**: retries, extra turns, re-reads. Shadow mode forwards your original request, so the agent never reacts to the change. |
| **A/B result** | *measured* | Whole sessions with the optimizer **on** compared with whole sessions with it in **shadow**, on the cost the provider reported and on outcome signals. | Anything not in those numbers. One person's sessions are noisy, so every figure comes with a range. |

**How sessions are assigned.** For an optimizer you set to `on`, each new session
is given an arm once, when it starts: *on*, or *off* (shadow). The arm is stored
and never changes, so a session's cached prefix is never disturbed by a switch.
Assignment is randomised in blocks of four (two on, two off, in a seeded random
order), so the two arms stay balanced. Sessions can be grouped by repo and task
type where Trove knows them, and are then balanced within each group. The
gateway cannot see repos, so today there is one group. If you would rather not
take part, set `enabled = false` under `[experiment]`; you then get shadow
estimates only. The control sessions forgo the optimizer's savings, which is the
price of an honest comparison.

**Where cost comes from.** Provider-reported usage (gateway), or the usage fields
in your agent's session logs, priced from the pricing table. Never from Trove's
own count of bytes it removed. A session with a request Trove cannot price
exactly is left out of cost, not guessed. Sessions that were not really run the
way they were assigned (held at shadow by the baseline or a rollback) are also
left out, and the count is shown. Each optimizer's own overhead (tool
definitions, injected text) is part of what the agent was billed, so it is
already inside the measured cost; the report shows it as a line item and does not
subtract it again.

**If the agent already trims.** Many agents truncate large tool output
themselves. Saving tokens on output the agent would have cut anyway saves nothing,
so a shadow estimate only counts what would actually have been sent: the agent's
size minus the optimizer's size, both limited by the agent's own cap. Trove ships
no built-in caps, because an agent's limit has to be checked against its current
documentation; until one is set, estimates assume the agent sends output in full.
A/B results need no such adjustment, because they use the real bill.

**Ranges, confidence and sample sizes.** Every A/B figure shows its label, a 95%
range (a seeded bootstrap, so the same data always gives the same range), the
number of sessions on each side, and a plain confidence word: *high*, *medium* or
*low*, from how narrow the range is and how many sessions stand behind it. Below
`min_sessions_per_arm` (default 20) on either side, the report says **"not enough
data yet"** and shows no number.

**Benchmark priors.** The benchmark lab's results can be combined with your
sessions, which pulls a noisy estimate toward the lab result and leaves a
precise one nearly alone. The combined range is shown next to your own and is
marked as including benchmark evidence. Rollback always uses your own data, so a
prior can never hide a loss. No benchmark results are committed yet, so the
shipped priors file is empty: for now only your own sessions count, and Trove
will not suggest turning an optimizer on from benchmark evidence.

**Promotion and rollback.**
- *Promotion*: an optimizer moves from shadow to **suggested on** only when
  benchmark evidence clears the lab's ship rule (enough paired runs, cost down by
  at least 5% across the whole range, success no more than 5 points worse).
  Trove only suggests; it never turns an optimizer on by itself.
- *Rollback*: an optimizer goes back to shadow for new sessions when, in your A/B
  sessions, cost per session is up by at least `cost_increase_percent` (default
  5%) with the whole range above zero, or turns or tool errors are up by at least
  `outcome_worsen_percent` (default 20%) in the same way, or abandoned sessions
  are up by 10 points or more. The reason is recorded and shown in the report,
  including that shadow mode could not have seen it. Running sessions keep their
  state, and `config.toml` is never edited.

**Why Trove's numbers may be lower than other tools' claims.** Many tools report
the tokens they removed, compared with a raw, untrimmed output nobody was going to
pay for. Trove compares real bills: it counts only what would have been sent, it
includes the optimizer's own overhead, and it includes the extra turns and retries
an optimization can cause. When an optimization makes the agent work harder, the
claim and the bill can point in opposite directions; Trove reports the bill.

Look at the results with `trove report` or `trove experiment status` (both support
`--json`). They are recomputed from stored metadata (arms, per-session cost and
signals), so a re-run gives the same answer.

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
