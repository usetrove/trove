"""Append-only record of finished runs, used for spend tracking and resume.

One JSON line per finished run, flushed and fsynced before the next run starts,
so an interruption never loses a paid-for result. A run that was started but not
finished is simply absent and gets redone on resume (its container is gone).

A run that ended in status "error" (it failed before it could be graded) stays in
the file and still counts toward spend, but resume retries it; the retry's line
supersedes it for analysis.
"""

from __future__ import annotations

import json
import os
from pathlib import Path

from .records import RunRecord

ERROR = "error"


class Ledger:
    def __init__(self, path: Path):
        self.path = path
        self._lines: list[RunRecord] = []
        self.records: dict[str, RunRecord] = {}   # latest line per run_id
        if path.exists():
            for n, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
                if not line.strip():
                    continue
                try:
                    rec = RunRecord(**json.loads(line))
                except (json.JSONDecodeError, TypeError) as e:
                    raise ValueError(
                        f"{path}:{n} is not a valid ledger line ({e}). Fix or remove that line; "
                        "do not delete the file, it holds what you have already spent."
                    ) from e
                self._lines.append(rec)
                self.records[rec.run_id] = rec

    def done(self, run_id: str) -> bool:
        rec = self.records.get(run_id)
        return rec is not None and rec.status != ERROR

    def add(self, rec: RunRecord) -> None:
        if self.done(rec.run_id):
            raise ValueError(f"run {rec.run_id} is already finished in the ledger.")
        line = json.dumps(rec.as_row(), sort_keys=True)
        with open(self.path, "a", encoding="utf-8", newline="\n") as f:
            f.write(line + "\n")
            f.flush()
            os.fsync(f.fileno())
        self._lines.append(rec)
        self.records[rec.run_id] = rec

    def spent(self, unknown_cost_usd: float) -> float:
        """Total spend over every line ever written, including superseded failed attempts.
        A run with unknown cost is charged `unknown_cost_usd` (the per-run cap) so the
        cap is never undercounted."""
        return sum(r.cost_usd if r.cost_usd is not None else unknown_cost_usd for r in self._lines)

    def final_records(self) -> list[RunRecord]:
        """The latest attempt per run, in a stable order, for analysis."""
        return sorted(self.records.values(), key=lambda r: r.run_id)
