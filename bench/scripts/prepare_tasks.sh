#!/usr/bin/env bash
# Prepare the SWE-bench task sets: runbook steps 6.1 to 6.3 plus the pilot folder.
# Calls no model and spends no API money. It does download Docker images and run tests.
#
# Run from anywhere, ideally inside tmux so a dropped SSH connection does not stop it:
#     tmux new -s prep
#     bash bench/scripts/prepare_tasks.sh
#
# Safe to run again: fetched tasks are kept, and the validation log of every run is kept.
# Settings (all optional, give them as VAR=value before the command):
#     SEED=20261114   seed for the random sample and for the main/warm-up split
#     N=80            candidates to fetch
#     MAIN=35         tasks in the evaluation set
#     WARM=8          tasks in the separate warm-up set
#     PILOT=3         tasks in the pilot folder (taken from the main set)
#     FRESH=1         delete the candidate and output folders first and start over
#     SKIP_VALIDATE=1 reuse existing validation.json markers (used by the tests)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

SEED="${SEED:-20261114}"
N="${N:-80}"
MAIN="${MAIN:-35}"
WARM="${WARM:-8}"
PILOT="${PILOT:-3}"
TASKS="${TASKS:-bench/tasks}"
CAND="$TASKS/swebench-cand"
MAIN_DIR="$TASKS/swebench-main"
WARM_DIR="$TASKS/swebench-warmup"
PILOT_DIR="$TASKS/swebench-pilot"
LOGDIR="${LOGDIR:-bench/results/prep}"
STAMP="$(date +%Y%m%d-%H%M%S)"

say() { printf '\n== %s\n' "$*"; }
die() { printf '\nSTOP: %s\n' "$*" >&2; exit 1; }

say "0. Checks"
if [ -z "${SKIP_VALIDATE:-}" ]; then
  command -v docker >/dev/null || die "docker is not installed or not on PATH."
  docker info >/dev/null 2>&1 || die "docker is not running, or your user is not in the docker group (run: groups)."
  python -m trovebench --help >/dev/null 2>&1 || die "cannot run 'python -m trovebench'. Activate the virtualenv (source .venv/bin/activate) and run from the repo."
  AVAIL_GB="$(df -BG --output=avail "$HOME" | tail -1 | tr -dc '0-9')"
  echo "free disk: ${AVAIL_GB} GB (SWE-bench recommends about 120 GB)"
  [ "${AVAIL_GB:-0}" -ge 100 ] || die "less than 100 GB free. Free some space (docker system df, docker image prune) and retry."
fi
mkdir -p "$LOGDIR"

if [ -n "${FRESH:-}" ]; then
  say "FRESH=1: removing $CAND $MAIN_DIR $WARM_DIR $PILOT_DIR"
  rm -rf "$CAND" "$MAIN_DIR" "$WARM_DIR" "$PILOT_DIR"
fi
for d in "$MAIN_DIR" "$WARM_DIR" "$PILOT_DIR"; do
  [ ! -e "$d" ] || die "$d already exists. Remove it, or run with FRESH=1, so one set is never silently mixed with another."
done

say "1. Fetch $N candidates (seed $SEED)"
if [ -d "$CAND" ] && [ -n "$(ls -A "$CAND" 2>/dev/null)" ]; then
  echo "$CAND already has tasks: keeping them (use FRESH=1 to refetch)."
else
  python -m trovebench fetch swebench -n "$N" --seed "$SEED" --out "$CAND"
fi
echo "candidates: $(ls "$CAND" | wc -l)"

say "2. Validate (the slow step: downloads images, runs tests twice per task)"
LOG="$LOGDIR/validate-$STAMP.txt"
if [ -n "${SKIP_VALIDATE:-}" ]; then
  echo "SKIP_VALIDATE set: using the validation.json markers already present."
else
  # validate exits 1 when any task is rejected, which is normal here, so do not stop on it.
  python -u -m trovebench validate --tasks "$CAND" 2>&1 | tee "$LOG" || true
  echo "log: $LOG"
fi

mapfile -t OK_IDS < <(find "$CAND" -mindepth 2 -maxdepth 2 -name validation.json -printf '%h\n' | xargs -r -n1 basename | sort)
OK="${#OK_IDS[@]}"
say "3. Result: $OK of $(ls "$CAND" | wc -l) tasks validated"
if [ -f "$LOG" ]; then
  echo "rejected, by reason (first 10 distinct):"
  grep '^REJECTED' "$LOG" | sed -E 's/^REJECTED +[^ ]+: //; s/ +\[[0-9]+\/[0-9]+ [0-9]+%\]$//' | cut -c1-140 | sort | uniq -c | sort -rn | head -10 || true
fi
NEED=$((MAIN + WARM))
if [ "$OK" -lt "$NEED" ]; then
  die "only $OK validated tasks, and you need $NEED ($MAIN main + $WARM warm-up). Send the rejection reasons above to Claude, or fetch more candidates (N=160 FRESH=1 bash $0)."
fi

say "4. Split into main / warm-up / pilot (disjoint, seed $SEED)"
SPLIT="$LOGDIR/split-$STAMP.txt"
# Compute the split first, so a failure here leaves no half-made folders behind.
printf '%s\n' "${OK_IDS[@]}" | python -c '
import random, sys
seed, main, warm, pilot = (int(x) for x in sys.argv[1:5])
ids = sorted(l.strip() for l in sys.stdin if l.strip())
random.Random(seed).shuffle(ids)
sets = {"main": ids[:main], "warmup": ids[main:main + warm]}
sets["pilot"] = sets["main"][:pilot]
for name, members in sets.items():
    print(name, *members)
' "$SEED" "$MAIN" "$WARM" "$PILOT" | tr -d '\r' > "$SPLIT"
trap 'rm -rf "$MAIN_DIR" "$WARM_DIR" "$PILOT_DIR"; echo "copy failed: removed the partly made sets" >&2' ERR
mkdir -p "$MAIN_DIR" "$WARM_DIR" "$PILOT_DIR"
while read -r name members; do
  case "$name" in main) dst="$MAIN_DIR";; warmup) dst="$WARM_DIR";; pilot) dst="$PILOT_DIR";; *) continue;; esac
  for id in $members; do cp -r "$CAND/$id" "$dst/$id"; done
  echo "$name: $(ls "$dst" | wc -l) tasks in $dst"
done < "$SPLIT"
trap - ERR

say "Done. Nothing here spent API money. Next (this one DOES spend money, so check your keys and caps first):"
cat <<EOF
  python -m trovebench plan --tasks $PILOT_DIR --repeats 1
  python -m trovebench run  --tasks $PILOT_DIR --repeats 1 --yes --batch bench/results/pilot-1 2>&1 | tee /tmp/pilot.log
The split is recorded in $LOGDIR/split-$STAMP.txt
EOF
