#!/usr/bin/env bash
# Runbook PART 11, the part that can be scripted: gather ONLY the publishable files of a study into
# bench/published/<study-id>/. Free. It does not commit, push, or edit the README: that needs your judgement.
#     bash bench/scripts/70_publish_prep.sh A        # or B
# It never copies runs/ (agent output, which can contain upstream code and issue text) or any task text.
set -euo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SUFFIX="${SUFFIX:-1}"
case "${1:-}" in
  A|a) ID="swebench-gateway-$SUFFIX";;
  B|b) ID="swebench-parity-$SUFFIX";;
  H|h) ID="hooks-single-$SUFFIX";;
  *) echo "usage: bash $0 A|B|H" >&2; exit 1;;
esac
SRC="${RESULTS:-$(ls -d bench/results/*/"$ID" 2>/dev/null | sort | tail -1)}"
[ -n "$SRC" ] && [ -d "$SRC" ] || { echo "STOP: no results folder for $ID." >&2; exit 1; }
DST="bench/published/$ID"
mkdir -p "$DST"
if git check-ignore -q "$DST/x"; then echo "STOP: $DST is git-ignored; pick another folder." >&2; exit 1; fi

n=0
for f in report.md comparisons.csv pairs.csv runs.csv parity.csv claims.csv cost_change.png success_diff.png latency_diff.png \
         versions.json config.used.yaml study.lock.json; do
  if [ -f "$SRC/$f" ]; then cp "$SRC/$f" "$DST/$f"; n=$((n + 1)); else echo "  (not present, skipped: $f)"; fi
done
echo "copied $n files from $SRC to $DST"
ls -1 "$DST"
echo
echo "Still to do by hand (runbook 11.2 to 11.5):"
echo "  1. git add $DST && git commit        (check 'git status' shows nothing from runs/ or tasks)"
echo "  2. update README 'Measured results' and CHANGELOG so they agree with the files; label every figure measured or estimated;"
echo "     say what the study does NOT cover; give the verdict in the ship rule's words, including NEED MORE DATA if so."
echo "  3. go run ./scripts/checkclaims --publish"
echo "  4. optional: python -m trovebench defaults --results $SRC --dry-run"
echo "  5. when completely finished: unset ANTHROPIC_API_KEY ; history -c   and rotate/delete the key in the Console."
echo "  To copy results to your own PC (run THERE):  rsync -av --exclude 'runs/' --exclude 'bin/' <USER>@<SERVER>:trove/$SRC/ ./bench-results/$ID/"
