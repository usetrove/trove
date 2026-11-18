"""How long a study will take, estimated from its pilot. Runs are sequential, so time grows with the number of runs.

Per-session time is the agent's wall time, sized with the same margin as the cost sizing (the slower of the mean and
0.75 x the slowest session), plus the pilot's per-session overhead for containers and grading when the pilot's total
elapsed time is known. Every figure is an estimate.
"""

from __future__ import annotations

import math
import statistics as st
from dataclasses import dataclass

SITTING_H = 6.0


@dataclass(frozen=True)
class TimeEstimate:
    per_session_s: float
    units_per_look: int
    looks: int
    includes_overhead: bool

    @property
    def per_look_h(self) -> float:
        return self.per_session_s * self.units_per_look / 3600

    @property
    def total_h(self) -> float:
        return self.per_look_h * self.looks


def estimate_time(walls: list[float], elapsed_s: float | None, units_per_look: int, looks: int) -> TimeEstimate | None:
    walls = [w for w in walls if w and w > 0]
    if not walls:
        return None
    agent = max(st.fmean(walls), max(walls) * 0.75)
    # An elapsed time shorter than the agent time means the pilot was resumed and only partly timed: ignore it.
    timed = elapsed_s is not None and elapsed_s >= sum(walls)
    overhead = (elapsed_s - sum(walls)) / len(walls) if timed else 0.0
    return TimeEstimate(agent + overhead, units_per_look, looks, timed)


def describe(est: TimeEstimate) -> str:
    basis = "pilot wall time incl. setup and grading" if est.includes_overhead else "agent time only; setup and grading not included"
    lines = [f"Time (estimated, from the {basis}): about {est.per_session_s / 60:.1f} min per session, "
             f"{est.per_look_h:.1f} h per look"]
    if est.looks >= 1:
        sittings = max(1, math.ceil(est.total_h / SITTING_H))
        lines[0] += f", {est.total_h:.1f} h for {est.looks} look(s) if none stops early."
        lines.append(f"That is about {sittings} sitting(s) of {SITTING_H:.0f} h. Stop any time (Ctrl-C) and rerun "
                     "03_run.sh to resume; finished runs are kept and not paid for twice.")
    else:
        lines[0] += "."
    return "\n".join(lines)
