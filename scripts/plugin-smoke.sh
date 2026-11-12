#!/bin/sh
# Smoke test for the Claude Code plugin's hooks, run through the plugin's own wrapper (plugin/trove/bin/trove),
# with no model call. Every hook is fed a fixture from internal/hooks/testdata; the live probe runs the zero-spend
# stub agent (qa/stubagent) and checks that the budgets it was handed add up to no more than the cap.
#
#   TROVE_DATA_DIR=<scratch folder> sh scripts/plugin-smoke.sh <path-to-stubagent>
#
# Needs `trove` on PATH (the build under test). It sends nothing to any provider: the key below is a fake in the
# accepted shape, and the stub never makes a request.
set -eu

repo=$(cd "$(dirname "$0")/.." && pwd)
root="$repo/plugin/trove"
stub="${1:?usage: plugin-smoke.sh <path-to-stubagent>}"
: "${TROVE_DATA_DIR:?set TROVE_DATA_DIR to a scratch folder}"
export CLAUDE_PLUGIN_ROOT="$root"

fail() { echo "plugin-smoke: $*" >&2; exit 1; }
command -v trove >/dev/null 2>&1 || fail "trove is not on PATH"
command -v sh >/dev/null 2>&1 || fail "sh is not on PATH; the plugin's hooks need it"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
printf 'alpha beta gamma\n' > "$work/notes.txt"

# 1. The SessionStart hook runs through the wrapper and exits 0.
sh "$root/bin/trove" hook session_reset </dev/null >/dev/null 2>&1 || fail "the session_reset hook failed"

# 2. The Read hook fires through the wrapper on a fixture payload and records a session file.
sed -e "s|{{SESSION}}|smoke-session|g" -e "s|{{PROMPT}}|smoke-prompt|g" -e "s|{{FILE}}|$work/notes.txt|g" \
  "$repo/internal/hooks/testdata/claude_post_read.json" \
  | sh "$root/bin/trove" hook post_tool_read_record >/dev/null 2>&1 || fail "the post_tool_read_record hook failed"
find "$TROVE_DATA_DIR/hooks/sessions" -name '*.feat' 2>/dev/null | grep -q . \
  || fail "the Read hook ran but recorded no session file under $TROVE_DATA_DIR/hooks/sessions"

# 3. The plugin's first-run command runs through the wrapper.
sh "$root/bin/trove" plugin start </dev/null >/dev/null 2>&1 || fail "trove plugin start failed"

# 4. The live probe runs the stub agent under a capped budget. The stub never calls a model.
ANTHROPIC_API_KEY="sk-ant-api03-FAKESMOKEKEYFORCIONLY0123456789abcdefghij" \
  trove hooks probe --live --max-usd 0.10 --claude "$stub" --json >"$work/probe.json" 2>"$work/probe.err" \
  || { cat "$work/probe.err" >&2; fail "the live probe with the stub agent failed"; }
log="$(dirname "$stub")/stubagent-args.log"
[ -s "$log" ] || fail "the stub agent was not called by the live probe"
total=$(tr '\037' ' ' < "$log" | grep -o -- '--max-budget-usd [0-9.]*' | awk '{s+=$2} END {printf "%.2f", s}')
awk -v t="$total" 'BEGIN { exit !(t <= 0.10 + 1e-9) }' \
  || fail "the stub was handed $total USD of budget, above the 0.10 cap"
rm -f "$log"

echo "plugin-smoke: ok. Hooks ran through the wrapper; the stub was handed $total USD of a 0.10 cap."
