"""The single-hook study template must stay a valid study once its placeholders are filled in."""

from collections import Counter
from pathlib import Path

import pytest

from trovebench.conditions import parse_conditions
from trovebench.manifest import parse_task
from trovebench.plan import build_plan
from trovebench.study import StudyError, load_study

TEMPLATE = Path(__file__).parent.parent / "studies" / "hooks-single-1.yaml.template"


def filled(tmp_path, **subs):
    text = TEMPLATE.read_text(encoding="utf-8")
    for k, v in {"<NEW_SEED>": "20261201", "<FROM_PILOT>": "25.0", **subs}.items():
        text = text.replace(k, v)
    p = tmp_path / "hooks-single-1.yaml"
    p.write_text(text, encoding="utf-8")
    return p


def task(i):
    return parse_task(dict(id=f"t{i}", source="smoke", repo="r", base_commit="c", license="MIT",
                           prompt="p", image="i", success_cmd="true", timeout_s=60))


def test_the_template_is_a_valid_study_once_filled_in(tmp_path):
    s = load_study(filled(tmp_path))
    assert s.baseline_condition == "direct"
    assert s.conditions == ("direct", "alone_hook_repeat_read", "alone_hook_quiet_commands")
    assert s.comparisons == (("alone_hook_repeat_read", "direct"), ("alone_hook_quiet_commands", "direct"))
    assert s.analysis.multiple_comparison == "holm"
    assert s.ship_rule == {"min_cost_reduction_pct": 5, "success_margin_pts": 5, "min_pairs": 30}
    assert s.cost_basis == "agent" and s.repeats == 3


def test_an_unfilled_template_is_refused_not_run(tmp_path):
    text = TEMPLATE.read_text(encoding="utf-8")
    p = tmp_path / "s.yaml"
    p.write_text(text, encoding="utf-8")
    with pytest.raises(StudyError):
        load_study(p)  # <NEW_SEED> is not a number, so it cannot be registered by accident


def test_every_condition_in_it_is_measurable_with_claude_code(tmp_path):
    s = load_study(filled(tmp_path))
    conds = parse_conditions(list(s.conditions), "claude_code")
    assert all(c.measurable for c in conds)
    assert [c.uses_gateway for c in conds] == [False, False, False]


def test_the_plan_runs_every_condition_on_every_task_and_repeat(tmp_path):
    s = load_study(filled(tmp_path))
    conds = parse_conditions(list(s.conditions), "claude_code")
    specs = build_plan([task(i) for i in range(4)], conds, s.repeats, s.order_seed)
    units = Counter(x.unit_id for x in specs)
    assert len(units) == 4 * s.repeats and set(units.values()) == {3}
    assert {x.condition for x in specs} == set(s.conditions)


def test_the_template_says_how_to_read_a_rare_hook():
    text = TEMPLATE.read_text(encoding="utf-8")
    assert "hook_activity.py" in text and "TEMPLATE" in text
