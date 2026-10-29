# bench: does Trove save money without hurting results?

`trovebench` runs a coding agent on real tasks under different Trove conditions, then compares **cost** and
**success** with paired statistics and a ship rule you write in config. It spends **your own** API key and
never anything else.

Status: Claude Code only. Codex is a documented stub (see "Known gaps").

## How a run works

```
host                                   fresh Docker container (one per run)
────                                   ─────────────────────────────────────
plan: tasks x conditions x repeats     repo at the pinned commit, deps installed (image cached per task)
  (seeded random order)                trove gateway start --listen 127.0.0.1:0   (gateway conditions only)
dry-run estimate, spend cap            claude --bare -p ... --output-format json  (ANTHROPIC_BASE_URL -> gateway)
ledger (fsync per run) -> resume       grade: restore tests, run success command
                                       stop gateway, copy its SQLite DB out, destroy the container
analyze: pairs, bootstrap CIs, ship rule
report.md + CSVs + charts
```

The gateway runs **inside** each container because Trove's gateway only binds to loopback. Each run gets its own
data dir, config and database, so runs cannot contaminate each other.

## Conditions

| Condition | What it is |
|---|---|
| `direct` | No gateway. Cost is *estimated* from the agent's own usage report. |
| `passthrough` | Gateway on, every optimizer off. The reference for the ship rule. |
| `alone:<optimizer>` | One optimizer `on`. |
| `all_safe` | Every optimizer that can be `on` for the agent. |

With Claude Code only `noise_normalizer` and `duplicate_result_collapser` can be `on`:
`volatile_prefix_mover` and `cache_breakpoints` act only for custom agents (Claude Code is never one), and
`output_trimmer` is gated to shadow mode. The report lists those as **NOT MEASURABLE WITH THIS AGENT**; they are never run
as no-ops. Every gateway run also checks, before and after, that Trove really applied the condition (it can silently
downgrade `on` to `shadow`), and marks the run `error` if not.

## Setup

Requirements: Python 3.10+, Docker (Linux containers), Go (to cross-compile the pinned Trove), an Anthropic API key.

```sh
pip install -e "bench[report,dev]"
export ANTHROPIC_API_KEY=...        # your own key; passed to containers by name, never written to disk
```

Edit `bench/config.yaml`:

- **`claude_version`: set it.** Real runs refuse to start unpinned. Use the output of `claude --version`.
- `model`: an exact model ID with a price in `internal/pricing/pricing.json` (default `claude-haiku-5-5`).
- `limits`: `batch_cap_usd` (hard stop), `run_cap_usd` (passed to `--max-budget-usd`), `run_timeout_s`.
- `ship_rule`: reference condition, minimum cost reduction `X`, success margin, minimum pairs, confidence.

## Costs (estimated)

Per-run cost is a guess from an assumed token profile (about 1.29M input tokens, mostly cache reads, and 20k output
tokens per run) at list prices from `pricing.json` (data version 2026-10-28.3). Real runs vary a lot with the task and
the model, so run `plan` first and then a small batch to calibrate: after any runs finish, the estimate switches to
the **observed mean cost**.

| Model | per run (est.) | smoke, 6 runs | 10 tasks x 2 cond. x 2 seeds = 40 runs | 100 tasks x 5 cond. x 3 = 1,500 runs |
|---|---:|---:|---:|---:|
| `claude-haiku-5-5` | $0.03 | $0.20 | $1.30 | $49 |
| `claude-sonnet-5-5` | $0.53 | $3.18 | $21.20 | $795 |

Estimates, not quotes. The spending cap is checked before every run (`spent + run_cap <= batch_cap`), and failed runs
count toward spend. The agent checks its per-run budget between API calls, so one run can overshoot its cap by about
one request; keep headroom.

## Commands

```sh
python -m trovebench smoke --fake                    # whole pipeline, mock upstream, no API key, no spend
python -m trovebench smoke --dry-run                 # estimate only
python -m trovebench smoke --yes                     # real: 3 tasks x (direct, all_safe), 1 repeat

python -m trovebench fetch polyglot -n 10            # tasks from Aider's polyglot (Python exercises)
python -m trovebench fetch swebench -n 10            # SWE-bench Verified (pytest-based repos)
python -m trovebench mine --since 2026-09-01         # fresh tasks from merged PRs (needs GITHUB_TOKEN, bench/repos.yaml)
python -m trovebench validate --tasks bench/tasks/swebench   # required for swebench/mined before use

python -m trovebench plan --tasks bench/tasks/polyglot --repeats 2        # estimate, spends nothing
python -m trovebench run  --tasks bench/tasks/polyglot --repeats 2 --yes  # run; interrupt and re-run to resume
python -m trovebench report --batch bench/results/<date>                  # rebuild the report
```

Results go to `bench/results/<date>/`: `report.md`, charts, `runs.csv`, `summary.csv`, `pairs.csv`,
`comparisons.csv`, `versions.json`, `config.used.yaml`, plus `runs/<id>/` with the redacted agent output, grading
output and the run's gateway database. To resume after an interruption, run the same command again with the same
`--batch`. Finished runs are never repeated; runs that failed before grading are retried.

## Adding a task

A task is a folder with `task.yaml` (and, for local tasks, a `repo/` folder):

```yaml
id: my-task                       # lowercase letters, digits . _ -
source: smoke                     # smoke | swebench | polyglot | mined
repo: "local:repo"                # or a git URL; local repos use base_commit: local
base_commit: local                # exact commit for git URLs
license: MIT
prompt: |                         # what the agent is asked
  The tests fail. Fix the bug. Do not modify the tests.
image: python:3.12-slim           # Debian/Ubuntu based
setup: []                         # runs once at image build (install dependencies)
grading_setup:                    # runs after the agent, before the success command
  - git checkout HEAD -- test_calc.py      # restore tests so the agent cannot pass by editing them
success_cmd: python -m unittest -q         # exit code 0 = solved
timeout_s: 600
```

Put a `gold.patch` next to it and `validate` will prove the task fails on the base and passes with the fix.
`swebench` and `mined` tasks are refused until they pass validation.

## Reading the results

- **Verdicts** (per optimizer, against the reference): `SHIP` (cost fell by at least X% and success did not drop past
  the margin, both at the chosen confidence, with enough pairs), `DON'T SHIP` (the best case misses the saving, or
  success clearly dropped), `NEED MORE DATA` (too few pairs, or the intervals still straddle a threshold).
- **Pairs.** A run is paired with the run of the same task and repeat index under the reference. Runs that failed
  before grading or have an unknown cost are excluded and listed under "Data quality".
- **Measured vs estimated.** Tokens and pass/fail are measured. Gateway costs are list price applied to measured
  tokens. `direct` costs are estimates from the agent's own report (no long-context rates), so `direct` is never a
  ship-rule reference. Agent-reported cost is a client-side estimate and is only used to flag disagreements over 10%.
- **Retries** are repeated identical tool calls, as counted by Trove's gateway.
- With the defaults a verdict needs 30 pairs per optimizer. A 10-task x 2-seed run gives 20, so it will say
  `NEED MORE DATA` by design.

## Reproducing a run

`versions.json` records the pinned Claude Code version, the exact model ID, the Trove commit **and** the SHA-256 of the
binary that ran (plus a dirty-tree flag), dataset revisions, task image IDs, the pricing data version and the config
hash. `config.used.yaml` and `plan.json` hold the rest, and the order seed fixes the run order. `report` recomputes every
table from `ledger.jsonl` with a fixed bootstrap seed, so `summary.csv` is byte-identical when regenerated. Model
outputs are not deterministic, so a fresh batch will differ; a regenerated *analysis* will not.

## Data sources and what we may publish

See [LICENSES.md](LICENSES.md). In short: publish aggregates, task IDs and links; keep task text and upstream code
local. Aider's polyglot exercises are copyright Exercism. The SWE-bench Verified dataset card declares no license.

## Safety

- API keys are passed with `docker -e NAME` (read from the environment), never on a command line, in a file, a
  manifest, a log or a result. All captured output is redacted (`trovebench/redact.py`).
- API-key authentication only. Subscription/OAuth credentials are never used; Claude Code runs with `--bare`, which
  skips the local keychain, hooks, plugins and `CLAUDE.md`.
- Containers have 4 GB / 2 CPU limits and are always removed, including after errors.
- Three failed-before-grading runs in a row stop the batch.

## Known gaps

- **Codex** is not implemented. It needs `codex exec --json`, `CODEX_API_KEY` and `openai_base_url` in
  `~/.codex/config.toml`; `--model` and `-C` were not documented where we looked.
- **Polyglot** is Python only. Other languages need their own image and test command.
- **SWE-bench**: pytest-based repos only (Django and SymPy use their own runners). Environments are built from the repo at
  its base commit rather than the official per-instance images, whose naming we did not verify; expect many instances to be
  rejected by `validate`.
- **Terminal-Bench** is not supported and its result-publishing terms are unconfirmed.
- Custom-agent optimizers and `output_trimmer` cannot be measured with Claude Code.
- The Claude Code result-JSON fields `num_turns` and `is_error` were not confirmed in the docs; the parser tolerates
  their absence and falls back to counting gateway requests. Confirm against a real run.
- **Not yet run end to end.** Everything outside Docker is covered by offline tests (`pytest bench/tests`). The container
  pipeline (image build, gateway in container, fake-agent smoke run, real smoke run) is written but has not been executed.
  Follow the step-by-step checklist in [VERIFY.md](VERIFY.md), starting with `python -m trovebench smoke --fake` (free).
