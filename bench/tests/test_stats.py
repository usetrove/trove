import pytest
from helpers import paired_runs, rec

from trovebench.stats import (
    DONT_SHIP, NEED_MORE_DATA, SHIP, ShipRule, bootstrap_ci, compare, make_pairs, summarize,
)

RULE = ShipRule(bootstrap_resamples=2000)


def verdict(records, rule=RULE):
    pairs = make_pairs(records, rule.reference, "all_safe")
    return compare(pairs, "all_safe", rule)


@pytest.mark.parametrize(
    "name, runs, want",
    [
        ("clear saving, no quality loss", paired_runs(40, 1.0, 0.8), SHIP),
        ("saving but too few pairs", paired_runs(10, 1.0, 0.8), NEED_MORE_DATA),
        ("no saving at all", paired_runs(40, 1.0, 1.0), DONT_SHIP),
        ("saving but success collapses", paired_runs(40, 1.0, 0.8, tr_pass=lambda i: False), DONT_SHIP),
        ("costs more", paired_runs(40, 1.0, 1.3), DONT_SHIP),
        ("noisy saving straddles threshold",
         paired_runs(40, 1.0, lambda i: 0.5 if i % 2 else 1.2), NEED_MORE_DATA),
    ],
)
def test_ship_rule_table(name, runs, want):
    assert verdict(runs).verdict == want, name


def test_ship_requires_both_conditions():
    # 20% cheaper, but the success drop is 10 points (4 of 40 pairs flip), beyond the 5-point margin.
    runs = paired_runs(40, 1.0, 0.8, tr_pass=lambda i: i % 10 != 0)
    c = verdict(runs)
    assert c.success_diff_pts == pytest.approx(-10.0)
    assert c.verdict != SHIP


def test_cost_change_estimate():
    c = verdict(paired_runs(40, 1.0, 0.8))
    assert c.cost_change_pct == pytest.approx(-20.0)
    assert c.n_pairs == 40


def test_deterministic():
    runs = paired_runs(40, 1.0, lambda i: 0.5 if i % 2 else 1.2)
    assert verdict(runs) == verdict(runs)


def test_pairing_drops_unmatched_and_excluded():
    runs = paired_runs(6, 1.0, 0.8)
    runs.append(rec("lonely", 0, "all_safe", 0.5))                      # no reference partner
    runs.append(rec("task0", 5, "passthrough", 1.0, status="infra"))    # excluded
    runs.append(rec("task0", 5, "all_safe", 0.5))                       # partner excluded
    runs.append(rec("task1", 7, "passthrough", None))                   # unknown cost
    runs.append(rec("task1", 7, "all_safe", 0.5))
    assert len(make_pairs(runs, "passthrough", "all_safe")) == 6


def test_no_pairs_is_need_more_data():
    assert compare([], "all_safe", RULE).verdict == NEED_MORE_DATA


def test_zero_reference_cost_is_an_error():
    with pytest.raises(ValueError, match="zero total cost"):
        verdict(paired_runs(4, 0.0, 0.0))


def test_bootstrap_constant_and_bounds():
    vals = [3.0] * 10
    lo, hi = bootstrap_ci(10, lambda idx: sum(vals[i] for i in idx) / len(idx),
                          resamples=500, seed=1, confidence=0.95)
    assert lo == hi == 3.0
    with pytest.raises(ValueError):
        bootstrap_ci(0, lambda idx: 0.0, resamples=10, seed=1, confidence=0.9)


def test_summarize():
    runs = paired_runs(10, 1.0, 0.5, tr_pass=lambda i: i < 5) + [rec("x", 0, "all_safe", None, status="infra")]
    s = summarize(runs, "all_safe", RULE)
    assert s.n_runs == 10 and s.n_excluded == 1
    assert s.mean_cost == pytest.approx(0.5) and s.median_cost == pytest.approx(0.5)
    assert s.success_rate == pytest.approx(0.5)
    assert s.success_ci[0] < 0.5 < s.success_ci[1]


def test_summarize_empty():
    assert summarize([], "direct", RULE).n_runs == 0


@pytest.mark.parametrize(
    "bad", [{"confidence": 1.2}, {"bootstrap_resamples": 5}, {"min_pairs": 1}, {"min_cost_reduction_pct": -1}]
)
def test_rule_validation(bad):
    with pytest.raises(ValueError):
        ShipRule.from_dict(bad)
