import argparse
from types import SimpleNamespace

from trovebench import cli
from trovebench.validate import Validation


def test_validate_prints_progress_and_keeps_result_lines_greppable(monkeypatch, capsys):
    tasks = [SimpleNamespace(id=f"t{i}") for i in range(4)]
    monkeypatch.setattr(cli, "load_config", lambda p: SimpleNamespace(agent="x", claude_version=None))
    monkeypatch.setattr(cli, "get_agent", lambda a: None)
    monkeypatch.setattr(cli, "load_tasks", lambda p: tasks)
    monkeypatch.setattr(cli, "validate_task",
                        lambda t, a, v: Validation(t.id, "ok" if t.id != "t1" else "rejected", "why"))
    rc = cli._cmd_validate(argparse.Namespace(config=None, tasks="x"))
    out, err = capsys.readouterr()
    lines = out.splitlines()
    assert rc == 1
    assert lines[0].startswith("OK") and lines[0].endswith("[1/4 25%]")
    assert lines[1].startswith("REJECTED") and lines[3].endswith("[4/4 100%]")
    assert "[2/4] validating t1" in err
    assert "validating" not in out
