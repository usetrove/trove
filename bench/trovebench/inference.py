"""Medians, task-clustered bootstrap, paired tests, Holm correction and the parity ratio.

Pure Python and deterministic: every random draw comes from an explicit seed, so the same
runs always give the same numbers. Repeats of one task are not independent, so every
interval resamples whole tasks (clusters), never individual runs.
"""

from __future__ import annotations

import math
import random
import statistics
from dataclasses import dataclass
from typing import Callable, Sequence

from .stats import Pair

EXACT_WILCOXON_MAX_N = 25
EXACT_PERMUTATION_MAX_N = 16


# --- resampling -------------------------------------------------------------

def group_by_task(pairs: Sequence[Pair]) -> list[list[Pair]]:
    groups: dict[str, list[Pair]] = {}
    for p in pairs:
        groups.setdefault(p.task_id, []).append(p)
    return [groups[k] for k in sorted(groups)]


def cluster_bootstrap(
    clusters: Sequence[Sequence], stat: Callable[[list], float], *, resamples: int, seed: int, confidence: float,
) -> tuple[float, float]:
    """Percentile CI of stat(items) when whole clusters are resampled with replacement.
    Draws where stat is undefined (it returns NaN) are dropped; if too few remain the CI is NaN."""
    if not clusters:
        raise ValueError("bootstrap needs at least one cluster.")
    rng = random.Random(seed)
    n = len(clusters)
    draws = []
    for _ in range(resamples):
        items = [x for _ in range(n) for x in clusters[int(rng.random() * n)]]
        v = stat(items)
        if not math.isnan(v):
            draws.append(v)
    if len(draws) < resamples * 0.9:
        return (math.nan, math.nan)
    draws.sort()
    a = (1 - confidence) / 2
    m = len(draws)
    return draws[int(a * m)], draws[min(m - 1, int((1 - a) * m))]


# --- paired tests -----------------------------------------------------------

def wilcoxon_signed_rank(diffs: Sequence[float]) -> tuple[float, float]:
    """Two-sided Wilcoxon signed-rank test on paired differences. Returns (W+, p).
    Zeros are dropped. Exact when n <= 25 and there are no ties among |d|; otherwise a normal
    approximation with tie and continuity corrections."""
    d = [x for x in diffs if x != 0]
    n = len(d)
    if n == 0:
        return 0.0, 1.0
    order = sorted(range(n), key=lambda i: abs(d[i]))
    ranks = [0.0] * n
    ties = []
    i = 0
    while i < n:
        j = i
        while j + 1 < n and abs(d[order[j + 1]]) == abs(d[order[i]]):
            j += 1
        for k in range(i, j + 1):
            ranks[order[k]] = (i + j) / 2 + 1
        ties.append(j - i + 1)
        i = j + 1
    w_plus = sum(r for r, x in zip(ranks, d) if x > 0)
    if n <= EXACT_WILCOXON_MAX_N and all(t == 1 for t in ties):
        total = n * (n + 1) // 2
        counts = [0] * (total + 1)
        counts[0] = 1
        for r in range(1, n + 1):
            for s in range(total, r - 1, -1):
                counts[s] += counts[s - r]
        w = int(w_plus)
        lower = sum(counts[: w + 1]) / 2**n
        upper = sum(counts[w:]) / 2**n
        return w_plus, min(1.0, 2 * min(lower, upper))
    mean = n * (n + 1) / 4
    var = n * (n + 1) * (2 * n + 1) / 24 - sum(t**3 - t for t in ties) / 48
    if var <= 0:
        return w_plus, 1.0
    z = (abs(w_plus - mean) - 0.5) / math.sqrt(var)
    return w_plus, min(1.0, math.erfc(max(z, 0.0) / math.sqrt(2)))


def sign_flip_permutation_test(diffs: Sequence[float], *, resamples: int, seed: int) -> float:
    """Two-sided paired permutation test of mean(diffs) == 0 by random sign flips (exact when
    n <= 16). Zeros are kept, since they pull the mean toward zero."""
    d = list(diffs)
    n = len(d)
    if n == 0:
        return 1.0
    observed = abs(sum(d))
    eps = 1e-12 * max(1.0, observed)
    if n <= EXACT_PERMUTATION_MAX_N:
        hits = 0
        for mask in range(2**n):
            s = sum(-x if (mask >> i) & 1 else x for i, x in enumerate(d))
            hits += abs(s) >= observed - eps
        return hits / 2**n
    rng = random.Random(seed)
    hits = 0
    for _ in range(resamples):
        s = sum(x if rng.random() < 0.5 else -x for x in d)
        hits += abs(s) >= observed - eps
    return (hits + 1) / (resamples + 1)


def mcnemar_exact(only_treated_passed: int, only_reference_passed: int) -> float:
    """Two-sided exact McNemar p from the discordant pair counts."""
    b, c = only_treated_passed, only_reference_passed
    n = b + c
    if n == 0:
        return 1.0
    tail = sum(math.comb(n, k) for k in range(min(b, c) + 1)) / 2**n
    return min(1.0, 2 * tail)


def holm(pvalues: Sequence[float]) -> list[float]:
    """Holm-Bonferroni adjusted p-values, in the input order."""
    m = len(pvalues)
    order = sorted(range(m), key=lambda i: pvalues[i])
    out = [0.0] * m
    running = 0.0
    for rank, i in enumerate(order):
        running = max(running, min(1.0, (m - rank) * pvalues[i]))
        out[i] = running
    return out


# --- one comparison ---------------------------------------------------------

@dataclass(frozen=True)
class Inference:
    n_pairs: int
    n_tasks: int
    median_cost_diff_usd: float          # treated - reference, per pair
    median_cost_diff_ci: tuple[float, float]
    median_cost_change_pct: float        # per-pair treated/reference - 1, in percent
    median_cost_change_ci: tuple[float, float]
    p_wilcoxon: float
    p_permutation: float
    success_diff_pts: float
    success_diff_ci: tuple[float, float]
    only_treated_passed: int
    only_reference_passed: int
    p_mcnemar: float
    median_latency_diff_s: float
    median_latency_diff_ci: tuple[float, float]
    p_latency_wilcoxon: float


def effort_heterogeneity_p(pairs: Sequence[Pair], *, resamples: int, seed: int) -> float | None:
    """Do the cost changes differ across effort levels? Permutes effort labels among the pairs of
    each task (so task difficulty cancels) and compares the spread (max - min) of the per-level
    mean % change (means, not medians: medians of two-valued data flip under shuffling and lose all power) with the observed spread. None when there is only one effort level."""
    levels = sorted({p.effort for p in pairs})
    if len(levels) < 2:
        return None
    by_task: dict[str, list[Pair]] = {}
    for p in pairs:
        by_task.setdefault(p.task_id, []).append(p)

    def pct(p: Pair) -> float | None:
        return (p.treated.cost_usd / p.ref.cost_usd - 1) * 100 if p.ref.cost_usd > 0 else None

    def spread(assign: list[tuple[str, float]]) -> float:
        groups: dict[str, list[float]] = {}
        for lvl, v in assign:
            groups.setdefault(lvl, []).append(v)
        means = [statistics.fmean(v) for v in groups.values() if v]
        return max(means) - min(means) if len(means) > 1 else 0.0

    tasks = [[(p.effort, v) for p in ps if (v := pct(p)) is not None] for ps in (by_task[k] for k in sorted(by_task))]
    observed = spread([x for t in tasks for x in t])
    rng = random.Random(seed)
    hits = 0
    for _ in range(resamples):
        flat = []
        for t in tasks:
            labels = [lvl for lvl, _ in t]
            rng.shuffle(labels)
            flat += [(lbl, v) for lbl, (_, v) in zip(labels, t)]
        hits += spread(flat) >= observed - 1e-12
    return (hits + 1) / (resamples + 1)


def infer(pairs: Sequence[Pair], *, resamples: int, seed: int, confidence: float) -> Inference | None:
    """Everything the report shows for one treated-vs-reference comparison. None when there are no pairs."""
    if not pairs:
        return None
    kw = dict(resamples=resamples, confidence=confidence)
    clusters = group_by_task(pairs)
    diffs = [p.treated.cost_usd - p.ref.cost_usd for p in pairs]

    def med_diff(items: list[Pair]) -> float:
        return statistics.median(p.treated.cost_usd - p.ref.cost_usd for p in items)

    def med_pct(items: list[Pair]) -> float:
        v = [(p.treated.cost_usd / p.ref.cost_usd - 1) * 100 for p in items if p.ref.cost_usd > 0]
        return statistics.median(v) if v else math.nan

    def succ(items: list[Pair]) -> float:
        return sum(p.treated.passed - p.ref.passed for p in items) / len(items) * 100

    def med_lat(items: list[Pair]) -> float:
        return statistics.median(p.treated.wall_s - p.ref.wall_s for p in items)

    b = sum(1 for p in pairs if p.treated.passed and not p.ref.passed)
    c = sum(1 for p in pairs if p.ref.passed and not p.treated.passed)
    return Inference(
        n_pairs=len(pairs), n_tasks=len(clusters),
        median_cost_diff_usd=med_diff(list(pairs)), median_cost_diff_ci=cluster_bootstrap(clusters, med_diff, seed=seed, **kw),
        median_cost_change_pct=med_pct(list(pairs)), median_cost_change_ci=cluster_bootstrap(clusters, med_pct, seed=seed + 1, **kw),
        p_wilcoxon=wilcoxon_signed_rank(diffs)[1],
        p_permutation=sign_flip_permutation_test(diffs, resamples=resamples, seed=seed + 2),
        success_diff_pts=succ(list(pairs)), success_diff_ci=cluster_bootstrap(clusters, succ, seed=seed + 3, **kw),
        only_treated_passed=b, only_reference_passed=c, p_mcnemar=mcnemar_exact(b, c),
        median_latency_diff_s=med_lat(list(pairs)), median_latency_diff_ci=cluster_bootstrap(clusters, med_lat, seed=seed + 4, **kw),
        p_latency_wilcoxon=wilcoxon_signed_rank([p.treated.wall_s - p.ref.wall_s for p in pairs])[1],
    )


# --- parity -----------------------------------------------------------------

@dataclass(frozen=True)
class ParityUnit:
    task_id: str
    none: float       # cost with no Trove
    plugin: float     # cost with the plugin only
    gateway: float    # cost with the gateway only


@dataclass(frozen=True)
class Parity:
    n_units: int
    gateway_saving_pct: float
    gateway_saving_ci: tuple[float, float]
    plugin_saving_pct: float
    plugin_saving_ci: tuple[float, float]
    plugin_share_of_gateway_pct: float | None   # None when not estimable
    plugin_share_ci: tuple[float, float] | None
    estimable: bool
    note: str


def parity(units: Sequence[ParityUnit], *, resamples: int, seed: int, confidence: float) -> Parity:
    """Plugin savings as a percentage of the gateway's savings, both against no Trove:
    (sum none - sum plugin) / (sum none - sum gateway). A ratio is only meaningful if the gateway
    saves something for sure, so when the gateway-saving CI includes 0 (or the ratio is undefined
    in many resamples) the share is reported as not estimable instead of as a wild number."""
    if not units:
        raise ValueError("parity needs at least one complete unit (a task run under none, plugin and gateway).")
    by_task: dict[str, list[ParityUnit]] = {}
    for u in units:
        by_task.setdefault(u.task_id, []).append(u)
    clusters = [by_task[k] for k in sorted(by_task)]
    kw = dict(resamples=resamples, confidence=confidence)

    def totals(items: list[ParityUnit]) -> tuple[float, float, float]:
        return sum(u.none for u in items), sum(u.plugin for u in items), sum(u.gateway for u in items)

    def gw_pct(items):
        n, _, g = totals(items)
        return (1 - g / n) * 100 if n > 0 else math.nan

    def pl_pct(items):
        n, p, _ = totals(items)
        return (1 - p / n) * 100 if n > 0 else math.nan

    def share(items):
        n, p, g = totals(items)
        return (n - p) / (n - g) * 100 if n - g > 0 else math.nan

    all_ = list(units)
    g_ci = cluster_bootstrap(clusters, gw_pct, seed=seed, **kw)
    p_ci = cluster_bootstrap(clusters, pl_pct, seed=seed + 1, **kw)
    g_pt, p_pt = gw_pct(all_), pl_pct(all_)
    if math.isnan(g_ci[0]) or g_ci[0] <= 0:
        return Parity(len(units), g_pt, g_ci, p_pt, p_ci, None, None, False,
                      "Not estimable: the gateway's own saving is not distinguishable from zero here, so a ratio to it would be noise.")
    s_ci = cluster_bootstrap(clusters, share, seed=seed + 2, **kw)
    if math.isnan(s_ci[0]):
        return Parity(len(units), g_pt, g_ci, p_pt, p_ci, None, None, False,
                      "Not estimable: the ratio was undefined in too many resamples.")
    return Parity(len(units), g_pt, g_ci, p_pt, p_ci, share(all_), s_ci, True, "")
