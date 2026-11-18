"""Long-session suite (plan item 10.5): manifests, the resume command and how follow-up results combine."""

from __future__ import annotations

import pytest

from test_sources import fake_checkout
from trovebench.agent.base import AgentResult, ModelUsage, combine_results
from trovebench.agent.cc import CcAgent
from trovebench.manifest import ManifestError, dump_task, load_tasks, parse_task
from trovebench.sources import longsession, polyglot


def test_longsession_tasks_carry_the_fixed_followups(tmp_path):
    co, out = tmp_path / "co", tmp_path / "out"
    fake_checkout(co)
    tasks = longsession.generate(co, "a" * 40, out, n=3, seed=7)
    loaded = load_tasks(out)
    assert len(loaded) == 3 and all(t.id.startswith("long-py-") for t in loaded)
    for t in loaded:
        assert t.followups == longsession.FOLLOWUPS               # identical for every task
        assert "long_session" in t.tags
        assert t.timeout_s == longsession.TIMEOUT_S
        # grading names the exercise's own test file only, so test_extra.py cannot fail a task
        name = t.id.removeprefix("long-py-")
        assert t.success_cmd.endswith(f"python -m pytest -q {name}_test.py")
        assert t.grading_setup[0].startswith("git checkout " + "a" * 40)
    assert [t.id for t in longsession.generate(co, "a" * 40, tmp_path / "o2", n=3, seed=7)] == [t.id for t in tasks]


def test_followups_never_steer_toward_trove():
    text = " ".join(longsession.FOLLOWUPS).lower()
    for word in ("trove", "re-read", "reread", "verbose", "cache", "token"):
        assert word not in text


def test_single_prompt_manifests_are_unchanged(tmp_path):
    co = tmp_path / "co"
    fake_checkout(co)
    t = polyglot.generate(co, "a" * 40, tmp_path / "out", n=1, seed=1)[0]
    assert t.followups == () and "followups" not in dump_task(t)


def test_followups_must_be_a_list_of_strings():
    base = dict(id="x", source="smoke", repo="local:x", base_commit="c", license="MIT", prompt="p", image="i",
                success_cmd="true", timeout_s=10)
    assert parse_task({**base, "followups": ["a", "b"]}).followups == ("a", "b")
    with pytest.raises(ManifestError, match="followups"):
        parse_task({**base, "followups": "just one"})


def test_claude_resume_command():
    a = CcAgent()
    base = dict(model="claude-sonnet-5-5", max_budget_usd=1.0, prompt_path="/bench/p.txt")
    assert a.command(**base).endswith("--no-session-persistence")              # single prompt: as before
    first = a.command(**base, persist=True)
    assert "--no-session-persistence" not in first and "--resume" not in first
    nxt = a.command(**base, resume="abc-123")
    assert "--resume abc-123 " in nxt and "--no-session-persistence" not in nxt
    assert "--resume 'x; rm -rf /' " in a.command(**base, resume="x; rm -rf /")  # quoted


def _r(cost, out_tokens, turns, *, err=False, sid="s"):
    return AgentResult(parsed=True, is_error=err, turns=turns, cost_usd_estimate=cost,
                       usage_by_model={"m": ModelUsage(10, out_tokens, 5, 2)}, session_id=sid)


def test_session_totals_take_the_latest_result_and_never_double_count():
    got = combine_results([_r(0.10, 100, 3), _r(0.25, 260, 7), _r(0.40, 400, 11)], session_totals=True)
    assert got.cost_usd_estimate == pytest.approx(0.40)
    assert got.usage_by_model["m"].output_tokens == 400 and got.turns == 11 and not got.is_error


def test_per_call_reports_are_added_up():
    got = combine_results([_r(0.10, 100, 3), _r(0.15, 160, 4)], session_totals=False)
    assert got.cost_usd_estimate == pytest.approx(0.25)
    assert got.usage_by_model["m"] == ModelUsage(20, 260, 10, 4) and got.turns == 7


def test_an_error_or_missing_result_in_any_call_marks_the_session():
    assert combine_results([_r(0.1, 1, 1), _r(0.2, 2, 2, err=True)], session_totals=True).is_error
    unparsed = AgentResult(parsed=False, detail="no JSON")
    got = combine_results([_r(0.1, 1, 1), unparsed], session_totals=True)
    assert got.is_error and got.cost_usd_estimate == pytest.approx(0.1)     # spend so far is still counted
    assert not combine_results([unparsed], session_totals=True).parsed
    assert not combine_results([], session_totals=True).parsed
