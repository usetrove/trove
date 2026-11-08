#!/usr/bin/env bash
# Phase 1: CLI contract sweep. Runs the built binary against a scratch data dir
# and a scratch HOME so no real ~/.trove or agent config is touched.
#
#   qa/cli_matrix.sh <path-to-trove-binary> <results-dir>
#
# Writes <results-dir>/cli_matrix.tsv with one line per check:
#   check <TAB> command <TAB> PASS|FAIL|NOTE <TAB> detail
set -u

BIN="${1:?usage: cli_matrix.sh <trove-binary> <results-dir>}"
OUT="${2:?usage: cli_matrix.sh <trove-binary> <results-dir>}"
HERE="$(cd "$(dirname "$0")" && pwd)"
SCRATCH="${QA_SCRATCH:-/tmp/trove-qa}/cli"

rm -rf "$SCRATCH"
mkdir -p "$SCRATCH/home" "$SCRATCH/data" "$OUT"
export TROVE_DATA_DIR="$SCRATCH/data"
export HOME="$SCRATCH/home" USERPROFILE="$SCRATCH/home"
export CLAUDE_CONFIG_DIR="$SCRATCH/home/.claude" CODEX_HOME="$SCRATCH/home/.codex"
export NO_COLOR=1

TSV="$OUT/cli_matrix.tsv"
: > "$TSV"

record() { printf '%s\t%s\t%s\t%s\n' "$1" "$2" "$3" "$4" >> "$TSV"; }

# 1. Every top-level command answers --help with exit 0.
CMDS=$("$BIN" --help 2>&1 | awk '/^Available Commands:/{f=1;next} /^Flags:/{f=0} f && NF {print $1}' | grep -v '^help$' | grep -v '^completion$')
for c in $CMDS; do
  if "$BIN" "$c" --help >/dev/null 2>"$SCRATCH/err.txt"; then
    record help "$c" PASS "--help exit 0"
  else
    record help "$c" FAIL "--help exit $? : $(head -c 200 "$SCRATCH/err.txt")"
  fi
done

# 2. Read-only commands that claim --json must print one valid JSON document on stdout.
JSON_CMDS=(
  "version" "doctor" "pricing show" "optimize status" "optimize suggest"
  "budget show" "receipts" "experiment status" "logs status" "hooks status"
  "mcp status" "contribute status" "report" "recommend"
)
for c in "${JSON_CMDS[@]}"; do
  # shellcheck disable=SC2086
  "$BIN" $c --json >"$SCRATCH/out.txt" 2>"$SCRATCH/err.txt"
  code=$?
  if grep -qiE 'panic|goroutine [0-9]+ \[' "$SCRATCH/err.txt" "$SCRATCH/out.txt"; then
    record json "$c" FAIL "panic text in output (exit $code)"
    continue
  fi
  if node -e 'JSON.parse(require("fs").readFileSync(process.argv[1],"utf8"))' "$SCRATCH/out.txt" 2>/dev/null; then
    if [ "$code" -eq 0 ]; then
      record json "$c" PASS "valid JSON, exit 0"
    else
      record json "$c" NOTE "valid JSON, exit $code"
    fi
  else
    record json "$c" FAIL "stdout is not valid JSON (exit $code): $(head -c 160 "$SCRATCH/out.txt" | tr '\n\t' '  ')"
  fi
done

# 3. Commands the earlier review flagged as possibly ignoring --json. Record what they print.
for c in "status-line" "reconcile --from 2026-10-01 --to 2026-10-31 --invoice 1" "undo" "mcp snippet"; do
  # shellcheck disable=SC2086
  "$BIN" $c --json >"$SCRATCH/out.txt" 2>"$SCRATCH/err.txt"
  code=$?
  if node -e 'JSON.parse(require("fs").readFileSync(process.argv[1],"utf8"))' "$SCRATCH/out.txt" 2>/dev/null; then
    record json-suspect "$c" PASS "valid JSON, exit $code"
  else
    record json-suspect "$c" NOTE "not JSON, exit $code: $(head -c 120 "$SCRATCH/out.txt" | tr '\n\t' '  ')"
  fi
done

# 3b. Fresh install (no trove.db yet): commands must give a plain-English message, not an OS error.
FRESH="$SCRATCH/fresh"
rm -rf "$FRESH" && mkdir -p "$FRESH"
for c in "experiment status" "report" "receipts" "budget show" "optimize status"; do
  # shellcheck disable=SC2086
  TROVE_DATA_DIR="$FRESH" "$BIN" $c >"$SCRATCH/out.txt" 2>"$SCRATCH/err.txt"
  code=$?
  if grep -qiE 'GetFileAttributes|no such file|cannot find|The system cannot|stat |open .*: ' "$SCRATCH/err.txt" "$SCRATCH/out.txt"; then
    record fresh-install "$c" FAIL "raw OS error on a fresh install (exit $code): $(head -c 160 "$SCRATCH/err.txt" "$SCRATCH/out.txt" | tr '\n\t' '  ')"
  else
    record fresh-install "$c" PASS "plain output on a fresh install (exit $code)"
  fi
done

# 4. `config` was removed until it is built; it must be an unknown command.
"$BIN" config get >/dev/null 2>"$SCRATCH/err.txt"
code=$?
if [ "$code" -ne 0 ] && grep -q '^Error: ' "$SCRATCH/err.txt"; then
  record removed "config get" PASS "unknown command, exit $code"
else
  record removed "config get" FAIL "expected an unknown-command error, got exit $code"
fi

# 5. Unknown command gives a plain-English error and non-zero exit.
"$BIN" definitely-not-a-command >/dev/null 2>"$SCRATCH/err.txt"
code=$?
if [ "$code" -ne 0 ] && [ -s "$SCRATCH/err.txt" ]; then
  record error "unknown command" PASS "exit $code, message: $(head -c 120 "$SCRATCH/err.txt" | tr '\n' ' ')"
else
  record error "unknown command" FAIL "exit $code, stderr empty or exit 0"
fi

# 6. Setup and undo against the scratch HOME. Setup must not write without asking.
mkdir -p "$CLAUDE_CONFIG_DIR"
echo '{"theme":"dark"}' > "$CLAUDE_CONFIG_DIR/settings.json"
before=$(node -e 'process.stdout.write(require("crypto").createHash("sha256").update(require("fs").readFileSync(process.argv[1])).digest("hex"))' "$CLAUDE_CONFIG_DIR/settings.json")
"$BIN" setup </dev/null >"$SCRATCH/setup.txt" 2>&1
after=$(node -e 'process.stdout.write(require("crypto").createHash("sha256").update(require("fs").readFileSync(process.argv[1])).digest("hex"))' "$CLAUDE_CONFIG_DIR/settings.json")
if [ "$before" = "$after" ]; then
  record setup "no-tty" PASS "settings.json unchanged without approval"
else
  record setup "no-tty" FAIL "settings.json changed without approval"
fi

echo "Wrote $TSV"
cut -f1,3 "$TSV" | sort | uniq -c
