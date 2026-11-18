"""Autopilot thresholds from benchmark pairs (plan item 10.7)."""

from __future__ import annotations

import csv
import json
import random
import tomllib

import pytest
import yaml

from trovebench.study import load_study
from trovebench.thresholds import ThresholdsError, generate, tier_of

STUDY = dict(id="comp-x", title="t", hypothesis="h", primary_metric="billed_cost_usd", baseline_condition="direct",
             conditions=["direct", "alone_noise_normalizer", "alone_verbosity_steer"], tasks="tasks/x", repeats=1,
             order_seed=1, ship_rule={"success_margin_pts": 5, "min_cost_reduction_pct": 5, "min_pairs": 30})


def results(tmp_path, pairs, *, simulated=False, tamper=False):
    d = tmp_path / "res"
    d.mkdir(parents=True)
    (d / "config.used.yaml").write_text(yaml.safe_dump(STUDY), encoding="utf-8")
    sha = load_study(d / "config.used.yaml").sha256
    (d / "study.lock.json").write_text(json.dumps({"study_sha256": "0" * 64 if tamper else sha}), encoding="utf-8")
    (d / "versions.json").write_text(json.dumps({"simulated": simulated}), encoding="utf-8")
    with open(d / "pairs.csv", "w", encoding="utf-8", newline="") as f:
        w = csv.writer(f)
        w.writerow(["condition", "reference", "task_id", "seed", "reference_cost_usd", "condition_cost_usd",
                    "reference_passed", "condition_passed"])
        for i, (cond, ref, c) in enumerate(pairs):
            w.writerow([cond, "direct", f"t{i}", 0, ref, c, True, True])
    return d


def test_tiers():
    assert tier_of("alone_hook_repeat_read") == "T1" and tier_of("passthrough_plugin") == "T1"
    assert tier_of("alone_verbosity_steer") == "T2"
    assert tier_of("alone_model_router") == "T3" and tier_of("trove_full") == "T3" and tier_of("task_cascade") == "T3"
    assert tier_of("direct") is None and tier_of("passthrough") is None


def test_sessions_per_arm_follow_the_benchmark_spread(tmp_path):
    rng = random.Random(3)
    pairs = [("alone_noise_normalizer", 1.0, 1.0 * (0.9 + rng.gauss(0, 0.10))) for _ in range(40)]
    pairs += [("alone_verbosity_steer", 1.0, 0.9) for _ in range(10)]          # too few: T2 keeps placeholders
    out = tomllib.loads(generate([results(tmp_path, pairs)]))
    t1 = out["tiers"]["T1"]
    sd = t1["cost_change_sd"]
    want = max(10, min(200, -(-2 * (1.959964 + 0.841621) ** 2 * sd * sd // 0.0025)))
    assert t1["probation_min_sessions_per_arm"] == int(want) and t1["pairs"] == 40
    assert t1["test_fail_margin_pts"] == 5 and t1["source"].startswith("bench:comp-x@")
    assert "T2" not in out["tiers"] and "T3" not in out["tiers"]


def test_refuses_simulated_and_tampered_results(tmp_path):
    with pytest.raises(ThresholdsError, match="simulated"):
        generate([results(tmp_path / "a", [], simulated=True)])
    with pytest.raises(ThresholdsError, match="lock"):
        generate([results(tmp_path / "b", [], tamper=True)])


def test_no_results_means_placeholders_stay():
    out = tomllib.loads(generate([]))
    assert out == {"version": 1}
