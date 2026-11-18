#!/usr/bin/env bash
# Free. Proves the harness works on this machine before any money is spent:
#   1. the bench's own tests,
#   2. the long-session tasks are generated,
#   3. the hook probe covers the pinned Claude Code version,
#   4. a fake end-to-end run in Docker (fake agent, mock upstream, the real Trove gateway and hooks in between).
#     bash bench/scripts/launch/01_check.sh
. "$(dirname "${BASH_SOURCE[0]}")/_common.sh"

command -v docker >/dev/null && docker info >/dev/null 2>&1 || stop "Docker is not running or your user cannot reach it (log out and back in after 00_vm_setup.sh)."
command -v go >/dev/null || stop "go is not on PATH."

say "1/4 bench tests"
python -m pytest -q bench/tests

say "2/4 long-session tasks"
need_tasks
echo "$(ls "$TASKS" | wc -l) tasks in $TASKS"

say "3/4 hook probe covers the pinned Claude Code version"
need_probe_for_pinned_version
echo "ok"

say "4/4 fake end-to-end runs (no API key used, no spend)"
rm -rf bench/results/launch/check-fake bench/results/launch/check-fake-long
python -m trovebench smoke --fake --batch bench/results/launch/check-fake
grep -q "fake agent" bench/results/launch/check-fake/report.md || stop "the fake run did not produce its report."
# One long-session task: the prompt and its follow-ups run as one session in one container. The fake agent cannot
# solve it, so "failed" is expected; what matters is that every run is gradeable (status ok), not an error.
FAKE_TASKS=bench/results/launch/fake-long-tasks
rm -rf "$FAKE_TASKS" && mkdir -p "$FAKE_TASKS" && cp -r "$TASKS/$(ls "$TASKS" | head -1)" "$FAKE_TASKS/"
python bench/scripts/launch/make_config.py bench/results/launch/fake-long.yaml claude-haiku-5-5 1 0.5 passthrough all_safe >/dev/null
python -m trovebench run --fake --config bench/results/launch/fake-long.yaml --tasks "$FAKE_TASKS" \
  --batch bench/results/launch/check-fake-long || true
python - <<'EOF'
import json, sys
rows = [json.loads(l) for l in open("bench/results/launch/check-fake-long/ledger.jsonl") if l.strip()]
bad = [r for r in rows if r["status"] != "ok"]
if not rows or bad:
    sys.exit(f"STOP: the fake long-session run had {len(bad)} error(s): {[r.get('note','')[:160] for r in bad]}")
print(f"ok: {len(rows)} long-session runs, all gradeable")
EOF

say "All checks passed. Next: bash bench/scripts/launch/02_pilot.sh  (real money: about \$1-3)"
