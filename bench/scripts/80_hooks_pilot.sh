#!/usr/bin/env bash
# Runbook PART D, step D.1: pilot the single-hook conditions on the 3 pilot tasks (3 tasks x 3 conditions = 9 runs).
# SPENDS REAL API MONEY (a few dollars) and needs the hook probe from 20_hook_probe.sh. Resumes if run again.
#     export ANTHROPIC_API_KEY=...
#     bash bench/scripts/80_hooks_pilot.sh
set -euo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PROBE="${PROBE:-/tmp/hookprobe.json}"
BATCH="${BATCH:-bench/results/pilot-hooks-1}"
CFG=/tmp/hooks-pilot.yaml

[ -n "${ANTHROPIC_API_KEY:-}" ] || { echo "STOP: ANTHROPIC_API_KEY is not set in this shell." >&2; exit 1; }
[ -f "$PROBE" ] || { echo "STOP: $PROBE missing. Run bash bench/scripts/20_hook_probe.sh first." >&2; exit 1; }
[ -d bench/tasks/swebench-pilot ] || { echo "STOP: bench/tasks/swebench-pilot missing (prepare_tasks.sh makes it)." >&2; exit 1; }
[ -z "$(git status --porcelain)" ] || { echo "STOP: git tree not clean:" >&2; git status --short >&2; exit 1; }

python - <<'PY'
import yaml
d = yaml.safe_load(open("bench/config.yaml"))
d["conditions"] = ["direct", "alone_hook_repeat_read", "alone_hook_quiet_commands"]
d["ship_rule"]["reference"] = "direct"
yaml.safe_dump(d, open("/tmp/hooks-pilot.yaml", "w"), sort_keys=False)
PY
python -m trovebench plan --config "$CFG" --tasks bench/tasks/swebench-pilot --repeats 1 --hook-capabilities "$PROBE"
python -u -m trovebench run --config "$CFG" --tasks bench/tasks/swebench-pilot --repeats 1 \
       --hook-capabilities "$PROBE" --yes --batch "$BATCH" 2>&1 | tee -a /tmp/pilot-hooks.log
echo
python bench/scripts/pilot_stats.py "$BATCH"
echo
echo "Did the hooks act at all? (if a hook acted in almost none of the runs, a full study of it will probably say 'no clear difference')"
python bench/tools/hook_activity.py "$BATCH"
echo
echo "Next: bash bench/scripts/81_hooks_study.sh"
