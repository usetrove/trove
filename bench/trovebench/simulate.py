"""Docker-free synthetic runs, for testing the pipeline (lock, plan, order, execute, report) end to end.

THIS MEASURES NOTHING ABOUT ANY AGENT. Costs and outcomes are drawn from the effect sizes in a
file the caller writes, so every number it produces is whatever was put in. Reports built from it
say so on the first line. It exists so the statistics and report can be exercised, and so a study
file can be dry-run before real money is spent.
"""

from __future__ import annotations

import math
import random
from pathlib import Path

import yaml

from .plan import RunSpec
from .records import RunRecord

_KEYS = {"base_cost_usd", "task_sd", "noise_sd", "cost_multiplier", "effort_cost_scale", "pass_rate", "wall_s", "seed"}


class SimulationError(ValueError):
    pass


def load_effects(path: Path) -> dict:
    d = yaml.safe_load(path.read_text(encoding="utf-8")) or {}
    extra = sorted(set(d) - _KEYS)
    if extra:
        raise SimulationError(f"{path}: unknown key(s) {', '.join(extra)}. Allowed: {', '.join(sorted(_KEYS))}.")
    return d


def make_run_one(effects: dict, *, model: str = "simulated"):
    """Return run_one(spec) -> RunRecord. Deterministic: the same spec and effects give the same record."""
    base = float(effects.get("base_cost_usd", 0.30))
    task_sd = float(effects.get("task_sd", 0.4))
    noise_sd = float(effects.get("noise_sd", 0.08))
    mult = effects.get("cost_multiplier") or {}
    escale = effects.get("effort_cost_scale") or {}
    pass_rate = effects.get("pass_rate") or {}
    walls = effects.get("wall_s") or {}
    seed = effects.get("seed", 0)

    def run_one(spec: RunSpec) -> RunRecord:
        task_rng = random.Random(f"{seed}|task|{spec.task_id}")          # a task is equally hard in every condition
        difficulty = math.exp(task_rng.gauss(0, task_sd))
        task_pass_u = task_rng.random()                                  # and equally likely to be solved
        noise = random.Random(f"{seed}|run|{spec.run_id}").gauss(0, noise_sd)
        cost = base * difficulty * float(escale.get(spec.effort, 1.0)) * float(mult.get(spec.condition, 1.0)) * math.exp(noise)
        p = float(pass_rate.get(spec.condition, pass_rate.get("default", 0.9)))
        wall = float(walls.get(spec.condition, walls.get("default", 60.0))) * difficulty
        return RunRecord(
            run_id=spec.run_id, task_id=spec.task_id, condition=spec.condition, seed=spec.seed, status="ok",
            passed=task_pass_u < p, timed_out=False, wall_s=round(wall, 1), turns=10, tool_calls=20, retries=0,
            cost_usd=round(cost, 6), cost_source="agent-usage", note="simulated", effort=spec.effort,
            agent_cost_usd=round(cost, 6),
        )

    return run_one
