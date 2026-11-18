"""Long-session task suite (plan item 10.5).

Earlier studies had a median of 5 turns, far shorter than a working session. Here each task is one polyglot
Python exercise followed by fixed follow-up prompts sent in the same Claude Code session
(`claude -p ... --resume <session id>`), the way a developer keeps working in one conversation: implement, harden,
refactor, review. The history grows with every prompt, as it does in a real session.

Design rules, fixed before any run:
  * the follow-ups are identical for every task and every condition, and none of them mentions or steers toward
    anything Trove does (no "re-read the file", no "run verbose");
  * grading runs only the exercise's own test file, restored from the dataset, so the agent's extra tests cannot
    fail a task and success means exactly what it means in the single-prompt suite;
  * the exercise list comes from the same pinned checkout as `fetch polyglot`, chosen by seed.
"""

from __future__ import annotations

import json
import random
from dataclasses import replace
from pathlib import Path

from ..manifest import Task
from .common import write_task
from .polyglot import PRACTICE, exercise_task

FOLLOWUPS: tuple[str, ...] = (
    "Now make the solution robust. Look for inputs the existing tests do not cover, such as empty input, very large "
    "input and invalid values, and handle them sensibly. Add tests for them in a new file `test_extra.py` in the same "
    "directory. Do not modify the existing test file. Run all the tests in the directory.",
    "Refactor the solution for readability: clear names, small functions and a short docstring for each public "
    "function. Do not change its behaviour. Run all the tests in the directory again.",
    "Review your work with `git diff`, fix anything you are not happy with, run all the tests in the directory one "
    "last time, and summarise what you changed in three bullet points.",
)

TIMEOUT_S = 2400


def long_task(checkout: Path, name: str, commit: str) -> Task:
    base = exercise_task(checkout, name, commit)
    ex = checkout / PRACTICE / name
    tests = json.loads((ex / ".meta" / "config.json").read_text(encoding="utf-8"))["files"]["test"]
    rel = f"{PRACTICE}/{name}"
    return replace(
        base,
        id=f"long-py-{name}",
        followups=FOLLOWUPS,
        # Only the exercise's own tests decide success; test_extra.py is the agent's and is not graded.
        success_cmd=f"cd {rel} && python -m pytest -q {' '.join(tests)}",
        timeout_s=TIMEOUT_S,
        tags=("polyglot", "python", "long_session"),
    )


def generate(checkout: Path, commit: str, out_dir: Path, *, n: int, seed: int) -> list[Task]:
    names = sorted(p.name for p in (checkout / PRACTICE).iterdir() if (p / ".meta" / "config.json").exists())
    if not names:
        raise ValueError(f"No Python exercises found under {checkout / PRACTICE}. Is this a polyglot-benchmark checkout?")
    chosen = sorted(random.Random(seed).sample(names, min(n, len(names))))
    tasks = [long_task(checkout, nm, commit) for nm in chosen]
    for t in tasks:
        write_task(out_dir, t)
    return tasks
