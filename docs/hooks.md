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
trove hooks verify       # the same check inside a Claude Code session you start yourself (any plan)
trove hooks install --rewrite   # also install the hooks that rewrite tool input and output
```

## What they do

| Hook (`trove hook <event>`) | Agent event | What it does |
|---|---|---|
| `pre_tool_repeat_read` | Claude Code `PreToolUse` on `Read` | If the session already read this file range and the file is unchanged (same modification time, size and content hash), denies the call once with a short message naming the earlier prompt, so the agent reuses what it has. |
| `post_tool_read_record` | Claude Code `PostToolUse` on `Read` | Records the read, but only after it succeeded. A read that was denied or failed is never taken for one that happened. |
| `post_tool_edit_record` | Claude Code `PostToolUse` on `Edit`, `Write`, `MultiEdit`, `NotebookEdit` | Records that a file was edited, for the session features ([features.md](features.md)). Never prints anything and never changes the call. |
| `session_reset` | `SessionStart`, Claude Code `PreCompact` | Pins the optimizer states for the session. Forgets earlier reads after a compaction or resume, because the agent no longer has those files in context. During a rule-set trial (`trove autopilot trial <rule set>`), it assigns the session an arm, and in the "on" arm returns the rule set's text as SessionStart `additionalContext`. It does this at startup and again after a resume, `/clear` or compaction, for Claude Code only (see [autopilot.md](autopilot.md)). |
| `session_end_receipt` | `SessionEnd` | Moves the hook counters into the database, runs the hook guardrail, writes the session receipt, refreshes the snapshot the other hooks read, then writes the session's features and, if stale, the repo's ([features.md](features.md)). |
| `post_tool_test_result` | Claude Code `PostToolUse` and `PostToolUseFailure` on `Bash` | The test-pass signal. When the command is exactly one of the project's test commands in `.trove/commands.toml` (an entry named `test`, or starting or ending with `test-`, `test_`, `-test`, `_test`; arguments compared after splitting on whitespace), it records the exit code and a hash of the command in `test_runs`. A successful call counts as exit 0; a failure's code is read from the first line of its error (`Exit code N`). Interrupted runs and failures without that line are not recorded. Never prints anything and never changes the call. The session roll-up carries the last status (pass, fail or none) and the run and failure counts. |
| `pre_tool_subagent` | `PreToolUse` on `Agent` (`Task` before Claude Code v2.1.63; checked 2026-11-17) | Does nothing unless a scout is installed and this session is in the "off" arm of its A/B trial; then it declines a call to the scout subagent with a short message asking the agent to do the work directly. See docs/tasks.md. |
| `pre_tool_large_output` (optional, `--large-output`) | `PreToolUse` on `Bash` | For a known-noisy command (`go test ./...`, `npm test`, bare `pytest`, `make`, verbose builds) adds a hint to use `trove_run`. Never blocks. Only fires when the MCP server is registered. |
| `post_tool_run` (installed with `--large-output` or `--rewrite`) | `PostToolUse` on `mcp__trove__trove_run` | Counts, once per session, that the agent used `trove_run`. It is how the guardrail for the hint above sees whether the hint is followed. Changes nothing. Run `trove hooks install` again if your hooks were installed with v0.3.0 or earlier; `trove hooks status` says when it is missing. |

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
| `hook_output_dedup` | `PostToolUse` on `Bash`, `Grep` and `Glob` (`post_tool_output`) | When an output (200 bytes or more, not an error) is byte-identical to one earlier in the session, replaces it with `[Trove: identical to an output earlier in this session (prompt #N, … ago); full output sha256:<id>]`. Only hashes are kept in the session ledger; there is no time-based expiry, and compaction or a resumed session forgets them. The repeat is written to the recovery store first and `trove_fetch_full` returns it. Needs the Trove MCP server and the recovery store, like the trimmer; otherwise it stays in shadow. `Read` is left out until a live probe confirms its output shape. |
| `hook_read_outline` | `PostToolUse` on `Read` (`post_tool_read_record`) | When a `Read` without offset or limit returns 40 KB or more of a file the outliner knows (Go, Python, JS/TS, Rust, Java, Kotlin, C#, Swift) with at least 3 declarations, replaces `tool_response.file.content` with the file's outline (the same outliner as `trove_read`) and a note saying how to read a section with offset and limit. Every other response field is kept. The full text goes to the recovery store first and `trove_fetch_full` returns it; a second full `Read` of the same file goes through whole (and is not denied by `hook_repeat_read`), so a file is outlined at most once a session. Needs the Trove MCP server and the recovery store, and stays in shadow until `trove hooks probe` has seen `Read` honour `updatedToolOutput` on the installed version (the response shape is the one recorded from Claude Code 2.1.318). If `hook_read_limit` is on, it limits large reads first and this one has nothing to do. |
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
| `hook_output_dedup` | the agent called `trove_fetch_full` for an output the hook collapsed as a repeat (`fetched_back`), counted once per output by the same `post_tool_fetch` hook. | `hook_fetch_back` | Uses `measure.refetch_rate_percent` (25% by default). |
| `hook_read_outline` | the agent needed the whole file after all (`extra_read`): a second full `Read` of an outlined file, or `trove_fetch_full` of its full text, counted once per outlined file between them. A `Read` with offset and limit is how an outline is meant to be used and is never counted. | `hook_extra_read` |
| `output_trimmer` | the agent called `trove_fetch_full` for an output the hook trimmed (`fetched_back`). A `PostToolUse` hook on `mcp__trove__trove_fetch_full` (`post_tool_fetch`, installed with `--rewrite`) does the counting, and only for ids this session's trimming produced, so fetches of `trove_run` output do not count against it. | `hook_fetch_back` | The limit for this one is half of `measure.refetch_rate_percent` (12% by default), because it hides text. 
| `hook_repeat_read` | a blocked read the agent retried (`reread_anyway`) | `hook_reread` |
| `hook_read_limit` | the agent read more of a file after its first read was limited (`extra_read`): a later `Read` of the same file that reaches past line 400. It is counted once per file, and a read inside the first 400 lines is not counted. Reading the rest is what the note asks for, so this rule is looser: the limit is twice `measure.refetch_rate_percent` (50% by default). | `hook_extra_read` |
| `hook_large_output` | the hint to use `trove_run` was given in a session and the agent never used it. A `PostToolUse` hook on `mcp__trove__trove_run` (`post_tool_run`, installed with `--large-output` or `--rewrite`) counts one `adopted` event per session. The rule compares sessions: it needs 20 sessions where the hint was given and rolls the hook back when fewer than 10 percent of them used `trove_run` (`measure.MinAdoptionPercent`). Codex sessions are not judged (its hooks cannot see `trove_run`), and the rule does not run at all unless the counter hook is installed, so an install from before it existed is not rolled back for something it could not count: `trove hooks status` says so and `trove hooks install` adds it. | `hook_unused_advice` |

Once at least `measure.min_sessions` (20) actions are counted and the share that did not help is above
`measure.refetch_rate_percent` (25%, or the stricter or looser limit in the table) beyond chance, by a sequential
test that stays valid however often it runs ([measurement.md](measurement.md), "Sequential rollback checks";
for example 11 of 20 or 19 of 40 at 25%), that optimizer goes back to `shadow` for new sessions and `trove report`
says why. Each optimizer is judged on its own. The limits for the trimmer, the read limit and the hint are placeholders, not tuned by a benchmark. These counts cannot see everything: a rewrite can cost extra turns
without any of them firing, which is why the A/B comparison remains the test of whether it saved money.

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
| `pytest` | `-q` | shadow only | the page that documents `-q` was not read; error visibility checked on a recorded run (below) |

To promote a shadow-only rule, read the page that settles it, change `Verified` and `VerifiedAt` in
`internal/optimize/quiet.go`, and say what the page said.

Error visibility is also checked mechanically. A rule's `ErrorFixture` names a recorded pair of
outputs from the same failing run, `internal/optimize/testdata/quiet/<name>.default.txt` and `.quiet.txt`, and a
test fails unless the quiet one keeps every failure line of the default one (`MissingFailureLines`, the trimmer's
failure-line rule). `pytest` has one (pytest 9.1.1, 2026-11-17: an assertion failure, a fixture error and an
exception; every failure line kept). Cargo was not available to record, so its rules have none. A passing
fixture makes a rule promotable, not promoted: on Codex the rewrite itself still waits for a live probe, so `Verified` stays false.

### Failures are never trimmed

The trimmer keeps every line that carries a failure (`--- FAIL`, `FAIL`, `ERROR`, `panic:`, `Traceback`,
`AssertionError`, `●`, `[ERROR]`, `thread '...' panicked`, ...), two lines of context either side, the whole
stack trace that follows (up to 40 lines), and the first 5 and last 10 lines where runners print their summary.
`TestEveryKindOfFailureLineSurvivesTrimming` and `TestNoisyBashOutputIsTrimmedRecoverablyAndFailuresSurvive`
check this on Go, Python, Jest, Cargo and Maven output. The shortened text is a pure function of the output.

### What your agent version allows

A release of an agent can ship with a hook field ignored for some tool, and a value in the wrong shape is
ignored with no error. So Trove does not assume. There are two ways to test, and Trove starts a model request
itself in only one of them:

- **`trove hooks verify` (any plan, including Claude.ai logins).** Trove runs nothing. It writes a settings file and one
  short prompt per tool; you start `claude --settings <file>` yourself, paste the prompts and approve each call. The
  test hooks in that file (written in the documented exec form, `command` plus `args`, so there is no shell quoting to get
  wrong on Windows) rewrite that one call and record what the agent did, and switch themselves off after an hour. The
  hooks reference (read 2026-11-06) does not say two things this relies on, so the first real run must confirm them:
  whether hooks in a `--settings` file are added to your other hooks or replace them for that session (either is
  harmless for the test), and that a PostToolUse hook's `tool_input` is the rewritten input (the earlier live probe on
  Claude Code 2.1.318 saw it; see `docs/hook-capabilities.json`).
  `trove hooks verify --finish --saw-output Bash,Read=no` reads the record. The input result is observed. A hook cannot see
  what the model was shown, so the output result is **what you report** about the model's reply, and it is stored as
  `method: session` with that said in the evidence. A tool you do not answer for keeps its output result untested.
- **`trove hooks probe --live --max-usd 0.25`** (or `go run ./scripts/hookprobe --live --max-usd 0.25`) runs one tiny
  `claude -p` session per tool (Bash, Read, Grep, Glob) in a throwaway folder. Because Trove starts those requests, it is
  **refused unless there is evidence Claude Code uses your own API key** (the same non-credential signals `trove setup`
  uses; `ANTHROPIC_API_KEY` for the script), and never runs on a Claude.ai login (Free, Pro, Max, Ultra, Team,
  Enterprise). It checks that

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
| `reissued` | the agent ran a quieted command again as first written | measured count |
| `fetched_back` | the agent fetched back an output a hook trimmed | measured count |
| `extra_read` | the agent read more of a file after `hook_read_limit` limited a read of it (once per file) | measured count |
| `adopted` | the agent used `trove_run` (once per session) | measured count |

Tokens saved is **estimated** (file size ÷ bytes per token, lower for line-limited reads) and counts only
blocks the agent did not retry. Bytes per token starts at 3.5. Once your history holds at least 20 clean
request pairs for the session's model family, the hooks use the ratio learned from them instead (`trove hooks
sync` refreshes it; a session pins the ratio when it starts). Learning compares the tool results one request
produced with the new tokens the next request read, only where the cache stayed warm, so a rebuilt cache
prefix does not distort it. The spool records the ratio and its sample size with each such estimate. Hints have no savings estimate, because the output they avoid is not known.
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
share the agent read again anyway is above `measure.refetch_rate_percent` (25%) beyond chance (sequential test, see above), `hook_repeat_read` goes back to
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
is opened right after it was written.

## Where the hooks go

| Agent | File | Notes |
|---|---|---|
| Claude Code, user | `~/.claude/settings.json` (or `$CLAUDE_CONFIG_DIR`) | `hooks.<Event>` array of matcher groups. |
| Claude Code, project | `<project>/.claude/settings.local.json` | Not committed, so a machine-specific path stays local. |
| Codex CLI, user | `~/.codex/hooks.json` | If `config.toml` already has inline `[hooks]`, Trove skips Codex rather than create both (Codex warns when a folder has both). Codex asks you to review new hooks with `/hooks`. |
| Cursor, user (experimental) | `~/.cursor/hooks.json` | `"version": 1`; `hooks.<event>` is a flat list of `{command, type, timeout, matcher}` (timeout in seconds). Events: `preToolUse`/`postToolUse` on `Read` and `Write`, `sessionStart`, `preCompact`, `sessionEnd`; `--rewrite` adds `preToolUse` on `Shell`. User level only. |
| Gemini CLI, user (experimental) | `~/.gemini/settings.json` | Same matcher groups as Claude Code, but `timeout` is in **milliseconds**. Events: `BeforeTool`/`AfterTool` on `read_file`, `AfterTool` on `replace\|write_file`, `SessionStart`, `PreCompress`, `SessionEnd`; `--rewrite` adds the shell pre-hook, the output hook and the fetch-back counter. Gemini CLI asks you to trust changed hooks. User level only. |

Cursor and Gemini CLI have not been run on a real install, so `trove hooks install` (`--agent all`) leaves them out. Install them only by name: `--agent cursor` or `--agent gemini`. `trove hooks uninstall` still removes Trove's entries from both.

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
  `tool_input.command`. Re-read 2026-11-17: `updatedInput` is valid only with `permissionDecision: "allow"`, which
  Trove never sends, so Codex documentation no longer counts as evidence for input rewriting (only a live or
  session probe does); PostToolUse has no field that replaces tool output (`updatedMCPToolOutput` is listed as
  unsupported), so Trove does not rewrite Codex output.
- Cursor (`cursor.com/docs/agent/hooks`, read 2026-11-17; the tool-event part of the page was read as a summary):
  common input `conversation_id`, `generation_id`, `hook_event_name`, `model`, `workspace_roots`; `preToolUse` adds
  `tool_name` (`Shell`, `Read`, `Write`, `Grep`, `MCP:<name>`), `tool_input`, `cwd` and may answer `permission`,
  `agent_message`, `updated_input`; `postToolUse` adds `tool_output` and may answer `additional_context`, or
  `updated_mcp_tool_output` for MCP tools only. Exit 2 blocks; other failures let the call through unless
  `failClosed`. The per-tool keys inside `tool_input` are not documented.
- Gemini CLI (`geminicli.com/docs/hooks/reference/`, read 2026-11-17): input `session_id`, `transcript_path`,
  `cwd`, `hook_event_name`; `BeforeTool` adds `tool_name`, `tool_input`; `AfterTool` adds `tool_response`
  (`llmContent`, `returnDisplay`, `error`). `BeforeTool` may answer `decision: "deny"` with `reason`, or
  `hookSpecificOutput.tool_input` (merged into the arguments); `AfterTool` `decision: "deny"` replaces the result
  with `reason`. Stdout must be only JSON. Neither Cursor's `preToolUse` nor Gemini's `BeforeTool` documents a field
  that shows the model a note without blocking, so the large-output hint is not sent to them, and not counted.

- Test-pass signal (`code.claude.com/docs/en/hooks`, read 2026-11-17): `PostToolUse` fires only after a tool call
  succeeds; `PostToolUseFailure` fires after one fails and matches on tool name. Its input has `error` (for Bash, a
  command that ran and exited has the first line `Exit code N`), optional `is_interrupt` and `duration_ms`. Neither
  event has an exit-code field. Seen live on Claude Code 2.1.326 (2026-11-17, Windows, headless `claude -p` in a
  throwaway Go project): a passing `go test ./...` arrived as `PostToolUse` with `stdout`, `stderr`, `interrupted`,
  `isImage`, `noOutputExpected`; a failing one as `PostToolUseFailure` with `error` starting `Exit code 1` and
  `is_interrupt: false`. The hook recorded exit 0 and 1 under one command hash, and both reached `test_runs`. The
  redacted failure payload is `internal/hooks/testdata/recorded/2.1.326/bash_post_failure.json`.

- Prompt submission and resume, for the cold-resume guard (read 2026-11-23 at `code.claude.com/docs/en/hooks`,
  `/hooks-guide`, `/statusline`, `/prompt-caching`, `/env-vars` and `/headless`):
  - `UserPromptSubmit` input is the common fields plus `prompt` and, when the session has a custom title,
    `session_title`. Common `prompt_id` is "Absent until the first user input". There is no matcher: "always
    fires on every occurrence".
  - It blocks with `decision: "block"`; `reason` is "Shown to the user when `decision` is `"block"`. Not added to
    context". By default the block message "ends with `Original prompt:` and the submitted prompt"
    (`suppressOriginalPrompt: true` leaves it out). Exit 2 blocks too, with stderr "treated the same way as
    `reason`". The hook "can't replace the prompt". Trove blocks with JSON and always exits 0.
  - `hookSpecificOutput.additionalContext` is "added to Claude's context alongside the submitted prompt";
    `systemMessage` is a "Warning message shown to the user". The page does not say whether `systemMessage` also
    reaches the model on this event. `additionalContext`, `systemMessage` and plain stdout are each capped at 10,000
    characters; longer text is saved to a file and replaced by its path and a 2,000-character preview.
  - Default timeouts: 600 s for command hooks, which "Claude Code lowers … to 30 on `UserPromptSubmit`". `SessionEnd`
    hooks "share a 1.5-second budget", raised to a longer per-hook `timeout` up to 60 s.
  - It "Also fires on turns Claude Code starts on its own": "A session-scoped scheduled task firing, including a
    `/loop` iteration", "A background subagent reporting back to the session that started it", "A message another
    session sends to your main conversation". No input field tells these apart from a typed prompt.
  - `SessionStart` with `source` `"resume"` (from `--resume`, `--continue` or `/resume`) or `"fork"`, when the
    transcript has a response and Claude Code is v2.1.251 or later, adds `seconds_since_last_response`,
    `context_tokens` ("Tokens the first request of the resumed session re-sends as its prompt"),
    `prompt_cache_likely_expired` ("`true` when the last response is older than the session's prompt cache lifetime
    or a later compaction replaced the cached conversation") and `estimated_cache_write_usd`. `SessionStart` cannot
    block.
  - The status line's `prompt_cache` has `warm`, `ttl` (`"5m"` or `"1h"`) and `expires_at`. The main conversation's
    cache lasts one hour on a subscription within its included usage and five minutes on an API key, usage credits
    or a cloud provider; `promptCacheTtl` or `CLAUDE_CODE_PROMPT_CACHE_TTL` changes it.
  - Not documented: whether a blocked prompt goes back to the input box (only an interrupt is said to keep it
    out), whether `--resume` keeps the session id, and any way for a hook to tell an unwatched `claude -p` run from an
    interactive session. `CLAUDECODE=1` marks every subprocess Claude Code starts, whatever the mode. Because of
    the last point the guard never blocks until a live check or the user's own session records that it can
    (`UserPromptSubmit` in `hook-capabilities.json`, `block`).

### Live check of the test-pass signal (manual)

Steps 1-4 were run on 2.1.326 on 2026-11-17 (passing and failing runs; the interrupt in step 3 and step 5 were
not). Re-run them when Claude Code changes. Use a throwaway data dir. `trove hooks sync` ingests the spool only when
the data dir already has a database, so in a fresh data dir end the session (the `SessionEnd` hook) first.

1. In a Go project, add `.trove/commands.toml` with `[commands.test]` and `run = ["go", "test", "./..."]`.
2. `TROVE_DATA_DIR=/tmp/trove-probe trove hooks install --scope project --yes`, then start Claude Code there.
3. Ask the agent to run `go test ./...` through Bash while the tests pass, then break one test and ask again, then
   ask it to run `go test ./... ` once more and interrupt it with Esc.
4. End the session. Check `sqlite3 /tmp/trove-probe/trove.db 'SELECT exit_code FROM test_runs ORDER BY ts'`
   shows `0` then a non-zero code, and no row for the interrupted run. If the failing run has no row, note the Claude
   Code version and report it: the failure payload differs from the documentation.
5. With the gateway in front of the session (API key), check that `sessions.outcome_signals` has
   `"tests":{"last":"fail","runs":2,"failed":1}`.
6. `trove hooks uninstall`, then record the version and date here.

### Live check of outline-first reads (manual)

Steps 1-2 were run on Claude Code 2.1.327 on 2026-11-17: `Read: rewrite output yes [live]` (the probe replaced
`tool_response.file.content`, the field this optimizer rewrites, and the model's reply contained the replacement), and
the recorded shape (`internal/hooks/testdata/recorded/2.1.327/read_post.json`) matches 2.1.318. Step 3 was not run.
`hook_read_outline` stays in shadow on any install whose own probe has not seen this, because built-in tools silently
ignore a replaced output whose shape does not match. It needs an API key and costs a few cents. Use a throwaway data dir.

1. `TROVE_DATA_DIR=/tmp/trove-probe trove hooks probe --live --max-usd 0.10 --tools Read --record-dir /tmp/read-rec`.
   Check that the output shows `Read: ... rewrite output yes [live]`. If it shows `no`, stop: the optimizer stays in
   shadow on that version, and the version goes under Known gaps.
2. Compare `/tmp/read-rec/read_post.json` with `internal/hooks/testdata/recorded/2.1.318/read_post.json`. The
   text must still be at `tool_response.file.content`. If it has moved, add the new recording and fix
   `withReadContent` before going on.
3. With `hook_read_outline = "on"`, `trove mcp install` done and that data dir, start Claude Code in a repo with a
   source file over 40 KB. Ask it to read the whole file and then say what the function on a named line does.
   Check that it got the outline (it says so or uses offset and limit), that the hook events record one
   `rewritten` for `hook_read_outline`, and that asking it to read the whole file again works and adds one
   `extra_read`.
4. Record the Claude Code version and date here.

## Known gaps

- The test-pass signal reaches the session roll-up only for sessions the gateway saw (subscription sessions have
  no `sessions` row yet). Test runs are stored under the base session key, so an idle-gap
  continuation (`key~n`) shows none. `trove_run` results are not counted yet; only Bash calls are.

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
  history or in the prompt cache.
- The rewrite guardrails count only what the hooks can see. A rewrite that costs extra turns, or a fetch-back
  by an agent without the `post_tool_fetch` hook installed, goes unseen; judge rewrites with the A/B comparison.
- Hook rewrites are Claude Code only for output; Codex gets Bash input rewriting from documentation, untested live.
- The quiet flags were checked against documentation read on 2026-11-04. Windows quoting of rewritten commands is
  unverified, which is why the allowlist accepts only plain words.
- Windows: commands are written with the binary's absolute path; how each agent's shell quotes a path with spaces on
  Windows is unverified.
