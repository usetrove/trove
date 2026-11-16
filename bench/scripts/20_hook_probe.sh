#!/usr/bin/env bash
# Runbook PART 5: the live hook probe. Needed only for Study B (the plugin / hooks conditions).
# Spends at most $0.25 of real API money, so it needs ANTHROPIC_API_KEY in this shell. Run it once.
#     export ANTHROPIC_API_KEY=...        (a real API key, never a subscription login)
#     bash bench/scripts/20_hook_probe.sh
# Output goes to /tmp/hookprobe.json (NOT into the repo: the default location would dirty the tree and
# every later run would then be recorded as trove_dirty).
set -euo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
OUT="${OUT:-/tmp/hookprobe.json}"

[ -n "${ANTHROPIC_API_KEY:-}" ] || { echo "STOP: ANTHROPIC_API_KEY is not set in this shell." >&2; exit 1; }
command -v go >/dev/null || { echo "STOP: go is not installed or not on PATH (needed to run the probe)." >&2; exit 1; }
[ -z "$(git status --porcelain)" ] || { echo "STOP: the git tree is not clean, so results would be recorded as trove_dirty:" >&2; git status --short >&2; exit 1; }

go run ./scripts/hookprobe --live --max-usd 0.25 --out "$OUT"

[ -z "$(git status --porcelain)" ] || { echo "STOP: the probe changed files in the repo (should not happen):" >&2; git status --short >&2; exit 1; }
echo
echo "Probe result: $OUT"
want="$(python - <<'EOF'
import re
m = re.search(r'^claude_version:\s*"?([^"\s#]+)', open("bench/config.yaml").read(), re.M)
print(m.group(1) if m else "?")
EOF
)"
echo "claude_version pinned in bench/config.yaml: $want"
grep -o '"agent_version"[^,}]*' "$OUT" || true
echo "These two versions must be the same. If they differ, stop and tell Claude."
echo "Next: bash bench/scripts/30_make_studies.sh"
