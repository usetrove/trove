# Check the history reader yourself

Written 2026-10-29. What this covers is explained in [logs.md](logs.md). These steps use a throwaway data directory so your real `~/.trove` is untouched.

## 1. First run

```sh
export TROVE_DATA_DIR=$(mktemp -d)
time trove
```

Expect, within 30 seconds: a notice that it is reading the last 30 days, then a report with a **From history** line, spend (or **API value**), waste found and the top leaks. Run it again: it should take well under a second.

## 2. See what was read

```sh
trove logs status
```

Check the file count, `skipped_lines` (should be small or zero), the plan and its evidence, and the list of checks that are not available.

## 3. Confirm nothing but metadata is stored

```sh
sqlite3 "$TROVE_DATA_DIR/trove.db" "select * from history_requests limit 3; select * from history_files limit 3;"
```

You should see numbers, model names and hashes. No prompts, no paths, no tool output, no session ids.

## 4. Confirm nothing in your agent's folder changed

Compare `ls -l --time=ctime ~/.claude/projects` before and after. Trove only opens files for reading.

## 5. Gateway and history together

With the gateway on, run an agent session, then `trove`. The report's **From history** line should say how many sessions "also went through the gateway and are counted there only". Total requests should equal gateway requests plus history-only requests.

## 6. Codex (unverified)

If you use Codex CLI, run `trove logs scan`, then compare one session against Codex's own `/status` token numbers. Report any mismatch, and the first line of a `rollout-*.jsonl` file with the contents of string fields blanked out, so the wrapper format can be confirmed.

## 7. Cost cross-check (Claude Code)

In a session file, the last `"type":"cost-state"` line has `totalCostUSD`. Trove's cost for that session should be close to it and not above it (see "Accuracy check" in logs.md).

## 8. Turn it off

`trove --no-history` reads nothing and creates nothing. `[history] enabled = false` in `config.toml` does the same permanently.
