# Launch study runbook

One pre-registered study, `bench/studies/launch-1.yaml`: long Claude Code sessions on Sonnet, comparing
no optimization, the Trove plugin, and the plugin with the gateway and model routing. Every number it produces is
labeled: pass/fail and token counts are measured; cost is estimated (list prices applied to the provider-reported usage
the gateway records for each request).

## What you need

- An Ubuntu 24.04 x86_64 VM with 4+ vCPUs, 16 GB RAM and 40 GB disk.
- A clone of this repository on the VM, at the commit that holds the lock file `bench/studies/launch-1.lock.json`.
- Your own pay-per-use Anthropic API key. Never a subscription login.

## Steps

```sh
bash bench/scripts/launch/00_vm_setup.sh      # once; then log out and back in
export ANTHROPIC_API_KEY=...                  # your key; it is passed to containers by name, never written to disk
bash bench/scripts/launch/01_check.sh         # free: tests and fake end-to-end runs
bash bench/scripts/launch/02_pilot.sh         # about $1-3: proves the conditions work, measures cost per run
tmux new -s study                             # so the run survives a closed SSH session
bash bench/scripts/launch/03_run.sh           # the study, overnight; detach with Ctrl-b d, reattach: tmux attach -t study
bash bench/scripts/launch/04_report.sh        # free: report, publish folder, priors, commit
git push
```

Each script stops with a plain message if something is wrong. Do not continue past a STOP.

## Money

- `BUDGET_USD` (default 70) limits the study; the study file's `budget.batch_cap_usd` (70) is the hard ceiling.
- The pilot measures cost per run and decides how many looks (repeats, at most 3) fit the budget.
- The study stops early once both comparisons are decided. Each look is tested at alpha / 3, so stopping early
  does not make a result easier to reach.
- `03_run.sh` can be rerun after an interruption; finished runs are kept and not paid for twice.

## What the result means

- Each comparison gets SHIP, DON'T SHIP or NEED MORE DATA under the registered rule: the whole 95%-level interval of
  the cost change at or below -5% (at the stricter per-look level), task success not more than 5 points lower, at
  least 30 pairs.
- The claim covers long, multi-prompt sessions on Sonnet with this Claude Code version. It says nothing about
  one-shot tasks, other models or other agents.
- Publish whatever comes back, including NEED MORE DATA.
