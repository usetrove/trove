"""Staged studies: one look per repeat, each at alpha / repeats, and the route_to field."""

from __future__ import annotations

import json
from types import SimpleNamespace

import pytest
import yaml

from helpers import rec
from trovebench.cli import UserError, _cmd_stage, _through
from trovebench.conditions import parse_conditions
from trovebench.ledger import Ledger
from trovebench.plan import build_plan
from trovebench.prereg import write_lock
from trovebench.study import StudyError, load_study
from trovebench.studyreport import ship_rule_for
from test_study import task

STUDY = dict(
    id="launch-x", title="Launch", hypothesis="Trove lowers cost without lowering success",
    primary_metric="billed_cost_usd", baseline_condition="passthrough",
    conditions=["passthrough", "passthrough_plugin", "trove_full"], tasks="tasks/smoke", repeats=2, order_seed=5,
    cost_basis="recorded", staged=True, route_to="claude-haiku-5-5",
)


def write(tmp_path, **over):
    p = tmp_path / "s.yaml"
    p.write_text(yaml.safe_dump({**STUDY, **over}), encoding="utf-8")
    return p


def test_staged_study_tests_each_look_at_alpha_over_repeats(tmp_path):
    s = load_study(write(tmp_path))
    assert s.staged and s.look_alpha == pytest.approx(0.025)
    assert ship_rule_for(s).confidence == pytest.approx(0.975)
    plain = load_study(write(tmp_path, staged=False))
    assert plain.look_alpha == pytest.approx(0.05)


@pytest.mark.parametrize("over,msg", [
    ({"repeats": 1}, "repeats of at least 2"),
    ({"route_to": ""}, "route_to"),
])
def test_invalid_staged_or_routed_study(tmp_path, over, msg):
    with pytest.raises(StudyError, match=msg):
        load_study(write(tmp_path, **over))


def test_study_route_to_reaches_the_conditions(tmp_path):
    s = load_study(write(tmp_path))
    conds = {c.name: c for c in parse_conditions(list(s.conditions), "claude_code", route_to=s.route_to)}
    assert conds["trove_full"].route_to == "claude-haiku-5-5"
    assert conds["passthrough_plugin"].recording_gateway


def test_staged_plan_finishes_each_repeat_before_the_next():
    tasks = [task(i) for i in range(6)]
    conds = parse_conditions(list(STUDY["conditions"]), "claude_code", route_to="claude-haiku-5-5")
    plan = build_plan(tasks, conds, 3, 9, staged=True)
    seeds = [s.seed for s in plan]
    assert seeds == sorted(seeds)                      # all of repeat 0, then 1, then 2
    assert len(plan) == 6 * 3 * 3
    first = [s.task_id for s in plan if s.seed == 0][::3]
    assert first != sorted(first)                      # still shuffled within a repeat
    assert build_plan(tasks, conds, 3, 9, staged=True) == plan
    assert build_plan(tasks, conds, 3, 9) != plan      # unstaged studies keep their old order


def test_through_repeat_keeps_only_the_first_looks():
    plan = build_plan([task(i) for i in range(4)], parse_conditions(["passthrough", "all_safe"], "claude_code"), 3, 1,
                      staged=True)
    assert {s.seed for s in _through(plan, 1)} == {0}
    assert {s.seed for s in _through(plan, 2)} == {0, 1}
    assert _through(plan, None) == plan
    with pytest.raises(UserError):
        _through(plan, 0)


def _batch(tmp_path, records):
    p = write(tmp_path)
    write_lock(p)
    b = tmp_path / "batch"
    b.mkdir()
    (b / "config.used.yaml").write_text(p.read_text(encoding="utf-8"), encoding="utf-8")
    (b / "study.lock.json").write_text((tmp_path / "s.lock.json").read_text(encoding="utf-8"), encoding="utf-8")
    led = Ledger(b / "ledger.jsonl")
    for r in records:
        led.add(r)
    return b


def test_stage_says_stop_when_every_comparison_is_decided(tmp_path, capsys):
    recs = []
    for i in range(40):
        recs += [rec(f"t{i}", 0, "passthrough", 1.00 + 0.01 * (i % 5)), rec(f"t{i}", 0, "passthrough_plugin", 0.70),
                 rec(f"t{i}", 0, "trove_full", 0.40)]
    assert _cmd_stage(SimpleNamespace(batch=_batch(tmp_path, recs))) == 0
    out = json.loads(capsys.readouterr().out)
    assert out["decided"] and {c["verdict"] for c in out["comparisons"]} == {"ship"}


def test_stage_asks_for_the_next_repeat_when_undecided(tmp_path, capsys):
    recs = []
    for i in range(10):          # too few pairs for any verdict
        recs += [rec(f"t{i}", 0, "passthrough", 1.0), rec(f"t{i}", 0, "passthrough_plugin", 0.7),
                 rec(f"t{i}", 0, "trove_full", 0.4)]
    assert _cmd_stage(SimpleNamespace(batch=_batch(tmp_path, recs))) == 3
    assert not json.loads(capsys.readouterr().out)["decided"]
