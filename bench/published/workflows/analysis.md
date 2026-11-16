# What limits the saving on scripted Claude Code workflows

Model claude-sonnet-5-5, list prices as of 2026-11-08, seed 20261108. All figures estimated, from scripted sessions.

## 1. Ceiling: what an oracle that deleted every tool output would save

| workflow | tier | saving now (est.) | ceiling (est.) | tool output share of the last request |
|---|---|---|---|---|
| quick-question | everyday | 0.0% | 6.5% | 10.6% |
| fix-failing-test | everyday | 0.0% | 25.1% | 32.5% |
| debug-with-logs | everyday | 3.7% | 27.8% | 49.7% |
| dependency-upgrade | everyday | 5.1% | 11.7% | 17.6% |
| rename-across-codebase | everyday | 0.0% | 36.0% | 45.7% |
| explore-and-review | everyday | 7.0% | 36.6% | 50.3% |
| long-feature-session | everyday | 8.6% | 56.9% | 61.3% |
| ci-failure-triage | heavy | 50.7% | 64.3% | 71.0% |
| container-build-debug | heavy | 19.3% | 36.7% | 54.6% |
| incident-log-forensics | heavy | 2.5% | 65.7% | 90.3% |

## 2. What-ifs: output size and trimmer thresholds

| what-if | everyday median (range) | heavy median (range) | failure lines kept | violations |
|---|---|---|---|---|
| shipped settings, sizes as scripted | 3.7% (0.0% to 8.6%) | 19.3% (2.5% to 50.7%) | 698/698 | 0 |
| outputs half as large | 2.2% (0.0% to 6.6%) | 20.4% (2.5% to 53.1%) | 505/505 | 0 |
| outputs twice as large | 4.9% (0.0% to 10.7%) | 18.6% (2.4% to 46.4%) | 1076/1076 | 0 |
| outputs four times as large | 4.6% (0.0% to 12.2%) | 17.5% (2.3% to 39.7%) | 1880/1880 | 0 |
| trimmer at its smallest allowed thresholds, sizes as scripted | 3.7% (0.0% to 8.6%) | 19.3% (2.5% to 50.7%) | 698/698 | 0 |
| trimmer at its smallest allowed thresholds, outputs twice as large | 4.9% (0.0% to 10.7%) | 18.6% (2.4% to 46.4%) | 1076/1076 | 0 |
| MODELLED, no such optimizer exists: a re-read of a changed file sent as only its changed lines | 3.7% (0.0% to 9.1%) | 19.4% (2.5% to 50.7%) | 698/698 | 0 |
| MODELLED, same, outputs twice as large | 4.9% (0.0% to 11.4%) | 18.9% (2.4% to 46.4%) | 1076/1076 | 0 |

## Read this before quoting anything

- Everything here is estimated from scripted sessions at list price. The size and tuning rows are what-ifs: they say what would happen if real outputs were that large or the trimmer ran that tight, not that they are.
- The ceiling is generous: it removes every tool output, including the ones a model needs to see. No safe optimizer reaches it.
- Smaller trimmer thresholds trim more outputs, and each trimmed output costs the model a fetch if it needs the rest. The key-lines column shows no failure line was lost; it does not measure whether the model loses time fetching.
- A threshold or default change is not a result until it has been measured on real tasks (success, not just cost). Nothing here changes a shipped default.
