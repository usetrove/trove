# Shared settings and checks for the study scripts. Sourced, never run directly.
# The study defaults to the launch study; pass another with STUDY=bench/studies/<id>.yaml.
set -euo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"

STUDY="${STUDY:-bench/studies/launch-1.yaml}"
[ -f "$STUDY" ] || { echo "STOP: no study file at $STUDY" >&2; exit 1; }
study_field() { python -c 'import sys,yaml; v=yaml.safe_load(open(sys.argv[1])).get(sys.argv[2]); print(" ".join(map(str,v)) if isinstance(v,list) else ("" if v is None else v))' "$STUDY" "$1"; }
STUDY_ID="$(study_field id)"
TASKS="bench/$(study_field tasks)"
MODEL="$(study_field model)"
ROUTE_TO="$(study_field route_to)"
PILOT_TASKS="bench/results/launch/${STUDY_ID}-pilot-tasks"
DATASET_COMMIT="7e0611e77b54e2dea774cdc0aa00cf9f7ed6144f"
BATCH="${BATCH:-bench/results/launch/${STUDY_ID}}"
PILOT_BATCH="${PILOT_BATCH:-bench/results/launch/${STUDY_ID}-pilot}"
CAPS="docs/hook-capabilities.json"
# Total you are willing to spend on this study, in USD. The study file's own hard cap (budget.batch_cap_usd) is the
# ceiling; this can only lower it.
BUDGET_USD="${BUDGET_USD:-$(python -c 'import sys,yaml; print(yaml.safe_load(open(sys.argv[1]))["budget"]["batch_cap_usd"])' "$STUDY")}"

say()  { printf '\n== %s\n' "$*"; }
stop() { printf '\nSTOP: %s\n' "$*" >&2; exit 1; }

need_key() {
  [ -n "${ANTHROPIC_API_KEY:-}" ] || stop "ANTHROPIC_API_KEY is not set in this shell. Export your own pay-per-use API key (never a subscription login)."
}

need_clean_tree() {
  [ -z "$(git status --porcelain)" ] || { git status --short >&2; stop "the git tree has uncommitted changes, so results would be marked trove_dirty. Commit or stash them first."; }
}

# The task folders are generated from pinned dataset revisions and never committed (the exercise text is Exercism's).
need_tasks() {
  if [ ! -d "$TASKS" ] || [ "$(ls "$TASKS" | wc -l)" -lt 30 ]; then
    case "$TASKS" in
      bench/tasks/longsession30)
        say "Generating the long-session tasks (pinned dataset commit ${DATASET_COMMIT:0:10})"
        python -m trovebench fetch longsession -n 30 --seed 20261118 --commit "$DATASET_COMMIT" --out "$TASKS" ;;
      bench/tasks/polyglot30)
        say "Generating the polyglot tasks (pinned dataset commit ${DATASET_COMMIT:0:10})"
        python -m trovebench fetch polyglot -n 30 --seed 20261108 --commit "$DATASET_COMMIT" --out "$TASKS" ;;
      *) stop "no task folder at $TASKS, and this script does not know how to generate it." ;;
    esac
  fi
}

need_probe_for_pinned_version() {
  local want
  want="$(python -c 'import re;print(re.search(r"^claude_version:\s*\"?([^\"\s#]+)", open("bench/config.yaml").read(), re.M).group(1))')"
  grep -q "\"agent_version\": \"$want\"" "$CAPS" || stop "$CAPS has no live hook probe for Claude Code $want (the version pinned in bench/config.yaml). Run: go run ./scripts/hookprobe --live --max-usd 0.25 --out /tmp/hookprobe.json, then copy it to $CAPS and commit."
}

# The study's conditions as run-config specs: conditions that need the cheaper model carry it after a colon.
pilot_conditions() {
  for c in $(study_field conditions); do
    case "$c" in
      trove_full) echo "trove_full:$ROUTE_TO" ;;
      alone_model_router) echo "alone:model_router:$ROUTE_TO" ;;
      *) echo "$c" ;;
    esac
  done
}
