# Agent hooks

Hooks are small commands that Claude Code and Codex CLI run around tool calls and when a session ends.
Trove's hooks work **without the gateway**, so they also help if you use a subscription. They never
read or change credentials, and they never touch your agent's traffic.

```
trove hooks install      # shows the change, asks, backs the file up
trove hooks status       # where they are installed and what they did
trove hooks uninstall    # removes only Trove's entries (same as `trove undo`)
trove hooks sync         # refresh what the hooks read after editing config.toml
trove hooks probe        # check which tool calls your agent version lets hooks rewrite
trove hooks install --rewrite   # also install the hooks that rewrite tool input and output
```

## What they do

| Hook (`trove hook <event>`) | Agent event | What it does |
|---|---|---|
| `pre_tool_repeat_read` | Claude Code `PreToolUse` on `Read` | If the session already read this file range and the file is unchanged (same modification time, size and content hash), denies the call once with a short message naming the earlier prompt, so the agent reuses what it has. |
| `post_tool_read_record` | Claude Code `PostToolUse` on `Read` | Records the read, but only after it succeeded. A read that was denied or failed is never taken for one that happened. |
| `post_tool_edit_record` | Claude Code `PostToolUse` on `Edit`, `Write`, `MultiEdit`, `NotebookEdit` | Records that a file was edited, for the session features ([features.md](features.md)). Never prints anything and never changes the call. |
| `session_reset` | `SessionStart`, Claude Code `PreCompact` | Pins the optimizer states for the session. Forgets earlier reads after a compaction or resume, because the agent no longer has those files in context. |
| `session_end_receipt` | `SessionEnd` | Moves the hook counters into the database, runs the hook guardrail, writes the session receipt, refreshes the snapshot the other hooks read, then writes the session's features and, if stale, the repo's ([features.md](features.md)). |
| `pre_tool_large_output` (optional, `--large-output`) | `PreToolUse` on `Bash` | For a known-noisy command (`go test ./...`, `npm test`, bare `pytest`, `make`, verbose builds) adds a hint to use `trove_run`. Never blocks. Only fires when the MCP server is registered. |

Session features also come from `post_tool_read_record`, `session_reset` (compactions) and `post_tool_output`
(tool-result features, only with `--rewrite`). Each call appends one numeric line to a small journal next to the
ledger; paths and commands are hashed first, nothing from the content is written, and a failure to record is ignored.

Codex CLI gets `session_reset`, `session_end_receipt` and, if you ask for it, `pre_tool_large_output`.
It does **not** get repeat-read prevention: Codex's hook page names no file-read tool to match.

## Rewriting tool input and output

With `--rewrite`, hooks can also change what a tool is asked to do and what the model sees back. Both go through
the shared engine (`TransformToolInput`, `TransformToolOutput`); the hook only parses the agent's JSON and prints
the engine's answer. Every rewrite is **opt-in, shadow first**, and does nothing until the agent version has
been **seen to honour it** (see "What your agent version allows" below).

| Optimizer (`[optimizers]`) | Hook | What it changes |
|---|---|---|
| `hook_read_limit` | `PreToolUse` on `Read` (`pre_tool_repeat_read`) | A `Read` of a file of 40 KB or more that sets neither `offset` nor `limit` gets `limit: 400`, and the model is told it got a partial read and how to read more. Smaller files, and reads that already ask for a range, are left alone. A repeat-read denial always wins over a limit. |
| `hook_quiet_commands` | `PreToolUse` on `Bash` (`pre_tool_large_output`) | Adds one output-quieting flag to a command on the allowlist below. Nothing else about the command changes. |
| `noise_normalizer` | `PostToolUse` on `Bash`, `Grep`, `Glob` (`post_tool_output`) | Lossless clean-up of the output (the same code the gateway runs). |
| `output_trimmer` | `PostToolUse` on `Bash` only | In large test and build output keeps failures, stack traces, their context and the first and last lines, and replaces the rest with `[Trove: N lines trimmed; full output sha256:<id>]`. The full output is written to the recovery store **before** the shortened text is sent, and `trove_fetch_full` returns it. Needs the Trove MCP server registered; without it, or if the store cannot be opened, nothing is trimmed. |

`updatedInput` replaces the **whole** input object, so the hook sends every field it received with only the one
changed field different. It sets no `permissionDecision`: your normal permission prompts still apply to the
rewritten call, and a hook never approves what you would have been asked about. `updatedToolOutput` is sent in
the tool's own response shape (for Bash, the object with `stdout`, `stderr`, `interrupted`, `isImage`, with
only the text changed), because Claude Code silently ignores a value that does not match. Output of an
interrupted command, output in a shape Trove does not recognise, and `Grep`/`Glob` results that are file lists
are left exactly as they are.

### Guardrails for rewrites

Each rewriting optimizer is watched by what the agent does next, from the hook's own counts ("measured"):

| Optimizer | Counted as "did not help" | Rule |
|---|---|---|
| `hook_quiet_commands` | the agent ran the same command again exactly as first written (`reissued`). It is counted once per command and the command is not quieted a second time, so the agent never has to run it twice to get its output. | `hook_rerun` |
| `output_trimmer` | the agent called `trove_fetch_full` for an output the hook trimmed (`fetched_back`). A `PostToolUse` hook on `mcp__trove__trove_fetch_full` (`post_tool_fetch`, installed with `--rewrite`) does the counting, and only for ids this session's trimming produced, so fetches of `trove_run` output do not count against it. | `hook_fetch_back` |
| `hook_repeat_read` | a blocked read the agent retried (`reread_anyway`) | `hook_reread` |

Once at least `measure.min_sessions` (20) actions are counted and the share that did not help reaches
`measure.refetch_rate_percent` (25%), that optimizer goes back to `shadow` for new sessions and `trove report`
says why. Each optimizer is judged on its own. These counts cannot see everything: a rewrite can cost extra turns
without any of them firing, which is why the A/B comparison remains the test of whether it saved money.
`hook_read_limit` has no such rule, because reading the rest of a limited file with `offset` and `limit` is
what it asks the agent to do.

### The quiet-command allowlist

A command is rewritten only if it is exactly a listed command followed by plain words (letters, digits and
`_-./=:@+,`). Anything with a pipe, `;`, `&&`, a redirect, a subshell, `$`, a quote, a glob, `&`, a newline,
`--`, a leading `sudo` or `VAR=1`, or a flag that already sets the verbosity is left alone. A rule is only used
for real if the flag's own documentation says it limits output without hiding errors or changing behaviour;
rules that fail that test only count in shadow mode.

| Command | Adds | Real rewrite? | Source (read 2026-11-04) |
|---|---|---|---|
| `pip install`, `pip3 install` | `-q` | yes | pip.pypa.io/en/stable/cli/pip/: "Give less output", maps to log levels so warnings and errors remain |
| `npm install`, `npm ci` | `--quiet` | yes | docs.npmjs.com/cli/v10/using-npm/config: shorthand for `--loglevel warn` |
| `cargo build`, `cargo check` | `--quiet` | shadow only | the page says "do not print cargo log messages" but not that compiler errors remain |
| `pytest` | `-q` | shadow only | the page that documents `-q` was not read |

To promote a shadow-only rule, read the page that settles it, change `Verified` and `VerifiedAt` in
`internal/optimize/quiet.go`, and say what the page said.

### Failures are never trimmed

The trimmer keeps every line that carries a failure (`--- FAIL`, `FAIL`, `ERROR`, `panic:`, `Traceback`,
`AssertionError`, `●`, `[ERROR]`, `thread '...' panicked`, ...), two lines of context either side, the whole
stack trace that follows (up to 40 lines), and the first 5 and last 10 lines where runners print their summary.
`TestEveryKindOfFailureLineSurvivesTrimming` and `TestNoisyBashOutputIsTrimmedRecoverablyAndFailuresSurvive`
check this on Go, Python, Jest, Cargo and Maven output. The shortened text is a pure function of the output.

### What your agent version allows

A release of an agent can ship with a hook field ignored for some tool, and a value in the wrong shape is
ignored with no error. So Trove does not assume: `trove hooks probe --live --max-usd 0.25` (or
`go run ./scripts/hookprobe --live --max-usd 0.25`) runs one tiny `claude -p` session per tool (Bash, Read,
Grep, Glob) in a throwaway folder, with your own login, and checks that

- the tool really ran with the rewritten input (the `PostToolUse` hook is told what it ran with), and
- the model's reply contains the replacement output.

The result is saved to `hooks/capabilities.json` in the data directory. Until a tool has passed, the rewriting
optimizers for it stay in shadow mode, and `trove doctor` says which optimizer is held back, for which tool, and
why. It also warns when the installed version differs from the probed one. **Rerun the probe after every agent
update.** Without `--live` the probe runs nothing and reports what the documentation says, which Trove treats as
"unsupported". `--record-dir` saves redacted copies of the hook inputs the agent really sent as test fixtures
(local paths and ids are replaced; the redaction has a test).

If `ANTHROPIC_API_KEY` is set in your shell and is not valid, `claude` answers 401 and ignores your login; the
probe now reports the agent's own message. Run it with the variable unset for that one command.

Codex CLI could not be run here. Its documentation says a `PreToolUse` hook can rewrite a call with
`updatedInput` (for Bash it must keep a string `command`) and that `updatedMCPToolOutput` and `suppressOutput`
"are parsed but not supported yet". So Codex gets the Bash input rewrite from documentation only, no output
rewriting, and the probe records that as a docs result, not a measurement.

The last probe result on this repo's development machine is `docs/hook-capabilities.json`.

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
| `rewritten` / `would_rewrite` | an input or output was changed / would have been | measured count |

Tokens saved is **estimated** (file size ÷ 3.5 bytes per token, lower for line-limited reads) and counts only
blocks the agent did not retry. Hints have no savings estimate, because the output they avoid is not known.
Rewrites record the bytes removed from what the model sees; that is **estimated from sizes** and cannot see
what the agent does next (an extra turn to read the rest of a limited file, a re-run of a command without its
flag). It is a shadow-style figure. Whether a rewrite saved money is a question for the session-level A/B
comparison (`docs/measurement.md`), and `bench` has `hooks` and `hooks_gateway` conditions so hooks alone can be
compared with the gateway on the same tasks. That benchmark has **not been run**: it spends real money, so no
hooks-versus-gateway savings figure exists yet. The runner is wired for it: a hooks condition installs the hooks
in the container, copies in the live probe result, and refuses to start unless the probe was for the container's
exact agent version and the hooks' snapshot matches the condition, so it cannot silently measure shadow mode:

```
go run ./scripts/hookprobe --live --max-usd 0.25 --out probe.json
# add hooks and hooks_gateway under conditions: in bench/config.yaml, then:
python -m trovebench run --hook-capabilities probe.json --dry-run
```

The container path has not been exercised against Docker, only its checks have unit tests.

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
A hard 150 ms deadline lets a call through if anything stalls; `post_tool_output` gets 250 ms because shortening
output writes the full text to the recovery store first. Past the deadline the original output goes through.

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
- Fixtures under `internal/hooks/testdata` are written from the documented examples, not captured from live agents,
  except `testdata/recorded/<version>/`, which `hookprobe --record-dir` captured from a real session (Claude Code
  2.1.318: Bash, Read, Grep, Glob). The recorded Grep and Glob responses carry `filenames` lists, and Read carries
  `file.content`; the hooks leave those shapes unchanged rather than guess at a replacement.
- The probe judges an input rewrite by what the `PostToolUse` hook is told the tool ran with, and an output rewrite
  by whether the model repeated a marker. It does not show that the rewritten output stays in later requests'
  history or in the prompt cache (`docs/non-gateway-optimization.md`, Q2).
- The rewrite guardrails count only what the hooks can see. A rewrite that costs extra turns, or a fetch-back
  by an agent without the `post_tool_fetch` hook installed, goes unseen; judge rewrites with the A/B comparison.
- Hook rewrites are Claude Code only for output; Codex gets Bash input rewriting from documentation, untested live.
- The quiet flags were checked against documentation read on 2026-11-04. Windows quoting of rewritten commands is
  unverified, which is why the allowlist accepts only plain words.
- Windows: commands are written with the binary's absolute path; how each agent's shell quotes a path with spaces on
  Windows is unverified.
