#!/usr/bin/env bash
# Step 3: numbers you CAN publish today, each labelled. Free; reads saved run data. It writes a README block to
# bench/published/<study-id>/activity.md and prints it. It does not edit the README: paste it in yourself.
#     bash bench/scripts/93_labelled_numbers.sh
# What this is and is not: "tokens removed" is an ESTIMATE of what the optimizers cut from requests. It is NOT a bill
# saving. The bill comparison stays in the study report, whatever it says. Never present one as the other.
# For real users' own sessions, Trove's shadow mode estimates what each optimizer WOULD save without changing anything
# (run `trove report` on a machine with real usage); label that "estimated", never "measured".
set -euo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
command -v python >/dev/null || { echo "STOP: python not found. Run: source .venv/bin/activate" >&2; exit 1; }
SUFFIX="${SUFFIX:-2}"
ID="swebench-gateway-$SUFFIX"
B="${RESULTS:-$(ls -d bench/results/*/"$ID" 2>/dev/null | sort | tail -1)}"
[ -n "$B" ] && [ -d "$B" ] || { echo "STOP: no results folder for $ID. Set RESULTS=..." >&2; exit 1; }
OUT="bench/published/$ID"
mkdir -p "$OUT"
if git check-ignore -q "$OUT/x"; then echo "STOP: $OUT is git-ignored." >&2; exit 1; fi
python bench/scripts/activity.py "$B" --markdown | tee "$OUT/activity.md"
echo
echo "wrote $OUT/activity.md (metadata only: counts and tokens; no prompt text or paths)"
echo "Next: paste it under 'Measured results' in the README next to the study verdict, then: go run ./scripts/checkclaims --publish"
