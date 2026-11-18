#!/usr/bin/env bash
# The study. Real money, capped. Leave it running overnight (use tmux or nohup; see README.md).
#     export ANTHROPIC_API_KEY=...
#     bash bench/scripts/launch/03_run.sh                                    # the launch study
#     STUDY=bench/studies/models-1.yaml bash bench/scripts/launch/03_run.sh  # another study
# A staged study runs one repeat ("look") at a time; after each, `trovebench stage` says whether every comparison is
# decided, and if so it stops, saving the rest of the budget. It never starts more looks than the pilot said fit the
# budget, and the study's own hard cap stops it in any case. A study that is not staged runs in one go.
# Interrupted? Run it again: finished runs are kept and not paid for twice.
. "$(dirname "${BASH_SOURCE[0]}")/_common.sh"
need_key
need_clean_tree
need_tasks
need_probe_for_pinned_version

if [ ! -f "${STUDY%.yaml}.lock.json" ]; then
  # Pre-registration: lock the study (its hash, the commit and the time) and commit the lock before any study run,
  # so nothing about the design can change once results exist. The pilot's runs are never part of the study.
  say "Registering $STUDY_ID before its first run"
  python -m trovebench prereg "$STUDY"
  git add "${STUDY%.yaml}.lock.json"
  git commit -q -m "bench: register $STUDY_ID before its first run" -m "Locks the study file's hash, the commit and the time; no results exist yet."
fi
[ -f "$PILOT_BATCH/sizing.json" ] || stop "no pilot sizing found at $PILOT_BATCH/sizing.json. Run 02_pilot.sh first."
ASSUME="$(python -c "import json;print(json.load(open('$PILOT_BATCH/sizing.json'))['assumed_run_usd'])")"
LOOKS="$(python -c "import json;print(json.load(open('$PILOT_BATCH/sizing.json'))['looks_that_fit'])")"
STAGED="$(python -c "import json;print(json.load(open('$PILOT_BATCH/sizing.json'))['staged'])")"

run_look() {
  set +e
  python -m trovebench run --study "$STUDY" --batch "$BATCH" --hook-capabilities "$CAPS" --assume-run-usd "$ASSUME" --yes "$@"
  rc=$?
  set -e
  if [ "$rc" -ne 0 ] && [ "$rc" -ne 3 ]; then stop "the run stopped with an error (exit $rc). Read the last lines above."; fi
}

if [ "$STAGED" != "True" ]; then
  say "$STUDY_ID is not staged: running every repeat in one go"
  run_look
else
  for k in $(seq 1 "$LOOKS"); do
    say "Look $k of at most $LOOKS: running repeat $k for every task and condition"
    run_look --through-repeat "$k"
    say "Look $k: is every comparison decided?"
    if python -m trovebench stage --batch "$BATCH"; then
      say "Every comparison is decided after look $k. Stopping here."
      break
    fi
    if [ "$rc" -eq 3 ]; then
      # Exit 3: this look did not finish (the spending cap, or several runs in a row failed). The line above says which.
      say "Look $k did not finish all its runs, so no further look is started. The report uses every finished pair."
      break
    fi
  done
fi

say "Done. Next: bash bench/scripts/launch/04_report.sh"
