"""Does the statistics code tell the truth? Planted-effect checks, with no model call.

For each planted effect (cost change, success change, or none) this builds many synthetic studies
with the simulator, runs the real compare() over them, and checks what the pipeline reports:

- coverage: the 95% cost interval should contain the planted cost change about 95% of the time;
- false ship: with no planted cost saving, the verdict must essentially never be "ship";
- detection: with a planted saving well above the 5% rule, "ship" should be reached once there are enough pairs;
- small samples: with fewer pairs than min_pairs the verdict must be need_more_data, never a number-based call.

Nothing here measures any agent. It tests the arithmetic and the rules, and says so in its output.
"""

from __future__ import annotations

import math
import random
from dataclasses import dataclass

from .plan import RunSpec
from .simulate import make_run_one
from .stats import NEED_MORE_DATA, ShipRule, compare, make_pairs


@dataclass
class Check:
    name: str
    trials: int
    passed: int
    detail: str
    min_rate: float = 1.0   # pass when at least this share of trials passed

    @property
    def ok(self) -> bool:
        return self.trials > 0 and self.passed / self.trials >= self.min_rate


def _study(n_tasks: int, seed: int, treated_mult: float, pass_rate: float = 0.9):
    """One synthetic study: n_tasks tasks run under 'passthrough' (reference) and 'treated'."""
    effects = {
        "seed": seed,
        "base_cost_usd": 0.30,
        "task_sd": 0.4,
        "noise_sd": 0.08,
        "cost_multiplier": {"passthrough": 1.0, "treated": treated_mult},
        "pass_rate": {"default": pass_rate},
    }
    run_one = make_run_one(effects)
    records = []
    for t in range(n_tasks):
        for cond in ("passthrough", "treated"):
            spec = RunSpec(run_id=f"task{t}__{cond}__s0", task_id=f"task{t}", condition=cond, seed=0)
            records.append(run_one(spec))
    return records


def _true_change_pct(treated_mult: float) -> float:
    return (treated_mult - 1.0) * 100.0


def coverage(treated_mult: float, trials: int, n_tasks: int = 40) -> Check:
    rule = ShipRule(reference="passthrough", bootstrap_resamples=2000)
    hits = 0
    for s in range(trials):
        pairs = make_pairs(_study(n_tasks, seed=1000 + s, treated_mult=treated_mult), "passthrough", "treated")
        c = compare(pairs, "treated", rule)
        target = _true_change_pct(treated_mult)
        if c.cost_ci[0] <= target <= c.cost_ci[1]:
            hits += 1
    return Check(
        name=f"95% cost interval covers the planted change ({_true_change_pct(treated_mult):+.0f}%)",
        trials=trials, passed=hits,
        detail=f"coverage {hits / trials:.1%}; a 95% interval should cover about 95%",
        min_rate=0.90,
    )


def false_ship(trials: int, n_tasks: int = 60) -> Check:
    """No planted cost change. Count how often the pipeline says 'ship' (a false saving)."""
    rule = ShipRule(reference="passthrough", bootstrap_resamples=2000)
    ships = 0
    for s in range(trials):
        pairs = make_pairs(_study(n_tasks, seed=5000 + s, treated_mult=1.0), "passthrough", "treated")
        if compare(pairs, "treated", rule).verdict == "ship":
            ships += 1
    return Check(
        name="no planted saving never reads as 'ship'",
        trials=trials, passed=trials - ships,
        detail=f"false 'ship' verdicts: {ships} of {trials}",
        min_rate=0.98,
    )


def detection(treated_mult: float, trials: int, n_tasks: int = 60) -> Check:
    """A planted saving well past the rule should reach 'ship' once there are enough pairs."""
    rule = ShipRule(reference="passthrough", bootstrap_resamples=2000)
    ships = 0
    for s in range(trials):
        pairs = make_pairs(_study(n_tasks, seed=9000 + s, treated_mult=treated_mult, pass_rate=0.9),
                           "passthrough", "treated")
        if compare(pairs, "treated", rule).verdict == "ship":
            ships += 1
    return Check(
        name=f"a planted saving of {_true_change_pct(treated_mult):+.0f}% reaches 'ship' with {n_tasks} tasks",
        trials=trials, passed=ships,
        detail=f"ship verdicts: {ships} of {trials}",
        min_rate=0.90,
    )


def small_sample(trials: int, n_tasks: int = 10) -> Check:
    """Fewer pairs than min_pairs: must say need_more_data, whatever the numbers look like."""
    rule = ShipRule(reference="passthrough", bootstrap_resamples=2000)
    ok = 0
    for s in range(trials):
        pairs = make_pairs(_study(n_tasks, seed=7000 + s, treated_mult=0.5), "passthrough", "treated")
        if compare(pairs, "treated", rule).verdict == NEED_MORE_DATA:
            ok += 1
    return Check(
        name=f"fewer than {rule.min_pairs} pairs reads need_more_data, even with a large planted saving",
        trials=trials, passed=ok, detail=f"need_more_data: {ok} of {trials}",
    )


def determinism() -> Check:
    rule = ShipRule(reference="passthrough", bootstrap_resamples=2000)
    a = compare(make_pairs(_study(40, 42, 0.8), "passthrough", "treated"), "treated", rule)
    b = compare(make_pairs(_study(40, 42, 0.8), "passthrough", "treated"), "treated", rule)
    same = (a.cost_ci == b.cost_ci) and (a.verdict == b.verdict) and (a.success_ci == b.success_ci)
    return Check(name="same data and seed give the same interval and verdict",
                 trials=1, passed=1 if same else 0, detail="identical" if same else "DIFFERENT")


def run_all(trials: int = 200) -> list[Check]:
    return [
        coverage(1.0, trials),
        coverage(0.8, trials),
        coverage(1.25, trials),
        false_ship(trials),
        detection(0.6, max(trials // 4, 20)),
        small_sample(max(trials // 4, 20)),
        determinism(),
    ]


def render(checks: list[Check]) -> str:
    lines = ["SIMULATED: planted effects, no model call. This checks the statistics, not any agent.", ""]
    lines.append("| check | result | detail | status |")
    lines.append("|---|---|---|---|")
    for c in checks:
        status = "PASS" if c.ok else "FAIL"
        lines.append(f"| {c.name} | {c.passed}/{c.trials} | {c.detail} | {status} |")
    return "\n".join(lines) + "\n"


if __name__ == "__main__":
    import sys

    checks = run_all(int(sys.argv[1]) if len(sys.argv) > 1 else 200)
    print(render(checks))
    sys.exit(0 if all(c.ok for c in checks) else 1)
