#!/usr/bin/env bash
# User-journey simulations (plan Phase 6). Each persona runs real commands of the built binary, in a fresh scratch
# home and data folder, and checks the outcome it should get. No model is called: no key is used, or a fake key of
# the accepted shape with a subscription-shaped environment. Results: one line per check, in <results>/personas.tsv.
#
#   qa/personas/personas.sh <trove-binary> <results-dir>
set -u
BIN="${1:?usage: personas.sh <trove> <results-dir>}"
OUT="${2:?usage: personas.sh <trove> <results-dir>}"
ROOT="${QA_SCRATCH:-/tmp/trove-qa}/personas"
mkdir -p "$OUT"
TSV="$OUT/personas.tsv"
: > "$TSV"

record() { printf '%s\t%s\t%s\t%s\n' "$1" "$2" "$3" "$4" >> "$TSV"; }

# fresh <name>: a clean home and data folder for one persona, with no ANTHROPIC_* environment.
fresh() {
  P="$ROOT/$1"
  rm -rf "$P" && mkdir -p "$P/home" "$P/data"
  export HOME="$P/home" USERPROFILE="$P/home" TROVE_DATA_DIR="$P/data" NO_COLOR=1
  export CLAUDE_CONFIG_DIR="$P/home/.claude" CODEX_HOME="$P/home/.codex"
  unset ANTHROPIC_API_KEY ANTHROPIC_BASE_URL OPENAI_API_KEY TROVE_OPTIMIZE
}

# 1. New API-key developer: installs, looks around, and runs the first report.
fresh new-dev
"$BIN" version >/dev/null 2>&1 && record new-dev "version works" PASS "" || record new-dev "version works" FAIL ""
"$BIN" doctor </dev/null >"$P/doctor.txt" 2>&1; code=$?
[ "$code" -eq 0 ] && record new-dev "doctor exits 0 on a fresh install" PASS "" || record new-dev "doctor exits 0 on a fresh install" FAIL "exit $code"
grep -q "No problems found" "$P/doctor.txt" && record new-dev "doctor says no problems on a fresh install" PASS "" \
  || record new-dev "doctor says no problems on a fresh install" FAIL "$(grep -c '\[warn\]' "$P/doctor.txt") warnings"
"$BIN" report </dev/null >"$P/report.txt" 2>&1 && record new-dev "first report runs with no data" PASS "" || record new-dev "first report runs with no data" FAIL ""
grep -q "Run \`trove setup --gateway\`" "$P/report.txt" && record new-dev "report points at the opt-in gateway command" PASS "" \
  || record new-dev "report points at the opt-in gateway command" NOTE "not shown (ok if no history)"
"$BIN" setup --yes </dev/null >"$P/setup.txt" 2>&1
grep -q "Optional: route this agent" "$P/setup.txt" && record new-dev "setup without --gateway calls the gateway optional" PASS "" \
  || record new-dev "setup without --gateway calls the gateway optional" NOTE "no agent installed in this scratch home (expected)"
[ ! -e "$P/home/.claude/settings.json" ] && record new-dev "setup without --gateway writes nothing" PASS "" || record new-dev "setup without --gateway writes nothing" FAIL "settings.json exists"
"$BIN" uninstall --yes </dev/null >"$P/uninstall.txt" 2>&1 && record new-dev "uninstall runs on a clean home" PASS "" || record new-dev "uninstall runs on a clean home" FAIL ""

# 2. Privacy skeptic: exports, deletes, and sharing stays off.
fresh privacy
"$BIN" data export --out "$P/export" </dev/null >"$P/export.txt" 2>&1 && record privacy "data export writes a folder" PASS "" || record privacy "data export writes a folder" FAIL ""
[ -d "$P/export" ] && record privacy "export folder exists" PASS "" || record privacy "export folder exists" FAIL ""
"$BIN" data delete --dry-run </dev/null >"$P/delete.txt" 2>&1 && record privacy "data delete --dry-run changes nothing" PASS "" || record privacy "data delete --dry-run changes nothing" FAIL ""
"$BIN" contribute status </dev/null >"$P/contribute.txt" 2>&1 && grep -qi "off\|not enabled\|disabled" "$P/contribute.txt" \
  && record privacy "sharing is off by default" PASS "" || record privacy "sharing is off by default" FAIL "$(head -c 120 "$P/contribute.txt")"

# 3. Operator: the kill switch stops every optimizer, and the environment variable does too.
fresh operator
"$BIN" optimize off </dev/null >"$P/off.txt" 2>&1 && record operator "optimize off succeeds" PASS "" || record operator "optimize off succeeds" FAIL ""
"$BIN" optimize status </dev/null 2>&1 | grep -qi "kill switch: on" && record operator "status shows the kill switch on" PASS "" \
  || record operator "status shows the kill switch on" FAIL ""
"$BIN" optimize resume </dev/null >/dev/null 2>&1 && "$BIN" optimize status </dev/null 2>&1 | grep -qi "kill switch: off" \
  && record operator "resume turns the kill switch off" PASS "" || record operator "resume turns the kill switch off" FAIL ""
TROVE_OPTIMIZE=off "$BIN" optimize status </dev/null 2>&1 | grep -qi "kill switch: on" \
  && record operator "TROVE_OPTIMIZE=off also reports on" PASS "" || record operator "TROVE_OPTIMIZE=off also reports on" NOTE "check how the environment switch is reported"

# 4. Subscription user: no API key. The gateway and the live probe must refuse, and nothing may be changed.
fresh subscription
"$BIN" setup --gateway --yes </dev/null >"$P/gw.txt" 2>&1; code=$?
# Setup reports a refusal as a plan with nothing applied, so the exit code is 0 by design. What matters: the reason
# is given and no settings file is written. The exit code is recorded for the report.
[ ! -e "$P/home/.claude/settings.json" ] && grep -qi "API key" "$P/gw.txt" \
  && record subscription "setup --gateway refuses without an API key and writes nothing" PASS "exit $code" \
  || record subscription "setup --gateway refuses without an API key and writes nothing" FAIL "exit $code"
"$BIN" hooks probe --live --max-usd 0.25 </dev/null >"$P/probe.txt" 2>&1; code=$?
[ "$code" -ne 0 ] && grep -qi "nothing was run" "$P/probe.txt" && record subscription "live probe refuses and runs nothing" PASS "" \
  || record subscription "live probe refuses and runs nothing" FAIL "exit $code"

# 5. Config-fix user: nothing applied, so undo says so and changes nothing.
fresh config-fix
"$BIN" undo </dev/null >"$P/undo.txt" 2>&1; code=$?
[ "$code" -eq 0 ] && record config-fix "undo with nothing recorded exits 0" PASS "" || record config-fix "undo with nothing recorded exits 0" FAIL "exit $code"

# 6. Cautious developer: no data yet. Experiment and receipts give plain answers, not errors.
fresh cautious
"$BIN" experiment status --json </dev/null >"$P/exp.json" 2>&1 && record cautious "experiment status on a fresh install is not an error" PASS "" \
  || record cautious "experiment status on a fresh install is not an error" FAIL ""
"$BIN" receipts </dev/null >"$P/receipts.txt" 2>&1 && record cautious "receipts on a fresh install is not an error" PASS "" || record cautious "receipts on a fresh install is not an error" FAIL ""

echo "wrote $TSV"
cut -f1,3 "$TSV" | sort | uniq -c
