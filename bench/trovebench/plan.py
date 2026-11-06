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
    effort: str = ""     # reasoning effort level; "" when the study has no effort axis
    unit_id: str = ""    # the (task, repeat, effort) unit every condition is paired within


def run_id(task_id: str, condition: str, seed: int, effort: str = "") -> str:
    base = f"{task_id}__{condition}__s{seed}"
    return f"{base}__e{effort}" if effort else base


def unit_id(task_id: str, seed: int, effort: str = "") -> str:
    base = f"{task_id}__s{seed}"
    return f"{base}__e{effort}" if effort else base


def build_plan(tasks: Sequence[Task], conditions: Sequence[Condition], repeats: int, order_seed: int,
               efforts: Sequence[str] = ()) -> list[RunSpec]:
    """Paired, randomized design. Each unit is one (task, repeat, effort); every measurable
    condition runs once in every unit. Unit order is shuffled, and condition order is shuffled
    inside each unit, both from one fixed seed, so a condition never systematically runs first
    and the same inputs always give the same plan."""
    if repeats < 1:
        raise ValueError("repeats must be at least 1.")
    if len(set(efforts)) != len(efforts):
        raise ValueError("an effort level is listed twice; each level may appear once.")
    measurable = [c for c in conditions if c.measurable]
    rng = random.Random(order_seed)
    units = [(t.id, s, e) for t in sorted(tasks, key=lambda t: t.id) for s in range(repeats) for e in (efforts or ("",))]
    rng.shuffle(units)
    specs: list[RunSpec] = []
    for task_id, seed, effort in units:
        order = list(measurable)
        rng.shuffle(order)
        uid = unit_id(task_id, seed, effort)
        specs += [RunSpec(run_id(task_id, c.name, seed, effort), task_id, c.name, seed, effort, uid) for c in order]
    return specs


def write_plan(path: Path, specs: Sequence[RunSpec]) -> None:
    path.write_text(json.dumps([asdict(s) for s in specs], indent=2) + "\n", encoding="utf-8", newline="\n")


def read_plan(path: Path) -> list[RunSpec]:
    return [RunSpec(**d) for d in json.loads(path.read_text(encoding="utf-8"))]
