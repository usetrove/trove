"""Markdown report, CSVs and charts. Deterministic for the same runs (except the PNG bytes)."""

from __future__ import annotations

import csv
import json
from collections import defaultdict
from pathlib import Path
from typing import Sequence

from .conditions import Condition
from .records import RECORD_FIELDS, RunRecord
from .stats import (
    DONT_SHIP, NEED_MORE_DATA, NOT_MEASURABLE, SHIP, Comparison, ShipRule, compare, make_pairs, summarize, usable,
)

VERDICT_TEXT = {
    SHIP: "SHIP",
    DONT_SHIP: "DON'T SHIP",
    NEED_MORE_DATA: "NEED MORE DATA",
    NOT_MEASURABLE: "NOT MEASURABLE WITH THIS AGENT",
}

# Chart palette (dataviz reference palette, light surface): categorical slot 1 for the single series,
# primary/secondary/muted ink, hairline grid.
BLUE, INK, INK2, MUTED, GRID, SURFACE = "#2a78d6", "#0b0b0b", "#52514e", "#898781", "#e1e0d9", "#fcfcfb"


def _csv(path: Path, header: Sequence[str], rows: Sequence[Sequence]) -> None:
    with open(path, "w", encoding="utf-8", newline="") as f:
        w = csv.writer(f, lineterminator="\n")
        w.writerow(header)
        w.writerows(rows)


def _f(x: float, nd: int = 4) -> str:
    return f"{x:.{nd}f}"


def analyze(records: Sequence[RunRecord], conds: Sequence[Condition], rule: ShipRule):
    """Returns (summaries, comparisons). Comparisons cover every measurable condition except the reference."""
    measurable = [c for c in conds if c.measurable]
    summaries = [summarize(records, c.name, rule) for c in measurable]
    comps: list[tuple[Condition, Comparison | None, str]] = []
    for c in conds:
        if c.name == rule.reference:
            continue
        if not c.measurable:
            comps.append((c, None, c.unsupported_reason or ""))
            continue
        comps.append((c, compare(make_pairs(records, rule.reference, c.name), c.name, rule), ""))
    return summaries, comps


def write_all(batch: Path, records: Sequence[RunRecord], conds: Sequence[Condition], rule: ShipRule,
              versions: dict, config_text: str, n_tasks: int) -> Path:
    records = sorted(records, key=lambda r: r.run_id)
    summaries, comps = analyze(records, conds, rule)

    _csv(batch / "runs.csv", RECORD_FIELDS, [[r.as_row()[k] for k in RECORD_FIELDS] for r in records])
    _csv(batch / "summary.csv",
         ["condition", "n_runs", "n_excluded", "mean_cost_usd", "median_cost_usd", "success_rate", "success_ci_lo",
          "success_ci_hi", "mean_turns", "mean_retries"],
         [[s.condition, s.n_runs, s.n_excluded, _f(s.mean_cost, 6), _f(s.median_cost, 6), _f(s.success_rate),
           _f(s.success_ci[0]), _f(s.success_ci[1]), _f(s.mean_turns, 2), _f(s.mean_retries, 2)] for s in summaries])
    pair_rows = []
    for c, comp, _ in comps:
        if comp is None:
            continue
        for p in make_pairs(records, rule.reference, c.name):
            pair_rows.append([c.name, rule.reference, p.task_id, p.seed, _f(p.ref.cost_usd, 6), _f(p.treated.cost_usd, 6),
                              int(p.ref.passed), int(p.treated.passed)])
    _csv(batch / "pairs.csv", ["condition", "reference", "task_id", "seed", "reference_cost_usd", "condition_cost_usd",
                               "reference_passed", "condition_passed"], pair_rows)
    _csv(batch / "comparisons.csv",
         ["condition", "reference", "n_pairs", "cost_change_pct", "cost_ci_lo", "cost_ci_hi", "success_diff_pts",
          "success_ci_lo", "success_ci_hi", "verdict"],
         [[c.name, rule.reference, k.n_pairs, _f(k.cost_change_pct, 2), _f(k.cost_ci[0], 2), _f(k.cost_ci[1], 2),
           _f(k.success_diff_pts, 2), _f(k.success_ci[0], 2), _f(k.success_ci[1], 2), k.verdict]
          for c, k, _ in comps if k is not None])
    (batch / "versions.json").write_text(json.dumps(versions, indent=2, sort_keys=True) + "\n", encoding="utf-8", newline="\n")
    (batch / "config.used.yaml").write_text(config_text, encoding="utf-8", newline="\n")

    charts = _charts(batch, summaries, comps, rule)
    md = _markdown(records, conds, summaries, comps, rule, versions, n_tasks, charts)
    out = batch / "report.md"
    out.write_text(md, encoding="utf-8", newline="\n")
    return out


def _markdown(records, conds, summaries, comps, rule: ShipRule, versions: dict, n_tasks: int, charts: list[str]) -> str:
    L: list[str] = []
    a = L.append
    a("# Trove benchmark report\n")
    a(f"Agent `{versions.get('agent')}` ({versions.get('agent_version_reported') or versions.get('agent_version_pinned') or 'unpinned'}), "
      f"model `{versions.get('model')}`, {n_tasks} task(s), {len(records)} run(s).\n")
    if versions.get("fake_agent"):
        a("> **This batch used the fake agent and a mock upstream. Nothing here measures real model behavior.**\n")
    if versions.get("trove_dirty"):
        a("> Trove was built from a working tree with uncommitted changes. The binary hash below identifies exactly what ran, "
          "but a commit alone will not reproduce it.\n")
    a("**How to read the numbers.** Token counts and pass/fail are *measured*. Costs for gateway conditions are computed at list "
      "price from measured token counts. Costs for the `direct` condition are *estimated* from the agent's own usage report "
      "(no gateway, long-context rates not modeled), so `direct` is never used as a reference for the ship rule.\n")

    a("## Ship decisions\n")
    a(f"Rule: ship an optimizer only if, over at least **{rule.min_pairs}** paired runs against `{rule.reference}`, "
      f"the {rule.confidence:.0%} bootstrap interval of the cost change is entirely at or below **-{rule.min_cost_reduction_pct:g}%** "
      f"and the interval of the success-rate difference stays above **-{rule.success_margin_pts:g} points**.\n")
    a("| Condition | Pairs | Cost change (95% CI) | Success diff, points (95% CI) | Verdict |")
    a("|---|---:|---|---|---|")
    for c, k, why in comps:
        if k is None:
            a(f"| `{c.name}` | - | - | - | {VERDICT_TEXT[NOT_MEASURABLE]}: {why} |")
            continue
        optimizer = bool(c.on)
        verdict = VERDICT_TEXT[k.verdict] if optimizer else "n/a (not an optimizer; shown for gateway overhead)"
        a(f"| `{c.name}` | {k.n_pairs} | {k.cost_change_pct:+.1f}% ({k.cost_ci[0]:+.1f}, {k.cost_ci[1]:+.1f}) | "
          f"{k.success_diff_pts:+.1f} ({k.success_ci[0]:+.1f}, {k.success_ci[1]:+.1f}) | {verdict} |")
    a("")
    for c, k, _ in comps:
        if k is not None and c.on:
            a(f"- `{c.name}`: {k.reason}")
    a("")

    a("## Per condition\n")
    a("| Condition | Runs | Excluded | Mean cost | Median cost | Success rate (95% CI) | Mean turns | Mean retries |")
    a("|---|---:|---:|---:|---:|---|---:|---:|")
    for s in summaries:
        a(f"| `{s.condition}` | {s.n_runs} | {s.n_excluded} | ${s.mean_cost:.4f} | ${s.median_cost:.4f} | "
          f"{s.success_rate:.0%} ({s.success_ci[0]:.0%}, {s.success_ci[1]:.0%}) | {s.mean_turns:.1f} | {s.mean_retries:.1f} |")
    a("\nExcluded runs failed before they could be graded, or have an unknown cost; they are listed below. "
      "Retries are repeated identical tool calls (from gateway conditions only; `direct` cannot observe them).\n")

    if charts:
        a("## Charts\n")
        for name, alt in charts:
            a(f"![{alt}]({name})\n")

    a("## Per task (mean cost, USD)\n")
    by_task: dict[str, dict[str, list[float]]] = defaultdict(lambda: defaultdict(list))
    for r in records:
        if usable(r):
            by_task[r.task_id][r.condition].append(r.cost_usd)
    names = [s.condition for s in summaries]
    a("| Task | " + " | ".join(f"`{n}`" for n in names) + " |")
    a("|---|" + "---:|" * len(names))
    for t in sorted(by_task):
        cells = [f"{sum(by_task[t][n]) / len(by_task[t][n]):.4f}" if by_task[t].get(n) else "-" for n in names]
        a(f"| {t} | " + " | ".join(cells) + " |")
    a("")

    bad = [r for r in records if not usable(r) or r.note]
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
    a("## Attribution\n")
    a("Task content belongs to its upstream projects (see `bench/LICENSES.md`). This report publishes only task IDs and aggregate numbers.\n")
    return "\n".join(L)


def _charts(batch: Path, summaries, comps, rule: ShipRule) -> list[tuple[str, str]]:
    try:
        import matplotlib

        matplotlib.use("Agg")
        import matplotlib.pyplot as plt
    except ImportError:
        return []
    out: list[tuple[str, str]] = []

    def style(ax, xlabel: str = "", ylabel: str = ""):
        ax.set_facecolor(SURFACE)
        for side in ("top", "right"):
            ax.spines[side].set_visible(False)
        for side in ("left", "bottom"):
            ax.spines[side].set_color(MUTED)
        ax.tick_params(colors=INK2, labelsize=9)
        ax.grid(axis="x" if ylabel == "" and xlabel else "y", color=GRID, linewidth=0.8)
        ax.set_axisbelow(True)

    def fig_ax(h: float):
        fig, ax = plt.subplots(figsize=(7.2, h), facecolor=SURFACE)
        return fig, ax

    if summaries:
        names = [s.condition for s in summaries]
        # 1. mean cost per condition (one measure, bars anchored at zero)
        fig, ax = fig_ax(0.6 * len(names) + 1.4)
        ys = list(range(len(names)))[::-1]
        vals = [s.mean_cost for s in summaries]
        ax.barh(ys, vals, color=BLUE, height=0.5)
        for y, v in zip(ys, vals):
            ax.text(v, y, f"  ${v:.4f}", va="center", color=INK, fontsize=9)
        ax.set_yticks(ys, names)
        ax.set_xlim(0, max(vals) * 1.25 if max(vals) > 0 else 1)
        ax.set_title("Mean cost per run (USD, list price from measured tokens)", loc="left", color=INK, fontsize=11)
        style(ax, xlabel="x")
        fig.tight_layout()
        fig.savefig(batch / "chart_cost.png", dpi=150, facecolor=SURFACE)
        plt.close(fig)
        out.append(("chart_cost.png", "Mean cost per run by condition"))

        # 2. success rate with bootstrap CI
        fig, ax = fig_ax(0.6 * len(names) + 1.4)
        for y, s in zip(ys, summaries):
            ax.plot([s.success_ci[0] * 100, s.success_ci[1] * 100], [y, y], color=BLUE, linewidth=2)
            ax.plot([s.success_rate * 100], [y], "o", color=BLUE, markersize=8, markeredgecolor=SURFACE, markeredgewidth=2)
            ax.text(s.success_ci[1] * 100 + 1.5, y, f"{s.success_rate:.0%}", va="center", color=INK, fontsize=9)
        ax.set_yticks(ys, names)
        ax.set_xlim(0, 112)
        ax.set_title("Success rate with 95% bootstrap interval (%)", loc="left", color=INK, fontsize=11)
        style(ax, xlabel="x")
        fig.tight_layout()
        fig.savefig(batch / "chart_success.png", dpi=150, facecolor=SURFACE)
        plt.close(fig)
        out.append(("chart_success.png", "Success rate by condition with confidence intervals"))

    rows = [(c.name, k) for c, k, _ in comps if k is not None and k.n_pairs > 0]
    if rows:
        # 3. cost change vs the reference, with the ship threshold
        fig, ax = fig_ax(0.6 * len(rows) + 1.6)
        ys = list(range(len(rows)))[::-1]
        for y, (name, k) in zip(ys, rows):
            ax.plot(list(k.cost_ci), [y, y], color=BLUE, linewidth=2)
            ax.plot([k.cost_change_pct], [y], "o", color=BLUE, markersize=8, markeredgecolor=SURFACE, markeredgewidth=2)
            ax.text(k.cost_ci[1] + 0.8, y, f"{k.cost_change_pct:+.1f}%", va="center", color=INK, fontsize=9)
        ax.axvline(0, color=MUTED, linewidth=1)
        ax.axvline(-rule.min_cost_reduction_pct, color=INK2, linewidth=1, linestyle=(0, (4, 3)))
        ax.set_ylim(-0.6, ys[0] + 1.0)
        ax.text(-rule.min_cost_reduction_pct, ys[0] + 0.62, f"ship threshold -{rule.min_cost_reduction_pct:g}%",
                color=INK2, fontsize=8, ha="center", va="bottom")
        ax.set_yticks(ys, [n for n, _ in rows])
        ax.set_title(f"Cost change vs {rule.reference} (paired, 95% bootstrap interval)", loc="left", color=INK, fontsize=11)
        style(ax, xlabel="x")
        fig.tight_layout()
        fig.savefig(batch / "chart_cost_change.png", dpi=150, facecolor=SURFACE)
        plt.close(fig)
        out.append(("chart_cost_change.png", "Paired cost change versus the reference condition"))
    return out
