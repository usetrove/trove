# History reader: what to verify and test later

Written 2026-10-29. Companion to [logs.md](logs.md) (what it does) and [logs-verification.md](logs-verification.md) (quick walkthrough). This is the full list of things that were **not** proven by the automated tests, in priority order. Use a throwaway data directory for every run: `export TROVE_DATA_DIR=$(mktemp -d)`. Your real `~/.trove` is another product's database; never point tests at it.

## A. Needs a machine or account I did not have (highest value)

| # | Check | How | Pass looks like |
|---|---|---|---|
| A1 | **Codex wrapper format.** The `{timestamp, type, payload}` line shape and the `function_call` / `function_call_output` item names are from community write-ups, not OpenAI. | Install Codex CLI, run one short session that uses a tool, then `head -c 600 ~/.codex/sessions/*/*/*/rollout-*.jsonl` (look at structure only). Compare with `internal/logs/cxhistory/testdata/rollout_basic.jsonl`. | Same top-level keys and type names. If not, fix `internal/logs/cxhistory/codex.go` and the fixture. |
| A2 | **Codex token numbers.** | `trove logs scan`, then compare a session's request count and token totals against Codex's own `/status`. | Input = input minus cached; totals within a few percent. |
| A3 | **Codex plan.** ChatGPT-login users cannot be told from local signals. | Run with and without `OPENAI_API_KEY` set; check `trove logs status`. | `api_key` with the variable, `unknown` without. `[history] plan = "subscription"` flips it. |
| A4 | **Subscription Claude Code user.** | On a machine logged in with a Claude subscription and no API key env var, run `trove` and `trove logs status`. | Plan `unknown` (or `subscription` with `forceLoginMethod`), report shows **API value**, not **Spend**. |
| A5 | **Older Claude Code versions.** Only 2.1.302 was inspected. | Find a transcript from an older version (or install one), run `trove logs scan --all`, check `skipped_lines` in `trove logs status`. | Few or no skipped lines; no crash. If lines are skipped, save a redacted copy as a new fixture. |
| A6 | **Windows and macOS/Linux paths.** Built and run on Windows only. | Run the full walkthrough on macOS or Linux. | Same results; CI's `go test -race ./...` passes there. |

## B. Numbers I could not fully explain

| # | Check | How |
|---|---|---|
| B1 | **Cost gap vs Claude Code.** Trove's per-session cost was 1 to 8 percent below Claude Code's `cost-state.totalCostUSD` in 7 of 8 sessions and 23 percent below in one. Cause not confirmed (guess: calls not written to the transcript, or subagent files). | Pick the 23 percent session. Sum its main file plus its `subagents/` files and compare to `totalCostUSD`. If a gap remains, list which models/requests are missing. |
| B2 | **Fast/priority tiers.** `speed` other than `standard` is passed as a service tier and comes back unpriced. | Run a session with a fast mode if you have one; check `unpriced` counts in `trove --json`. |
| B3 | **Token-size ratio.** Oversized/duplicate findings convert result bytes to tokens at 3.5 bytes per token with no calibration. | For a few large tool results, compare the estimate with the real jump in `input + cache` tokens on the next request. |

## C. Behavior to try by hand

1. **First run under 30 s on a heavy history.** `time trove` with a cold temp dir. (Measured 5.2 s on 460 MB.) For a stress test: `TROVE_PERF_MB=450 go test ./internal/logs -run Large -v`.
2. **Second run is incremental.** `trove logs scan --json` twice; the second must show `files_read: 0`.
3. **Live session.** Start `trove logs scan` while Claude Code is mid-session with a tool running, then again after it finishes. The request should appear once, with its tool result size, not twice.
4. **Agent folder untouched.** Compare modification times under `~/.claude/projects` before and after a scan.
5. **No content stored.** `sqlite3 "$TROVE_DATA_DIR/trove.db" "select * from history_requests limit 5; select * from history_files limit 5;"` shows numbers, model names, hashes only. Search the whole file: `strings "$TROVE_DATA_DIR/trove.db" | grep -i "<a word from one of your prompts>"` finds nothing.
6. **Secrets.** Run with `ANTHROPIC_API_KEY=sk-test-123 trove --verbose 2>&1 | grep sk-test` finds nothing. `trove logs status` shows `env:ANTHROPIC_API_KEY`, never the value.
7. **Gateway plus history, no double count.** With the gateway on and `trove setup` done, run a Claude Code session, then `trove`. The **From history** line should name excluded sessions; total requests equal gateway requests plus history-only requests. Repeat with Codex: Codex history after the first gateway Codex request should be excluded.
8. **Opt-outs.** `trove --no-history` creates no database on a fresh dir. `[history] enabled = false` in `config.toml` does the same. `plan = "bogus"` gives a plain-English config error.
9. **Corrupt file.** Append a line of garbage to a copy of a transcript under a test `CLAUDE_CONFIG_DIR`; `trove logs status` shows it as one skipped line and everything else still reads.
10. **Retention.** Set `CLAUDE_CONFIG_DIR` to a folder with only an old file (mtime over 30 days): `trove logs scan` ignores it; `--all` reads it.

## D. Design decisions worth a second look

- Gateway wins **per session**: a session that started before the gateway was turned on loses its earlier history rows. Acceptable? The alternative is a per-time-span rule.
- Codex overlap is **time-based** (everything from the first gateway Codex request on is dropped), which undercounts if the gateway is stopped later.
- Plan detection reads the **current** environment of the shell running `trove`, then restamps all stored history rows with that answer on every scan.
- Subscription users see **API value** only, with no percent-of-plan-limit. Decide whether a user-supplied plan limit should be added.
- Duplicate-result detection only looks **within one scan of a file**; a repeat across two scans is missed.
- `long_session_drift` is not implemented for history.

## E. Automated checks to rerun

```sh
go test ./...                     # everything
go test -race ./...               # CI does this on Linux/macOS; not run on Windows here
go test ./internal/logs/... -fuzz FuzzHandleLine -fuzztime 60s -run xxx   # run from internal/logs/cchistory
golangci-lint run ./internal/logs/...
```

Goldens are refreshed with `-update` (`internal/logs/cchistory`, `internal/logs/cxhistory`, `internal/report`) and `UPDATE_GOLDEN=1` (`internal/config`). Review the diff before keeping any refresh.
