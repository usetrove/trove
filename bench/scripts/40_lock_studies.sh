#!/usr/bin/env bash
# Runbook PART 7 (second half): register (lock) the two study files and commit them locally BEFORE any study run.
# Spends nothing. After this the study files can never be edited. It commits but does NOT push; push yourself
# if you want the registration on record before the runs start (git push).
#     bash bench/scripts/40_lock_studies.sh
# SUFFIX=1 must match the one used in 30_make_studies.sh. ONLY_A=1 locks Study A alone (no hook probe needed).
set -euo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SUFFIX="${SUFFIX:-1}"
A="bench/studies/swebench-gateway-$SUFFIX.yaml"
B="bench/studies/swebench-parity-$SUFFIX.yaml"
PROBE="${PROBE:-/tmp/hookprobe.json}"

[ -f "$A" ] || { echo "STOP: $A not found. Run 30_make_studies.sh first." >&2; exit 1; }
FILES=("$A" "${A%.yaml}.lock.json")
python -m trovebench prereg "$A"
echo "== plan, Study A (spends nothing): the estimate must be under the batch cap"
python -m trovebench plan --study "$A"
if [ -z "${ONLY_A:-}" ]; then
  [ -f "$B" ] || { echo "STOP: $B not found." >&2; exit 1; }
  [ -f "$PROBE" ] || { echo "STOP: $PROBE missing, so Study B cannot run. Run 20_hook_probe.sh, or use ONLY_A=1." >&2; exit 1; }
  python -m trovebench prereg "$B"
  echo "== plan, Study B (spends nothing)"
  python -m trovebench plan --study "$B" --hook-capabilities "$PROBE"
  FILES+=("$B" "${B%.yaml}.lock.json")
fi

git add "${FILES[@]}"
git commit -m "bench: register swebench studies ($SUFFIX)" -- "${FILES[@]}"
if [ -n "$(git status --porcelain)" ]; then
  echo "WARNING: the tree is not clean, so runs will be recorded as trove_dirty. Fix this before running:" >&2
  git status --short >&2
  exit 1
fi
echo
echo "Locked and committed: ${FILES[*]}"
echo "Next (spends real money, needs ANTHROPIC_API_KEY): bash bench/scripts/50_run_study.sh A"
