#!/usr/bin/env bash
# Step 4: test levers other than the two gateway optimizers: the two hooks, each by itself, against no Trove.
# Hooks work without a gateway, so they reach subscription users too. This only chains the existing scripts in order,
# so each stage is a separate, explicit command; the ones that spend money say so and stop at a clear boundary.
#     bash bench/scripts/94_other_levers.sh            # shows where you are and the next command
#     bash bench/scripts/94_other_levers.sh pilot      # SPENDS a few dollars (9 runs)      -> 80_hooks_pilot.sh
#     bash bench/scripts/94_other_levers.sh study      # free: writes the study; LOCK=1 registers it -> 81_hooks_study.sh
#     bash bench/scripts/94_other_levers.sh run        # SPENDS real money, up to the study's cap -> 50_run_study.sh H
#     bash bench/scripts/94_other_levers.sh analyze    # free: results, plus how often each hook acted -> 60 H + hook_activity.py
# Read the hook_activity output BEFORE the table: a hook that rarely acted cannot show a saving however good it is.
set -euo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
command -v python >/dev/null || { echo "STOP: python not found. Run: source .venv/bin/activate" >&2; exit 1; }
PROBE="${PROBE:-/tmp/hookprobe.json}"
SUFFIX="${SUFFIX:-1}"
STEP="${1:-status}"
have() { [ -e "$1" ] && echo yes || echo no; }

case "$STEP" in
  status)
    echo "hook probe ($PROBE):            $(have "$PROBE")   (make it with: bash bench/scripts/20_hook_probe.sh)"
    echo "hooks pilot results:            $(have bench/results/pilot-hooks-1/ledger.jsonl)"
    echo "single-hook study file:         $(have bench/studies/hooks-single-$SUFFIX.yaml)"
    echo "single-hook study locked:       $(have bench/studies/hooks-single-$SUFFIX.lock.json)"
    echo "single-hook study results:      $(ls -d bench/results/*/hooks-single-$SUFFIX 2>/dev/null | tail -1 || true)"
    echo; echo "Next: run the first stage that says 'no', in the order pilot, study, run, analyze."
    echo "What to expect: hook_quiet_commands only acts on a few install commands, so on many tasks it may never act."
    ;;
  pilot)   exec bash bench/scripts/80_hooks_pilot.sh ;;
  study)   exec bash bench/scripts/81_hooks_study.sh ;;
  run)     SUFFIX="$SUFFIX" exec bash bench/scripts/50_run_study.sh H ;;
  analyze)
    B="${RESULTS:-$(ls -d bench/results/*/hooks-single-"$SUFFIX" 2>/dev/null | sort | tail -1)}"
    [ -n "$B" ] && [ -d "$B" ] || { echo "STOP: no results for hooks-single-$SUFFIX. Has it run?" >&2; exit 1; }
    echo "== How often did each hook act? (read this first)"
    python bench/tools/hook_activity.py "$B"
    echo
    SUFFIX="$SUFFIX" bash bench/scripts/60_analyze_study.sh H
    ;;
  *) echo "usage: bash $0 [status|pilot|study|run|analyze]" >&2; exit 1;;
esac
