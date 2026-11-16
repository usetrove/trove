#!/usr/bin/env bash
# Runbook PART D, step D.2: write the single-hook study from its template, with caps from the hooks pilot.
# Spends nothing. Run once to write the file and read the numbers; run again with LOCK=1 to lock and commit it.
#     bash bench/scripts/81_hooks_study.sh
#     LOCK=1 bash bench/scripts/81_hooks_study.sh
# Then: bash bench/scripts/50_run_study.sh H     and     bash bench/scripts/60_analyze_study.sh H
# Settings: PILOT=bench/results/pilot-hooks-1  SEED=20261203  HEADROOM=1.3  SUFFIX=1  REPEATS=3
set -euo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PILOT="${PILOT:-bench/results/pilot-hooks-1}"
SEED="${SEED:-20261203}"
HEADROOM="${HEADROOM:-1.3}"
SUFFIX="${SUFFIX:-1}"
REPEATS="${REPEATS:-3}"
PROBE="${PROBE:-/tmp/hookprobe.json}"
F="bench/studies/hooks-single-$SUFFIX.yaml"

if [ ! -f "$F" ]; then
  STATS="$(python bench/scripts/pilot_stats.py "$PILOT" --json)"
  NT="$(ls bench/tasks/swebench-main | wc -l)"
  read -r RUNCAP BATCH RUNS HOURS <<<"$(STATS="$STATS" NT="$NT" RP="$REPEATS" HR="$HEADROOM" python - <<'PY'
import json, math, os, sys
s = json.loads(os.environ["STATS"]); nt, rp, hr = int(os.environ["NT"]), int(os.environ["RP"]), float(os.environ["HR"])
if s["ok"] < 3 or s["mean_cost_usd"] is None: sys.exit("STOP: the hooks pilot has fewer than 3 ok runs.")
if s["errors"] > s["runs"] / 2: sys.exit("STOP: more than half of the hooks pilot runs errored.")
cap = max(0.5, math.ceil(s["max_cost_usd"] * 1.5 * 4) / 4)
runs = nt * 3 * rp
print(f'{cap:.2f} {math.ceil((runs * s["mean_cost_usd"] * hr + cap) / 5) * 5:.1f} {runs} {runs * s["mean_wall_s"] / 3600:.1f}')
PY
)"
  sed -e "s/<NEW_SEED>/$SEED/g" -e "s/batch_cap_usd: <FROM_PILOT>/batch_cap_usd: $BATCH/" \
      -e "s/run_cap_usd: <FROM_PILOT>/run_cap_usd: $RUNCAP/" -e "s/^repeats: .*/repeats: $REPEATS/" \
      -e "s/^id: hooks-single-1/id: hooks-single-$SUFFIX/" bench/studies/hooks-single-1.yaml.template > "$F"
  ! grep -n '<[A-Z_]*>' "$F" | grep -v '^[0-9]*:#' || { echo "STOP: a placeholder is left in $F" >&2; exit 1; }
  echo "wrote $F: $RUNS runs, batch cap \$$BATCH, run cap \$$RUNCAP, about $HOURS hours."
  echo "Check those, then lock with:  LOCK=1 bash $0"
  echo "(Too many hours or dollars? delete $F and rerun with REPEATS=2.)"
  exit 0
fi

[ -n "${LOCK:-}" ] || { echo "$F exists. Lock and commit it with:  LOCK=1 bash $0"; exit 0; }
[ -f "$PROBE" ] || { echo "STOP: $PROBE missing." >&2; exit 1; }
python -m trovebench prereg "$F"
python -m trovebench plan --study "$F" --hook-capabilities "$PROBE"
git add "$F" "${F%.yaml}.lock.json"
git commit -q -m "bench: register hooks-single-$SUFFIX" -- "$F" "${F%.yaml}.lock.json"
[ -z "$(git status --porcelain)" ] || { echo "WARNING: tree not clean (trove_dirty):" >&2; git status --short >&2; exit 1; }
echo "Locked and committed. Next: bash bench/scripts/50_run_study.sh H"
