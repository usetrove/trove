"""Check a finished launch pilot and size the study from it. Reads only the pilot folder; calls nothing.

    python bench/scripts/launch/pilot_check.py <pilot batch> <route_to model> <budget usd>

Stops (exit 1) unless:
  * every run was gradeable (no harness errors),
  * trove_full really routed: its gateway records show requests served by the route_to model,
  * the plugin conditions really ran with the plugin (hook data saved next to the run).
Then writes <pilot batch>/sizing.json with the mean cost per run and how many looks (repeats) of the study fit the
budget, which 03_run.sh reads. The pilot's pass/fail results are never used.
"""

from __future__ import annotations

import json
import math
import sqlite3
import statistics as st
import sys
from pathlib import Path

batch, route_to, budget = Path(sys.argv[1]), sys.argv[2], float(sys.argv[3])
STUDY_UNITS_PER_LOOK = 30 * 3   # 30 tasks x 3 conditions per repeat
STUDY_REPEATS = 3

latest = {}
for line in (batch / "ledger.jsonl").read_text(encoding="utf-8").splitlines():
    if line.strip():
        r = json.loads(line)
        latest[r["run_id"]] = r
runs = list(latest.values())
problems = []
errors = [r for r in runs if r["status"] != "ok"]
for r in errors:
    problems.append(f"{r['run_id']}: {r.get('note', '')[:200]}")
if len(runs) < 6:
    problems.append(f"only {len(runs)} of 6 pilot runs finished")


def models_served(run_dir: Path) -> dict[str, int]:
    db = run_dir / "trove-data" / "trove.db"
    if not db.exists():
        return {}
    con = sqlite3.connect(f"file:{db.as_posix()}?mode=ro", uri=True)
    try:
        return dict(con.execute("SELECT model, COUNT(*) FROM requests GROUP BY model").fetchall())
    finally:
        con.close()


for r in runs:
    d = batch / "runs" / r["run_id"]
    served = models_served(d)
    if r["condition"] == "trove_full" and r["status"] == "ok" and not served.get(route_to):
        problems.append(f"{r['run_id']}: no request was served by {route_to}, so model_router never routed (models: {served})")
    if r["condition"] == "passthrough_plugin" and r["status"] == "ok" and not (d / "trove-hooks-data").exists():
        problems.append(f"{r['run_id']}: no hook data was saved, so the plugin may not have run")
    if r["status"] == "ok":
        print(f"  {r['condition']:<20} {r['task_id']:<34} ${r['cost_usd']:.3f}  passed={r['passed']}  served by {served}")

costs = [r["cost_usd"] for r in runs if r["status"] == "ok" and r.get("cost_usd") is not None]
if problems:
    print("\nSTOP: the pilot found problems. Do not run the study until they are fixed:")
    for p in problems:
        print("  -", p)
    sys.exit(1)

mean = st.fmean(costs)
# Size with a margin: the longest condition dominates, and later tasks may cost more than these two.
per_run = max(mean, max(costs) * 0.75)
per_look = per_run * STUDY_UNITS_PER_LOOK
looks = min(STUDY_REPEATS, math.floor(budget / per_look)) if per_look > 0 else STUDY_REPEATS
sizing = {"mean_cost_usd": round(mean, 4), "assumed_run_usd": round(per_run, 4), "per_look_usd": round(per_look, 2),
          "budget_usd": budget, "looks_that_fit": looks}
(batch / "sizing.json").write_text(json.dumps(sizing, indent=2) + "\n", encoding="utf-8")
print(f"\nMean cost per run ${mean:.3f} (estimated, from the gateway's records). Sizing with ${per_run:.3f} per run:")
print(f"one look (30 tasks x 3 conditions) is about ${per_look:.2f}; {looks} of {STUDY_REPEATS} look(s) fit ${budget:.0f}.")
if looks < 1:
    print("STOP: not even one look fits the budget. Raise BUDGET_USD, or tell Claude the pilot cost so the study can be resized.")
    sys.exit(1)
print("Next: bash bench/scripts/launch/03_run.sh")
