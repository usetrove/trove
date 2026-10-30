# Agent hooks

Hooks are small commands that Claude Code and Codex CLI run around tool calls and when a session ends.
Trove's hooks work **without the gateway**, so they also help if you use a subscription. They never
read or change credentials, and they never touch your agent's traffic.

```
trove hooks install      # shows the change, asks, backs the file up
trove hooks status       # where they are installed and what they did
trove hooks uninstall    # removes only Trove's entries (same as `trove undo`)
trove hooks sync         # refresh what the hooks read after editing config.toml
```

## What they do

| Hook (`trove hook <event>`) | Agent event | What it does |
|---|---|---|
| `pre_tool_repeat_read` | Claude Code `PreToolUse` on `Read` | If the session already read this file range and the file is unchanged (same modification time, size and content hash), denies the call once with a short message naming the earlier prompt, so the agent reuses what it has. |
| `post_tool_read_record` | Claude Code `PostToolUse` on `Read` | Records the read, but only after it succeeded. A read that was denied or failed is never taken for one that happened. |
| `session_reset` | `SessionStart`, Claude Code `PreCompact` | Pins the optimizer states for the session. Forgets earlier reads after a compaction or resume, because the agent no longer has those files in context. |
| `session_end_receipt` | `SessionEnd` | Moves the hook counters into the database, runs the hook guardrail, writes the session receipt, refreshes the snapshot the other hooks read. |
| `pre_tool_large_output` (optional, `--large-output`) | `PreToolUse` on `Bash` | For a known-noisy command (`go test ./...`, `npm test`, bare `pytest`, `make`, verbose builds) adds a hint to use `trove_run`. Never blocks. Only fires when the MCP server is registered. |

Codex CLI gets `session_reset`, `session_end_receipt` and, if you ask for it, `pre_tool_large_output`.
It does **not** get repeat-read prevention: Codex's hook page names no file-read tool to match.

### Guarantees

- **A changed file is never blocked.** Modification time, size and a content hash must all match. Files over
  2 MB, empty files, missing files, paged (PDF) reads and relative paths are always allowed.
- **No loops.** Once a read is denied, the agent's retry of that same version goes through. That retry is
  counted as `reread_anyway`, the signal that the block did not help.
- **Fail open.** Any error, panic, bad input or timeout ends in exit 0 with no output, which both agents read
  as "go ahead". Exit code 2 would block, so Trove never uses it. Errors go to `hooks.log` in the data
  directory (redacted, file names removed, capped at 256 KB).
- **Metadata only.** The per-session ledger holds hashes, sizes, times and counters. Never file contents,
  paths, commands or the raw session id. Files are 0600.
- **Opt-in, shadow first, switched only at session boundaries.** Installing does not turn anything on. Add
  under `[optimizers]` in `config.toml`:

  ```toml
  [optimizers]
  hook_repeat_read = "shadow"   # counts what it would block, changes nothing
  # hook_repeat_read = "on"
  # hook_large_output = "shadow"
  ```

  A session keeps the state it started with. Run `trove hooks sync` (session end does it too) to refresh the
  snapshot the hooks read. The kill switch (`trove optimize off`, `TROVE_OPTIMIZE=off`) and a guardrail
  rollback lower the state immediately. A session that started before the hooks were installed stays off.

## Measurement

Each decision is one row in `hook_events`: hook, kind, bytes, estimated tokens, hashed session key.

| Kind | Meaning | Label |
|---|---|---|
| `blocked` | the read was denied | measured count |
| `would_block` | shadow mode: it would have been denied | measured count |
| `advised` / `would_advise` | a hint was added / would have been | measured count |
| `reread_anyway` | the agent retried a denied read | measured count |

Tokens saved is **estimated** (file size ÷ 3.5 bytes per token, lower for line-limited reads) and counts only
blocks the agent did not retry. Hints have no savings estimate, because the output they avoid is not known.

**Guardrail** (`measure.EvaluateHooks`): once at least `measure.min_sessions` (20) reads were blocked, if the
share the agent read again anyway reaches `measure.refetch_rate_percent` (25%), `hook_repeat_read` goes back to
`shadow` for new sessions and the event appears in `trove report`. There is no command yet to lift a rollback:
`measure.ClearOverride` exists but is not wired to the CLI, for the gateway optimizers either. Once it is lifted,
counts from before that moment are not held against the hook.

Unlike the gateway optimizers, hooks have no 7-day baseline gate: they can run without the gateway, so there is
no baseline of outcome signals to compare with. Shadow mode is the evidence step; the guardrail is the safety.

## How it stays fast

`trove hook` is dispatched in `main` before the command tree is built. The per-call hooks read two small files
(the snapshot and the session's journal), do one `stat` and one hash, and append one line each to the
journal and the spool. They never open SQLite. The journal is append-only, so concurrent hooks never rewrite or
rename a file. `session_end_receipt` is the only hook that opens the database; it has a 3 s timeout configured.
A hard 150 ms deadline lets a call through if anything stalls.

Measured on a Windows 11 dev machine with real-time antivirus: about 14 ms median over an empty Go program for the
whole process, and 21 ms median for the handler alone, almost all of it a fixed ~17 ms the system adds when a file
is opened right after it was written. See `docs/hooks-verification.md` for the numbers to check on macOS and Linux.

## Where the hooks go

| Agent | File | Notes |
|---|---|---|
| Claude Code, user | `~/.claude/settings.json` (or `$CLAUDE_CONFIG_DIR`) | `hooks.<Event>` array of matcher groups. |
| Claude Code, project | `<project>/.claude/settings.local.json` | Not committed, so a machine-specific path stays local. |
| Codex CLI, user | `~/.codex/hooks.json` | If `config.toml` already has inline `[hooks]`, Trove skips Codex rather than create both (Codex warns when a folder has both). Codex asks you to review new hooks with `/hooks`. |

Your own hooks are never changed. Trove appends its own matcher groups and recognises them on undo by the command
prefix it recorded. If you edit the file after installing, undo still removes only Trove's handlers (and any empty
event it leaves behind); if you did not, it restores your file byte for byte.

## Contract facts relied on

Verified 2026-10-29 / 2026-10-30. Re-check when either agent changes.

- Claude Code (`code.claude.com/docs/en/hooks`): `PreToolUse` input has `session_id`, `prompt_id`, `tool_name`,
  `tool_input` (Read: absolute `file_path`, optional `offset`, `limit`). Deny with exit 2, or
  `hookSpecificOutput.permissionDecision: "deny"` plus `permissionDecisionReason`. A timed-out PreToolUse proceeds.
  `SessionEnd` has no decision control, shares a 1.5 s budget raisable per hook up to 60 s. Groups from every
  settings file merge and run in parallel. A `Read` matcher does not fire for `@file` mentions.
- Codex CLI (`learn.chatgpt.com/docs/hooks`, reached from `developers.openai.com/codex/hooks` by redirect): hooks
  on by default; `hooks.json` or inline `[hooks]` in `~/.codex` or a trusted `.codex`; new hooks need `/hooks`
  review; `PreToolUse` deny supported; **`ask`, `continue`, `stopReason`, `suppressOutput` are rejected** (Trove
  emits none of them); `SessionEnd` default timeout 1 s, maximum 3 s; `Bash` and `exec_command` pass
  `tool_input.command`.

## Known gaps

- Repeat-read prevention is Claude Code only.
- Without the gateway there is no spend data for a session, so `session_end_receipt` writes a receipt only for
  sessions the gateway saw. For others it still ingests the hook counters. The status-line numbers come from the
  gateway's daily table; hooks do not invent spend.
- Codex session keys are derived from the Codex session id, which the gateway does not use, so Codex hook counts do
  not join gateway rows yet.
- Fixtures under `internal/hooks/testdata` are written from the documented examples, not captured from live agents.
- Windows: commands are written with the binary's absolute path; how each agent's shell quotes a path with spaces on
  Windows is unverified.
