#!/usr/bin/env bash
# One command for the Trove savings suite. See the approved plan for what each tier measures.
#
#   bash qa/suite/run.sh                       # free tier only (no spend); prints the paid tier's status
#   bash qa/suite/run.sh --all --budget 25 --yes   # free tier, then the paid tier if every gate passes
#   bash qa/suite/run.sh --fake                # free tier, plus the whole pipeline on the fake agent (no spend)
#
# Every run writes qa/results/suite-<timestamp>/SUITE_REPORT.md and suite.json. Nothing is published, pushed or
# committed by this script.
set -u

HERE="$(cd "$(dirname "$0")/../.." && pwd)"   # the repo root
cd "$HERE" || exit 1

ALL=0; BUDGET=""; YES=0; FAKE=0
while [ $# -gt 0 ]; do
  case "$1" in
    --all) ALL=1 ;;
    --budget) BUDGET="${2:-}"; shift ;;
    --yes) YES=1 ;;
    --fake) FAKE=1 ;;
    -h|--help) sed -n '2,11p' "$0"; exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
  shift
done

STAMP="$(date +%Y%m%d-%H%M%S)"
OUT="$HERE/qa/results/suite-$STAMP"
mkdir -p "$OUT/free" "$OUT/paid" "$OUT/bin"
EXE=""; case "$(uname -s)" in MINGW*|MSYS*|CYGWIN*) EXE=".exe" ;; esac
BIN="$OUT/bin/trove$EXE"
STUB="$OUT/bin/stubagent$EXE"
REPLAY="$OUT/bin/replay$EXE"
LOADBIN="$OUT/bin/loadtest$EXE"
FAKEBIN="$OUT/bin/benchfake$EXE"
LOG="$OUT/suite.log"
: > "$LOG"
log() { echo "$*" | tee -a "$LOG"; }

# ---- build the pieces under test, once --------------------------------------------------------------------------
log "== build"
go build -trimpath -buildvcs=false -o "$BIN" ./cmd/trove || { log "build failed: cannot build the trove binary"; exit 1; }
go build -o "$STUB" ./qa/stubagent || { log "build failed: stub agent"; exit 1; }
go build -o "$REPLAY" ./qa/replay || { log "build failed: replay tool"; exit 1; }
go build -o "$LOADBIN" ./qa/loadtest || { log "build failed: load tool"; exit 1; }
go build -o "$FAKEBIN" ./bench/tools/benchfake || { log "build failed: fake upstream"; exit 1; }
log "built: $("$BIN" version 2>/dev/null | head -1)"

# ---- free tier --------------------------------------------------------------------------------------------------
log "== free tier: contract and safety"
bash qa/cli_matrix.sh "$BIN" "$OUT/free" >>"$LOG" 2>&1 || true
bash qa/personas/personas.sh "$BIN" "$OUT/free" >>"$LOG" 2>&1 || true

log "== free tier: gateway overhead (1/10/50 sessions, measured)"
PER="${PER:-20}" bash qa/benchmarks/run_overhead.sh "$BIN" "$OUT/free" >>"$LOG" 2>&1 || true

log "== free tier: ceilings on real Claude Code history (measured bytes, estimated tokens)"
if [ -d "${CLAUDE_LOGS:-$HOME/.claude/projects}" ]; then
  "$REPLAY" -dir "${CLAUDE_LOGS:-$HOME/.claude/projects}" -per "${PER_SESSION:-8}" > "$OUT/free/replay.tsv" 2>>"$LOG" || true
else
  echo "no Claude Code logs found under ${CLAUDE_LOGS:-$HOME/.claude/projects}" > "$OUT/free/replay.tsv"
fi

log "== free tier: do the statistics tell the truth? (planted effects, simulated)"
(cd bench && python -m trovebench.validity "${VALIDITY_TRIALS:-200}") > "$OUT/free/validity.md" 2>>"$LOG" || true

# ---- paid tier: gated -------------------------------------------------------------------------------------------
PAID_BLOCKERS=()
block() { PAID_BLOCKERS+=("$1"); }

if [ "$ALL" -eq 1 ] || [ -n "$BUDGET" ]; then
  [ -n "$BUDGET" ] || block "no --budget given (the paid tier needs a spend cap, for example --budget 25)"
  [ "$YES" -eq 1 ] || block "no --yes given (the paid tier refuses to spend without an explicit yes)"
  docker info >/dev/null 2>&1 || block "Docker is not running (start Docker Desktop, then run again)"
  KEY="${ANTHROPIC_API_KEY:-}"
  case "$KEY" in
    sk-ant-api0[0-9]-*|sk-ant-usr*) : ;;
    "") block "ANTHROPIC_API_KEY is not set in this environment" ;;
    *) block "ANTHROPIC_API_KEY does not look like a Console key (expected sk-ant-api0N-... or sk-ant-usr-...)" ;;
  esac
  [ -n "${ANTHROPIC_BASE_URL:-}" ] && block "ANTHROPIC_BASE_URL is set; unset it so the paid runs go straight to Anthropic"
  [ -f bench/studies/savings-1.yaml ] || block "the study bench/studies/savings-1.yaml is not written yet"
  [ -d bench/tasks/trigger ] || block "the trigger task layer (bench/tasks/trigger) is not written yet"
  block "conditions mcp, ext_rtk and ext_token_savior are not implemented in the bench lab yet (bench/trovebench/conditions.py)"
fi

PAID_STATUS="not requested (run with --all --budget N --yes to attempt it)"
if [ "$ALL" -eq 1 ] || [ -n "$BUDGET" ]; then
  if [ "${#PAID_BLOCKERS[@]}" -eq 0 ]; then
    PAID_STATUS="all gates passed, but the paid runner is not wired yet; nothing was spent"
  else
    PAID_STATUS="blocked, nothing was spent: $(printf '%s; ' "${PAID_BLOCKERS[@]}")"
  fi
  log "== paid tier: $PAID_STATUS"
fi

if [ "$FAKE" -eq 1 ]; then
  log "== fake-agent pipeline (SIMULATED, $0)"
  (cd bench && python -m trovebench smoke --fake --batch "$OUT/paid/fake-batch") >>"$LOG" 2>&1 || true
fi

# ---- report -----------------------------------------------------------------------------------------------------
{
  printf '{"stamp":"%s","commit":"%s","paid_status":"%s"}\n' "$STAMP" "$(git rev-parse --short HEAD 2>/dev/null)" "$PAID_STATUS"
} > "$OUT/suite.json"
python qa/suite/report.py "$OUT" > "$OUT/SUITE_REPORT.md" 2>>"$LOG" || log "report generation failed; see $LOG"
log "== done: $OUT/SUITE_REPORT.md"
