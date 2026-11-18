# Launch study runbook

The final launch study is `bench/studies/launch-2.yaml`: long Claude Code working sessions on Sonnet (the workday
suite: an exercise plus seven ordinary follow-ups), comparing no optimization, the Trove plugin, and the plugin with
the gateway and model routing. Every arm launches Claude Code the same way, which launch-1 did not (see the correction
at the end of `bench/published/launch-1/report.md`). Every number it produces is labeled: pass/fail and token counts
are measured; cost is estimated (list prices applied to the provider-reported usage the gateway records for each
request).

The scripts default to launch-2. launch-1 is finished and published; do not rerun it.

## What you need

- An Ubuntu 24.04 x86_64 VM with 4+ vCPUs, 16 GB RAM and 40 GB disk.
- A clone of this repository on the VM, at the latest commit of the branch. `03_run.sh` locks the study (commits `bench/studies/launch-2.lock.json`) before its first run.
- Your own pay-per-use Anthropic API key. Never a subscription login.

## Steps

```sh
bash bench/scripts/launch/00_vm_setup.sh      # once; then log out and back in
export ANTHROPIC_API_KEY=...                  # your key; it is passed to containers by name, never written to disk
bash bench/scripts/launch/01_check.sh         # free: tests and fake end-to-end runs
bash bench/scripts/launch/02_pilot.sh         # about $5-15 (cap $20): proves the conditions work, measures cost per run
tmux new -s study                             # so the run survives a closed SSH session
bash bench/scripts/launch/03_run.sh           # the study, overnight; detach with Ctrl-b d, reattach: tmux attach -t study
bash bench/scripts/launch/04_report.sh        # free: report, publish folder, priors, commit
git push
```

Each script stops with a plain message if something is wrong. Do not continue past a STOP.

## Money

- `BUDGET_USD` (default 100) limits the study; the study file's `budget.batch_cap_usd` (100) is the hard ceiling.
  To spend less, run `export BUDGET_USD=70` before the pilot; fewer looks fit, so the result is less likely to be decided.
- The pilot measures cost per run and decides how many looks (repeats, at most 3) fit the budget.
- The study stops early once both comparisons are decided. Each look is tested at alpha / 3, so stopping early
  does not make a result easier to reach.
- `03_run.sh` can be rerun after an interruption; finished runs are kept and not paid for twice.

## What the result means

- Each comparison gets one of three verdicts under the registered rule (the report's own labels in brackets):
  - **saving confirmed** (SHIP): the whole interval of the cost change is at or below -5% (at the stricter per-look
    level), task success is not more than 5 points lower, and there are at least 30 pairs. You can call it verified.
  - **not proven yet** (NEED MORE DATA): the evidence does not settle it either way. Show the numbers only as early.
  - **worse** (DON'T SHIP): it cost more or lowered task success. Make no savings claim for it.
- The claim covers long working sessions (the workday suite) on Sonnet with this Claude Code version. It says nothing about
  one-shot tasks, other models or other agents.
- Publish whatever comes back, including "not proven yet".

## Later studies

The same scripts run the other registered studies; pass the study file in `STUDY` to steps 2 to 4:

```sh
STUDY=bench/studies/models-1.yaml bash bench/scripts/launch/02_pilot.sh
STUDY=bench/studies/models-1.yaml bash bench/scripts/launch/03_run.sh
STUDY=bench/studies/models-1.yaml bash bench/scripts/launch/04_report.sh
```

| Study | What it answers | Plan items | Cap |
|---|---|---|---|
| `launch-2` | no optimization vs the plugin vs plugin + gateway + routing, workday Sonnet sessions | 10.3, launch claim | $100 |
| `launch-1` | finished and published; its plugin comparison is confounded (see its correction) | - | - |
| `models-1` | cascade, scout, model routing, a cheaper default, a settings profile; one-shot Sonnet tasks | 7.1, 7.2, 7.5 prior | $50 |
| `components-1` | every optimizer and hook by itself, long Sonnet sessions | 10.4, 10.6, 7.4, 1.4 priors | $90 |

`04_report.sh` regenerates the recommender defaults and the benchmark priors from every study published so far, so
plan items 1.4 (priors) and 7.5 (the model_default evidence) fill in from the results with no other step.
