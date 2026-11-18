#!/usr/bin/env bash
# The launch study. Real money, capped. Leave it running overnight (use tmux or nohup; see README.md).
#     export ANTHROPIC_API_KEY=...
#     bash bench/scripts/launch/03_run.sh
# It runs the study one repeat ("look") at a time. After each look it asks `trovebench stage` whether both comparisons
# are decided; if so it stops, saving the rest of the budget. It never starts more looks than the pilot said fit the
# budget, and the study's own hard cap stops it in any case. Interrupted? Run it again: finished runs are kept.
. "$(dirname "${BASH_SOURCE[0]}")/_common.sh"
need_key
need_clean_tree
need_tasks
need_probe_for_pinned_version

[ -f "${STUDY%.yaml}.lock.json" ] || stop "$STUDY is not pre-registered (no lock file). It must be locked and committed before running."
[ -f "$PILOT_BATCH/sizing.json" ] || stop "no pilot sizing found at $PILOT_BATCH/sizing.json. Run 02_pilot.sh first."
ASSUME="$(python -c "import json;print(json.load(open('$PILOT_BATCH/sizing.json'))['assumed_run_usd'])")"
LOOKS="$(python -c "import json;print(json.load(open('$PILOT_BATCH/sizing.json'))['looks_that_fit'])")"

for k in $(seq 1 "$LOOKS"); do
  say "Look $k of at most $LOOKS: running repeat $k for every task and condition"
  set +e
  python -m trovebench run --study "$STUDY" --batch "$BATCH" --hook-capabilities "$CAPS" \
    --through-repeat "$k" --assume-run-usd "$ASSUME" --yes
  rc=$?
  set -e
  if [ "$rc" -ne 0 ] && [ "$rc" -ne 3 ]; then stop "the run stopped with an error (exit $rc). Read the last lines above."; fi

  say "Look $k: is every comparison decided?"
  if python -m trovebench stage --batch "$BATCH"; then
    say "Both comparisons are decided after look $k. Stopping here."
    break
  fi
  if [ "$rc" -eq 3 ]; then
    # Exit 3: this look did not finish (the spending cap, or several runs in a row failed). The line above says which.
    say "Look $k did not finish all its runs, so no further look is started. The report uses every finished pair."
    break
  fi
done

say "Done. Next: bash bench/scripts/launch/04_report.sh"
