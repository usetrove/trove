"""Batch executor: spending cap, resume, clean interrupt.

The actual work of one run is injected as `run_one`, so this logic is tested
with a fake and never needs Docker or an API key.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Callable, Sequence

from .ledger import Ledger
from .plan import RunSpec
from .records import RunRecord
from .redact import redact

MAX_CONSECUTIVE_ERRORS = 3

STOP_DONE = "done"
STOP_CAP = "budget_cap"
STOP_INTERRUPTED = "interrupted"
STOP_ERRORS = "repeated_errors"


@dataclass(frozen=True)
class BatchResult:
    stop_reason: str
    ran: int
    skipped_done: int
    remaining: int
    spent_usd: float

    @property
    def complete(self) -> bool:
        return self.stop_reason == STOP_DONE


def execute(
    specs: Sequence[RunSpec],
    ledger: Ledger,
    run_one: Callable[[RunSpec], RunRecord],
    *,
    batch_cap_usd: float,
    run_cap_usd: float,
    log: Callable[[str], None] = print,
) -> BatchResult:
    """Run every spec not already in the ledger, in plan order.

    Before each run we require spent + run_cap <= batch_cap, so the batch cannot
    pass its cap unless a single run overshoots its own cap by more than the
    agent's budget check allows (that check happens between API calls, so one
    request of overshoot is possible; see README).
    """
    ran = skipped = 0
    errors_in_a_row = 0
    todo = [s for s in specs if not ledger.done(s.run_id)]
    skipped = len(specs) - len(todo)
    for i, spec in enumerate(todo):
        spent = ledger.spent(run_cap_usd)
        if spent + run_cap_usd > batch_cap_usd:
            log(f"Stopping: ${spent:.2f} spent and the next run could cost up to ${run_cap_usd:.2f}, "
                f"which would pass the ${batch_cap_usd:.2f} batch cap. Raise limits.batch_cap_usd to continue with --resume.")
            return BatchResult(STOP_CAP, ran, skipped, len(todo) - i, spent)
        log(f"[{skipped + ran + 1}/{len(specs)}] {spec.run_id}")
        try:
            rec = run_one(spec)
        except KeyboardInterrupt:
            log("Interrupted. Finished runs are saved; rerun with --resume to continue.")
            return BatchResult(STOP_INTERRUPTED, ran, skipped, len(todo) - i, ledger.spent(run_cap_usd))
        except Exception as e:  # a bug or environment failure in one run must not lose the batch
            rec = RunRecord(
                run_id=spec.run_id, task_id=spec.task_id, condition=spec.condition, seed=spec.seed,
                status="error", passed=False, timed_out=False, wall_s=0.0, turns=0, tool_calls=0, retries=0,
                cost_usd=None, cost_source="none", note=redact(f"{type(e).__name__}: {e}")[:500], effort=spec.effort,
            )
        if rec.run_id != spec.run_id:
            raise RuntimeError(
                f"run_one returned a record for {rec.run_id} but was asked to run {spec.run_id}; "
                "this is a bug in the runner, and continuing would corrupt resume and spend tracking."
            )
        ledger.add(rec)
        ran += 1
        errors_in_a_row = errors_in_a_row + 1 if rec.status == "error" else 0
        if errors_in_a_row >= MAX_CONSECUTIVE_ERRORS:
            log(f"Stopping: {MAX_CONSECUTIVE_ERRORS} runs in a row failed before grading ({rec.note}). "
                "Fix the environment, then rerun with --resume (failed runs are retried).")
            return BatchResult(STOP_ERRORS, ran, skipped, len(todo) - i - 1, ledger.spent(run_cap_usd))
    return BatchResult(STOP_DONE, ran, skipped, 0, ledger.spent(run_cap_usd))
