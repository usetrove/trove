#!/usr/bin/env bash
# Runbook PARTS 9 and 10: sanity checks first, then the results tables. Free; calls nothing.
#     bash bench/scripts/60_analyze_study.sh A        # or B
# Optional: RESULTS=bench/results/<date>/<study-id> to point at a specific results folder.
set -euo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SUFFIX="${SUFFIX:-1}"
case "${1:-}" in
  A|a) ID="swebench-gateway-$SUFFIX";;
  B|b) ID="swebench-parity-$SUFFIX";;
  *) echo "usage: bash $0 A|B" >&2; exit 1;;
esac
B="${RESULTS:-$(ls -d bench/results/*/"$ID" 2>/dev/null | sort | tail -1)}"
[ -n "$B" ] && [ -d "$B" ] || { echo "STOP: no results folder for $ID under bench/results/. Has the study run?" >&2; exit 1; }
[ -f "$B/runs.csv" ] || { echo "STOP: $B/runs.csv missing: the run did not reach its report step." >&2; exit 1; }
echo "results: $B"

echo; echo "== PART 9. Sanity checks (read these before the numbers)"
B="$B" python - <<'EOF'
import collections, csv, json, os
b = os.environ["B"]
v = json.load(open(f"{b}/versions.json"))
bad = []
if v.get("trove_dirty") is not False: bad.append(f"trove_dirty is {v.get('trove_dirty')!r}, must be false")
if v.get("agent_version_reported") != v.get("agent_version_pinned"):
    bad.append(f"agent version reported {v.get('agent_version_reported')!r} != pinned {v.get('agent_version_pinned')!r}")
for k in ("trove_commit", "model", "agent_version_pinned", "agent_version_reported", "trove_dirty"):
    print(f"  {k}: {v.get(k)}")
rows = list(csv.DictReader(open(f"{b}/runs.csv")))
by = collections.defaultdict(collections.Counter)
for r in rows:
    by[r["condition"]][r["status"]] += 1
print("  statuses per condition:")
for c, k in sorted(by.items()):
    print(f"    {c}: {dict(k)}")
    if k["ok"] < 0.8 * sum(k.values()):
        bad.append(f"{c}: fewer than 80% of runs are ok, so the paired design lost data (see runs/<id>/agent_stderr_tail.txt)")
print()
print("  PROBLEMS:" if bad else "  No automatic problems found (still read report.md in full).")
for x in bad:
    print("   -", x)
EOF
echo "  Also open $B/report.md and read all of it, including any NOT MEASURABLE lines and gateway cross-check warnings."

if [ -f "$B/comparisons.csv" ]; then
  echo; echo "== PART 10. Registered comparisons (cost figures are ESTIMATED; pass/fail, time and tokens are MEASURED)"
  B="$B" python - <<'EOF'
import csv, os
rows = list(csv.DictReader(open(os.environ["B"] + "/comparisons.csv")))
hdr = ("condition", "vs", "pairs", "tasks", "cost change %", "95% CI", "success diff (pts)", "adj p", "verdict")
print(" | ".join(hdr)); print(" | ".join("---" for _ in hdr))
for r in rows:
    print(" | ".join([r["condition"], r["reference"], r["n_pairs"], r["n_tasks"],
        f'{float(r["median_cost_change_pct"]):.1f}', f'{float(r["ci_lo"]):.1f} to {float(r["ci_hi"]):.1f}',
        f'{float(r["success_diff_pts"]):.1f}', r["p_adjusted"], r["verdict"]]))
EOF
fi

echo; echo "== Per-condition raw table (ok runs only)"
B="$B" python - <<'EOF'
import collections, csv, os, statistics as st
rows = [r for r in csv.DictReader(open(os.environ["B"] + "/runs.csv")) if r["status"] == "ok"]
by = collections.defaultdict(list)
for r in rows: by[r["condition"]].append(r)
print("condition | runs | passed | mean cost $ (estimated) | mean wall s"); print("--- | --- | --- | --- | ---")
for c, rs in sorted(by.items()):
    cost = [float(r["agent_cost_usd"] or r["cost_usd"]) for r in rs if (r.get("agent_cost_usd") or r["cost_usd"])]
    print(f'{c} | {len(rs)} | {sum(r["passed"] == "True" for r in rs)} of {len(rs)} | '
          f'{(st.fmean(cost) if cost else float("nan")):.4f} | {st.fmean(float(r["wall_s"]) for r in rs):.0f}')
EOF

echo; echo "Reading the verdict: a condition ships only if the whole 95% interval for the cost change is at or below -5%,"
echo "success does not fall by more than 5 points, there are at least 30 pairs and the Holm-adjusted p is below 0.05."
echo "Otherwise it says NEED MORE DATA or does not ship. Never round a result up."
echo
echo "Next: bash bench/scripts/70_publish_prep.sh ${1^^}"
