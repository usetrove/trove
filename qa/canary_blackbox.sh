#!/usr/bin/env bash
# Phase 3: black-box canary run against the BUILT binary (rules 1, 2, 3, 7).
#
# Plants unique canary strings in: the prompt, a tool result, a file path, an
# API key, an OAuth token, and an agent history file. Runs real commands through
# the gateway (against the fake upstream, benchfake) and the reporting commands.
# Then scans every place Trove could write to for each canary.
#
#   qa/canary_blackbox.sh <trove-binary> <benchfake-binary> <results-dir>
#
# Output: <results-dir>/canary.tsv with PASS/FAIL per check, and canary_scan.txt.
set -u

BIN="${1:?usage: canary_blackbox.sh <trove> <benchfake> <results-dir>}"
FAKE="${2:?usage: canary_blackbox.sh <trove> <benchfake> <results-dir>}"
OUT="${3:?usage: canary_blackbox.sh <trove> <benchfake> <results-dir>}"
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="${QA_SCRATCH:-/tmp/trove-qa}/canary"
PORT=18765

rm -rf "$ROOT"
mkdir -p "$ROOT/home/.claude/projects/canary-proj" "$ROOT/data" "$ROOT/export" "$OUT"
export TROVE_DATA_DIR="$ROOT/data" HOME="$ROOT/home" USERPROFILE="$ROOT/home"
export CLAUDE_CONFIG_DIR="$ROOT/home/.claude" NO_COLOR=1

# Unique canaries. Each one must never appear in anything Trove writes.
# The key-shaped values are assembled at run time so this file holds no
# literal that secret scanners would flag. They are fake.
C_KEY="sk-ant-api03-$(printf %s CANARYKEY7f3a9c)"
C_OAUTH="sk-ant-oat01-$(printf %s CANARYOAUTH4b2e)"
C_PROMPT="CANARYPROMPT9d1f"
C_TOOL="CANARYTOOLOUT5a6c"
C_PATH="CANARYPATH8e0b"
C_HISTORY="CANARYHISTORY2c7d"
CANARIES=("$C_KEY" "$C_OAUTH" "$C_PROMPT" "$C_TOOL" "$C_PATH" "$C_HISTORY")

TSV="$OUT/canary.tsv"
: > "$TSV"
record() { printf '%s\t%s\t%s\n' "$1" "$2" "$3" >> "$TSV"; }

# Start the fake upstream.
ADDRF="$ROOT/fake-addr.txt"
"$FAKE" mock -addr-file "$ADDRF" >"$ROOT/fake.log" 2>&1 &
FAKE_PID=$!
for _ in $(seq 1 50); do [ -s "$ADDRF" ] && break; sleep 0.1; done
FAKE_ADDR="$(cat "$ADDRF")"
export TROVE_UPSTREAMS_ANTHROPIC="http://$FAKE_ADDR"

# Plant an agent history line that contains a canary, to test logs scan.
cat > "$ROOT/home/.claude/projects/canary-proj/session.jsonl" <<EOF
{"type":"user","message":{"role":"user","content":"$C_HISTORY please read the file"},"sessionId":"s1","timestamp":"2026-11-06T10:00:00Z"}
{"type":"assistant","requestId":"req1","message":{"role":"assistant","model":"claude-sonnet-5-5","content":[{"type":"text","text":"$C_HISTORY reply"}],"usage":{"input_tokens":1200,"output_tokens":300}},"sessionId":"s1","timestamp":"2026-11-06T10:00:05Z"}
EOF

# Start the gateway in the background.
"$BIN" gateway start --listen "127.0.0.1:$PORT" >"$ROOT/gateway.log" 2>&1 &
GW_PID=$!
for _ in $(seq 1 50); do
  if curl -s "http://127.0.0.1:$PORT/trove/health" >/dev/null 2>&1; then break; fi
  sleep 0.2
done

BODY=$(cat <<EOF
{"model":"claude-sonnet-5-5","max_tokens":64,"messages":[
 {"role":"user","content":"$C_PROMPT explain this code in $C_PATH"},
 {"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"cat $C_PATH"}}]},
 {"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"$C_TOOL output line"}]}
]}
EOF
)

# 1. A normal API-key request is forwarded.
code=$(curl -s -o "$ROOT/resp1.txt" -w '%{http_code}' -X POST "http://127.0.0.1:$PORT/anthropic/v1/messages" \
  -H "x-api-key: $C_KEY" -H "anthropic-version: 2023-06-01" -H "content-type: application/json" -d "$BODY")
if [ "$code" = "200" ]; then record "api-key request forwarded" PASS "HTTP $code"; else record "api-key request forwarded" FAIL "HTTP $code"; fi

# 2. An OAuth/subscription token is refused and never stored.
code=$(curl -s -o "$ROOT/resp2.txt" -w '%{http_code}' -X POST "http://127.0.0.1:$PORT/anthropic/v1/messages" \
  -H "Authorization: Bearer $C_OAUTH" -H "anthropic-version: 2023-06-01" -H "content-type: application/json" -d "$BODY")
if [ "$code" = "401" ]; then record "OAuth token refused" PASS "HTTP $code"; else record "OAuth token refused" FAIL "HTTP $code"; fi

# 3. Recording and reporting commands.
"$BIN" logs scan </dev/null >"$ROOT/logs.txt" 2>&1
"$BIN" report </dev/null >"$ROOT/report.txt" 2>&1
"$BIN" report --json </dev/null >"$ROOT/report.json" 2>&1
"$BIN" receipts </dev/null >"$ROOT/receipts.txt" 2>&1
"$BIN" data export --out "$ROOT/export/bundle" </dev/null >"$ROOT/export-cmd.txt" 2>&1
"$BIN" share --yes --out "$ROOT/share" </dev/null >"$ROOT/share-cmd.txt" 2>&1
"$BIN" gateway stop </dev/null >"$ROOT/stop.txt" 2>&1
sleep 1
kill "$FAKE_PID" 2>/dev/null

# Scan every surface Trove writes to, and every stdout/stderr it produced.
# Binary files (sqlite db) are scanned byte-wise; grep -a treats them as text.
SCAN="$OUT/canary_scan.txt"
: > "$SCAN"
for c in "${CANARIES[@]}"; do
  hits=$(grep -rla -- "$c" "$ROOT/data" "$ROOT/export" "$ROOT"/*.txt "$ROOT"/*.json "$ROOT"/*.svg "$ROOT/gateway.log" 2>/dev/null | sed "s|$ROOT/||")
  if [ -z "$hits" ]; then
    record "no $c in outputs" PASS "absent from data dir, export, and all command output"
  else
    record "no $c in outputs" FAIL "found in: $(echo "$hits" | tr '\n' ' ')"
    echo "$c -> $hits" >> "$SCAN"
  fi
done

# Expected by design: the gateway stores no prompt text, so the history canary
# may appear only in the agent's own log, never in Trove's db. Check the db alone.
if grep -qa -- "$C_PROMPT" "$ROOT/data/trove.db" 2>/dev/null; then
  record "prompt canary not in trove.db" FAIL "prompt text reached the database"
else
  record "prompt canary not in trove.db" PASS "database is metadata only"
fi

# Positive control: a scan that cannot fail proves nothing. Plant a canary the
# gateway never saw and confirm the same scan finds it.
echo "CONTROL$C_TOOL" > "$ROOT/data/positive-control.txt"
if grep -rla -- "CONTROL$C_TOOL" "$ROOT/data" >/dev/null 2>&1; then
  record "positive control detected by scan" PASS "scan finds a planted canary"
else
  record "positive control detected by scan" FAIL "scan missed a planted canary; the leak checks above are not trustworthy"
fi
rm -f "$ROOT/data/positive-control.txt"

echo "Wrote $TSV"
cat "$TSV"
