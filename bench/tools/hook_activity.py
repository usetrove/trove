"""How often did each hook actually act, per condition?

A study of one hook against no hook can come back "no clear difference". That means one of two very different
things: the hook acted and changed nothing that matters, or the hook almost never acted, so there was nothing
to measure. This tool tells them apart. It reads the counters Trove's hooks already leave in each run's saved
data directory (nothing new is recorded) and prints, per condition, in how many runs a hook acted.

    python bench/tools/hook_activity.py bench/results/<DATE>/<STUDY_ID>

A hook "acted" when it really changed something: kind blocked, rewritten or advised. A would_* event is a
hook that was in shadow mode and only noted what it would have done; it is counted separately.
"""

from __future__ import annotations

import csv
import json
import sqlite3
import sys
from collections import Counter, defaultdict
from pathlib import Path

ACTED = ("blocked", "rewritten", "advised")
WOULD = ("would_block", "would_rewrite", "would_advise")


def run_events(run_dir: Path) -> Counter:
    """Counts of (hook, kind) for one run, from the hooks' database and any spool not yet ingested."""
    out: Counter = Counter()
    data = run_dir / "trove-data"
    db = data / "trove.db"
    if db.is_file():
        con = sqlite3.connect(f"file:{db.as_posix()}?mode=ro", uri=True)
        try:
            for hook, kind, n in con.execute("SELECT hook, kind, COUNT(*) FROM hook_events GROUP BY hook, kind"):
                out[(hook, kind)] += n
        except sqlite3.DatabaseError:
            pass  # no hook_events table: the hooks recorded nothing in this run
        finally:
            con.close()
    spool = data / "hooks" / "events.jsonl"
    if spool.is_file():
        for line in spool.read_text(encoding="utf-8", errors="replace").splitlines():
            try:
                e = json.loads(line)
            except json.JSONDecodeError:
                continue  # a torn line
            if e.get("h") and e.get("k"):
                out[(e["h"], e["k"])] += 1
    return out


def summarise(batch_dir: Path) -> dict[str, dict]:
    """Per condition: runs, runs in which a hook acted, runs that only noted what it would do, and total acts by hook."""
    rows = list(csv.DictReader((batch_dir / "runs.csv").open(encoding="utf-8")))
    res: dict[str, dict] = defaultdict(lambda: {"runs": 0, "acted_runs": 0, "would_only_runs": 0, "acts": Counter()})
    for r in rows:
        c = res[r["condition"]]
        c["runs"] += 1
        ev = run_events(batch_dir / "runs" / r["run_id"])
        acted = {k: n for k, n in ev.items() if k[1] in ACTED}
        if acted:
            c["acted_runs"] += 1
            for (hook, _kind), n in acted.items():
                c["acts"][hook] += n
        elif any(k[1] in WOULD for k in ev):
            c["would_only_runs"] += 1
    return dict(res)


def render(summary: dict[str, dict]) -> str:
    lines = ["condition | runs | hook acted in | only noted what it would do | acts by hook"]
    for cond, c in sorted(summary.items()):
        pct = 100 * c["acted_runs"] / c["runs"] if c["runs"] else 0.0
        acts = ", ".join(f"{h} {n}" for h, n in sorted(c["acts"].items())) or "-"
        lines.append(f"{cond} | {c['runs']} | {c['acted_runs']} ({pct:.0f}%) | {c['would_only_runs']} | {acts}")
    lines.append("")
    lines.append("If a single-hook condition shows a hook acted in only a small share of runs, a result of 'no clear "
                 "difference' says the hook rarely had anything to do on these tasks, not that it does nothing.")
    return "\n".join(lines)


def main(argv: list[str]) -> int:
    if len(argv) != 2:
        print(__doc__)
        return 2
    batch = Path(argv[1])
    if not (batch / "runs.csv").is_file():
        print(f"{batch}/runs.csv not found. Pass the results folder of one study, bench/results/<DATE>/<STUDY_ID>.")
        return 1
    print(render(summarise(batch)))
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
