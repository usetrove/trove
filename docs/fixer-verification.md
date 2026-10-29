# trove fix: what to test and verify on a real install

The automated tests use fixtures and a fake home. This is what they cannot tell you.
Always use a throwaway project, and set `TROVE_DATA_DIR` (the real `~/.trove` is a different product's database).
Tick items off as you go; write down anything surprising.

## 0. Setup

```
export TROVE_DATA_DIR=$(mktemp -d)        # PowerShell: $env:TROVE_DATA_DIR = (New-Item -Type Directory "$env:TEMP\trove-test").FullName
go build -o trove ./cmd/trove
cd <a git clone you don't mind editing>
```
Back up first: `cp -r <project> <project>.bak`. Afterwards `diff -r` against it.

## 1. Things I could not verify (highest value)

- [ ] **Project folder naming.** Run Claude Code once in the project, then `ls ~/.claude/projects/`.
      Does the folder name equal the project path with every non-letter/digit turned into `-`?
      (`internal/fixer/usage.go` `encodeProject`.) If not, `mcp_prune` silently finds no history. Fix the function.
- [ ] **MCP tool names.** In a session that calls an MCP tool, check the history `.jsonl` for `"name":"mcp__<server>__<tool>"`.
      Check how a server name with spaces or dots is written (`normalizeServer` assumes other characters become `_`).
- [ ] **`disabledMcpjsonServers` really disables.** After applying an `mcp_prune` fix, start Claude Code in the project and run `/mcp`.
      The server should be absent or disabled. Check it works from `.claude/settings.local.json` (not only `settings.json`).
      Remove the entry and confirm it comes back.
- [ ] **Does Claude Code reload settings mid-session?** Is it right to hold fixes while a session is open?
- [ ] **Tool search.** With tool search on, does an unused MCP server still add tool definitions to the prompt?
      If not, the `mcp_prune` token estimate is too high. Compare gateway `segment_token_estimates` with the server on and off.
- [ ] **Codex.** Find how MCP tool calls appear in `~/.codex/sessions/**/rollout-*.jsonl`, and whether `cwd` is in `session_meta`.
      Needed before a Codex `mcp_prune` (the TOML helper `DisableMCPServerTOML` is ready). Confirm `enabled = false` under
      `[mcp_servers.<name>]` disables the server.
- [ ] **Model setting.** After a `model_default` fix, `/status` in Claude Code should show the new model. Confirm a project
      `model` or `ANTHROPIC_MODEL` really overrides it (the fix refuses in both cases).

## 2. Each fix, by hand

### cache_breaker_fix
- [ ] Add `Today's date: 2026-10-29` and a prose line `We chose X on 2026-03-01` to `CLAUDE.md`. `trove fix` lists one fix.
- [ ] `trove fix <id>` diff removes only the first. A date inside a code fence is left alone.
- [ ] Apply with `--yes`; the file has no double blank line; `trove undo <id>` restores it (`cmp` against the backup copy).
- [ ] CRLF file and a file with no trailing newline both round-trip.

### context_slim
- [ ] On your real `CLAUDE.md` (over ~1500 estimated tokens): read every proposed section. Are any actually project-specific?
      Any wrongly removed section is a bug in the heuristics (`contextslim.go`: duplicate-of-README, generic heading, repeated).
- [ ] The title and every section holding rules (never/must/always...) or commands stay.
- [ ] Token estimate (bytes / 4) vs. a real count of the file (e.g. via the gateway's `context_file_weight` finding).

### mcp_prune
- [ ] Project with `.mcp.json` of 2+ servers and at least 5 sessions where one is never called: only that one is proposed.
- [ ] Fewer than 5 sessions: nothing is proposed. A server you called once: not proposed.
- [ ] `.claude/settings.local.json` exists already with other keys/comments-free JSON: only `disabledMcpjsonServers` changes.
- [ ] Without gateway data the fix shows "-" for tokens and dollars and a note why. After a few sessions through
      `trove gateway`, it shows a number; sanity-check it against the server's real tool count.
- [ ] `.mcp.json` itself is byte-identical afterwards.

### model_default
- [ ] Needs 20+ small-task requests on a frontier model (gateway or `trove logs scan` history). Check the stated percentage.
- [ ] Shown as "suggestion"; `trove fix --all --apply --yes` does NOT apply it; `trove fix <id> --apply` does.
- [ ] With `ANTHROPIC_MODEL` set it becomes a manual step.

## 3. Safety behavior

- [ ] **Active session.** Start Claude Code in the project (or send one request through the gateway), then
      `trove fix <id> --apply --yes`: the result is `queued`, no file changed, `~/.trove/changes.json` absent.
      Wait out `fixer.activity_minutes` (or set it to 1), then `trove fix --apply-queued` applies it.
- [ ] Edit the file after queueing: `--apply-queued` drops the fix and leaves your edit alone.
- [ ] **Refusals.** Point a fix at managed paths by temporarily setting `CLAUDE_CONFIG_DIR` to a folder named
      `claude-code` under `/etc` (Linux/macOS) and creating `managed-settings.json`; confirm nothing in it is ever edited.
      Same for `~/.claude.json`, `.env`, `auth.json`.
- [ ] **Symlinked CLAUDE.md** pointing outside the project is refused.
- [ ] **Undo after your own edit.** Apply a fix, edit the file, `trove undo <id>`: reported `left_alone`, file untouched,
      the backup path is printed and still exists. Then `trove undo --all` does the same without deleting the record.
- [ ] **Two fixes, one file** (cache_breaker_fix + context_slim on the same `CLAUDE.md`): `--all --apply` applies both;
      `trove undo --all` restores the original exactly; undoing only the first reports `left_alone`.
- [ ] `trove undo` with no id still reverses `trove setup` changes too. Check you are happy that it also reverses fixes.
- [ ] `--json` with `--apply` and no `--yes` changes nothing.

## 4. Measurement and report

- [ ] After applying a fix, `sqlite3 $TROVE_DATA_DIR/trove.db 'select * from fixes'` shows id, kind, a path hash,
      timestamps and estimates, and no path or file text.
- [ ] `trove report` shows "Config fixes you applied" with "Measured: not enough data yet" until `measure.min_sessions`
      (default 20) sessions exist on both sides, and labels the estimate "Estimated".
- [ ] After enough sessions: the measured line appears. Check it by hand: mean prompt tokens per request for sessions started
      before vs. after `applied_at`. A session already running when you applied must be in neither group.
- [ ] `trove undo <id>` removes the fix from the report.
- [ ] Not implemented, by design: automatic rollback of a fix when guardrails trip. Decide if that is acceptable.

## 5. Known rough edges to look at

- Several fixes in place mix their effects; the report says so but does not separate them.
- Token estimates for files are bytes / 4.
- `trove undo` with no id reverses everything including fixes; there is no "setup only" mode.
- `fix` only checks the current project (`--project`), not every project in your history.
