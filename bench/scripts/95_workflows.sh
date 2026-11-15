#!/usr/bin/env bash
# Replay scripted everyday Claude Code sessions through the real optimizer engine, then report three numbers.
# Offline: no API calls, no money, no Docker, no disk to speak of. Takes well under a minute after the build.
#     bash bench/scripts/95_workflows.sh
# Settings (VAR=value before the command):
#     SEED=20261108        the sessions' small variations (timings, ids); same seed, same report
#     MODEL=claude-sonnet-5-5   the pinned model used to price the sessions
#     OUT=bench/published/workflows   where workflows.md and workflows.json go (must not be git-ignored)
#     REPLAY=1             ALSO replay your own real Claude Code logs (read-only, counts and bytes only, nothing stored)
#     REPLAY_DIR=~/.claude/projects   which logs to replay when REPLAY=1
#
# What the three numbers are, and the only honest way to quote them:
#   1. ESTIMATED saving (median and range over the scripted workflows). The sessions are scripted, so say "on scripted
#      everyday workflows", never "you will save".
#   2. MEASURED cache and correctness: requests whose cached prefix stayed byte-identical, broken invariants, and whether a
#      repeat run gave the same bytes. This is the safety claim, and it is a strong one.
#   3. MEASURED engine time per request (p50, p99), in-process; the gateway adds its own forwarding time.
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
print("\n== The three numbers, as you could quote them")
print(f"1. ESTIMATED: on scripted everyday Claude Code workflows the optimizers cut list-price cost by a median of "
      f"{h['median_saved_pct']}% (range {h['min_saved_pct']}% to {h['max_saved_pct']}%).")
print(f"2. MEASURED: {h['prefix_preserved']} of {h['requests']} requests kept the cached prefix byte-identical; "
      f"{h['violations']} broken invariants; repeat runs byte-identical: {h['all_deterministic']}.")
print(f"3. MEASURED: engine time per request p50 {h['engine_p50_ms']} ms, p99 {h['engine_p99_ms']} ms (in-process, excludes gateway forwarding).")
print("\nDo not drop the words 'scripted' and 'estimated'. The saving is small on short sessions and larger on long, noisy ones;")
print("the per-workflow table says which. It is not a forecast of anyone's bill.")
PY
echo
echo "Wrote $OUT/workflows.md and $OUT/workflows.json. To publish: git add $OUT && git commit, then update the README"
echo "with the three lines above, then: go run ./scripts/checkclaims --publish"
