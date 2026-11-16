#!/usr/bin/env bash
# Step 6.4, after the pilot has finished: what did it cost, how long did it take, did runs error?
# Free. Calls nothing. Safe to run while the pilot is still going (it just shows what is done so far).
#     bash bench/scripts/10_pilot_report.sh [pilot-folder]        default: bench/results/pilot-1
set -euo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PILOT="${1:-bench/results/pilot-1}"

python bench/scripts/pilot_stats.py "$PILOT"
echo
echo "Read the 'error' count first. Errors mean the run failed before grading (the harness could not run the agent or"
echo "the tests). A few are tolerable. If most runs errored, do not go on: paste this output and the last lines of"
echo "/tmp/pilot.log, and look at one run's agent_stderr_tail.txt:"
echo "    ls $PILOT/runs | head -3 ; cat $PILOT/runs/<one of those>/agent_stderr_tail.txt"
echo
echo "Next: bash bench/scripts/20_hook_probe.sh   (needed for Study B only; spends at most \$0.25)"
echo "  or: bash bench/scripts/30_make_studies.sh (writes the study files, spends nothing)"
