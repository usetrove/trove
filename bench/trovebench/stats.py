"""Paired comparison, bootstrap confidence intervals and the ship rule.

Everything here is a pure function of its inputs plus an explicit RNG seed, so
the same runs always give byte-identical numbers.
"""

from __future__ import annotations

import dataclasses
import random
import statistics
from dataclasses import dataclass
from typing import Callable, Sequence

from .records import OK, RunRecord

SHIP = "ship"
DONT_SHIP = "dont_ship"
NEED_MORE_DATA = "need_more_data"
NOT_MEASURABLE = "not_measurable_with_agent"


@dataclass(frozen=True)
class ShipRule:
    reference: str = "passthrough"
    min_cost_reduction_pct: float = 5.0
    success_margin_pts: float = 5.0
    min_pairs: int = 30
    confidence: float = 0.95
    bootstrap_resamples: int = 10000
    bootstrap_seed: int = 20261029

    @staticmethod
    def from_dict(d: dict) -> "ShipRule":
        rule = ShipRule(**d)
        if not 0.5 < rule.confidence < 1:
            raise ValueError("ship_rule.confidence must be between 0.5 and 1, for example 0.95.")
        if rule.bootstrap_resamples < 100:
            raise ValueError("ship_rule.bootstrap_resamples must be at least 100.")
        if rule.min_pairs < 2:
            raise ValueError("ship_rule.min_pairs must be at least 2.")
        if rule.min_cost_reduction_pct < 0 or rule.success_margin_pts < 0:
            raise ValueError("ship_rule thresholds must not be negative.")
        return rule


def bootstrap_ci(
    n: int,
    stat: Callable[[list[int]], float],
    *,
    resamples: int,
    seed: int,
    confidence: float,
) -> tuple[float, float]:
    """Percentile bootstrap CI of stat(indices) over n items resampled with replacement."""
    if n < 1:
        raise ValueError("bootstrap needs at least one item.")
    rng = random.Random(seed)
    draws = []
    for _ in range(resamples):
        idx = [int(rng.random() * n) for _ in range(n)]
        draws.append(stat(idx))
    draws.sort()
    alpha = (1 - confidence) / 2
    lo = draws[int(alpha * resamples)]
    hi = draws[min(resamples - 1, int((1 - alpha) * resamples))]
    return lo, hi


@dataclass(frozen=True)
class Pair:
    task_id: str
    seed: int
    ref: RunRecord
    treated: RunRecord

    @property
    def effort(self) -> str:
        return self.treated.effort


COST_BASES = ("recorded", "agent")


def rebase_cost(records: Sequence[RunRecord], basis: str) -> list[RunRecord]:
    """Put every record on one cost source. "recorded" keeps each run's own (gateway-measured for gateway
    conditions, agent-usage for the rest); "agent" uses the agent-reported cost for all runs, which is the
    only basis that compares conditions with and without a gateway like for like."""
    if basis not in COST_BASES:
        raise ValueError(f"cost basis must be one of {', '.join(COST_BASES)}.")
    if basis == "recorded":
        return list(records)
    return [dataclasses.replace(r, cost_usd=r.agent_cost_usd, cost_source="agent-usage") for r in records]


def usable(r: RunRecord) -> bool:
    return r.status == OK and r.cost_usd is not None


def make_pairs(records: Sequence[RunRecord], reference: str, treated: str, effort: str | None = None) -> list[Pair]:
    """Pair runs that share (task, seed, effort). A pair needs both runs gradeable with a known cost.
    effort=None pools every effort level; a level name keeps only that level."""
    ref = {(r.task_id, r.seed, r.effort): r for r in records if r.condition == reference and usable(r)}
    mine = (r for r in records if r.condition == treated and usable(r) and (effort is None or r.effort == effort))
    out = []
    for r in sorted(mine, key=lambda r: (r.task_id, r.seed, r.effort)):
        other = ref.get((r.task_id, r.seed, r.effort))
        if other is not None:
            out.append(Pair(r.task_id, r.seed, other, r))
    return out


@dataclass(frozen=True)
class Comparison:
    treated: str
    reference: str
    n_pairs: int
    cost_change_pct: float          # mean treated cost / mean reference cost - 1, in percent
    cost_ci: tuple[float, float]
    success_diff_pts: float         # treated success rate - reference success rate, in points
    success_ci: tuple[float, float]
    verdict: str
    reason: str


def compare(pairs: Sequence[Pair], treated: str, rule: ShipRule) -> Comparison:
    n = len(pairs)
    if n == 0:
        return Comparison(treated, rule.reference, 0, 0.0, (0.0, 0.0), 0.0, (0.0, 0.0), NEED_MORE_DATA,
                          "No paired runs yet. Run more tasks under both conditions.")
    ref_cost = [p.ref.cost_usd for p in pairs]
    tr_cost = [p.treated.cost_usd for p in pairs]
    ref_ok = [1.0 if p.ref.passed else 0.0 for p in pairs]
    tr_ok = [1.0 if p.treated.passed else 0.0 for p in pairs]

    def cost_pct(idx: list[int]) -> float:
        r = sum(ref_cost[i] for i in idx)
        t = sum(tr_cost[i] for i in idx)
        return (t / r - 1.0) * 100.0 if r > 0 else 0.0

    def succ_pts(idx: list[int]) -> float:
        return (sum(tr_ok[i] for i in idx) - sum(ref_ok[i] for i in idx)) / len(idx) * 100.0

    if sum(ref_cost) <= 0:
        raise ValueError(f"Reference condition '{rule.reference}' has zero total cost, so a percent change is meaningless. Check the cost data.")

    everyone = list(range(n))
    kw = dict(resamples=rule.bootstrap_resamples, confidence=rule.confidence)
    cost_ci = bootstrap_ci(n, cost_pct, seed=rule.bootstrap_seed, **kw)
    succ_ci = bootstrap_ci(n, succ_pts, seed=rule.bootstrap_seed + 1, **kw)
    cost_est, succ_est = cost_pct(everyone), succ_pts(everyone)

    x, m = rule.min_cost_reduction_pct, rule.success_margin_pts
    if n < rule.min_pairs:
        verdict, reason = NEED_MORE_DATA, f"Only {n} paired runs; the rule needs at least {rule.min_pairs}."
    elif cost_ci[1] <= -x and succ_ci[0] >= -m:
        verdict = SHIP
        reason = (f"Cost fell by at least {x:g}% (CI upper bound {cost_ci[1]:+.1f}%) and success did not "
                  f"drop by more than {m:g} points (CI lower bound {succ_ci[0]:+.1f}).")
    elif cost_ci[0] > -x:
        verdict = DONT_SHIP
        reason = f"Even the best case saves less than {x:g}% (CI lower bound {cost_ci[0]:+.1f}%)."
    elif succ_ci[1] < -m:
        verdict = DONT_SHIP
        reason = f"Success dropped by more than {m:g} points even in the best case (CI upper bound {succ_ci[1]:+.1f})."
    else:
        verdict = NEED_MORE_DATA
        reason = "The confidence intervals still straddle a threshold; more paired runs are needed."
    return Comparison(treated, rule.reference, n, cost_est, cost_ci, succ_est, succ_ci, verdict, reason)


@dataclass(frozen=True)
class ConditionSummary:
    condition: str
    n_runs: int
    n_excluded: int                 # infra failures and runs with unknown cost
    mean_cost: float
    median_cost: float
    success_rate: float             # fraction 0..1
    success_ci: tuple[float, float]
    mean_turns: float
    mean_retries: float


def summarize(records: Sequence[RunRecord], condition: str, rule: ShipRule) -> ConditionSummary:
    mine = [r for r in records if r.condition == condition]
    good = [r for r in mine if usable(r)]
    if not good:
        return ConditionSummary(condition, 0, len(mine), 0.0, 0.0, 0.0, (0.0, 0.0), 0.0, 0.0)
    ok = [1.0 if r.passed else 0.0 for r in good]
    ci = bootstrap_ci(
        len(good), lambda idx: sum(ok[i] for i in idx) / len(idx),
        resamples=rule.bootstrap_resamples, seed=rule.bootstrap_seed + 2, confidence=rule.confidence,
    )
    costs = [r.cost_usd for r in good]
    return ConditionSummary(
        condition=condition,
        n_runs=len(good),
        n_excluded=len(mine) - len(good),
        mean_cost=statistics.fmean(costs),
        median_cost=statistics.median(costs),
        success_rate=sum(ok) / len(ok),
        success_ci=ci,
        mean_turns=statistics.fmean(r.turns for r in good),
        mean_retries=statistics.fmean(r.retries for r in good),
    )
