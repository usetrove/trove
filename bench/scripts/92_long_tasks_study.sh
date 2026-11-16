#!/usr/bin/env bash
# Step 2: do the optimizers save money where there is more to save, i.e. in LONG sessions? Reuses tasks already on disk:
# no downloads, nothing deleted. Writes (does not run) a registered study on the long half of your 35 tasks.
#
# HOW THE TASKS ARE CHOSEN (fixed BEFORE any new run, from the first study's passthrough runs only):
#   keep a task if its passthrough run in swebench-gateway-2 took more turns than the TURN_PCT percentile of those runs.
# Turn count is measured and does not depend on whether the optimizer helps, and the new study uses fresh runs, so
# picking tasks this way does not bias the treatment effect. It does mean the answer covers LONG SESSIONS ONLY, and the study
# says so. Never choose tasks by how much an optimizer saved on them: that is cherry-picking and the result would be void.
#
#     bash bench/scripts/92_long_tasks_study.sh                # writes bench/tasks/long-turns and the study file (suffix 4)
#     SUFFIX=4 ONLY_A=1 bash bench/scripts/40_lock_studies.sh  (or LOCK=1 here)
#     export ANTHROPIC_API_KEY=...;  SUFFIX=4 bash bench/scripts/50_run_study.sh A;  SUFFIX=4 bash bench/scripts/60_analyze_study.sh A
# Settings: TURN_PCT=50 (keep tasks above this percentile)  REPEATS=8  SUFFIX=4  FROM=bench/results/<date>/swebench-gateway-2
set -euo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
command -v python >/dev/null || { echo "STOP: python not found. Run: source .venv/bin/activate" >&2; exit 1; }
TURN_PCT="${TURN_PCT:-50}"
REPEATS="${REPEATS:-8}"
SUFFIX="${SUFFIX:-4}"
FROM="${FROM:-$(ls -d bench/results/*/swebench-gateway-2 2>/dev/null | sort | tail -1)}"
SRC="bench/tasks/swebench-main"
DST="bench/tasks/long-turns"
die() { printf '\nSTOP: %s\n' "$*" >&2; exit 1; }
[ -f "$FROM/ledger.jsonl" ] || die "no ledger in FROM=$FROM."
[ -d "$SRC" ] || die "$SRC not found."
[ ! -e "$DST" ] || die "$DST already exists. Delete it (it is a copy) to choose again with a different TURN_PCT, or the choice is not fixed in advance."

LIST="$(python - "$FROM/ledger.jsonl" "$TURN_PCT" <<'PY'
import json, sys, statistics as st
latest = {}
for line in open(sys.argv[1], encoding="utf-8"):
    if line.strip():
        r = json.loads(line); latest[r["run_id"]] = r
turns = {r["task_id"]: r["turns"] for r in latest.values() if r["condition"] == "passthrough" and r["status"] == "ok"}
if len(turns) < 20:
    sys.exit("STOP: fewer than 20 passthrough runs to rank by length.")
v = sorted(turns.values()); k = int(len(v) * float(sys.argv[2]) / 100)
cut = v[min(k, len(v) - 1)]
keep = sorted(t for t, n in turns.items() if n > cut)
print(cut, *keep)
PY
)"
CUT="${LIST%% *}"; IDS="${LIST#* }"
N=$(echo "$IDS" | wc -w)
echo "turn cut-off: more than $CUT turns in the first study's passthrough run  ->  $N tasks"
[ "$N" -ge 12 ] || die "only $N long tasks; a task-level interval needs about 12 or more. Lower TURN_PCT (e.g. TURN_PCT=30)."
mkdir -p "$DST"
for id in $IDS; do [ -d "$SRC/$id" ] || die "task $id missing from $SRC"; cp -r "$SRC/$id" "$DST/$id"; done
echo "copied $N tasks to $DST (git-ignored copy; the originals are untouched)"

NOTE="Tasks were chosen before any run: those whose passthrough run in study swebench-gateway-2 took more than $CUT turns ($N of $(ls "$SRC" | wc -l)). The result applies to long sessions only." \
TASKS="tasks/long-turns" MIN_TASKS=12 REPEATS="$REPEATS" SUFFIX="$SUFFIX" \
  bash bench/scripts/90_final_study.sh
