#!/usr/bin/env bash
# A tighter second study of passthrough vs all_safe on the 35 tasks that are ALREADY prepared (no new downloads,
# no new disk use, nothing is deleted). Each task runs REPEATS times per condition.
#
# What this can and cannot do (read before spending):
#   - Repeats of one task are not independent. The analysis resamples whole TASKS (stats.compare, inference.py), so the
#     interval is honest: it narrows only as far as repeats really add information. Repeats mostly shrink the
#     run-to-run noise; they do not add new kinds of task, so the answer covers THESE 35 tasks, not SWE-bench in general.
#   - It cannot create a saving. If all_safe does not lower cost on these tasks the verdict will say NEED MORE DATA or
#     DON'T SHIP, and that is what gets published.
#
# Spends no API money until you run 50_run_study.sh. Run in tmux, inside the virtualenv:
#     cd ~/trove && source .venv/bin/activate
#     bash bench/scripts/90_final_study.sh                       # writes the study file, shows the plan
#     SUFFIX=3 ONLY_A=1 bash bench/scripts/40_lock_studies.sh    # register + commit it (or LOCK=1 here)
#     export ANTHROPIC_API_KEY=...
#     SUFFIX=3 bash bench/scripts/50_run_study.sh A
#     SUFFIX=3 bash bench/scripts/60_analyze_study.sh A
# Settings (VAR=value before the command):
#     REPEATS=6         runs per task per condition. Total runs = tasks x 2 x REPEATS
#     SEED_S=20261203   order/bootstrap seed
#     SUFFIX=3          study id is swebench-gateway-$SUFFIX
#     TASKS=tasks/swebench-main   (relative to bench/) the tasks to reuse
#     COST_FROM=...     a finished batch whose mean cost sets the caps (default: swebench-gateway-2)
#     HEADROOM=1.5      batch cap = expected spend x this, plus one run cap
#     MIN_TASKS=30      fewest tasks allowed (92_long_tasks_study.sh lowers it; under about 12 tasks a task-level interval is unreliable)
#     NOTE="..."        extra sentence for the study's hypothesis (92 uses it to record how the tasks were chosen)
#     LOCK=1            also register + commit the study file (needs a clean git tree)
set -euo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

REPEATS="${REPEATS:-6}"
SEED_S="${SEED_S:-20261203}"
SUFFIX="${SUFFIX:-3}"
TASKS="${TASKS:-tasks/swebench-main}"
COST_FROM="${COST_FROM:-$(ls -d bench/results/*/swebench-gateway-2 2>/dev/null | sort | tail -1)}"
HEADROOM="${HEADROOM:-1.5}"
MIN_TASKS="${MIN_TASKS:-30}"
STUDY="bench/studies/swebench-gateway-$SUFFIX.yaml"

die() { printf '\nSTOP: %s\n' "$*" >&2; exit 1; }
command -v python >/dev/null || die "python not found. Activate the virtualenv first:  source .venv/bin/activate"
[ ! -e "$STUDY" ] || die "$STUDY already exists. A study file is never rewritten; use SUFFIX=4 for a new one."
[ -d "bench/$TASKS" ] || die "bench/$TASKS not found. It is the task set the first study used."
[ -n "$COST_FROM" ] && [ -f "$COST_FROM/ledger.jsonl" ] || die "COST_FROM has no ledger.jsonl. Point it at a finished batch, e.g. bench/results/2026-11-15/swebench-gateway-2."
[ "$REPEATS" -ge 2 ] || die "REPEATS must be at least 2 (use the first study if you only want one run per task)."
NT="$(ls "bench/$TASKS" | wc -l)"
[ "$NT" -ge "$MIN_TASKS" ] || die "only $NT tasks in bench/$TASKS; this needs at least $MIN_TASKS (MIN_TASKS=...)."
[ "$((NT * REPEATS))" -ge 30 ] || die "$NT tasks x $REPEATS repeats is under the ship rule's 30 pairs; raise REPEATS."

echo "== Disk check (this study downloads nothing new; the images for these $NT tasks must still be present)"
df -h "$HOME" | tail -1
AVAIL_GB="$(df -BG --output=avail "$HOME" | tail -1 | tr -dc '0-9')"
[ "${AVAIL_GB:-0}" -ge 5 ] || die "only ${AVAIL_GB} GB free; the runs write logs and containers. Free space first (docker system df shows what Docker holds)."
echo "images present for the first study's tasks (a missing one is re-downloaded on first use):"
docker images --format '{{.Repository}}' 2>/dev/null | grep -c . || true

echo; echo "== Estimate from your own first study (ESTIMATED; free)"
python bench/scripts/power_check.py "$COST_FROM" || echo "(power check skipped)"
echo "Note: that estimate treats runs as independent tasks. Here only $NT distinct tasks exist, so the real interval will be wider"
echo "than the estimate for the cost rule. The honest answer is whatever 60_analyze_study.sh prints."

echo; echo "== Write the study file (caps from the measured mean cost of $COST_FROM)"
STATS="$(python bench/scripts/pilot_stats.py "$COST_FROM" --json)"
read -r RUNCAP BATCH HOURS MEAN RUNS <<<"$(STATS="$STATS" NT="$NT" RP="$REPEATS" HR="$HEADROOM" python - <<'EOF'
import json, math, os, sys
s = json.loads(os.environ["STATS"]); nt = int(os.environ["NT"]); rp = int(os.environ["RP"]); hr = float(os.environ["HR"])
if s["ok"] < 3 or s["mean_cost_usd"] is None:
    sys.exit("STOP: COST_FROM has fewer than 3 ok runs with a known cost.")
mean, mx, wall = s["mean_cost_usd"], s["max_cost_usd"], s["mean_wall_s"]
run_cap = max(0.5, math.ceil(mx * 1.5 * 4) / 4)
runs = nt * 2 * rp
batch = math.ceil((runs * mean * hr + run_cap) / 5) * 5
print(f"{run_cap:.2f} {batch:.1f} {runs * wall / 3600:.1f} {mean:.3f} {runs}")
EOF
)"
echo "tasks: $NT   repeats: $REPEATS   runs: $RUNS   mean cost per run \$$MEAN   run cap \$$RUNCAP   batch cap \$$BATCH   about $HOURS hours (one after another)"
echo "If that is too much, rerun with a lower REPEATS (before locking)."

cat > "$STUDY" <<EOF
id: swebench-gateway-$SUFFIX
title: "SWE-bench Verified, $NT tasks x $REPEATS repeats: all safe gateway optimizers vs the same gateway with them off"
hypothesis: >
  Turning on all_safe lowers the billed cost per task by at least 5% against the same gateway with every
  optimizer off, without lowering the share of tasks that pass by more than 5 points. Repeats of one task are
  analysed as one cluster, so the result covers these $NT tasks only. ${NOTE:-}
primary_metric: billed_cost_usd
secondary_metrics: [success_rate, latency_s]
baseline_condition: passthrough
conditions: [passthrough, all_safe]
tasks: $TASKS
repeats: $REPEATS
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
  echo; echo "== Register + commit"
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
