"""Every condition in a batch launches the agent the same way, so arms differ only in Trove (launch-1 lesson)."""

from pathlib import Path

from trovebench.conditions import parse_condition
from trovebench.docker_run import ContainerRunner


def _runner(*specs: str) -> ContainerRunner:
    conds = {}
    for s in specs:
        c = parse_condition(s, "claude_code", route_to="claude-haiku-5-5")
        conds[c.name] = c
    return ContainerRunner(agent=None, model="m", run_cap_usd=1, run_timeout_s=1, batch_dir=Path("."), tasks={},
                           conditions=conds, table=None, claude_version="x")


def test_baseline_runs_full_when_any_arm_needs_plugins():
    assert _runner("passthrough", "passthrough_plugin", "trove_full").bare() is False


def test_bare_only_when_no_arm_needs_full_mode():
    assert _runner("passthrough", "all_safe").bare() is True

