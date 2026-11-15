"""Summarise a finished (or running) batch from its ledger: statuses, mean cost, mean wall time.

Used by 10_pilot_report.sh and 30_make_studies.sh. Reads only <batch>/ledger.jsonl; calls nothing.
    python bench/scripts/pilot_stats.py bench/results/pilot-1          # human summary
    python bench/scripts/pilot_stats.py bench/results/pilot-1 --json   # numbers for other scripts
"""

import collections
import json
import statistics as st
import sys
from pathlib import Path


def load(batch: Path) -> dict:
    ledger = batch / "ledger.jsonl"
    if not ledger.exists():
        sys.exit(f"STOP: {ledger} not found. Has the pilot started, and is the folder name right?")
    latest = {}          # the last line per run wins, as in the harness
    for line in ledger.read_text(encoding="utf-8").splitlines():
        if line.strip():
            rec = json.loads(line)
            latest[rec["run_id"]] = rec
    recs = list(latest.values())
    ok = [r for r in recs if r["status"] == "ok"]
    costs = [r["cost_usd"] for r in ok if r.get("cost_usd") is not None]
    by_cond = collections.defaultdict(lambda: [0, 0])
    for r in ok:
        by_cond[r["condition"]][0] += 1
        by_cond[r["condition"]][1] += bool(r["passed"])
    return {
        "runs": len(recs),
        "ok": len(ok),
        "errors": sum(r["status"] == "error" for r in recs),
        "passed": sum(bool(r["passed"]) for r in ok),
        "mean_cost_usd": st.fmean(costs) if costs else None,
        "max_cost_usd": max(costs) if costs else None,
        "mean_wall_s": st.fmean(r["wall_s"] for r in ok) if ok else None,
        "by_condition": {c: {"ok": a, "passed": p} for c, (a, p) in sorted(by_cond.items())},
        "notes": sorted({(r.get("note") or "")[:120] for r in recs if r["status"] != "ok" and r.get("note")})[:5],
    }


if __name__ == "__main__":
    args = [a for a in sys.argv[1:] if not a.startswith("--")]
    if len(args) != 1:
        sys.exit(__doc__)
    s = load(Path(args[0]))
    if "--json" in sys.argv:
        print(json.dumps(s))
        sys.exit(0)
    print(f"runs recorded: {s['runs']}   ok: {s['ok']}   error (not graded): {s['errors']}   passed: {s['passed']} of {s['ok']}")
    if s["mean_cost_usd"] is not None:
        print(f"mean cost per ok run: ${s['mean_cost_usd']:.3f}   max: ${s['max_cost_usd']:.3f}   mean wall: {s['mean_wall_s']:.0f}s")
    for c, v in s["by_condition"].items():
        print(f"  {c:36} {v['passed']} of {v['ok']} passed")
    for n in s["notes"]:
        print(f"  note: {n}")
