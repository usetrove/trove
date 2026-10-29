"""Prove a generated task is solvable before any agent is paid to attempt it.

Checks, in fresh containers: (1) the success command FAILS on the unmodified base, (2) it PASSES once the
upstream fix (gold.patch) is applied. Tasks that fail either check are rejected with the reason. Tasks with no
gold.patch (smoke, polyglot) are reported as not validated, not as failed.
"""

from __future__ import annotations

import json
from dataclasses import dataclass
from pathlib import Path

from . import docker_env as de
from .agent.base import Agent
from .manifest import Task

MARKER = "validation.json"
NEEDS_VALIDATION = {"swebench", "mined"}


@dataclass(frozen=True)
class Validation:
    task_id: str
    status: str          # "ok" | "rejected" | "skipped"
    reason: str


def _grade(c: de.Container, task: Task) -> de.ExecResult:
    steps = [*task.grading_setup, task.success_cmd]
    return c.exec(" && ".join(steps), timeout=min(task.timeout_s, 1800))


def validate_task(task: Task, agent: Agent, agent_version: str | None) -> Validation:
    gold = Path(task.base_dir) / "gold.patch"
    if not gold.exists():
        return Validation(task.id, "skipped", "no gold.patch to validate against")
    tag, image_id = de.build_task_image(task, agent, agent_version)
    with de.Container(tag, f"val-{task.id}-base") as c:
        base = _grade(c, task)
    if base.rc == 0 or base.timed_out:
        why = "timed out" if base.timed_out else "the tests already pass on the unmodified base, so they prove nothing"
        return Validation(task.id, "rejected", why)
    with de.Container(tag, f"val-{task.id}-gold") as c:
        c.put(gold, "/tmp/gold.patch")
        applied = c.exec("git apply --whitespace=nowarn /tmp/gold.patch")
        if applied.rc != 0:
            return Validation(task.id, "rejected", f"gold patch does not apply: {applied.err[-200:]}")
        fixed = _grade(c, task)
    if fixed.rc != 0:
        tail = (fixed.out + fixed.err)[-300:].replace("\n", " ")
        return Validation(task.id, "rejected", f"tests still fail with the gold patch applied (environment issue?): {tail}")
    (Path(task.base_dir) / MARKER).write_text(
        json.dumps({"ok": True, "image_id": image_id, "base_commit": task.base_commit}, indent=2) + "\n",
        encoding="utf-8", newline="\n")
    return Validation(task.id, "ok", "fails on base, passes with the gold patch")


def is_validated(task: Task) -> bool:
    m = Path(task.base_dir) / MARKER
    try:
        d = json.loads(m.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return False
    return bool(d.get("ok")) and d.get("base_commit") == task.base_commit
