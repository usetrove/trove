#!/usr/bin/env bash
# B1 from RECORD.md: gateway overhead under concurrency, measured on this machine, with zero spend.
# The upstream is the benchfake mock on loopback; the gateway is the real `trove gateway start`.
#
#   qa/benchmarks/run_overhead.sh <trove-binary> <results-dir>
set -u
BIN="${1:?usage: run_overhead.sh <trove> <results-dir>}"
OUT="${2:?usage: run_overhead.sh <trove> <results-dir>}"
HERE="$(cd "$(dirname "$0")/.." && pwd)"
SCR="${QA_SCRATCH:-/tmp/trove-qa}/overhead"
BINDIR="$HERE/bin"
mkdir -p "$SCR" "$OUT" "$BINDIR"

go build -o "$BINDIR/benchfake.exe" ./bench/tools/benchfake 2>/dev/null || (cd "$HERE/.." && go build -o "$BINDIR/benchfake.exe" ./bench/tools/benchfake)
go build -o "$BINDIR/loadtest.exe" "$HERE/loadtest" 2>/dev/null || (cd "$HERE" && go build -o "$BINDIR/loadtest.exe" ./loadtest)
LT="$BINDIR/loadtest.exe"; FAKE="$BINDIR/benchfake.exe"

export TROVE_DATA_DIR="$SCR/data" HOME="$SCR/home" USERPROFILE="$SCR/home" NO_COLOR=1
mkdir -p "$TROVE_DATA_DIR" "$HOME"
unset ANTHROPIC_BASE_URL

rm -f "$SCR/fake-addr.txt"   # a stale address from an earlier run would point the load at a dead port
"$FAKE" mock -addr-file "$SCR/fake-addr.txt" >"$SCR/fake.log" 2>&1 &
FAKE_PID=$!
for _ in $(seq 1 50); do [ -s "$SCR/fake-addr.txt" ] && break; sleep 0.1; done
FAKE_ADDR="$(cat "$SCR/fake-addr.txt")"
for _ in $(seq 1 50); do curl -s "http://$FAKE_ADDR/v1/messages" -o /dev/null -X POST -d "{}" 2>/dev/null && break; sleep 0.1; done
GW_PORT=18766
TROVE_UPSTREAMS_ANTHROPIC="http://$FAKE_ADDR" "$BIN" gateway start --listen "127.0.0.1:$GW_PORT" >"$SCR/gw.log" 2>&1 &
for _ in $(seq 1 50); do curl -s "http://127.0.0.1:$GW_PORT/trove/health" >/dev/null 2>&1 && break; sleep 0.2; done

# The body is a short, ordinary request. The key is a fake in the accepted shape, sent to the local fake only.
cat > "$SCR/body.json" <<'EOF'
{"model":"claude-sonnet-5-5","max_tokens":64,"messages":[{"role":"user","content":"Reply with the word ok."}]}
EOF
KEY="sk-ant-api03-FAKEBENCHKEY0123456789abcdefghijkl"
PER=${PER:-20}

{
  printf 'path\tconcurrency\trequests\tp50_ms\tp90_ms\tp99_ms\terrors\n'
  for c in 1 10 50; do
    printf 'direct\t'; "$LT" -url "http://$FAKE_ADDR/v1/messages" -body "$SCR/body.json" -key "$KEY" -c "$c" -n "$PER"
    printf 'gateway\t'; "$LT" -url "http://127.0.0.1:$GW_PORT/anthropic/v1/messages" -body "$SCR/body.json" -key "$KEY" -c "$c" -n "$PER"
  done
} | tee "$OUT/overhead.tsv"

"$BIN" gateway stop >/dev/null 2>&1
kill "$FAKE_PID" 2>/dev/null
echo "wrote $OUT/overhead.tsv"
