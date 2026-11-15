"""How many task pairs would the ship rule need? Estimated from a finished batch's own ledger. Free; calls nothing.

    python bench/scripts/power_check.py bench/results/2026-11-15/swebench-gateway-2 [--treated all_safe] [--reference passthrough]

It uses the spread of per-task cost and the rate at which pass/fail differs between the two conditions, then asks:
for a TRUE saving of S%, how many pairs give an 80% chance that the whole 95% interval clears the ship rule?
This is an approximation (normal theory, one-sided 97.5% bound, 80% power, true success difference of 0). It is
labelled ESTIMATED. A small pilot gives a rough spread, so treat the answer as +/- 30%. It does not change any verdict.
"""

import argparse
import json
import math
import statistics as st
import sys
from pathlib import Path

Z_BOUND, Z_POWER = 1.96, 0.84          # 97.5% one-sided bound, 80% power


def load(batch: Path, cond: str, ref: str):
    ledger = batch / "ledger.jsonl"
    if not ledger.exists():
        sys.exit(f"STOP: {ledger} not found.")
    latest = {}
    for line in ledger.read_text(encoding="utf-8").splitlines():
        if line.strip():
            r = json.loads(line)
            latest[r["run_id"]] = r
    by = {}
    for r in latest.values():
        c = r.get("agent_cost_usd")
        if r["status"] == "ok" and c is None:
            c = r.get("cost_usd")
        if r["status"] == "ok" and c is not None:
            by[(r["condition"], r["task_id"], r["seed"])] = (float(c), 1.0 if r["passed"] else 0.0)
    pairs = [(by[(ref, t, s)], v) for (c, t, s), v in by.items() if c == cond and (ref, t, s) in by]
    return pairs


def need(delta: float, sd: float) -> float:
    """Pairs so that (Z_BOUND + Z_POWER) * sd / sqrt(n) <= delta."""
    return math.inf if delta <= 0 else ((Z_BOUND + Z_POWER) * sd / delta) ** 2


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("batch", type=Path)
    ap.add_argument("--treated", default="all_safe")
    ap.add_argument("--reference", default="passthrough")
    ap.add_argument("--margin", type=float, default=5.0, help="success margin in points")
    ap.add_argument("--min-saving", type=float, default=5.0, help="ship rule's minimum cost reduction in percent")
    a = ap.parse_args()
    pairs = load(a.batch, a.treated, a.reference)
    n = len(pairs)
    if n < 10:
        sys.exit(f"only {n} usable pairs of {a.treated} vs {a.reference} in {a.batch}; too few to estimate a spread.")
    ref_c = [p[0][0] for p in pairs]
    tr_c = [p[1][0] for p in pairs]
    mean_ref = st.fmean(ref_c)
    obs = st.fmean(tr_c) / mean_ref - 1
    disc = sum(p[0][1] != p[1][1] for p in pairs) / n
    sd_succ = st.pstdev([100 * (p[1][1] - p[0][1]) for p in pairs])
    print(f"{a.treated} vs {a.reference}: {n} pairs from {a.batch}")
    print(f"  observed cost change {100 * obs:+.1f}% (measured in this batch, agent-reported usage at list price = ESTIMATED cost)")
    print(f"  pass/fail differs in {100 * disc:.0f}% of pairs; success-difference spread {sd_succ:.0f} points per pair")
    n_succ = need(a.margin, sd_succ)
    print(f"  success rule alone (interval above -{a.margin:g} points, optimizer truly costs no accuracy): about {math.ceil(n_succ)} pairs")
    print("  cost rule (whole interval at or below -%g%%), by TRUE saving (ESTIMATED):" % a.min_saving)
    worst = n_succ
    for s in (10, 15, 20, 30):
        ratio = 1 - s / 100
        # per-pair residual of the ratio statistic at the hypothesised ratio, in percent of mean reference cost
        resid = [100 * (t - ratio * r) / mean_ref for r, t in zip(ref_c, tr_c)]
        sd_cost = st.pstdev(resid)
        n_cost = need(s - a.min_saving, sd_cost)
        print(f"    true saving {s:>2}%: cost rule needs about {math.ceil(n_cost):>5} pairs; both rules about {math.ceil(max(n_cost, n_succ)):>5}")
        worst = max(worst, n_cost)
    print("  a true saving of %g%% or less can never pass, whatever the sample size." % a.min_saving)
    print("  If the optimizer really saves nothing, no study will ship it; that is a result, not a failure.")


if __name__ == "__main__":
    main()
