#!/usr/bin/env bash
# Runbook PART 8: run one registered study. THIS SPENDS REAL API MONEY (up to the study's batch cap).
# Run inside tmux. Safe to run again after a crash or a dropped connection: finished runs are never repeated.
#     export ANTHROPIC_API_KEY=...
#     bash bench/scripts/50_run_study.sh A        # Study A: gateway optimizers
#     bash bench/scripts/50_run_study.sh B        # Study B: plugin / hooks parity (needs 20_hook_probe.sh first)
#     bash bench/scripts/50_run_study.sh H        # Study D: each hook by itself (runbook Part D; see 81_hooks_study.sh)
# A and B can run at the same time in two tmux windows, but they share API rate limits and each container
# uses 4 GB RAM and 2 CPUs. Running them one after another is the safer default.
set -euo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SUFFIX="${SUFFIX:-1}"
PROBE="${PROBE:-/tmp/hookprobe.json}"

case "${1:-}" in
  A|a) STUDY="bench/studies/swebench-gateway-$SUFFIX.yaml"; EXTRA=(); LOG="/tmp/run-A.log";;
  B|b) STUDY="bench/studies/swebench-parity-$SUFFIX.yaml"; EXTRA=(--hook-capabilities "$PROBE"); LOG="/tmp/run-B.log"
       [ -f "$PROBE" ] || { echo "STOP: $PROBE missing. Run bench/scripts/20_hook_probe.sh first." >&2; exit 1; };;
  H|h) STUDY="bench/studies/hooks-single-$SUFFIX.yaml"; EXTRA=(--hook-capabilities "$PROBE"); LOG="/tmp/run-H.log"
       [ -f "$PROBE" ] || { echo "STOP: $PROBE missing. Run bench/scripts/20_hook_probe.sh first." >&2; exit 1; };;
  *) echo "usage: bash $0 A|B|H   (A gateway, B parity, H single hooks)" >&2; exit 1;;
esac
[ -n "${ANTHROPIC_API_KEY:-}" ] || { echo "STOP: ANTHROPIC_API_KEY is not set in this shell." >&2; exit 1; }
[ -f "${STUDY%.yaml}.lock.json" ] || { echo "STOP: ${STUDY} is not locked. Run 40_lock_studies.sh first." >&2; exit 1; }
[ -z "$(git status --porcelain)" ] || { echo "STOP: git tree not clean (results would say trove_dirty):" >&2; git status --short >&2; exit 1; }
if [ -z "${TMUX:-}" ]; then echo "WARNING: not inside tmux. If SSH drops, the run stops (rerunning this resumes it)."; sleep 3; fi

echo "Running $STUDY. Log: $LOG"
echo "Watch from another window:  watch -n 10 'wc -l bench/results/*/\$(basename $STUDY .yaml)/ledger.jsonl'"
set +e
python -u -m trovebench run --study "$STUDY" "${EXTRA[@]}" --yes 2>&1 | tee -a "$LOG"
CODE=${PIPESTATUS[0]}
set -e
echo
case "$CODE" in
  0) echo "Study finished: every planned run is done.";;
  3) echo "Study STOPPED EARLY (cap, repeated errors or interrupt). See the reason above. A partial report covers fewer pairs than registered: do not present it as the full study. After fixing the cause, rerun this exact command to resume.";;
  *) echo "Run exited with code $CODE. Read the message above; rerunning resumes where it stopped.";;
esac
echo "Next: bash bench/scripts/60_analyze_study.sh ${1^^}"
exit "$CODE"
