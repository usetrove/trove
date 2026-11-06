import csv
import json

import pytest
import yaml

from trovebench.cli import main

STUDY = {
    "id": "parity-sim", "title": "Parity (simulated)", "hypothesis": "plugin saves about half of the gateway's saving",
    "primary_metric": "billed_cost_usd", "baseline_condition": "direct",
    "conditions": ["direct", "hooks", "all_safe", "hooks_gateway"], "tasks": "tasks/smoke", "repeats": 3, "order_seed": 4,
    "efforts": ["low", "high"], "analysis": {"bootstrap_resamples": 1000, "bootstrap_seed": 2},
    "ship_rule": {"min_pairs": 12},
    "parity": {"none": "direct", "plugin": "hooks", "gateway": "all_safe", "both": "hooks_gateway"},
}
EFFECTS = {
    "seed": 11, "base_cost_usd": 0.4, "noise_sd": 0.05, "effort_cost_scale": {"low": 0.6, "high": 1.5},
    "cost_multiplier": {"hooks": 0.85, "all_safe": 0.70, "hooks_gateway": 0.62}, "pass_rate": {"default": 0.9},
}
N_RUNS = 14 * 3 * 2 * 4   # tasks x repeats x efforts x conditions


@pytest.fixture
def env(tmp_path):
    tasks = tmp_path / "tasks"
    tasks.mkdir()
    for i in range(14):
        (tasks / f"t{i:02d}.yaml").write_text(yaml.safe_dump(dict(
            id=f"t{i:02d}", source="smoke", repo="r", base_commit="c", license="MIT", prompt="p", image="i",
            success_cmd="true", timeout_s=60)), encoding="utf-8")
    study = tmp_path / "parity-sim.yaml"
    study.write_text(yaml.safe_dump(STUDY), encoding="utf-8")
    effects = tmp_path / "effects.yaml"
    effects.write_text(yaml.safe_dump(EFFECTS), encoding="utf-8")
    return tmp_path, tasks, study, effects


def csv_rows(path):
    with open(path, encoding="utf-8", newline="") as f:
        return list(csv.DictReader(f))


def test_unregistered_study_cannot_run(env, capsys):
    tmp, tasks, study, effects = env
    assert main(["run", "--study", str(study), "--tasks", str(tasks), "--batch", str(tmp / "b"), "--simulate", str(effects)]) == 2
    assert "not pre-registered" in capsys.readouterr().err


def test_parity_study_end_to_end_simulated(env, capsys):
    tmp, tasks, study, effects = env
    batch = tmp / "b"
    args = ["--study", str(study), "--tasks", str(tasks), "--batch", str(batch)]
    assert main(["prereg", str(study)]) == 0
    assert main(["plan", *args]) == 0
    assert main(["run", *args, "--simulate", str(effects)]) == 0
    out = capsys.readouterr().out
    assert "SIMULATION" in out and f"Simulated {N_RUNS} run(s)" in out
    md = (batch / "report.md").read_text(encoding="utf-8")
    assert "Plugin as % of gateway" in md and "Pre-registration" in md and "SIMULATED" in md
    rows = csv_rows(batch / "parity.csv")
    pooled = next(r for r in rows if r["effort"] == "all")
    assert pooled["estimable"] == "1" and abs(float(pooled["plugin_share_of_gateway_pct"]) - 50) < 4     # 15 of 30 points
    assert float(pooled["share_ci_lo"]) < float(pooled["plugin_share_of_gateway_pct"]) < float(pooled["share_ci_hi"])
    assert {r["effort"] for r in rows} == {"all", "low", "high"}
    assert (batch / "charts" / "cost_change.png").exists() and (batch / "study.lock.json").exists()
    assert len(csv_rows(batch / "runs.csv")) == N_RUNS
    assert {r["effort"] for r in csv_rows(batch / "runs.csv")} == {"low", "high"}

    # resume is a no-op, the report can be rebuilt from the folder alone, and the rebuild checks the lock
    assert main(["run", *args, "--simulate", str(effects)]) == 0
    assert f"Simulated 0 run(s), {N_RUNS} already done" in capsys.readouterr().out
    assert main(["report", "--batch", str(batch)]) == 0
    cfg = batch / "config.used.yaml"
    cfg.write_text(cfg.read_text(encoding="utf-8").replace("repeats: 3", "repeats: 9"), encoding="utf-8")
    assert main(["report", "--batch", str(batch)]) == 2
    assert "no longer matches" in capsys.readouterr().err


def test_plan_is_identical_across_invocations(env):
    tmp, tasks, study, effects = env
    main(["prereg", str(study)])
    plans = []
    for name in ("a", "b"):
        batch = tmp / name
        assert main(["run", "--study", str(study), "--tasks", str(tasks), "--batch", str(batch), "--simulate", str(effects)]) == 0
        plans.append(json.loads((batch / "plan.json").read_text(encoding="utf-8")))
    assert plans[0] == plans[1]
    assert (tmp / "a" / "ledger.jsonl").read_text() == (tmp / "b" / "ledger.jsonl").read_text()


def test_edited_study_is_refused(env, capsys):
    tmp, tasks, study, effects = env
    assert main(["prereg", str(study)]) == 0
    study.write_text(study.read_text(encoding="utf-8").replace("repeats: 3", "repeats: 4"), encoding="utf-8")
    assert main(["plan", "--study", str(study), "--tasks", str(tasks)]) == 2
    assert "changed since" in capsys.readouterr().err


def test_study_rejects_overrides(env, capsys):
    tmp, tasks, study, effects = env
    main(["prereg", str(study)])
    assert main(["plan", "--study", str(study), "--tasks", str(tasks), "--repeats", "1"]) == 2
    assert "re-register" in capsys.readouterr().err
