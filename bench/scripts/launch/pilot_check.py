"""Check a finished pilot and size its study from it. Reads only the pilot folder and the study file; calls nothing.

    python bench/scripts/launch/pilot_check.py <pilot batch> <study.yaml> <budget usd>

Stops (exit 1) unless:
  * every run was gradeable (no harness errors),
  * routed conditions (trove_full, alone_model_router) had requests served by the study's route_to model,
  * default_model ran every request on route_to,
  * plugin conditions really ran with the plugin (hook data saved next to the run).
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

import yaml

batch, study_path, budget = Path(sys.argv[1]), Path(sys.argv[2]), float(sys.argv[3])
study = yaml.safe_load(study_path.read_text(encoding="utf-8"))
route_to = study.get("route_to") or ""
n_tasks = len([p for p in (Path("bench") / study["tasks"]).iterdir() if p.is_dir()])
units_per_look = n_tasks * len(study["conditions"])
repeats = int(study["repeats"])
staged = bool(study.get("staged"))

latest = {}
for line in (batch / "ledger.jsonl").read_text(encoding="utf-8").splitlines():
    if line.strip():
        r = json.loads(line)
        latest[r["run_id"]] = r
runs = list(latest.values())
want = 2 * len(study["conditions"])
problems = [f"{r['run_id']}: {r.get('note', '')[:200]}" for r in runs if r["status"] != "ok"]
if len(runs) < want:
    problems.append(f"only {len(runs)} of {want} pilot runs finished")


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
    cond, ok = r["condition"], r["status"] == "ok"
    if ok and cond in ("trove_full", "alone_model_router") and not served.get(route_to):
        problems.append(f"{r['run_id']}: no request was served by {route_to}, so model_router never routed (models: {served})")
    if ok and cond == "default_model" and set(served) - {route_to}:
        problems.append(f"{r['run_id']}: default_model sent requests to other models: {served}")
    if ok and cond in ("passthrough_plugin",) and not (d / "trove-hooks-data").exists():
        problems.append(f"{r['run_id']}: no hook data was saved, so the plugin may not have run")
    if ok:
        print(f"  {cond:<26} {r['task_id']:<34} ${r['cost_usd']:.3f}  passed={r['passed']}  served by {served}")

if problems:
    print("\nSTOP: the pilot found problems. Do not run the study until they are fixed:")
    for p in problems:
        print("  -", p)
    sys.exit(1)

costs = [r["cost_usd"] for r in runs if r["status"] == "ok" and r.get("cost_usd") is not None]
mean = st.fmean(costs)
# Size with a margin: the costliest condition dominates, and other tasks may cost more than these two.
per_run = max(mean, max(costs) * 0.75)
per_look = per_run * units_per_look
fit = math.floor(budget / per_look) if per_look > 0 else repeats
looks = min(repeats, fit) if staged else (repeats if per_look * repeats <= budget else 0)
sizing = {"study": study["id"], "mean_cost_usd": round(mean, 4), "assumed_run_usd": round(per_run, 4),
          "per_look_usd": round(per_look, 2), "budget_usd": budget, "looks_that_fit": looks, "staged": staged}
(batch / "sizing.json").write_text(json.dumps(sizing, indent=2) + "\n", encoding="utf-8")
print(f"\nMean cost per run ${mean:.3f} (estimated, from the run records). Sizing with ${per_run:.3f} per run:")
print(f"one repeat ({n_tasks} tasks x {len(study['conditions'])} conditions) is about ${per_look:.2f}; "
      + (f"{looks} of {repeats} look(s) fit ${budget:.0f}." if staged else
         f"the whole study ({repeats} repeat(s)) {'fits' if looks else 'does NOT fit'} ${budget:.0f}."))
if looks < 1:
    print("STOP: the study does not fit the budget. Raise BUDGET_USD (up to the study's batch cap), or tell Claude the "
          "pilot cost so the study can be resized before it is registered.")
    sys.exit(1)
print("Next: bash bench/scripts/launch/03_run.sh" + ("" if "launch-2" in study_path.name else f"   (with STUDY={study_path})"))
