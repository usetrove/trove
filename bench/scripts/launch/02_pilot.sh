#!/usr/bin/env bash
# Real money, small: about $1-3. Runs 2 long-session tasks x the 3 launch conditions x 1 repeat on Sonnet, with the
# same settings as the study, to prove the plugin, MCP server, gateway and routing really work in containers and to
# measure the real cost per run. The pilot's results are NEVER part of the study's verdict; only its cost per run is
# used, to size the study.
#     export ANTHROPIC_API_KEY=...
#     bash bench/scripts/launch/02_pilot.sh
. "$(dirname "${BASH_SOURCE[0]}")/_common.sh"
need_key
need_clean_tree
need_tasks
need_probe_for_pinned_version

ROUTE_TO="$(python -c 'import yaml;print(yaml.safe_load(open("bench/studies/launch-1.yaml"))["route_to"])')"
MODEL="$(python -c 'import yaml;print(yaml.safe_load(open("bench/studies/launch-1.yaml"))["model"])')"

rm -rf "$PILOT_TASKS" && mkdir -p "$PILOT_TASKS"
for t in $(ls "$TASKS" | sort | tail -2); do cp -r "$TASKS/$t" "$PILOT_TASKS/"; done
python bench/scripts/launch/make_config.py bench/results/launch/pilot.yaml "$MODEL" 8 1.5 \
  passthrough passthrough_plugin "trove_full:$ROUTE_TO" >/dev/null

say "Pilot: 2 tasks x 3 conditions on $MODEL (hard cap \$8)"
python -m trovebench run --config bench/results/launch/pilot.yaml --tasks "$PILOT_TASKS" --batch "$PILOT_BATCH" \
  --hook-capabilities "$CAPS" --yes || true

say "Pilot summary"
python bench/scripts/pilot_stats.py "$PILOT_BATCH"
python bench/scripts/launch/pilot_check.py "$PILOT_BATCH" "$ROUTE_TO" "$BUDGET_USD"
