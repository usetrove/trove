import dataclasses

import pytest
import yaml
from helpers import rec

from trovebench.plan import run_id
from trovebench.prereg import verify_lock, write_lock
from trovebench.studyreport import (CONFIRMED, OVERSTATED, UNSUPPORTED, WITHIN, analyze_study, judge_claim,
                                    write_study_report)
from trovebench.study import load_study

STUDY = dict(
    id="parity-t", title="Parity test", hypothesis="plugin saves about half of what the gateway saves",
    primary_metric="billed_cost_usd", baseline_condition="direct",
    conditions=["direct", "hooks", "all_safe", "hooks_gateway"], tasks="tasks/smoke", repeats=2, order_seed=1,
    efforts=["low", "high"], analysis={"bootstrap_resamples": 1000, "bootstrap_seed": 5},
    ship_rule={"min_pairs": 10}, parity={"none": "direct", "plugin": "hooks", "gateway": "all_safe", "both": "hooks_gateway"},
    claims=[{"condition": "hooks", "claimed_cost_reduction_pct": 15, "source_url": "https://example.com/claim", "retrieved_at": "2026-11-06"}],
)
COST = {"direct": 1.0, "hooks": 0.85, "all_safe": 0.70, "hooks_gateway": 0.60}


def make_records(n_tasks=20, passed=lambda c, t: True, cost=lambda c, t, e: COST[c] * (1 + 0.02 * (t % 3))):
    out = []
    for t in range(n_tasks):
        for seed in range(2):
            for effort in ("low", "high"):
                for c in COST:
                    r = rec(f"t{t}", seed, c, cost(c, t, effort), passed(c, t))
                    out.append(dataclasses.replace(r, run_id=run_id(f"t{t}", c, seed, effort), effort=effort,
                                                   agent_cost_usd=cost(c, t, effort)))
    return out


def study(tmp_path, **over):
    p = tmp_path / "s.yaml"
    p.write_text(yaml.safe_dump({**STUDY, **over}), encoding="utf-8")
    return p, load_study(p)


def test_parity_study_end_to_end(tmp_path):
    p, s = study(tmp_path)
    write_lock(p)
    s, lock = verify_lock(p)
    out = tmp_path / "out"
    an = write_study_report(out, s, lock, make_records(), {"agent": "fake", "model": "m", "fake_agent": True}, p.read_text())
    for f in ("report.md", "runs.csv", "pairs.csv", "comparisons.csv", "parity.csv", "claims.csv", "versions.json",
              "config.used.yaml", "study.lock.json"):
        assert (out / f).exists(), f
    assert (out / "charts" / "cost_change.png").exists()
    pooled = next(p for p in an.parity if p.stratum == "all").parity
    assert pooled.estimable and pooled.plugin_share_of_gateway_pct == pytest.approx(15 / 30 * 100, abs=3)
    lo, hi = pooled.plugin_share_ci
    assert lo < 50 < hi
    assert {p.stratum for p in an.parity} == {"all", "low", "high"}
    md = (out / "report.md").read_text(encoding="utf-8")
    assert "Plugin as % of gateway" in md and "fake agent" in md and "estimated" in md.lower()
    assert "## By reasoning effort" in md and "Claimed versus measured" in md


def test_decisions_and_holm(tmp_path):
    _, s = study(tmp_path)
    an = analyze_study(s, make_records())
    rows = {r.condition: r for r in an.rows if r.stratum == "all"}
    assert rows["hooks"].verdict == "ship" and rows["all_safe"].verdict == "ship"
    assert rows["hooks"].p_adjusted >= rows["hooks"].p_primary
    assert rows["hooks"].inference.median_cost_change_pct == pytest.approx(-15, abs=1)
    assert an.incomplete_units == 0 and an.total_units == 20 * 2 * 2


def test_success_loss_blocks_ship(tmp_path):
    _, s = study(tmp_path)
    an = analyze_study(s, make_records(passed=lambda c, t: not (c == "hooks" and t % 2 == 0)))
    row = next(r for r in an.rows if r.condition == "hooks" and r.stratum == "all")
    assert row.verdict == "dont_ship" and row.inference.success_diff_pts < -20


def test_no_effect_is_not_shipped_and_cost_basis_is_agent(tmp_path):
    _, s = study(tmp_path)
    recs = make_records(cost=lambda c, t, e: 1.0)
    # the gateway-measured cost differs, but the study compares on the agent-reported cost
    recs = [dataclasses.replace(r, cost_usd=0.5) if r.condition == "all_safe" else r for r in recs]
    an = analyze_study(s, recs)
    row = next(r for r in an.rows if r.condition == "all_safe" and r.stratum == "all")
    assert row.inference.median_cost_change_pct == 0 and row.verdict != "ship"


def test_incomplete_units_are_counted_and_excluded(tmp_path):
    _, s = study(tmp_path)
    recs = [r for r in make_records() if not (r.condition == "hooks" and r.task_id == "t0")]
    an = analyze_study(s, recs)
    assert an.incomplete_units == 4
    row = next(r for r in an.rows if r.condition == "hooks" and r.stratum == "all")
    assert row.inference.n_tasks == 19


def test_effort_heterogeneity_detects_effort_dependent_saving(tmp_path):
    _, s = study(tmp_path)
    an = analyze_study(s, make_records(cost=lambda c, t, e: 1.0 if c == "direct" else (0.6 if e == "low" else 0.95)))
    het = next(r.heterogeneity_p for r in an.rows if r.condition == "hooks" and r.stratum == "all")
    assert het is not None and het < 0.05
    flat = analyze_study(s, make_records())
    assert next(r.heterogeneity_p for r in flat.rows if r.condition == "hooks" and r.stratum == "all") > 0.05


@pytest.mark.parametrize("claimed,ci,expected", [
    (15, (-18.0, -12.0), WITHIN), (10, (-18.0, -12.0), CONFIRMED), (30, (-18.0, -12.0), OVERSTATED),
    (30, (-5.0, 4.0), UNSUPPORTED), (30, None, UNSUPPORTED), (30, (float("nan"), float("nan")), UNSUPPORTED),
])
def test_judge_claim(claimed, ci, expected):
    assert judge_claim(claimed, ci) == expected


@pytest.mark.parametrize("over,msg", [
    ({"claims": [{"condition": "hooks", "claimed_cost_reduction_pct": 15}]}, "claim"),
    ({"claims": [{"condition": "direct", "claimed_cost_reduction_pct": 15, "source_url": "u", "retrieved_at": "d"}]}, "non-baseline"),
    ({"parity": {"none": "direct", "plugin": "hooks"}}, "parity"),
    ({"parity": {"none": "direct", "plugin": "hooks", "gateway": "nope"}}, "not in"),
    ({"cost_basis": "vibes"}, "cost_basis"),
])
def test_claim_and_parity_validation(tmp_path, over, msg):
    from trovebench.study import StudyError

    with pytest.raises(StudyError, match=msg):
        study(tmp_path, **over)
