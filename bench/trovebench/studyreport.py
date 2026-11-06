"""Per-study analysis and report: Markdown, CSVs and charts under bench/results/<date>/<study-id>/.

analyze_study() is pure (records + study in, numbers out) so it can be tested without files.
Everything shown is labeled measured (token counts, pass/fail, wall time) or estimated (cost:
list price applied to the agent-reported token counts, never an invoice).
"""

from __future__ import annotations

import json
import math
from dataclasses import dataclass, field
from pathlib import Path
from typing import Sequence

from . import inference as inf
from .records import RECORD_FIELDS, RunRecord
from .report import BLUE, GRID, INK, INK2, MUTED, SURFACE, _csv, _f
from .stats import (DONT_SHIP, NEED_MORE_DATA, SHIP, Comparison, ShipRule, compare, make_pairs, rebase_cost, summarize,
                    usable)
from .study import Study

ALL = "all"
VERDICT_TEXT = {SHIP: "SHIP", DONT_SHIP: "DON'T SHIP", NEED_MORE_DATA: "NEED MORE DATA"}
CONFIRMED, WITHIN, OVERSTATED, UNSUPPORTED = "exceeds claim", "consistent with claim", "overstated", "not supported"


@dataclass(frozen=True)
class CompRow:
    condition: str
    reference: str
    stratum: str                       # "all" or an effort level
    inference: inf.Inference | None
    ship: Comparison | None
    p_primary: float | None
    p_adjusted: float | None
    verdict: str
    reason: str
    heterogeneity_p: float | None = None   # only on the "all" stratum with 2+ effort levels


@dataclass(frozen=True)
class ClaimRow:
    condition: str
    claimed_pct: float
    source_url: str
    retrieved_at: str
    measured_median_pct: float | None
    measured_ci: tuple[float, float] | None
    verdict: str


@dataclass(frozen=True)
class ParityRow:
    stratum: str
    parity: inf.Parity


@dataclass(frozen=True)
class StudyAnalysis:
    rule: ShipRule
    rows: list[CompRow]
    parity: list[ParityRow]
    claims: list[ClaimRow]
    incomplete_units: int
    total_units: int
    summaries: list = field(default_factory=list)


def ship_rule_for(study: Study) -> ShipRule:
    d = {"confidence": 1 - study.analysis.alpha, "bootstrap_resamples": study.analysis.bootstrap_resamples,
         "bootstrap_seed": study.analysis.bootstrap_seed, **study.ship_rule, "reference": study.baseline_condition}
    return ShipRule.from_dict(d)


def judge_claim(claimed_pct: float, ci: tuple[float, float] | None) -> str:
    """Compare a claimed cost reduction with the measured interval of the median % cost change."""
    if ci is None or math.isnan(ci[0]):
        return UNSUPPORTED
    lo, hi = ci
    target = -claimed_pct
    if lo <= target <= hi:
        return WITHIN
    if hi < target:
        return CONFIRMED
    return OVERSTATED if hi < 0 else UNSUPPORTED


def _primary_p(study: Study, i: inf.Inference) -> float:
    return {"billed_cost_usd": i.p_wilcoxon, "success_rate": i.p_mcnemar, "latency_s": i.p_latency_wilcoxon}[study.primary_metric]


def analyze_study(study: Study, records: Sequence[RunRecord]) -> StudyAnalysis:
    recs = rebase_cost(records, study.cost_basis)
    rule = ship_rule_for(study)
    a = study.analysis
    strata = [ALL, *study.efforts] if study.efforts else [ALL]
    rows: list[CompRow] = []
    for stratum in strata:
        batch = []
        for treated, ref in study.comparisons:
            pairs = make_pairs(recs, ref, treated, effort=None if stratum == ALL else stratum)
            i = inf.infer(pairs, resamples=a.bootstrap_resamples, seed=a.bootstrap_seed, confidence=rule.confidence)
            het = None
            if i and stratum == ALL and len(study.efforts) > 1:
                het = inf.effort_heterogeneity_p(pairs, resamples=a.bootstrap_resamples, seed=a.bootstrap_seed + 9)
            batch.append((treated, ref, pairs, i, compare(pairs, treated, rule), het))
        tested = [k for k, b in enumerate(batch) if b[3] is not None]
        raw = [_primary_p(study, batch[k][3]) for k in tested]
        adj = dict(zip(tested, inf.holm(raw) if a.multiple_comparison == "holm" else raw))
        for k, (treated, ref, pairs, i, ship, het) in enumerate(batch):
            p_adj = adj.get(k)
            verdict, reason = ship.verdict, ship.reason
            if ship.verdict == SHIP and p_adj is not None and p_adj >= a.alpha:
                verdict = NEED_MORE_DATA
                reason = (f"The intervals clear the rule, but the primary-metric p-value is {p_adj:.3f} after "
                          f"{'Holm' if a.multiple_comparison == 'holm' else 'no'} correction, above alpha {a.alpha:g}.")
            rows.append(CompRow(treated, ref, stratum, i, ship if i else None, _primary_p(study, i) if i else None,
                                p_adj, verdict, reason, het))

    parity_rows: list[ParityRow] = []
    if study.parity:
        for stratum in strata:
            units = _parity_units(recs, study, None if stratum == ALL else stratum)
            if units:
                parity_rows.append(ParityRow(stratum, inf.parity(units, resamples=a.bootstrap_resamples,
                                                                  seed=a.bootstrap_seed + 20, confidence=rule.confidence)))
    claims = []
    for c in study.claims:
        row = next((r for r in rows if r.condition == c["condition"] and r.stratum == ALL and r.inference), None)
        ci = row.inference.median_cost_change_ci if row else None
        claims.append(ClaimRow(c["condition"], float(c["claimed_cost_reduction_pct"]), c["source_url"], str(c["retrieved_at"]),
                               row.inference.median_cost_change_pct if row else None, ci,
                               judge_claim(float(c["claimed_cost_reduction_pct"]), ci)))

    units: dict[str, set[str]] = {}
    for r in recs:
        u = f"{r.task_id}|{r.seed}|{r.effort}"
        units.setdefault(u, set())
        if usable(r):
            units[u].add(r.condition)
    incomplete = sum(1 for have in units.values() if have != set(study.conditions))
    summaries = [summarize(recs, c, rule) for c in study.conditions]
    return StudyAnalysis(rule, rows, parity_rows, claims, incomplete, len(units), summaries)


def _parity_units(recs: Sequence[RunRecord], study: Study, effort: str | None) -> list[inf.ParityUnit]:
    want = {role: study.parity[role] for role in ("none", "plugin", "gateway")}
    cost: dict[tuple, dict[str, float]] = {}
    for r in recs:
        if usable(r) and (effort is None or r.effort == effort):
            cost.setdefault((r.task_id, r.seed, r.effort), {})[r.condition] = r.cost_usd
    return [inf.ParityUnit(k[0], c[want["none"]], c[want["plugin"]], c[want["gateway"]])
            for k, c in sorted(cost.items()) if all(v in c for v in want.values())]


# --- writing ----------------------------------------------------------------

def write_study_report(out: Path, study: Study, lock: dict, records: Sequence[RunRecord], versions: dict,
                       study_text: str) -> StudyAnalysis:
    out.mkdir(parents=True, exist_ok=True)
    records = sorted(records, key=lambda r: r.run_id)
    an = analyze_study(study, records)
    recs = rebase_cost(records, study.cost_basis)

    _csv(out / "runs.csv", RECORD_FIELDS, [[r.as_row()[k] for k in RECORD_FIELDS] for r in records])
    pair_rows = []
    for treated, ref in study.comparisons:
        for p in make_pairs(recs, ref, treated):
            pair_rows.append([treated, ref, p.task_id, p.seed, p.effort, _f(p.ref.cost_usd, 6), _f(p.treated.cost_usd, 6),
                              int(p.ref.passed), int(p.treated.passed), _f(p.ref.wall_s, 1), _f(p.treated.wall_s, 1)])
    _csv(out / "pairs.csv", ["condition", "reference", "task_id", "seed", "effort", "reference_cost_usd", "condition_cost_usd",
                             "reference_passed", "condition_passed", "reference_wall_s", "condition_wall_s"], pair_rows)
    _csv(out / "comparisons.csv",
         ["condition", "reference", "effort", "n_pairs", "n_tasks", "median_cost_change_pct", "ci_lo", "ci_hi",
          "median_cost_diff_usd", "success_diff_pts", "success_ci_lo", "success_ci_hi", "median_latency_diff_s",
          "p_wilcoxon", "p_permutation", "p_mcnemar", "p_primary", "p_adjusted", "effort_heterogeneity_p", "verdict",
          "rule_cost_change_pct", "rule_cost_ci_lo", "rule_cost_ci_hi", "rule_success_diff_pts", "rule_success_ci_lo"],
         [_comp_csv(r) for r in an.rows if r.inference])
    if an.parity:
        _csv(out / "parity.csv", ["effort", "n_units", "gateway_saving_pct", "gateway_ci_lo", "gateway_ci_hi", "plugin_saving_pct",
                                  "plugin_ci_lo", "plugin_ci_hi", "plugin_share_of_gateway_pct", "share_ci_lo", "share_ci_hi", "estimable"],
             [[p.stratum, p.parity.n_units, _f(p.parity.gateway_saving_pct, 2), *(_f(x, 2) for x in p.parity.gateway_saving_ci),
               _f(p.parity.plugin_saving_pct, 2), *(_f(x, 2) for x in p.parity.plugin_saving_ci),
               *((_f(p.parity.plugin_share_of_gateway_pct, 1), *(_f(x, 1) for x in p.parity.plugin_share_ci)) if p.parity.estimable else ("", "", "")),
               int(p.parity.estimable)] for p in an.parity])
    if an.claims:
        _csv(out / "claims.csv", ["condition", "claimed_cost_reduction_pct", "source_url", "retrieved_at", "measured_median_cost_change_pct",
                                  "ci_lo", "ci_hi", "verdict"],
             [[c.condition, c.claimed_pct, c.source_url, c.retrieved_at,
               _f(c.measured_median_pct, 2) if c.measured_median_pct is not None else "",
               *((_f(c.measured_ci[0], 2), _f(c.measured_ci[1], 2)) if c.measured_ci else ("", "")), c.verdict] for c in an.claims])
    (out / "versions.json").write_text(json.dumps(versions, indent=2, sort_keys=True) + "\n", encoding="utf-8", newline="\n")
    (out / "config.used.yaml").write_text(study_text, encoding="utf-8", newline="\n")
    (out / "study.lock.json").write_text(json.dumps(lock, indent=2, sort_keys=True) + "\n", encoding="utf-8", newline="\n")
    charts = _charts(out, study, an)
    (out / "report.md").write_text(_markdown(study, lock, an, recs, versions, charts), encoding="utf-8", newline="\n")
    return an


def _comp_csv(r: CompRow) -> list:
    i = r.inference
    return [r.condition, r.reference, r.stratum, i.n_pairs, i.n_tasks, _f(i.median_cost_change_pct, 2), *(_f(x, 2) for x in i.median_cost_change_ci),
            _f(i.median_cost_diff_usd, 6), _f(i.success_diff_pts, 2), *(_f(x, 2) for x in i.success_diff_ci), _f(i.median_latency_diff_s, 1),
            _f(i.p_wilcoxon, 4), _f(i.p_permutation, 4), _f(i.p_mcnemar, 4), _f(r.p_primary, 4), _f(r.p_adjusted, 4),
            "" if r.heterogeneity_p is None else _f(r.heterogeneity_p, 4), r.verdict,
            _f(r.ship.cost_change_pct, 2), _f(r.ship.cost_ci[0], 2), _f(r.ship.cost_ci[1], 2),
            _f(r.ship.success_diff_pts, 2), _f(r.ship.success_ci[0], 2)]


def _ci(ci, nd=1, suffix="") -> str:
    return "n/a" if math.isnan(ci[0]) else f"{ci[0]:+.{nd}f}{suffix}, {ci[1]:+.{nd}f}{suffix}"


def _markdown(study: Study, lock: dict, an: StudyAnalysis, recs, versions: dict, charts: list[tuple[str, str]]) -> str:
    L: list[str] = []
    a = L.append
    rule = an.rule
    a(f"# {study.title}\n")
    a(f"Study `{study.id}` · agent `{versions.get('agent', study.agent)}` · model `{versions.get('model', study.model)}` · "
      f"{len({r.task_id for r in recs})} task(s) · {len(recs)} run(s)\n")
    if versions.get("simulated"):
        a("> **SIMULATED. Every run here was drawn from a file of chosen effect sizes. Nothing in this report measures any agent or model; "
          "it only exercises the analysis.**\n")
    elif versions.get("fake_agent"):
        a("> **This study used the fake agent and a mock upstream. Nothing here measures real model behavior.**\n")
    a("## Pre-registration\n")
    a(f"- Hypothesis: {study.hypothesis}")
    a(f"- Primary metric: `{study.primary_metric}`; baseline: `{study.baseline_condition}`; conditions: "
      + ", ".join(f"`{c}`" for c in study.conditions))
    a(f"- Efforts: {', '.join(study.efforts) if study.efforts else 'model default only'}; repeats: {study.repeats}; order seed: {study.order_seed}")
    a(f"- Locked {lock.get('locked_at')} at commit `{(lock.get('git_commit') or 'unknown')[:10]}`, file hash `{lock.get('study_sha256', '')[:12]}`. "
      "Every comparison below was registered before the first run; anything added afterwards is marked exploratory.\n")
    a("**How to read the numbers.** Pass/fail, wall time and token counts are *measured*. Cost is *estimated*: list price applied to the "
      f"token counts the agent itself reported, using the same source for every condition (`{study.cost_basis}` basis). It is not an invoice. "
      "Cost changes are the median of per-pair changes against the baseline, with a bootstrap interval that resamples whole tasks, "
      "because repeats of one task are not independent.\n")
    if an.incomplete_units:
        a(f"> {an.incomplete_units} of {an.total_units} unit(s) (a task, repeat and effort level) are missing a gradeable run in at least one condition. "
          "They are left out of every paired comparison.\n")

    a("## Decisions\n")
    a(f"Rule: ship only if, over at least **{rule.min_pairs}** pairs against `{rule.reference}`, the {rule.confidence:.0%} interval of the cost change "
      f"is entirely at or below **-{rule.min_cost_reduction_pct:g}%**, success does not fall by more than **{rule.success_margin_pts:g} points** "
      f"(interval lower bound), and the primary p-value is below **{study.analysis.alpha:g}** after "
      f"{'Holm' if study.analysis.multiple_comparison == 'holm' else 'no'} correction.\n")
    a(_table(an.rows, ALL))
    for r in (x for x in an.rows if x.stratum == ALL and x.inference):
        a(f"- `{r.condition}`: {r.reason}")
    a("")
    if study.efforts:
        a("## By reasoning effort\n")
        a("Effort-level results are descriptive; each level has fewer pairs than the pooled result.\n")
        for lvl in study.efforts:
            a(f"### {lvl}\n")
            a(_table(an.rows, lvl))
        het = [(r.condition, r.heterogeneity_p) for r in an.rows if r.heterogeneity_p is not None]
        if het:
            a("Does the effect differ by effort? Permutation test of the spread between levels (small p suggests it does):\n")
            a("| Condition | p |")
            a("|---|---:|")
            for c, p in het:
                a(f"| `{c}` | {p:.3f} |")
            a("")
    if an.parity:
        a("## Parity: plugin savings as a share of the gateway's\n")
        a(f"Both measured against `{study.parity['none']}`. Plugin = `{study.parity['plugin']}`, gateway = `{study.parity['gateway']}`"
          + (f", both = `{study.parity['both']}`" if "both" in study.parity else "") + ".\n")
        a("| Effort | Units | Gateway saving (CI) | Plugin saving (CI) | Plugin as % of gateway (CI) |")
        a("|---|---:|---|---|---|")
        for p in an.parity:
            x = p.parity
            share = f"{x.plugin_share_of_gateway_pct:.0f}% ({_ci(x.plugin_share_ci, 0, '%')})" if x.estimable else x.note
            a(f"| {p.stratum} | {x.n_units} | {x.gateway_saving_pct:.1f}% ({_ci(x.gateway_saving_ci, 1, '%')}) | "
              f"{x.plugin_saving_pct:.1f}% ({_ci(x.plugin_saving_ci, 1, '%')}) | {share} |")
        a("")
    if an.claims:
        a("## Claimed versus measured\n")
        a("| Condition | Claimed cost reduction | Source | Measured median cost change (CI) | Verdict |")
        a("|---|---:|---|---|---|")
        for c in an.claims:
            m = "-" if c.measured_median_pct is None else f"{c.measured_median_pct:+.1f}% ({_ci(c.measured_ci, 1, '%')})"
            a(f"| `{c.condition}` | {c.claimed_pct:g}% | {c.source_url} (retrieved {c.retrieved_at}) | {m} | {c.verdict} |")
        a("\n'consistent with claim' means the claimed figure lies inside the measured interval; 'exceeds claim' means the interval is entirely "
          "below it (a bigger saving than claimed); 'overstated' means a saving exists but is smaller than claimed; 'not supported' means no saving was detected. "
          "A claim is about the vendor's own workload, so a mismatch here is about this task suite, not proof the vendor is wrong.\n")
    a("## Per condition\n")
    a("| Condition | Runs | Excluded | Mean cost (est.) | Median cost (est.) | Success rate (95% CI) |")
    a("|---|---:|---:|---:|---:|---|")
    for s in an.summaries:
        a(f"| `{s.condition}` | {s.n_runs} | {s.n_excluded} | ${s.mean_cost:.4f} | ${s.median_cost:.4f} | "
          f"{s.success_rate:.0%} ({s.success_ci[0]:.0%}, {s.success_ci[1]:.0%}) |")
    a("")
    if charts:
        a("## Charts\n")
        for name, alt in charts:
            a(f"![{alt}](charts/{name})\n")
    bad = [r for r in recs if not usable(r) or r.note]
    if bad:
        a("## Data quality\n")
        a("| Run | Status | Note |")
        a("|---|---|---|")
        for r in bad:
            a(f"| {r.run_id} | {r.status} | {r.note or '-'} |")
        a("")
    a("## Versions (pinned for reproduction)\n")
    a("```json")
    a(json.dumps(versions, indent=2, sort_keys=True))
    a("```\n")
    a("## Data use\n")
    a("Transcripts of these runs, if kept, are for analysis and debugging only. No model is trained on anything produced by these runs. "
      "Task content belongs to its upstream projects (see `bench/LICENSES.md`); this report publishes only task IDs and aggregates.\n")
    return "\n".join(L)


def _table(rows: Sequence[CompRow], stratum: str) -> str:
    out = ["| Condition | Pairs | Median cost change, est. (CI) | Success diff, pts (CI) | Median latency diff | p (primary, adj.) | Verdict |",
           "|---|---:|---|---|---|---|---|"]
    for r in rows:
        if r.stratum != stratum:
            continue
        i = r.inference
        if i is None:
            out.append(f"| `{r.condition}` | 0 | - | - | - | - | {VERDICT_TEXT[NEED_MORE_DATA]} |")
            continue
        out.append(f"| `{r.condition}` | {i.n_pairs} | {i.median_cost_change_pct:+.1f}% ({_ci(i.median_cost_change_ci, 1, '%')}) | "
                   f"{i.success_diff_pts:+.1f} ({_ci(i.success_diff_ci)}) | {i.median_latency_diff_s:+.1f}s ({_ci(i.median_latency_diff_ci, 1, 's')}) | "
                   f"{r.p_primary:.3f}, {r.p_adjusted:.3f} | {VERDICT_TEXT[r.verdict]} |")
    return "\n".join(out) + "\n"


def _charts(out: Path, study: Study, an: StudyAnalysis) -> list[tuple[str, str]]:
    try:
        import matplotlib

        matplotlib.use("Agg")
        import matplotlib.pyplot as plt
    except ImportError:
        return []
    cdir = out / "charts"
    cdir.mkdir(exist_ok=True)
    made: list[tuple[str, str]] = []
    strata = [ALL, *study.efforts] if study.efforts else [ALL]
    rows = [r for r in an.rows if r.inference]
    if not rows:
        return made

    def forest(fname, alt, title, point, ci, ref_line, unit, threshold=None):
        # one panel per effort stratum, one row per condition; a single hue, strata separated by panels
        conds = [c for c, _ in study.comparisons]
        fig, axes = plt.subplots(1, len(strata), figsize=(3.6 * len(strata) + 1.6, 0.55 * len(conds) + 1.8), facecolor=SURFACE,
                                 sharey=True, squeeze=False)
        ys = list(range(len(conds)))[::-1]
        for ax, stratum in zip(axes[0], strata):
            ax.set_facecolor(SURFACE)
            for side in ("top", "right"):
                ax.spines[side].set_visible(False)
            ax.grid(axis="x", color=GRID, linewidth=0.8)
            ax.set_axisbelow(True)
            ax.tick_params(colors=INK2, labelsize=9)
            for y, c in zip(ys, conds):
                r = next((x for x in rows if x.condition == c and x.stratum == stratum), None)
                if r is None:
                    continue
                lo, hi = ci(r.inference)
                v = point(r.inference)
                if not math.isnan(lo):
                    ax.plot([lo, hi], [y, y], color=BLUE, linewidth=2)
                ax.plot([v], [y], "o", color=BLUE, markersize=7, markeredgecolor=SURFACE, markeredgewidth=2)
            ax.axvline(ref_line, color=MUTED, linewidth=1)
            if threshold is not None:
                ax.axvline(threshold, color=INK2, linewidth=1, linestyle=(0, (4, 3)))
            ax.set_title(stratum if len(strata) > 1 else "", color=INK2, fontsize=10, loc="left")
            ax.set_xlabel(unit, color=INK2, fontsize=9)
        axes[0][0].set_yticks(ys, conds)
        fig.suptitle(title, x=0.01, ha="left", color=INK, fontsize=11)
        fig.tight_layout()
        fig.savefig(cdir / fname, dpi=150, facecolor=SURFACE)
        plt.close(fig)
        made.append((fname, alt))

    forest("cost_change.png", "Median paired cost change versus the baseline", f"Median cost change vs {study.baseline_condition} (estimated, 95% CI)",
           lambda i: i.median_cost_change_pct, lambda i: i.median_cost_change_ci, 0, "% change", threshold=-an.rule.min_cost_reduction_pct)
    forest("success_diff.png", "Paired success-rate difference versus the baseline", f"Success difference vs {study.baseline_condition} (measured, 95% CI)",
           lambda i: i.success_diff_pts, lambda i: i.success_diff_ci, 0, "points", threshold=-an.rule.success_margin_pts)
    forest("latency_diff.png", "Median paired wall-time difference versus the baseline", f"Wall-time difference vs {study.baseline_condition} (measured, 95% CI)",
           lambda i: i.median_latency_diff_s, lambda i: i.median_latency_diff_ci, 0, "seconds")
    return made
