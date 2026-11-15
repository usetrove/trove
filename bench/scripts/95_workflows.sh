#!/usr/bin/env bash
# Replay scripted Claude Code sessions (everyday and heavy-output) through the real optimizer engine, then report the numbers.
# Offline: no API calls, no money, no Docker, no disk to speak of. Takes well under a minute after the build.
#     bash bench/scripts/95_workflows.sh
# Settings (VAR=value before the command):
#     SEED=20261108        the sessions' small variations (timings, ids); same seed, same report
#     MODEL=claude-sonnet-5-5   the pinned model used to price the sessions
#     OUT=bench/published/workflows   where workflows.md and workflows.json go (must not be git-ignored)
#     REPLAY=1             ALSO replay your own real Claude Code logs (read-only, counts and bytes only, nothing stored)
#     REPLAY_DIR=~/.claude/projects   which logs to replay when REPLAY=1
#
# What the numbers are, and the only honest way to quote them:
#   1+2. ESTIMATED saving for two tiers: everyday sessions, and heavy-output sessions (a waste-rich ceiling, never averaged in).
#      The sessions are scripted, so say "on scripted workflows", never "you will save".
#   3. MEASURED nothing lost: failure lines still visible, trimmed output recoverable, cached prefix byte-identical, repeat runs
#      identical. This is the safety claim, and it is a strong one.
#   4. MEASURED engine time per request (p50, p99), in-process; the gateway adds its own forwarding time.
set -euo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
command -v go >/dev/null || { echo "STOP: go is not installed or not on PATH." >&2; exit 1; }
SEED="${SEED:-20261108}"
MODEL="${MODEL:-claude-sonnet-5-5}"
OUT="${OUT:-bench/published/workflows}"
if git check-ignore -q "$OUT/x" 2>/dev/null; then echo "STOP: $OUT is git-ignored; pick another folder with OUT=..." >&2; exit 1; fi

# Never touch a real ~/.trove: the run uses a scratch data folder that is removed at the end.
SCRATCH="$(mktemp -d)"
trap 'rm -rf "$SCRATCH"' EXIT
export TROVE_DATA_DIR="$SCRATCH/data"; mkdir -p "$TROVE_DATA_DIR"

echo "== Build"
go build -o "$SCRATCH/trove" ./cmd/trove
mkdir -p "$OUT"

echo; echo "== Replay the scripted workflows (seed $SEED, model $MODEL)"
"$SCRATCH/trove" bench workflows --seed "$SEED" --model "$MODEL" --out "$OUT" > "$SCRATCH/report.txt"
cat "$OUT/workflows.md"

if [ -n "${REPLAY:-}" ]; then
  RD="${REPLAY_DIR:-$HOME/.claude/projects}"
  echo; echo "== Replay of your own real session logs ($RD): read-only, aggregates only"
  [ -d "$RD" ] || { echo "STOP: $RD does not exist. Set REPLAY_DIR=..." >&2; exit 1; }
  "$SCRATCH/trove" bench stress --layers realism --mode smoke --replay-dir "$RD" --out "$SCRATCH/replay" > /dev/null
  python - "$SCRATCH/replay/stress.json" <<'PY' 2>/dev/null || echo "(open $SCRATCH/replay/stress.md for the replay section)"
import json, sys
d = json.load(open(sys.argv[1]))
for l in d["layers"]:
    r = (l.get("savings") or {}).get("replay")
    if r:
        print(json.dumps(r, indent=2)); break
else:
    print("no replay section in the report")
PY
  echo "Real-log numbers are ESTIMATED too (the engine's bytes-per-token ratio, list price). They come from YOUR sessions only."
fi

python - "$OUT/workflows.json" <<'PY'
import json, sys
h = json.load(open(sys.argv[1]))["headline"]
e, v = h["everyday"], h["heavy"]
print()
print("== The numbers, as you could quote them")
print(f"1. ESTIMATED: on scripted everyday Claude Code workflows the optimizers cut list-price cost by a median of "
      f"{e['median_saved_pct']}% (range {e['min_saved_pct']}% to {e['max_saved_pct']}%).")
print(f"2. ESTIMATED: on scripted heavy-output sessions (verbose test runs, build logs, incident logs) the median is "
      f"{v['median_saved_pct']}% (range {v['min_saved_pct']}% to {v['max_saved_pct']}%). That tier was chosen for high waste: quote it as a ceiling "
      f"for output-heavy work, never as a typical day, and never average it with the everyday tier.")
print(f"3. MEASURED: nothing lost and cache safe. {h['key_lines_kept']} of {h['key_lines']} failure/error lines still visible to the model; "
      f"{h['prefix_preserved']} of {h['requests']} requests kept the cached prefix byte-identical; {h['violations']} broken invariants; "
      f"repeat runs byte-identical: {h['all_deterministic']}.")
print(f"4. MEASURED: engine time per request p50 {h['engine_p50_ms']} ms, p99 {h['engine_p99_ms']} ms (in-process, excludes gateway forwarding).")
print()
print("Do not drop 'scripted' or 'estimated'. It is not a forecast of anyone's bill.")
PY
echo
echo "Wrote $OUT/workflows.md and $OUT/workflows.json. To publish: git add $OUT && git commit, then update the README"
echo "with the numbers above, then: go run ./scripts/checkclaims --publish"
