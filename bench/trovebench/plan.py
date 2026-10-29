"""Expand tasks x conditions x repeats into a deterministic, randomized run order."""

from __future__ import annotations

import json
import random
from dataclasses import asdict, dataclass
from pathlib import Path
from typing import Sequence

from .conditions import Condition
from .manifest import Task


@dataclass(frozen=True)
class RunSpec:
    run_id: str
    task_id: str
    condition: str
    seed: int


def run_id(task_id: str, condition: str, seed: int) -> str:
    return f"{task_id}__{condition}__s{seed}"


def build_plan(tasks: Sequence[Task], conditions: Sequence[Condition], repeats: int, order_seed: int) -> list[RunSpec]:
    """Only measurable conditions are planned. The order is shuffled with a fixed seed so that
    a condition never systematically runs first, and the same inputs always give the same plan."""
    if repeats < 1:
        raise ValueError("repeats must be at least 1.")
    specs = [
        RunSpec(run_id(t.id, c.name, s), t.id, c.name, s)
        for t in sorted(tasks, key=lambda t: t.id)
        for c in conditions
        if c.measurable
        for s in range(repeats)
    ]
    random.Random(order_seed).shuffle(specs)
    return specs


def write_plan(path: Path, specs: Sequence[RunSpec]) -> None:
    path.write_text(json.dumps([asdict(s) for s in specs], indent=2) + "\n", encoding="utf-8", newline="\n")


def read_plan(path: Path) -> list[RunSpec]:
    return [RunSpec(**d) for d in json.loads(path.read_text(encoding="utf-8"))]
