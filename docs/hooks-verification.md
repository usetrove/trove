# Hooks: what to verify on a real install

Automated tests cover the contract with documented example inputs, fail-open behavior, merge and undo, and the
guardrail. They cannot show how a real agent behaves. Tick these off on a machine with Claude Code (and Codex CLI if
you use it). Use a scratch data directory first: `export TROVE_DATA_DIR=$(mktemp -d)/.trove`.

## Install and undo

- [ ] `trove hooks install --dry-run` lists Claude Code (and Codex) with the exact JSON it would add, and writes nothing.
- [ ] `trove hooks install` asks before applying. Answer `n`: nothing changes.
- [ ] After `--yes`, `~/.claude/settings.json` still has every hook and setting you had. A backup is in `<data dir>/backups`.
- [ ] Start Claude Code and run `/hooks`: Trove's entries show under PreToolUse (Read), PostToolUse (Read), SessionStart, PreCompact, SessionEnd.
- [ ] Codex: open `/hooks`, review and trust Trove's entries. Confirm Codex does not warn about two hook formats.
- [ ] `trove hooks uninstall` restores the files. Add a hook of your own first, run uninstall again, and confirm yours survives.

## Repeat-read prevention (Claude Code)

Set `hook_repeat_read = "shadow"`, run `trove hooks sync`, start a **new** session.

- [ ] Ask Claude to read a file twice in one session. `trove hooks status` shows `1 would block (shadow)`. Nothing was blocked.
- [ ] Switch to `"on"`, `trove hooks sync`, new session. The second read of an unchanged file is denied and Claude's reply shows Trove's message and carries on without it. Check the message names a sensible earlier prompt.
- [ ] Edit the file (or `touch` it) between the two reads: the second read is **not** blocked.
- [ ] Ask Claude to read the file again after a denial ("read it again, I need the full text"): the retry goes through and `status` counts one `re-read anyway`. No loop.
- [ ] Run `/compact`, then ask for the file again: it is not blocked.
- [ ] Mention a file with `@file` and have Claude read it: confirm what happens (the Read matcher does not fire for `@` mentions, so no block is expected).
- [ ] Read a file with a line `limit`/`offset`, then the whole file: the second is not blocked (different range).
- [ ] With the kill switch (`trove optimize off`), the next read is not blocked, even mid-session.
- [ ] Change `hook_repeat_read` while a session is running: that session keeps its old behavior; a new one picks up the change.
- [ ] A read denied by Claude's permission prompt is not later treated as "already read".

## Session end

- [ ] Quit Claude Code normally. `<data dir>/hooks/sessions/` loses that session's file and `events.jsonl` is ingested (`trove hooks status` counts appear).
- [ ] With the gateway on, `trove receipts` shows a receipt for the session immediately, not 30 minutes later.
- [ ] With `notifications.desktop = true` on macOS/Linux, a notification appears when the session ends.
- [ ] Exit with Ctrl-C, `/clear` and `/resume`: the hook does not hang the exit, and nothing is left running.
- [ ] Codex: closing a session runs the SessionEnd hook within its 1-3 s limit (check `<data dir>/hooks.log` for timeouts).

## Fail open

- [ ] `chmod 000 <data dir>/hooks` (or make the data directory read-only), use the agent: it works normally, `hooks.log` may be empty because the log is unwritable.
- [ ] Put garbage in `<data dir>/hooks/config.json` and in a session file: the agent works, hooks are inert.
- [ ] Rename the `trove` binary the hooks point at: Claude Code shows a non-blocking hook error and carries on.

## Timing (per call, on your OS)

- [ ] `time` 20 runs of `echo '<a PreToolUse Read input>' | trove hook pre_tool_repeat_read` and compare with an empty Go program (`package main; func main(){}`) on the same machine. The difference is Trove's cost; the task target is under 20 ms typical.
- [ ] On macOS and Linux run `go test ./internal/hooks -run TestHandlerTiming -v`: alone p95 must be under 20 ms and 8-at-once p95 under 100 ms. These are enforced there but have only been run on Windows so far.

Measured on the development machine (Windows 11, real-time antivirus, 2026-10-30):

| | median |
|---|---|
| empty Go program, spawned from bash | 206 ms |
| `trove hook pre_tool_repeat_read` | 220 ms |
| handler alone, 500-entry ledger | p50 21 ms, p95 26 ms |
| handler, 8 at once | p50 89 ms, p95 152 ms |

The handler figures are dominated by a fixed ~17 ms the system charges to open any file written since it was last
opened (measured with a 200-byte and a 200 KB file alike), which every call pays because it reads the journal the
previous call appended to. Expect much less on macOS and Linux, but that is unmeasured.

## Contract details still to confirm

- [ ] Claude Code: the unread last ~80k characters of `code.claude.com/docs/en/hooks` (of 283k) were not read. Skim them for anything on `PreToolUse` ordering, `PreCompact` input, or SessionEnd limits that changes this design.
- [ ] Codex: the shell tool name your Codex version sends in `PreToolUse` for commands (`Bash`, `exec_command`) and whether any file-read tool exists by now. If one does, repeat-read could be added for Codex.
- [ ] Windows: whether each agent's hook shell accepts the quoted absolute path of `trove.exe` as written.
