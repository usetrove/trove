# Shared settings and checks for the launch-study scripts. Sourced, never run directly.
set -euo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"

STUDY="bench/studies/launch-1.yaml"
STUDY_ID="launch-1"
TASKS="bench/tasks/longsession30"
PILOT_TASKS="bench/tasks/longsession-pilot"
DATASET_COMMIT="7e0611e77b54e2dea774cdc0aa00cf9f7ed6144f"
BATCH="${BATCH:-bench/results/launch/${STUDY_ID}}"
PILOT_BATCH="${PILOT_BATCH:-bench/results/launch/pilot}"
CAPS="docs/hook-capabilities.json"
# Total you are willing to spend on this study, in USD. The study file's own hard cap (budget.batch_cap_usd) is the
# ceiling; this can only lower it.
BUDGET_USD="${BUDGET_USD:-70}"

say()  { printf '\n== %s\n' "$*"; }
stop() { printf '\nSTOP: %s\n' "$*" >&2; exit 1; }

need_key() {
  [ -n "${ANTHROPIC_API_KEY:-}" ] || stop "ANTHROPIC_API_KEY is not set in this shell. Export your own pay-per-use API key (never a subscription login)."
}

need_clean_tree() {
  [ -z "$(git status --porcelain)" ] || { git status --short >&2; stop "the git tree has uncommitted changes, so results would be marked trove_dirty. Commit or stash them first."; }
}

need_tasks() {
  if [ ! -d "$TASKS" ] || [ "$(ls "$TASKS" | wc -l)" -lt 30 ]; then
    say "Generating the long-session tasks (pinned dataset commit ${DATASET_COMMIT:0:10})"
    python -m trovebench fetch longsession -n 30 --seed 20261118 --commit "$DATASET_COMMIT" --out "$TASKS"
  fi
}

need_probe_for_pinned_version() {
  local want
  want="$(python -c 'import re;print(re.search(r"^claude_version:\s*\"?([^\"\s#]+)", open("bench/config.yaml").read(), re.M).group(1))')"
  grep -q "\"agent_version\": \"$want\"" "$CAPS" || stop "$CAPS has no live hook probe for Claude Code $want (the version pinned in bench/config.yaml). Run: go run ./scripts/hookprobe --live --max-usd 0.25 --out /tmp/hookprobe.json, then copy it to $CAPS and commit."
}
