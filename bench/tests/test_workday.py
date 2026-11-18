"""Workday suite (launch-2): one exercise, then a working day of ordinary follow-ups in the same session."""

from __future__ import annotations

from test_sources import fake_checkout
from trovebench.manifest import load_tasks
from trovebench.sources import longsession, workday


def test_workday_tasks_are_long_and_identical_across_tasks(tmp_path):
    co, out = tmp_path / "co", tmp_path / "out"
    fake_checkout(co)
    tasks = workday.generate(co, "a" * 40, out, n=3, seed=7)
    loaded = load_tasks(out)
    assert len(loaded) == 3 and all(t.id.startswith("day-py-") for t in loaded)
    for t in loaded:
        assert t.followups == workday.FOLLOWUPS
        assert len(t.followups) >= 6                  # far longer than the three-follow-up launch-1 sessions
        assert "workday" in t.tags and t.timeout_s == workday.TIMEOUT_S
        name = t.id.removeprefix("day-py-")
        # only the exercise's own tests decide success; the agent's extra files cannot fail a task
        assert t.success_cmd.endswith(f"python -m pytest -q {name}_test.py")
    assert [t.id for t in workday.generate(co, "a" * 40, tmp_path / "o2", n=3, seed=7)] == [t.id for t in tasks]


def test_workday_followups_never_steer_toward_trove():
    text = " ".join(workday.FOLLOWUPS).lower()
    for word in ("trove", "re-read", "reread", "verbose", "cache", "token", "context", "cheap", "model"):
        assert word not in text


def test_workday_does_not_change_the_launch_1_suite():
    assert len(longsession.FOLLOWUPS) == 3
