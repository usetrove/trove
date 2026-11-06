# Verifying bench/ end to end

Status when this was written (2026-10-29): all Python logic is covered by offline tests (`pytest bench/tests`, 98 pass).
The **container pipeline has never been run**, because Docker was not running on the dev machine. This checklist is the
remaining work, in order. Stop at the first failure and fix it before spending money.

Run everything from the repo root. Use a throwaway batch folder for experiments so you never mix test runs into real results.

## 0. Prerequisites

- [ ] Docker Desktop is running: `docker info` prints a server version (Linux containers mode).
- [ ] `go version` works (the harness cross-compiles Trove and `benchfake` for Linux).
- [ ] `pip install -e "bench[report,dev]"`
- [ ] `python -m pytest bench/tests -q` passes (98 tests at time of writing).
- [ ] `ANTHROPIC_API_KEY` is **not** needed until step 3. The fake runs never use your real key.

## 1. Fake smoke run (free, no API key)

```sh
python -m trovebench smoke --fake --batch bench/results/verify-fake
```

This builds the Linux `trove` and `benchfake` binaries, builds three task images (first build pulls `python:3.12-slim`
and runs `apt-get`, so allow a few minutes), then runs 3 tasks x 2 conditions (`direct`, `all_safe`) x 1 repeat = 6 runs.
The agent is a fake that talks to a mock upstream inside the container; the real Trove gateway and optimizers sit in between.

Expected:
- [ ] Exit code 0 and the line `Stop reason: done`.
- [ ] `bench/results/verify-fake/report.md` exists, starts with the banner "used the fake agent and a mock upstream".
- [ ] `runs.csv` has 6 rows, all `status` = `ok`, all `passed` = True (each task ships a `solution.sh` that the fake agent runs).
- [ ] The 3 `all_safe` rows have `cost_source` = `gateway` and non-zero token columns; the 3 `direct` rows have `cost_source` = `agent-usage`.
- [ ] `verify-fake/runs/<id>/trove-data/trove.db` exists for each `all_safe` run.
- [ ] `versions.json` has a `trove_binary_sha256`, `image_ids`, and `fake_agent: true`.
- [ ] Charts (`chart_*.png`) exist and are readable.
- [ ] `grep -r "sk-ant" bench/results/verify-fake` finds only the placeholder fake key (`benchfake-not-a-real-key`) or nothing.

Likely failure points, in order: `go build` for `./bench/tools/benchfake` or `./cmd/trove`; the image build (apt or network);
the gateway not starting in the container (check `runs/<id>/gateway.log` and the error text); `trove optimize status --json`
shape differing from what `_assert_configured` expects; a run marked `error` with a "downgraded" note (the baseline gate, which
should be off because the config sets `baseline_days = 0` and `baseline_sessions = 0`).

### 1b. Resume works (still free)

```sh
python -m trovebench smoke --fake --batch bench/results/verify-resume
```
Press Ctrl-C after two or three runs. Then run the **same command again**.
- [ ] It prints `Interrupted. Finished runs are saved` the first time.
- [ ] The second run skips the finished runs (`skipped N already done`) and completes the rest.
- [ ] `ledger.jsonl` ends with exactly 6 distinct run ids, none duplicated.
- [ ] No leftover containers: `docker ps -a --filter name=tb-` is empty.

### 1c. Condition really applied

Check that a gateway condition actually changed something, using one `all_safe` run folder:
```sh
python -c "import sqlite3,sys; c=sqlite3.connect('file:bench/results/verify-fake/runs/<run id>/trove-data/trove.db?mode=ro',uri=True); print(c.execute('select states from session_pins').fetchall())"
```
- [ ] `noise_normalizer` and `duplicate_result_collapser` show `on`.
- [ ] If the table is empty for an `all_safe` run, the optimizers never engaged; investigate before trusting any result.

## 2. Real dry runs (free)

```sh
python -m trovebench smoke --dry-run
python -m trovebench plan --tasks bench/tasks/smoke --repeats 2
```
- [ ] They print an estimate and a worst case, and do not start Docker or need an API key.
- [ ] Running `smoke` without `--yes` (and without `--dry-run`) refuses with "This will spend real API money".

## 3. Real smoke run (about $0.20 on the default model)

1. [ ] Set `claude_version` in `bench/config.yaml` to your pinned Claude Code version (`claude --version`). Real runs refuse without it.
2. [ ] `export ANTHROPIC_API_KEY=...` (your own metered key).

```sh
python -m trovebench smoke --yes --batch bench/results/verify-real
```

Expected:
- [ ] 6 runs finish; spend is well under $1 (check `Ran ..., spent $...`).
- [ ] `versions.json` has `agent_version_reported` matching your pin.
- [ ] `all_safe` runs have `turns` > 0, `cost_source` = `gateway`, and a cost within about 10% of the agent's own estimate
      (otherwise the `note` column says "differs").
- [ ] **Confirm the unverified Claude Code result fields.** Open `runs/<id>/agent_stdout.txt` and check it contains
      `num_turns`, `total_cost_usd`, `modelUsage`, `subtype`. If `num_turns` is missing the harness falls back to the gateway's
      request count; decide whether that is acceptable.
- [ ] Run `claude --help` in the image (`docker run --rm <image> claude --help`) and confirm `--bare`, `--max-budget-usd`,
      `--permission-mode`, `--allowedTools` and `--no-session-persistence` still exist at your pinned version.
- [ ] Nothing in `bench/results/verify-real` contains your API key: `grep -r "<first 12 chars of key>" bench/results/verify-real` is empty.

## 4. Acceptance run: 10 tasks x 2 conditions x 2 seeds (about $1.30 estimated on the default model)

Make a config with two conditions and 2 repeats:
```sh
cp bench/config.yaml bench/acceptance.yaml
```
Edit `bench/acceptance.yaml`: set `repeats: 2` and `conditions: [passthrough, all_safe]` (keep `claude_version` set).

```sh
python -m trovebench fetch polyglot -n 10 --out bench/tasks/acceptance
python -m trovebench plan --config bench/acceptance.yaml --tasks bench/tasks/acceptance --batch bench/results/acceptance
python -m trovebench run  --config bench/acceptance.yaml --tasks bench/tasks/acceptance --batch bench/results/acceptance --yes
```

Expected:
- [ ] The plan says 40 runs and an estimate under the batch cap ($25).
- [ ] The run completes with `Stop reason: done` and total spend under the cap.
- [ ] `report.md` and the CSVs exist under `bench/results/acceptance/`.
- [ ] The ship table says **NEED MORE DATA** for `all_safe` (20 pairs is below `min_pairs: 30`). That is correct, not a bug.
- [ ] Interrupt and resume: start the run again, Ctrl-C partway, re-run the same command; finished runs are not repeated and spend carries over.
- [ ] Cap enforcement: set `limits.batch_cap_usd` to about double one run's cost, run in a new `--batch`, and confirm it stops with
      `Stop reason: budget_cap` before exceeding it.

## 5. Reproducibility

```sh
cp bench/results/acceptance/summary.csv /tmp/summary.before.csv
python -m trovebench report --batch bench/results/acceptance
diff /tmp/summary.before.csv bench/results/acceptance/summary.csv
```
- [ ] `diff` prints nothing (byte-identical).
- [ ] `versions.json` has the Trove commit, `trove_dirty`, the binary SHA-256, model ID, pinned agent version, dataset commit
      and image IDs. If `trove_dirty` is true, the commit alone will not reproduce the build; commit first for a publishable run.

## 6. Optional: SWE-bench and mined tasks

```sh
python -m trovebench fetch swebench -n 10
python -m trovebench validate --tasks bench/tasks/swebench       # expect many REJECTED; that is the gate working
export GITHUB_TOKEN=...; cp bench/repos.example.yaml bench/repos.yaml
python -m trovebench mine --since 2026-09-01
python -m trovebench validate --tasks bench/tasks/mined
```
- [ ] `fetch swebench` records a dataset revision; `validate` writes `validation.json` only for tasks that fail on base and
      pass with the gold patch.
- [ ] `run` refuses unvalidated swebench/mined tasks.
- [ ] Mined manifests record repo, base commit, issue link, license and PR in each `task.yaml`.

## After it passes

- Update the "Not yet run end to end" bullet in `bench/README.md` (Known gaps) with the date and what you ran.
- Fix anything found, and add an offline test for each bug so it cannot come back.
- Remaining known gaps (not part of this checklist): Codex adapter, non-Python polyglot, Django/SymPy SWE-bench, Terminal-Bench.

---

# Rigorous studies (pre-registered, paired): status and test plan

Added in commit e36ae2e. Design and statistics are in `docs/benchmark-methodology.md`.
**This work is only partly done.** Everything below marked BUILT passes its offline tests but has never run
against a real agent; everything marked NOT BUILT does not exist yet.

## Status by item

| Item | State |
|---|---|
| Study files, pre-registration lock, paired randomized plan with effort axis | BUILT (offline tested) |
| Statistics (`inference.py`), per-study report, charts, CSVs | BUILT (offline tested) |
| Study (a) parity: `bench/studies/parity.yaml` | BUILT; never run for real; needs Docker, API key, tasks, and a hook check (see A below) |
| Docker-free `--simulate` mode | BUILT; measures nothing, for pipeline testing only |
| Defaults generator (`trovebench defaults`) and Go load test | BUILT; only ever fed simulated data, so no rule is `benchmark` yet |
| Methodology doc | BUILT |
| Study (b) cascading (cheap model, escalate on failing tests, vs strong alone) | NOT BUILT (`cascade.py`) |
| Study (c) scout subagent on/off condition | NOT BUILT |
| Study (d) settings-profile conditions | NOT BUILT |
| Study (e) third-party tool adapters (`trovebench/tools/`) | NOT BUILT. Analysis of claim vs measurement exists (`claims:` in a study file). The real tool is still to be chosen, and its claim and install steps verified from official docs |
| Study (f) open-weight models: Codex CLI adapter (`agent/codex.py`) through `/compat/<name>` | NOT BUILT. Verify Codex's current custom-endpoint and reasoning-effort config from official docs first |
| Task licence allowlist check in `validate`; `publishable` flag; `LICENSES.md` update | NOT BUILT |
| Transcript policy (gitignored `transcripts/`, `NO_TRAINING.md`, `data_use` in versions.json) | NOT BUILT |
| CLAUDE.md layout line mentions bench as "Python, later" | not updated |

## Acceptance criteria still open
- Parity study run end to end for real, reporting plugin savings as a % of the gateway's with CIs.
- At least one third-party tool measured against its own claim.
- Defaults file regenerated from real results that met the ship rule (`python -m trovebench defaults --results ...`).

## Tests to run at the end

Free, no Docker:
- [ ] `cd bench && python -m pytest tests -q` passes (173 at the time of writing).
- [ ] `go test ./internal/recommend ./internal/experiment` passes.
- [ ] Simulated parity run, then read the report. Needs a task folder and an effects file (see `tests/test_study_cli.py` for a working
      example). Check the report opens with the SIMULATED banner and `parity.csv` has a plugin-share row with a CI.
- [ ] `python -m trovebench defaults --results <that simulated folder>` is refused with a "simulated" message.
- [ ] `python -m trovebench defaults --dry-run` reports no result folders and leaves `internal/recommend/defaults.toml` unchanged.
- [ ] Editing a locked study file makes `plan`/`run`/`report` refuse with "changed since".

Needs Docker and an API key (real money; read the plan estimate first):
- [ ] **A. Confirm hooks really fire in the hook conditions.** Hook conditions (`hooks`, `hooks_gateway`) now run Claude Code
      *without* `--bare`, because `--bare` skips hooks (docs, verified 2026-11-06). Run `go run ./scripts/hookprobe --live`, then
      one `hooks` run, and check `trove-data/` in the run folder shows hook activity. There is no automatic assertion yet, so a
      silent no-op would produce a plugin-only result identical to no Trove.
- [ ] **B. Confirm `--effort` works** at the pinned Claude Code version: `docker run --rm <image> claude --help` lists `--effort`,
      and a `low` vs `high` run differs in reasoning tokens. The user-level `effortLevel` setting may be ignored for newer models;
      the harness uses the flag, not the setting.
- [ ] **C. Confirm the usage fields.** `agent_stdout.txt` contains `modelUsage` (camelCase per-model keys) and `total_cost_usd`.
      Unconfirmed whether the CLI uses `modelUsage` or `model_usage`; the parser reads `modelUsage`, so a mismatch would make
      `agent_cost_usd` empty and the whole study unusable. Check `agent_cost_usd` is filled in `runs.csv`.
- [ ] **D. Cost cross-check.** For gateway conditions, `note` flags runs where the agent's own estimate and the gateway differ by
      more than 10%. Review those before trusting the parity numbers.
- [ ] Generate tasks: `python -m trovebench fetch polyglot -n 20`, then `validate`.
- [ ] `prereg`, `plan --study bench/studies/parity.yaml` (free), read the estimate and worst case, then `run ... --yes`.
- [ ] Read the report: pairs per effort, "incomplete units" count, parity row estimable or not, Holm-adjusted p-values.
- [ ] Only after real results exist: `python -m trovebench defaults --results bench/results/<date>/parity-1`, then
      `go test ./internal/recommend ./internal/experiment`, then review the diff before committing.
