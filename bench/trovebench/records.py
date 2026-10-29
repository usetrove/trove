"""One row per agent run. This is the unit that analysis and CSV export work on."""

from __future__ import annotations

from dataclasses import asdict, dataclass, fields


@dataclass(frozen=True)
class RunRecord:
    run_id: str
    task_id: str
    condition: str
    seed: int                  # repeat index, 0-based; pairs runs across conditions
    status: str                # "ok" = agent ran and success was graded; anything else is excluded from analysis
    passed: bool
    timed_out: bool
    wall_s: float
    turns: int
    tool_calls: int
    retries: int               # repeated identical tool calls (tool_calls - distinct_calls)
    cost_usd: float | None     # None when unknown
    cost_source: str           # "gateway" (measured by Trove) or "agent-usage" (recomputed from the agent's reported usage)
    input_uncached_tokens: int = 0
    cache_write_tokens: int = 0
    cache_read_tokens: int = 0
    output_tokens: int = 0
    reasoning_tokens: int = 0
    note: str = ""

    def as_row(self) -> dict:
        return asdict(self)


RECORD_FIELDS = [f.name for f in fields(RunRecord)]

OK = "ok"
