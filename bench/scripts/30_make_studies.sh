#!/usr/bin/env bash
# Runbook PART 7 (first half): write the two study files with spending caps taken from the pilot, then show
# `plan` for each. Spends nothing, locks nothing. Read the plan, then lock with 40_lock_studies.sh.
#     bash bench/scripts/30_make_studies.sh
# Settings (all optional, VAR=value before the command):
#     PILOT=bench/results/pilot-1   the finished pilot batch (its mean cost sets the caps)
#     TASKS=tasks/swebench-main     main set, relative to bench/
#     REPEATS=3                     repeats per task per condition (lower it if the cost or hours are too high)
#     SEED_A=20261201 SEED_B=20261202   fresh order/bootstrap seeds, one per study
#     HEADROOM=1.3                  batch cap = expected spend x this, plus one run cap
#     BATCH_CAP=90 RUN_CAP=0.5       set the caps yourself instead of deriving them from the pilot (dollars)
#     SUFFIX=1                      study ids are swebench-gateway-$SUFFIX and swebench-parity-$SUFFIX
# A registered study file can never be edited. If the files exist already this stops; to change anything use a new SUFFIX.
set -euo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

PILOT="${PILOT:-bench/results/pilot-1}"
TASKS="${TASKS:-tasks/swebench-main}"
REPEATS="${REPEATS:-3}"
SEED_A="${SEED_A:-20261201}"
SEED_B="${SEED_B:-20261202}"
HEADROOM="${HEADROOM:-1.3}"
SUFFIX="${SUFFIX:-1}"
A="bench/studies/swebench-gateway-$SUFFIX.yaml"
B="bench/studies/swebench-parity-$SUFFIX.yaml"

for f in "$A" "$B"; do
  [ ! -e "$f" ] || { echo "STOP: $f already exists. A study file is never rewritten; use SUFFIX=2 for a new pair." >&2; exit 1; }
done
[ -d "bench/$TASKS" ] || { echo "STOP: bench/$TASKS not found. Run bench/scripts/prepare_tasks.sh first." >&2; exit 1; }
NTASKS="$(ls "bench/$TASKS" | wc -l)"

STATS="$(python bench/scripts/pilot_stats.py "$PILOT" --json)"
read -r RUNCAP BATCHA BATCHB HOURS_A HOURS_B MEAN <<<"$(STATS="$STATS" NT="$NTASKS" RP="$REPEATS" HR="$HEADROOM" python - <<'EOF'
import json, math, os, sys
s = json.loads(os.environ["STATS"])
nt, rp, hr = int(os.environ["NT"]), int(os.environ["RP"]), float(os.environ["HR"])
if s["ok"] < 3 or s["mean_cost_usd"] is None:
    sys.exit("STOP: the pilot has fewer than 3 ok runs with a known cost; the caps cannot be set from it. Run 10_pilot_report.sh.")
if s["errors"] > s["runs"] / 2:
    sys.exit("STOP: more than half of the pilot runs errored. Fix that before spending on a study.")
mean, mx, wall = s["mean_cost_usd"], s["max_cost_usd"], s["mean_wall_s"]
quarter = lambda x: math.ceil(x * 4) / 4
run_cap = max(0.5, quarter(mx * 1.5))               # a run that costs 50% more than the worst pilot run is stopped
runs = nt * 4 * rp                                  # both studies use four conditions
batch = math.ceil((runs * mean * hr + run_cap) / 5) * 5
print(f"{run_cap:.2f} {batch:.1f} {batch:.1f} {runs * wall / 3600:.1f} {runs * wall / 3600:.1f} {mean:.3f}")
EOF
)"
RUNCAP="${RUN_CAP:-$RUNCAP}"; BATCHA="${BATCH_CAP:-$BATCHA}"; BATCHB="${BATCH_CAP:-$BATCHB}"
RUNS=$((NTASKS * 4 * REPEATS))
echo "tasks: $NTASKS   runs per study: $RUNS (4 conditions x $REPEATS repeats)"
echo "pilot mean cost per run: \$$MEAN  ->  run cap \$$RUNCAP, batch cap \$$BATCHA per study"
echo "expected time per study (runs go one after another): about $HOURS_A hours"

cat > "$A" <<EOF
id: swebench-gateway-$SUFFIX
title: "SWE-bench Verified subset: the gateway optimizers, every state Autopilot could reach"
hypothesis: >
  Turning on noise_normalizer, duplicate_result_collapser, or both lowers the billed cost per task
  against the same gateway with every optimizer off, without lowering the share of tasks that pass.
primary_metric: billed_cost_usd
secondary_metrics: [success_rate, latency_s]
baseline_condition: passthrough
conditions: [passthrough, alone_noise_normalizer, alone_duplicate_result_collapser, all_safe]
tasks: $TASKS
repeats: $REPEATS
order_seed: $SEED_A
cost_basis: agent
analysis:
  alpha: 0.05
  bootstrap_resamples: 10000
  bootstrap_seed: $SEED_A
  multiple_comparison: holm
ship_rule:
  min_cost_reduction_pct: 5
  success_margin_pts: 5
  min_pairs: 30
budget:
  batch_cap_usd: $BATCHA
  run_cap_usd: $RUNCAP
EOF

cat > "$B" <<EOF
id: swebench-parity-$SUFFIX
title: "SWE-bench Verified subset: no Trove vs plugin only vs gateway only vs both"
hypothesis: >
  The plugin's hooks alone deliver a meaningful share of the gateway's cost saving, and using both
  saves more than either alone, without lowering task success.
primary_metric: billed_cost_usd
secondary_metrics: [success_rate, latency_s]
baseline_condition: direct
conditions: [direct, hooks, all_safe, hooks_gateway]
tasks: $TASKS
repeats: $REPEATS
order_seed: $SEED_B
cost_basis: agent
parity: {none: direct, plugin: hooks, gateway: all_safe, both: hooks_gateway}
analysis:
  alpha: 0.05
  bootstrap_resamples: 10000
  bootstrap_seed: $SEED_B
  multiple_comparison: holm
ship_rule:
  min_cost_reduction_pct: 5
  success_margin_pts: 5
  min_pairs: 30
budget:
  batch_cap_usd: $BATCHB
  run_cap_usd: $RUNCAP
EOF
echo "wrote $A and $B"

cat <<EOF

Check the hours above are acceptable. If not, delete the two new study files (they are not locked yet) and
rerun with fewer repeats, e.g.  REPEATS=2 bash $0
(\`plan --study\` refuses an unlocked study, so the harness's own estimate is shown by the next script, right after locking.)
When happy: bash bench/scripts/40_lock_studies.sh
EOF
