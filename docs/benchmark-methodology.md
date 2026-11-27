# Benchmark methodology

How the Trove benchmark lab (`bench/`, Python package `trovebench`) compares coding-agent setups, and
how a result becomes a recommendation. This page is written to be published with the results.

Two rules frame everything: **honest measurement is the product**, and **a wrong number is worse than no
number**. Every figure is labeled *measured* or *estimated*, and a comparison that cannot be made
fairly is reported as "not estimable" rather than forced into a number.

## 1. Design

**Paired.** The unit of analysis is a *unit* = one task, one repeat and one reasoning-effort level.
Every condition being compared runs once in every unit, so each condition sees the same tasks, the
same repeats and the same effort. Conditions are compared within units, never across them.

**Randomized.** Unit order is shuffled, and condition order is shuffled inside each unit, from one
seed stored in the study file. No condition systematically runs first, so drift in the provider
(load, caching warm-up, model updates during a batch) is spread across conditions. The same inputs
always produce the same plan.

**Several repeats.** Agents are not deterministic. Each task runs `repeats` times per condition and
effort. Repeats of one task are *not* independent observations, so every interval below resamples
whole tasks (see section 4).

**More than one reasoning effort.** Studies list `efforts` (`low`, `medium`, `high`, and where the
model supports it `xhigh`, `max`). Independent tests found results differ by effort, so each effort
is analysed on its own, pooled, and tested for whether the effect differs between levels.

**Fresh environment per run.** One container per run, one pinned agent version, one pinned model ID
(never an alias), API-key authentication only. No subscription or OAuth credential is ever used.

**Tasks.** Only tasks whose licence allows use and publication. Task content is not republished; a
report names task IDs and shows aggregates.

## 2. Pre-registration

A study is a file in `bench/studies/`. It states, before any run: the hypothesis, the primary
metric, the baseline, the conditions, the effort levels, the repeats, the order seed, the analysis
settings and the ship rule.

`python -m trovebench prereg <study>` writes a lock (file hash, git commit, time). Afterwards:

- `run` and `plan` refuse a study that has no lock or has changed since it was locked.
- `report` re-checks the hash recorded with the results, so a report is always of the registered study.
- Changing a design means registering a new study id. Analyses added afterwards are labeled
  *exploratory*.

## 3. What is measured

| Quantity | Source | Label |
|---|---|---|
| Pass or fail | The task's own tests, run after the agent finishes | measured |
| Wall time | Clock around the agent run | measured |
| Token counts | The agent's own usage report; for gateway conditions also the gateway's count of provider-reported usage | measured |
| Cost | List price (`internal/pricing`) applied to token counts | **estimated** |

**Cost basis.** The primary basis is `agent`: for *every* condition, cost is the list price applied to
the usage the agent itself reported. A gateway only exists in some conditions, so using the gateway's
count for those and the agent's for the rest would compare two different instruments. The gateway
count is kept as a cross-check, and a difference of more than 10% on a run is flagged in the report.
Costs are never an invoice: they do not include discounts, long-context surcharges the usage report
cannot show, or credits. The agent's own `total_cost_usd` is a client-side estimate and is not used.

**Exclusions.** A run that failed before it could be graded (agent crash, API error, missing usage)
is excluded, with its reason in the report's data-quality table. A unit missing a usable run in any
condition is left out of every paired comparison and counted in the report.

## 4. Statistics

All procedures are deterministic given the seed stored in the study.

- **Effect on cost:** per pair, the change `treated / baseline - 1`. The report gives the *median* of
  the per-pair changes (robust to the heavy tails of agent cost) with a 95% percentile bootstrap
  interval that **resamples whole tasks**, because repeats of a task are correlated.
- **Success:** the difference in pass rate, in points, with a task-clustered bootstrap interval, and an
  exact McNemar test on the discordant pairs.
- **p-values:** a Wilcoxon signed-rank test (exact for up to 25 untied pairs, normal approximation with
  tie and continuity corrections otherwise) and a paired sign-flip permutation test on cost
  differences. The study registers which one is primary.
- **Multiple comparisons:** Holm correction across the comparisons registered in the study, within
  each effort stratum.
- **Effort heterogeneity:** a permutation test that shuffles effort labels within each task and
  compares the spread of the per-level mean cost changes. A small p suggests the effect depends on effort.
- **Parity headline (study a):** plugin saving as a percentage of the gateway's saving, both against
  no Trove: `(none - plugin) / (none - gateway)` on summed costs, with a task-clustered bootstrap
  interval. If the interval of the gateway's own saving includes zero, the ratio is reported as
  **not estimable**.

The implementation is `bench/trovebench/inference.py`; its tests compare against brute-force
enumeration and SciPy.

### 4a. Covariate adjustment in the in-product A/B (pre-registered 2026-11-17)

This applies to the A/B Trove runs on a user's own sessions (`internal/experiment`, see
`docs/measurement.md` section 4a), not to the lab studies above. The method was written down here
before it was implemented, and any later change is a new dated entry.

- **Method.** CUPED (controlled-experiment using pre-experiment data). For each cost metric *Y*
  (cost per session, cost per turn, cost per uncached input token) and covariate *X*, a session's
  adjusted value is `Y - θ_s (X - mean_s(X))`, where `θ_s = cov(X, Y) / var(X)` and `mean_s(X)` are
  fitted on **both arms together** within stratum *s*. The effect is the stratified difference of the
  adjusted arm means, divided by the **unadjusted** stratified off-arm mean for a relative change.
- **Covariate.** Prior cost: the mean of the same metric over the last 5 *off*-arm sessions in the
  same stratum (repo and task type) from **earlier randomisation blocks**. Every session in a block
  gets the same covariate, so it cannot depend on the session's own arm. Only off-arm sessions are
  used, so the covariate is never moved by the optimizer. Project (repo) is already the stratum key.
- **Not a covariate.** Session length (turns) and anything else measured during the session: the
  optimizer can change them, and adjusting for them would bias the effect.
- **Missing covariate.** A session with no earlier off-arm session in its stratum gets `mean_s(X)`, so
  it is not adjusted and not dropped.
- **When it is used.** Only when, in the strata used, at least 80% of sessions have a covariate and
  the pooled within-stratum correlation of *X* and *Y* is at least 0.3 in absolute value (below that
  the variance gain is under 9% and not worth the extra fitting). Otherwise the unadjusted figure is
  reported. The check looks only at *X* and *Y*, never at the arm difference.
- **Range.** 95% percentile bootstrap, seeded, resampling (*Y*, *X*) pairs per arm within stratum, with
  θ re-fitted in every resample so the range includes the uncertainty in θ.
- **Reporting.** Both ranges are shown: the adjusted one (used for the verdict and rollback) and the
  unadjusted one. Outcome metrics (turns, tool errors, abandoned) are not adjusted.

## 5. The ship rule

A condition is recommended only if **all** hold, against the registered baseline:

1. at least `min_pairs` pairs (default 30);
2. the whole 95% interval of the cost change is at or below `-min_cost_reduction_pct` (default 5%);
3. the lower bound of the success-rate difference is above `-success_margin_pts` (default 5 points);
4. the primary p-value, after Holm correction, is below alpha.

Failing (2) or (3) the other way, with the whole interval on the wrong side, is "don't ship"; an
interval that still straddles a threshold is "need more data". Success is a guardrail, not something
to trade against cost.

## 6. From results to recommendations

`python -m trovebench defaults --results <study folder>...` generates
`internal/recommend/defaults.toml` and `internal/experiment/priors.toml` from result folders. Nothing
is hand-written. It starts from `bench/defaults_seed.toml` (rules of thumb, `evidence = "none"`), and:

- a rule becomes `evidence = "benchmark"`, with the study id and lock hash as `source`, only when the
  study registered a recommendation for it, its pooled result is "ship", and **no effort level is a
  "don't ship"**;
- a rule the results contradict ("don't ship") is removed;
- inconclusive results change nothing;
- results from simulated or fake-agent runs are refused;
- a study whose registered rule is weaker than the one Trove enforces (5%, 5 points, 30 pairs) can
  never raise a rule's evidence;
- a result folder whose study file no longer matches its lock is refused.

The generated file starts with a header naming its inputs, and CI loads it with the real Go loader.

## 7. Studies

| | Study | Question | Status |
|---|---|---|---|
| a | Parity | No Trove vs plugin only vs gateway only vs both on one Claude Code version: what share of the gateway's saving does the plugin deliver? | harness, analysis and report built; `bench/studies/parity.yaml` registered-ready; real run pending |
| b | Cascading | Cheap model first, escalate on failing tests, vs the strong model alone | not built |
| c | Scout subagent | With vs without | not built |
| d | Settings profiles | Each profile vs default settings | not built |
| e | Third-party tools | Each wrapped tool vs no tool, with its claimed saving shown next to the measurement | claim-vs-measurement analysis built; tool adapters not built. On real sessions, the claims table (section 11) does this for hook tools |
| f | Open-weight and self-hosted models | Cost, success and latency through OpenAI-compatible upstreams | not built |

## 8. Threats to validity

- **Estimated cost.** Cost is list price on reported usage, not billing.
- **Hook conditions and `--bare`.** Claude Code's `--bare` mode skips hooks (documented), so
  conditions that rely on Trove's hooks run without it. Confirm with the live hook probe that the hooks
  fire before trusting a plugin-only result.
- **Task suite.** Results hold for the suite's tasks (language, size, test setup). A claim measured on
  a vendor's own workload may legitimately differ.
- **Model drift.** Providers update models. The model ID and agent version are pinned and recorded;
  randomized order spreads within-batch drift but cannot remove it.
- **Caching.** Provider caches warm across runs. Randomized order and a fresh container per run limit
  this; it is not eliminated.
- **Small samples.** With few tasks the task-clustered intervals are wide. The ship rule says "need
  more data" rather than guessing.
- **Multiple studies.** Holm correction covers one study; running many studies and reporting the
  winners would still be selective, which is why studies are registered first.

## 9. Data use

Full transcripts of benchmark runs are kept only for analysis and debugging, are redacted for keys,
and are never used to train any model. Training, if any, uses metadata, derived features and outcomes
only (never generated code or text). Reports publish task IDs and aggregates.

## 10. Reproducing a study

```
pip install -e "bench[dev,report]"
python -m trovebench prereg bench/studies/parity.yaml      # lock the design
python -m trovebench plan   --study bench/studies/parity.yaml    # estimate; spends nothing
python -m trovebench run    --study bench/studies/parity.yaml --yes
python -m trovebench report --batch bench/results/<date>/<study-id>
```

`--simulate <effects.yaml>` runs synthetic, Docker-free runs to exercise the pipeline. It measures
nothing about any agent, and its reports and results are labeled and refused by `defaults`.

## 11. The claims table

Added 2026-11-26. Code: `internal/report/claims.go`, `internal/report/trial.go`, `internal/experiment`.

`trove report claims` puts each token-saving tool's own claim next to what Trove measured on one
machine's real sessions. Third-party tools Trove can hand calls to (RTK, ZTK) and Trove's own
optimizers share one table. A row with no clear effect stays in it. `--markdown` prints the table
ready to publish, and `--json` gives the same cells.

This is not a lab study (sections 1 to 10). It runs on someone's everyday work, so it answers "what did
this tool do to my usage", not "what does it do on a fixed task suite". The two complement each other:
the lab controls the tasks, and the claims table measures the plan limit, which only a subscriber has.

### Design

- **Randomized per session.** When a hook tool is enabled, Trove writes its own Bash `PreToolUse` hook,
  `trove hook handoff <tool>`, instead of the tool's ([external.md](external.md#randomized-test-for-hook-tools)).
  At `SessionStart` each new Claude Code session is put in the "on" or "off" arm by a hash of its hashed
  session key, and the arm is recorded. In "on" the tool gets the hook input byte for byte and its output
  goes back unchanged. In "off" nothing runs. Trove's own hook optimizers are split the same way.
  Request-level optimizers are split by the gateway.
- **Intention to treat.** Every assigned session counts under its arm, whether or not the tool acted in it.
  Dropping "on" sessions where it never acted would drop exactly the ones it could not change.
- **Finished sessions only.** A session counts once it has been quiet for 30 minutes.
- **Tools are not modified.** A tool is run only if it documents one hook command that can be handed the
  input unchanged. A tool that needs several hooks across events, or its plugin's own layout, is not run.
  Running part of it would measure something other than what it claims. See
  [external.md](external.md#considered-and-not-run).

### What each column means

| Column | What it is | Label |
|---|---|---|
| Its claim | Quoted from the tool's own README or site, with the link. The date Trove checked it is in the tool's manifest. For Trove's own optimizers: the shadow estimate (what it would have saved had it been on) for the report period, or "none". | claimed / estimated |
| Effect on the weekly limit per session | The relative change in the share of the weekly limit a session used, on against off, and the same as a difference in points of the limit (1 point = 1% of the weekly limit). Each session's tokens by type are turned into a share of the limit with this account's limit fit ([limit-fit.md](limit-fit.md)). When the weekly meter has a fit per token type, its weights are used. Otherwise it is API list prices times the fitted scale ("estimated from API prices"). | estimated |
| Tokens or cost per session | Hook trials: tokens per session (input, cache reads and writes, output) from the agent's own logs. Gateway trials: cost per session at API list prices, from provider-reported usage. | measured / estimated |
| Pass-rate change | Among sessions that ran a test command Trove recognizes, the share whose last test passed, on minus off, in points. | measured |
| Sessions (on / off) | Sessions compared in each arm, after exclusions. | |
| Dates | First and last assignment in the trial (UTC). | |

A gateway trial's weekly-limit cell says "not applicable": those sessions use an API key, and API traffic
does not draw on a plan limit.

### Statistics

- **Effect ranges.** 95% percentile bootstrap, 2,000 resamples, seeded from `[experiment] seed` and the
  optimizer and metric names, so the same data always gives the same table. Sessions are stratified by
  repo and task type. Each stratum's two arms are resampled separately, and strata are weighted by size. A
  stratum with fewer than `min(min_sessions_per_arm, 10)` sessions in either arm is left out, never padded.
- **Pass-rate range.** The same bootstrap on pass or fail per session, without strata.
- **Minimum data.** Nothing is shown until each arm has `[experiment] min_sessions_per_arm` sessions
  (default 20). The pass rate also needs that many test-running sessions in each arm.
- **"No clear effect"** means the 95% range includes no change. It is reported as a result.
- **What the limit range does not cover.** The range covers how sessions vary. It does not add the limit
  fit's own uncertainty, because every session is weighed with the same point weights. Under the API-price
  basis the relative change is exactly the relative change in API-equivalent cost, because the fitted
  scale cancels. Under a fit per token type the relative change depends on the weight ratios, whose 90%
  intervals `trove report` shows.

### Exclusions

- Sessions with no usage in the agent's logs yet, or still running.
- Sessions the limit fit cannot weigh (for example an unpriced model under the API-price basis). They are
  left out of the weekly-limit figure only.
- Tools Trove has no built-in manifest for.

### Threats to validity

- **One account.** The weekly-limit figures come from one account's meter. Another account's weights may
  differ, and the meter also moves with use from claude.ai and other machines (the fit's "outside" share).
  Publish them as one account's estimate, never as official weights.
- **One person's work.** The tasks are whatever the user did. A tool that helps on large shell output
  looks weaker for someone who rarely runs shell commands. The table gives the dates. Say whose machine.
- **Only Bash calls.** A hand-off tool sees only shell calls, not the agent's built-in Read, Grep or Glob.
  That is the tool's own reach. A claim measured on shell output alone can read as larger.
- **Pass rate is a weak signal.** It counts only sessions that ran a recognized test command, and only the
  last result.
- **Several rows.** Each row has its own 95% range, with no correction across rows. Read a single
  borderline row with care.

### Reproducing a row

```
trove setup --status-line            # Claude Code's status line records the plan meter (report.md)
trove external install rtk           # shows licence, command and claim; asks before installing
trove external enable rtk            # Trove writes `trove hook handoff rtk`; new sessions are split
# use Claude Code normally until each arm has at least 20 finished sessions
trove report                         # refreshes the limit fit and the trial
trove report claims --markdown       # the table
trove report claims --json           # the same cells, with every figure behind them
```

Each session's arm is in `experiment_assignments`, its tokens in `history_requests`, its test results in
`test_runs`, and the fit in `limitfit.json`. Everything is local and holds metadata only.

### Before publishing

1. **Licence.** A tool whose licence restricts commercial use (for example PolyForm Noncommercial) is run
   for a published table only with the author's written permission. Trove does not decide whether a
   published comparison counts as noncommercial.
2. **Invite each author.** Send the author of every third-party row this section, their row and its
   `--json`, and invite them to check the method, the hook command and the quoted claim. Give them at
   least 14 days. Fix any factual error they find. If they disagree with the method, publish their
   response next to the row.
3. **Versions and dates.** Publish the tool version, the Trove commit, the Claude Code version and the
   dates with the table.
