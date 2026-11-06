import json
from collections import Counter

import pytest
import yaml

from trovebench.conditions import parse_conditions
from trovebench.manifest import parse_task
from trovebench.plan import build_plan
from trovebench.prereg import lock_path, verify_lock, write_lock
from trovebench.study import StudyError, load_study

STUDY = dict(
    id="parity-1", title="Parity", hypothesis="hooks save a share of what the gateway saves",
    primary_metric="billed_cost_usd", baseline_condition="direct", conditions=["direct", "passthrough", "hooks"],
    tasks="tasks/smoke", repeats=2, order_seed=3, efforts=["low", "high"],
)


def write(tmp_path, **over):
    p = tmp_path / "s.yaml"
    p.write_text(yaml.safe_dump({**STUDY, **over}), encoding="utf-8")
    return p


def task(i):
    return parse_task(dict(id=f"t{i}", source="smoke", repo="r", base_commit="c", license="MIT",
                           prompt="p", image="i", success_cmd="true", timeout_s=60))


def test_load_valid_study(tmp_path):
    s = load_study(write(tmp_path))
    assert s.comparisons == (("passthrough", "direct"), ("hooks", "direct"))
    assert s.efforts == ("low", "high") and len(s.sha256) == 64


@pytest.mark.parametrize("over,msg", [
    ({"primary_metric": "vibes"}, "primary_metric"),
    ({"baseline_condition": "nope"}, "baseline_condition"),
    ({"efforts": ["low", "low"]}, "efforts"),
    ({"efforts": ["extreme"]}, "efforts"),
    ({"conditions": ["direct"]}, "two conditions"),
    ({"surprise": 1}, "unknown key"),
    ({"secondary_metrics": ["billed_cost_usd"]}, "secondary_metrics"),
    ({"id": "a/b"}, "id"),
])
def test_invalid_study_is_rejected(tmp_path, over, msg):
    with pytest.raises(StudyError, match=msg):
        load_study(write(tmp_path, **over))


def test_missing_registration_fields(tmp_path):
    p = write(tmp_path)
    d = yaml.safe_load(p.read_text())
    del d["hypothesis"]
    p.write_text(yaml.safe_dump(d))
    with pytest.raises(StudyError, match="hypothesis"):
        load_study(p)


def test_lock_roundtrip_and_tamper(tmp_path):
    p = write(tmp_path)
    with pytest.raises(StudyError, match="not pre-registered"):
        verify_lock(p)
    write_lock(p)
    assert json.loads(lock_path(p).read_text())["study_sha256"] == load_study(p).sha256
    assert verify_lock(p)[0].id == "parity-1"
    assert write_lock(p) == lock_path(p)          # same file: idempotent
    write(tmp_path, repeats=5)
    with pytest.raises(StudyError, match="changed since"):
        verify_lock(p)
    with pytest.raises(StudyError, match="cannot be edited"):
        write_lock(p)


def test_paired_plan_every_condition_in_every_unit():
    conds = parse_conditions(["direct", "passthrough", "all_safe"], "claude_code")
    specs = build_plan([task(i) for i in range(4)], conds, 2, order_seed=9, efforts=("low", "high"))
    units = Counter(s.unit_id for s in specs)
    assert len(units) == 4 * 2 * 2 and set(units.values()) == {3}
    for uid in units:
        assert {s.condition for s in specs if s.unit_id == uid} == {"direct", "passthrough", "all_safe"}
    assert len({s.run_id for s in specs}) == len(specs)
    assert {s.effort for s in specs} == {"low", "high"}
    # units are contiguous, so a unit's runs are never interleaved with another's
    seen = [s.unit_id for s in specs]
    assert [u for i, u in enumerate(seen) if i == 0 or seen[i - 1] != u] == list(dict.fromkeys(seen))


def test_condition_order_is_randomized_and_deterministic():
    conds = parse_conditions(["direct", "passthrough", "all_safe"], "claude_code")
    tasks = [task(i) for i in range(30)]
    a = build_plan(tasks, conds, 1, 5)
    assert a == build_plan(tasks, conds, 1, 5)
    assert a != build_plan(tasks, conds, 1, 6)
    firsts = Counter(next(s.condition for s in a if s.unit_id == u) for u in {s.unit_id for s in a})
    assert set(firsts) == {"direct", "passthrough", "all_safe"}   # nobody is always first


def test_no_effort_keeps_plain_run_ids():
    conds = parse_conditions(["direct", "passthrough"], "claude_code")
    specs = build_plan([task(0)], conds, 1, 1)
    assert {s.run_id for s in specs} == {"t0__direct__s0", "t0__passthrough__s0"}
    assert all(s.effort == "" for s in specs)


def test_duplicate_effort_rejected():
    conds = parse_conditions(["direct", "passthrough"], "claude_code")
    with pytest.raises(ValueError, match="effort"):
        build_plan([task(0)], conds, 1, 1, efforts=("low", "low"))


def test_rebase_cost_puts_all_conditions_on_the_agent_source():
    import dataclasses

    from helpers import rec

    from trovebench.stats import make_pairs, rebase_cost

    a = dataclasses.replace(rec("t", 0, "direct", 1.0), agent_cost_usd=1.0, cost_source="agent-usage")
    b = dataclasses.replace(rec("t", 0, "passthrough", 0.8), agent_cost_usd=0.9)   # gateway says 0.8, agent says 0.9
    assert make_pairs([a, b], "direct", "passthrough")[0].treated.cost_usd == 0.8
    rebased = rebase_cost([a, b], "agent")
    assert make_pairs(rebased, "direct", "passthrough")[0].treated.cost_usd == 0.9
    assert {r.cost_source for r in rebased} == {"agent-usage"}
    assert make_pairs(rebase_cost([dataclasses.replace(a, agent_cost_usd=None), b], "agent"), "direct", "passthrough") == []
    with pytest.raises(ValueError):
        rebase_cost([a], "vibes")


def test_colon_condition_names_are_rejected_with_the_fix(tmp_path):
    with pytest.raises(StudyError, match="alone_noise_normalizer"):
        load_study(write(tmp_path, conditions=["direct", "alone:noise_normalizer"]))
    from trovebench.conditions import parse_condition

    assert parse_condition("alone_noise_normalizer", "claude_code").name == "alone_noise_normalizer"
