#!/usr/bin/env bash
# A study sized to reach a verdict other than NEED MORE DATA, if the optimizer really works.
# Two conditions only (passthrough vs all_safe), so there is one comparison and no Holm penalty.
# One run per task (REPEATS=1): every pair is a different task, so the bootstrap's independence assumption holds.
#
# Why about 350 tasks: the ship rule needs the 95% interval of the SUCCESS difference to stay above -5 points
# (stats.py compare()). With pass/fail disagreeing on ~12% of tasks that takes roughly 350-400 pairs for an 80%
# chance of passing, if the optimizer truly costs no accuracy. The cost rule alone needs far fewer (about 100 pairs for a
# true 15% saving). These are estimates from the interval widths of study swebench-gateway-2, not exact: the script
# prints a better figure from that study's own pairs.csv when it is available (POWER_FROM=...).
#
# It does NOT spend API money. It downloads Docker images (disk!), validates tasks, writes + registers the study.
#     tmux new -s final
#     bash bench/scripts/90_final_study.sh
# Then, in order (these spend money; the last two are free):
#     SUFFIX=3 ONLY_A=1 bash bench/scripts/40_lock_studies.sh      (run by this script when LOCK=1)
#     SUFFIX=3 bash bench/scripts/50_run_study.sh A
#     SUFFIX=3 bash bench/scripts/60_analyze_study.sh A
#     SUFFIX=3 bash bench/scripts/70_publish_prep.sh A
# Settings (VAR=value before the command):
#     MAIN=350          tasks in the study (each runs once per condition)
#     N=500             candidates to fetch (SWE-bench Verified has 500 in total, so this is the ceiling)
#     SEED=20261201     task sample seed. SEED_S=20261203 is the order/bootstrap seed.
#     SUFFIX=3          study id is swebench-gateway-$SUFFIX
#     COST_FROM=bench/results/2026-11-15/swebench-gateway-2   a finished batch whose mean cost sets the caps
#     HEADROOM=1.5      batch cap = expected spend x this, plus one run cap
#     LOCK=1            also register + commit the study file (needs a clean git tree)
#     DISK_GB=250       free space required before fetching (500 images are far more than the 120 GB of a 35-task set)
set -euo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

MAIN="${MAIN:-350}"
N="${N:-500}"
SEED="${SEED:-20261201}"
SEED_S="${SEED_S:-20261203}"
SUFFIX="${SUFFIX:-3}"
COST_FROM="${COST_FROM:-$(ls -d bench/results/*/swebench-gateway-2 2>/dev/null | sort | tail -1)}"
HEADROOM="${HEADROOM:-1.5}"
DISK_GB="${DISK_GB:-250}"
TASKROOT="bench/tasks/final"                       # git-ignored by bench/tasks/*/
STUDY="bench/studies/swebench-gateway-$SUFFIX.yaml"

die() { printf '\nSTOP: %s\n' "$*" >&2; exit 1; }
[ ! -e "$STUDY" ] || die "$STUDY already exists. A study file is never rewritten; use SUFFIX=4 for a new one."
[ "$MAIN" -ge 30 ] || die "MAIN must be at least 30 (the ship rule's minimum pairs)."
[ -n "$COST_FROM" ] && [ -f "$COST_FROM/ledger.jsonl" ] || die "COST_FROM has no ledger.jsonl. Point it at a finished batch, e.g. bench/results/2026-11-15/swebench-gateway-2."

echo "== 0. Better sample-size estimate from your own data (free)"
python bench/scripts/power_check.py "$COST_FROM" || echo "(power check skipped; continuing with the rule-of-thumb size MAIN=$MAIN)"

if [ ! -d "$TASKROOT/swebench-main" ]; then
  echo; echo "== 1. Fetch + validate tasks (slow, downloads images, no API money)"
  AVAIL_GB="$(df -BG --output=avail "$HOME" | tail -1 | tr -dc '0-9')"
  [ "${AVAIL_GB:-0}" -ge "$DISK_GB" ] || die "only ${AVAIL_GB} GB free; $N tasks need about $DISK_GB GB. Free space (docker system prune), or lower N and MAIN (then the study is smaller and may still say NEED MORE DATA)."
  # WARM=1 because prepare_tasks.sh wants a warm-up set; this study does not use it.
  TASKS="$TASKROOT" SEED="$SEED" N="$N" MAIN="$MAIN" WARM=1 PILOT=1 LOGDIR="bench/results/prep-final" \
    bash bench/scripts/prepare_tasks.sh
else
  echo "== 1. $TASKROOT/swebench-main already exists: reusing it."
fi
NT="$(ls "$TASKROOT/swebench-main" | wc -l)"
[ "$NT" -ge 30 ] || die "only $NT validated tasks in the study set."
[ "$NT" -ge "$MAIN" ] || echo "NOTE: only $NT of the $MAIN wanted tasks validated. The study will have $NT pairs, so it may still say NEED MORE DATA."

echo; echo "== 2. Write the study file (caps from the measured mean cost of $COST_FROM)"
STATS="$(python bench/scripts/pilot_stats.py "$COST_FROM" --json)"
read -r RUNCAP BATCH HOURS MEAN <<<"$(STATS="$STATS" NT="$NT" HR="$HEADROOM" python - <<'EOF'
import json, math, os, sys
s = json.loads(os.environ["STATS"]); nt = int(os.environ["NT"]); hr = float(os.environ["HR"])
if s["ok"] < 3 or s["mean_cost_usd"] is None:
    sys.exit("STOP: COST_FROM has fewer than 3 ok runs with a known cost.")
mean, mx, wall = s["mean_cost_usd"], s["max_cost_usd"], s["mean_wall_s"]
run_cap = max(0.5, math.ceil(mx * 1.5 * 4) / 4)
runs = nt * 2
batch = math.ceil((runs * mean * hr + run_cap) / 5) * 5
print(f"{run_cap:.2f} {batch:.1f} {runs * wall / 3600:.1f} {mean:.3f}")
EOF
)"
echo "tasks: $NT   runs: $((NT * 2))   mean cost per run \$$MEAN   run cap \$$RUNCAP   batch cap \$$BATCH   about $HOURS hours (runs go one after another)"

cat > "$STUDY" <<EOF
id: swebench-gateway-$SUFFIX
title: "SWE-bench Verified, $NT tasks: all safe gateway optimizers vs the same gateway with them off"
hypothesis: >
  Turning on all_safe lowers the billed cost per task by at least 5% against the same gateway with every
  optimizer off, without lowering the share of tasks that pass by more than 5 points.
primary_metric: billed_cost_usd
secondary_metrics: [success_rate, latency_s]
baseline_condition: passthrough
conditions: [passthrough, all_safe]
tasks: tasks/final/swebench-main
repeats: 1
order_seed: $SEED_S
cost_basis: agent
analysis:
  alpha: 0.05
  bootstrap_resamples: 10000
  bootstrap_seed: $SEED_S
  multiple_comparison: holm
ship_rule:
  min_cost_reduction_pct: 5
  success_margin_pts: 5
  min_pairs: 30
budget:
  batch_cap_usd: $BATCH
  run_cap_usd: $RUNCAP
EOF
echo "wrote $STUDY"

if [ -n "${LOCK:-}" ]; then
  echo; echo "== 3. Register + commit"
  SUFFIX="$SUFFIX" ONLY_A=1 bash bench/scripts/40_lock_studies.sh
else
  cat <<EOF

Not locked yet. Read the numbers above, then:
  SUFFIX=$SUFFIX ONLY_A=1 bash bench/scripts/40_lock_studies.sh
  export ANTHROPIC_API_KEY=...
  SUFFIX=$SUFFIX bash bench/scripts/50_run_study.sh A
  SUFFIX=$SUFFIX bash bench/scripts/60_analyze_study.sh A

Do NOT look at partial results and stop or extend the study based on them: that inflates false positives.
Whatever the final verdict says is the answer, including NEED MORE DATA or DON'T SHIP.
EOF
fi
