#!/usr/bin/env bash
# Step 1 of "how do I get a savings number": did the optimizers do anything in the first study? Free; reads saved run data.
#     bash bench/scripts/91_optimizer_activity.sh                      # defaults to the newest swebench-gateway-2 results
#     RESULTS=bench/results/<date>/<study-id> bash bench/scripts/91_optimizer_activity.sh
# It tells you which of these is true, and so which next step is worth paying for:
#   - the optimizers removed almost nothing (tasks too short: use 92_long_tasks_study.sh),
#   - they removed a lot but the bill did not fall (cache damage or overhead: look at the cache-read share it prints),
#   - they were silently downgraded to shadow in some runs (a harness problem to fix before any new study).
set -euo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
command -v python >/dev/null || { echo "STOP: python not found. Run: source .venv/bin/activate" >&2; exit 1; }
SUFFIX="${SUFFIX:-2}"
B="${RESULTS:-$(ls -d bench/results/*/swebench-gateway-"$SUFFIX" 2>/dev/null | sort | tail -1)}"
[ -n "$B" ] && [ -d "$B" ] || { echo "STOP: no results folder found. Set RESULTS=bench/results/<date>/<study-id>." >&2; exit 1; }
python bench/scripts/activity.py "$B"
echo
echo "Next: bash bench/scripts/92_long_tasks_study.sh (if long sessions removed more), or bash bench/scripts/93_labelled_numbers.sh"
