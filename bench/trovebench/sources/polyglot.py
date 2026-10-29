"""Aider polyglot benchmark adapter (Python exercises).

Layout verified 2026-10-29 by cloning Aider-AI/polyglot-benchmark at 7e0611e: one folder per language,
`<lang>/exercises/practice/<exercise>/` with `.docs/instructions.md` (+ optional `instructions.append.md`),
`.meta/config.json` listing files.solution and files.test, and `.meta/example.*` (the reference solution).

All exercise content is copyright Exercism (see bench/LICENSES.md); manifests generated here stay local.
v1 covers Python only; the other five languages need their own image and test command.
"""

from __future__ import annotations

import json
import random
from pathlib import Path

from ..manifest import Task
from .common import write_task

REPO_URL = "https://github.com/Aider-AI/polyglot-benchmark"
ATTRIBUTION = "Exercise content copyright Exercism, used under Exercism's open source licenses."
LANG = "python"
PRACTICE = f"{LANG}/exercises/practice"


def exercise_task(checkout: Path, name: str, commit: str) -> Task:
    ex = checkout / PRACTICE / name
    cfg = json.loads((ex / ".meta" / "config.json").read_text(encoding="utf-8"))
    solution, tests = cfg["files"]["solution"], cfg["files"]["test"]
    docs = (ex / ".docs" / "instructions.md").read_text(encoding="utf-8")
    extra = ex / ".docs" / "instructions.append.md"
    if extra.exists():
        docs += "\n" + extra.read_text(encoding="utf-8")
    rel = f"{PRACTICE}/{name}"
    prompt = (
        f"Implement the exercise in `{rel}`: edit {', '.join(f'`{s}`' for s in solution)} so that the tests in "
        f"{', '.join(f'`{t}`' for t in tests)} pass. Do not modify the tests. Run them with "
        f"`cd {rel} && python -m pytest -q`.\n\n{docs}"
    )
    return Task(
        id=f"polyglot-py-{name}",
        source="polyglot", repo=REPO_URL, base_commit=commit, license="see-upstream", prompt=prompt,
        image="python:3.12-slim",
        # Delete every .meta folder first: it holds the reference solutions and the agent must not find them.
        setup=("pip install -q pytest", "find . -name .meta -type d -prune -exec rm -rf {} +"),
        grading_setup=tuple(f"git checkout {commit} -- {rel}/{t}" for t in tests),
        success_cmd=f"cd {rel} && python -m pytest -q",
        timeout_s=1200, dataset_commit=commit, tags=("polyglot", "python"), attribution=ATTRIBUTION,
        issue_url=f"{REPO_URL}/tree/{commit}/{rel}",
    )


def generate(checkout: Path, commit: str, out_dir: Path, *, n: int, seed: int) -> list[Task]:
    names = sorted(p.name for p in (checkout / PRACTICE).iterdir() if (p / ".meta" / "config.json").exists())
    if not names:
        raise ValueError(f"No Python exercises found under {checkout / PRACTICE}. Is this a polyglot-benchmark checkout?")
    chosen = sorted(random.Random(seed).sample(names, min(n, len(names))))
    tasks = [exercise_task(checkout, nm, commit) for nm in chosen]
    for t in tasks:
        write_task(out_dir, t)
    return tasks
