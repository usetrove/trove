#!/usr/bin/env bash
# Free. After 03_run.sh: build the report, gather only the publishable files into bench/published/<study id>/,
# regenerate the recommender defaults, benchmark priors and Autopilot thresholds from every published study, and commit.
#     bash bench/scripts/launch/04_report.sh
# It never copies runs/ (agent output) or any task text. It does not push: review the commit, then `git push`.
. "$(dirname "${BASH_SOURCE[0]}")/_common.sh"
[ -f "$BATCH/ledger.jsonl" ] || stop "no study results at $BATCH. Run 03_run.sh first."
DST="bench/published/$STUDY_ID"

say "Report"
python -m trovebench report --batch "$BATCH"
python -m trovebench stage --batch "$BATCH" > "$BATCH/stage.json" || true
python bench/scripts/activity.py "$BATCH" --markdown > "$BATCH/activity.md" || echo "  (optimizer activity summary unavailable)"
{ printf '\n### What the hooks did in this study\n\n'; python bench/tools/hook_activity.py "$BATCH"; } >> "$BATCH/activity.md" || echo "  (hook activity summary unavailable)"

say "Publishable files -> $DST"
mkdir -p "$DST/charts"
for f in report.md comparisons.csv pairs.csv runs.csv summary.csv versions.json config.used.yaml study.lock.json \
         stage.json activity.md; do
  [ -f "$BATCH/$f" ] && cp "$BATCH/$f" "$DST/$f" || echo "  (not present, skipped: $f)"
done
cp "$BATCH"/charts/*.png "$DST/charts/" 2>/dev/null || cp "$BATCH"/*.png "$DST/charts/" 2>/dev/null || true
if grep -rIl 'sk-ant-' "$DST" >/dev/null 2>&1; then stop "a published file contains something that looks like an API key. Nothing was committed."; fi

say "Recommender defaults and benchmark priors from every published study (plan item 1.4)"
RESULTS=()
for d in bench/published/*/; do [ -f "$d/study.lock.json" ] && [ -f "$d/comparisons.csv" ] && RESULTS+=("${d%/}"); done
python -m trovebench defaults --results "${RESULTS[@]}"
say "Autopilot trial thresholds per tier from every published study (plan item 10.7)"
python -m trovebench thresholds --results "${RESULTS[@]}"
go test ./internal/recommend/... ./internal/experiment/... ./internal/autopilot/... >/dev/null && echo "generated files load in Go: ok"

say "Verdicts"
python - "$BATCH/stage.json" <<'EOF'
import json, sys
d = json.load(open(sys.argv[1]))
plain = {"ship": "saving confirmed", "need_more_data": "not proven yet", "dont_ship": "worse: no savings claim"}
for c in d["comparisons"]:
    print(f"  {c['condition']:<26} {plain.get(c['verdict'], c['verdict'])}")
EOF
echo "Full report: $DST/report.md"

say "Commit"
git add "$DST" internal/recommend/defaults.toml internal/experiment/priors.toml internal/autopilot/thresholds.toml
git commit -m "bench: publish $STUDY_ID results" -m "Report, verdicts and per-run metadata from the pre-registered study; no agent output or task text." || echo "(nothing new to commit)"
echo
echo "Review with: git show --stat HEAD   then push with: git push"
echo "When you are completely finished: unset ANTHROPIC_API_KEY, and rotate or delete the key in the Console."
