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
| e | Third-party tools | Each wrapped tool vs no tool, with its claimed saving shown next to the measurement | claim-vs-measurement analysis built; tool adapters not built |
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
