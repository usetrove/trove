import re
import tomllib
from pathlib import Path

import pytest
import yaml

from trovebench.cli import main
from trovebench.defaults_gen import (CONTRADICTED, GO_MIN_COST_REDUCTION_PCT, GO_MIN_PAIRS, GO_SUCCESS_MARGIN_PTS, INCONCLUSIVE,
                                     SUPPORTED, DefaultsError, evaluate, generate)

REPO = Path(__file__).resolve().parents[2]
SEED = (REPO / "bench" / "defaults_seed.toml").read_text(encoding="utf-8")

STUDY = {
    "id": "opt-sim", "title": "Optimizers (simulated)", "hypothesis": "the hooks save cost without hurting success",
    "primary_metric": "billed_cost_usd", "baseline_condition": "direct",
    "conditions": ["direct", "alone_noise_normalizer", "alone_duplicate_result_collapser", "hooks", "passthrough"], "tasks": "unused", "repeats": 3, "order_seed": 4,
    "efforts": ["low", "high"], "analysis": {"bootstrap_resamples": 1000, "bootstrap_seed": 2},
    "ship_rule": {"min_pairs": 30, "min_cost_reduction_pct": 5, "success_margin_pts": 5},
    "recommends": {
        "alone_noise_normalizer": {"rule_id": "repeat-reads-proven", "kind": "optimizer", "name": "hook_repeat_read"},
        "alone_duplicate_result_collapser": {"rule_id": "collapse-duplicates", "kind": "optimizer", "name": "duplicate_result_collapser",
                  "reason": "Sessions here repeat identical tool results about {repeat_reads} times (over {sessions} sessions)."},
        "hooks": {"rule_id": "x", "kind": "optimizer", "name": "hook_read_limit"},
        "passthrough": {"rule_id": "y", "kind": "optimizer", "name": "hook_quiet_commands"},
    },
}
EFFECTS = {"seed": 3, "base_cost_usd": 0.4, "noise_sd": 0.05, "pass_rate": {"default": 0.9, "hooks": 0.3},
           "cost_multiplier": {"alone_noise_normalizer": 0.8, "alone_duplicate_result_collapser": 0.85, "hooks": 0.8, "passthrough": 0.95}}


def run_study(tmp_path, study=STUDY, effects=EFFECTS, name="opt-sim"):
    tasks = tmp_path / "tasks"
    tasks.mkdir(exist_ok=True)
    for i in range(14):
        (tasks / f"t{i:02d}.yaml").write_text(yaml.safe_dump(dict(
            id=f"t{i:02d}", source="smoke", repo="r", base_commit="c", license="MIT", prompt="p", image="i",
            success_cmd="true", timeout_s=60)), encoding="utf-8")
    sp = tmp_path / f"{name}.yaml"
    sp.write_text(yaml.safe_dump(study), encoding="utf-8")
    ep = tmp_path / "effects.yaml"
    ep.write_text(yaml.safe_dump(effects), encoding="utf-8")
    batch = tmp_path / "2026-11-06" / name
    assert main(["prereg", str(sp)]) == 0
    assert main(["run", "--study", str(sp), "--tasks", str(tasks), "--batch", str(batch), "--simulate", str(ep)]) == 0
    return batch


@pytest.fixture(scope="module")
def batch(tmp_path_factory):
    return run_study(tmp_path_factory.mktemp("sim"))


def test_no_results_returns_the_seed_unchanged():
    text, priors, findings = generate(SEED, [])
    assert findings == []
    seed, got = tomllib.loads(SEED), tomllib.loads(text)
    assert got["rule"] == seed["rule"] and got["release"] == seed["release"] and got["min_sessions"] == seed["min_sessions"]
    assert tomllib.loads(priors) == {"version": 1}
    assert text.startswith("# GENERATED") and "Do not edit by hand" in text


def test_simulated_results_are_refused(batch):
    with pytest.raises(DefaultsError, match="simulated"):
        generate(SEED, [batch])


def test_findings_classify_supported_contradicted_inconclusive(batch):
    got = {f.name: f for f in evaluate(batch, allow_simulated=True)}
    assert got["hook_repeat_read"].status == SUPPORTED and got["duplicate_result_collapser"].status == SUPPORTED
    assert got["hook_read_limit"].status == CONTRADICTED
    assert got["hook_quiet_commands"].status == INCONCLUSIVE


def test_generation_upgrades_adds_and_removes(batch):
    text, priors, _ = generate(SEED, [batch], allow_simulated=True)
    d = tomllib.loads(text)
    rules = {r["name"]: r for r in d["rule"]}
    assert rules["hook_repeat_read"]["evidence"] == "benchmark" and rules["hook_repeat_read"]["source"].startswith("bench:opt-sim@")
    assert "hook_read_limit" not in rules                               # contradicted: removed
    assert rules["hook_quiet_commands"]["evidence"] == "none"           # inconclusive: stays a rule of thumb
    new = rules["duplicate_result_collapser"]
    assert new["evidence"] == "benchmark" and new["action"] == "shadow" and "{repeat_reads}" in new["reason"]
    assert d["release"] == "bench-2026-11-06"
    assert sum(r["evidence"] == "benchmark" for r in d["rule"]) == 2
    p = tomllib.loads(priors)["optimizers"]
    assert set(p) == {"hook_repeat_read", "duplicate_result_collapser"}
    for v in p.values():                                                 # consistent with Go Prior.Supports
        assert v["pairs"] >= GO_MIN_PAIRS and v["cost_ci_high"] <= -GO_MIN_COST_REDUCTION_PCT / 100
        assert v["success_ci_low"] >= -GO_SUCCESS_MARGIN_PTS / 100 and v["cost_ci_low"] <= v["cost_change"] <= v["cost_ci_high"]
    assert p["hook_repeat_read"]["cost_change"] == pytest.approx(-0.2, abs=0.03)


def test_generation_is_deterministic(batch):
    assert generate(SEED, [batch], allow_simulated=True)[:2] == generate(SEED, [batch], allow_simulated=True)[:2]


def test_weak_registered_rule_is_never_enough(tmp_path):
    weak = {**STUDY, "ship_rule": {"min_pairs": 10, "min_cost_reduction_pct": 1, "success_margin_pts": 5}}
    b = run_study(tmp_path, study=weak)
    got = {f.name: f for f in evaluate(b, allow_simulated=True)}
    assert got["hook_repeat_read"].status == INCONCLUSIVE and "weaker" in got["hook_repeat_read"].why
    text, priors, _ = generate(SEED, [b], allow_simulated=True)
    assert all(r["evidence"] == "none" for r in tomllib.loads(text)["rule"]) and "optimizers" not in tomllib.loads(priors)


def test_edited_study_results_are_not_trusted(tmp_path):
    b = run_study(tmp_path)
    cfg = b / "config.used.yaml"
    cfg.write_text(cfg.read_text(encoding="utf-8").replace("repeats: 3", "repeats: 5"), encoding="utf-8")
    with pytest.raises(DefaultsError, match="no longer matches"):
        evaluate(b, allow_simulated=True)


def test_new_rule_without_a_reason_is_an_error(tmp_path):
    study = {**STUDY, "recommends": {"alone_duplicate_result_collapser": {"rule_id": "z", "kind": "optimizer", "name": "duplicate_result_collapser"}}}
    b = run_study(tmp_path, study=study)
    with pytest.raises(DefaultsError, match="reason"):
        generate(SEED, [b], allow_simulated=True)


def test_go_constants_match_the_python_mirror():
    src = (REPO / "internal" / "experiment" / "prior.go").read_text(encoding="utf-8")
    grab = lambda name: float(re.search(rf"{name}\s*=\s*([0-9.]+)", src).group(1))   # noqa: E731
    assert grab("ShipMinCostReduction") * 100 == GO_MIN_COST_REDUCTION_PCT
    assert grab("ShipSuccessMargin") * 100 == GO_SUCCESS_MARGIN_PTS
    assert grab("ShipMinPairs") == GO_MIN_PAIRS


def test_cli_refuses_simulated_results(batch, tmp_path, capsys):
    out = tmp_path / "d.toml"
    assert main(["defaults", "--results", str(batch), "--out", str(out), "--priors-out", str(tmp_path / "p.toml")]) == 2
    assert "simulated" in capsys.readouterr().err and not out.exists()
