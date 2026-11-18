"""Workday task suite (launch-2).

launch-1's long sessions were still short: a median of about 6 tool calls and 60k cached tokens read per session,
so fixed per-request costs dominated and there was little history for anything to act on. A working session is
longer. Here each task is one polyglot Python exercise followed by seven fixed follow-ups in the same Claude Code
session, the ordinary work a developer does after a first implementation: harden, add a command-line entry point,
add logging, measure speed, refactor, document, review.

Design rules, fixed before any run (the same as the long-session suite):
  * the follow-ups are identical for every task and every condition, and none of them mentions or steers toward
    anything Trove does (no re-reading, no verbose output, nothing about models or context);
  * grading runs only the exercise's own test file, restored from the dataset, so files the agent adds cannot fail
    a task and success means exactly what it means in the single-prompt suite;
  * the exercise list comes from the same pinned checkout as `fetch polyglot`, chosen by seed.
"""

from __future__ import annotations

import random
from dataclasses import replace
from pathlib import Path

from ..manifest import Task
from .common import write_task
from .longsession import long_task
from .polyglot import PRACTICE

FOLLOWUPS: tuple[str, ...] = (
    "Now make the solution robust. Look for inputs the existing tests do not cover, such as empty input, very large "
    "input and invalid values, and handle them sensibly. Add tests for them in a new file `test_extra.py` in the same "
    "directory. Do not modify the existing test file. Run all the tests in the directory.",
    "Add a small command-line entry point, `cli.py` in the same directory, that exposes the main function: it reads "
    "its input from command-line arguments or standard input and prints the result. Add tests for it to "
    "`test_extra.py` and run all the tests in the directory.",
    "Add logging with Python's `logging` module: log a warning whenever the solution receives invalid input, and make "
    "sure nothing is logged for valid input. Add a test that checks this, and run all the tests in the directory.",
    "A teammate is worried about speed. Write `bench_speed.py` in the same directory that times the main function on "
    "a few thousand generated inputs and prints the timings. Run it. If you find a clear, safe improvement, make it, "
    "run the timing script again and run all the tests in the directory.",
    "Refactor the solution and the command-line entry point for readability: clear names, small functions and a "
    "short docstring for each public function. Do not change behaviour. Run all the tests in the directory again.",
    "Write a `README.md` in the same directory describing what the code does, how to use the command-line entry "
    "point, and two worked examples. Check that both examples really produce the output you wrote by running them.",
    "Review all of your work with `git status` and `git diff`, fix anything you are not happy with, run all the tests "
    "in the directory one last time, and summarise what you changed in five bullet points.",
)

TIMEOUT_S = 3600


def day_task(checkout: Path, name: str, commit: str) -> Task:
    base = long_task(checkout, name, commit)
    return replace(base, id=f"day-py-{name}", followups=FOLLOWUPS, timeout_s=TIMEOUT_S,
                   tags=("polyglot", "python", "long_session", "workday"))


def generate(checkout: Path, commit: str, out_dir: Path, *, n: int, seed: int) -> list[Task]:
    names = sorted(p.name for p in (checkout / PRACTICE).iterdir() if (p / ".meta" / "config.json").exists())
    if not names:
        raise ValueError(f"No Python exercises found under {checkout / PRACTICE}. Is this a polyglot-benchmark checkout?")
    chosen = sorted(random.Random(seed).sample(names, min(n, len(names))))
    tasks = [day_task(checkout, nm, commit) for nm in chosen]
    for t in tasks:
        write_task(out_dir, t)
    return tasks
