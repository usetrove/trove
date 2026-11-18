#!/usr/bin/env bash
# Real money, small. Runs 2 of the study's tasks x every condition x 1 repeat, with the study's model and settings,
# to prove each condition really works in containers (the plugin and MCP server, routing, installed features) and to
# measure the real cost per run. The pilot's results are NEVER part of the study's verdict; only its cost per run is
# used, to size the study.
#     export ANTHROPIC_API_KEY=...
#     bash bench/scripts/launch/02_pilot.sh                                    # the launch study
#     STUDY=bench/studies/models-1.yaml bash bench/scripts/launch/02_pilot.sh  # another study
. "$(dirname "${BASH_SOURCE[0]}")/_common.sh"
need_key
need_clean_tree
need_tasks
need_probe_for_pinned_version

mapfile -t CONDS < <(pilot_conditions)
rm -rf "$PILOT_TASKS" && mkdir -p "$PILOT_TASKS"
for t in $(ls "$TASKS" | sort | tail -2); do cp -r "$TASKS/$t" "$PILOT_TASKS/"; done
CFG="bench/results/launch/${STUDY_ID}-pilot.yaml"
RUN_CAP="$(python -c 'import sys,yaml; print(yaml.safe_load(open(sys.argv[1]))["budget"]["run_cap_usd"])' "$STUDY")"
python bench/scripts/launch/make_config.py "$CFG" "$MODEL" 20 "$RUN_CAP" "${ROUTE_TO:--}" "${CONDS[@]}" >/dev/null

say "Pilot for $STUDY_ID: 2 tasks x ${#CONDS[@]} conditions on $MODEL (hard cap \$20)"
python -m trovebench run --config "$CFG" --tasks "$PILOT_TASKS" --batch "$PILOT_BATCH" --hook-capabilities "$CAPS" --yes || true

say "Pilot summary"
python bench/scripts/pilot_stats.py "$PILOT_BATCH"
python bench/scripts/launch/pilot_check.py "$PILOT_BATCH" "$STUDY" "$BUDGET_USD"
