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
| `user_prompt_cold_check` | Claude Code `UserPromptSubmit` | The cold-resume guard (`hook_cold_resume`, off by default; see [below](#cold-resume-guard)). When a large session's prompt cache has expired, it warns about what the next prompt will cost, or stops the first prompt after a cold resume once. Then, for a prompt the guard let through, the routine steer (`hook_routine_steer`, off by default; see [below](#routine-steer)). Returns at once while both are off. |
| `pre_model_switch` | Claude Code `PreModelSwitch` | The model-switch guard (`hook_model_switch`, off by default; see [below](#model-switch-and-cold-compact-guards)). Adds what switching a large, warm session to another model costs to Claude Code's own confirmation. Never blocks or asks. |
| `post_model_switch` | Claude Code `PostModelSwitch` | Records that a switch the model-switch guard warned about went ahead, and updates whether the routine steer may act on the session's new model. Prints nothing. |
| `pre_compact_cold_check` | Claude Code `PreCompact`, matcher `manual` | The cold-compact guard (`hook_cold_compact`, off by default). Stops a manual `/compact` once when the cache has expired and the history is large. The existing `session_reset` entry on `PreCompact` is unchanged. |
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

## Cold-resume guard

Coming back to a large session after its prompt cache expired makes the next request write the whole context to
the cache again. On a subscription that shows up as a jump in the 5-hour and weekly meters; on an API key, as a
large cache-write bill. `hook_cold_resume` (technique B21, T1 guard) says what continuing will cost before it happens
and, in `ask` mode, gives you one chance to `/clear` instead.

```toml
[optimizers]
hook_cold_resume = "shadow"     # off (default) | shadow | on

[optimize]
cold_resume_action = "warn"     # what "on" does: warn | ask
cold_resume_min_tokens = 150000 # act from this many tokens written to the cache again...
cold_resume_min_week_pct = 3.0  # ...or from this estimated share of the weekly limit (low end of its range)
```

The state, action and thresholds are pinned when a session starts, so a change applies to new sessions (rule 5).
Autopilot never turns the guard on: it changes what you see, not what is sent. Its guardrail can still lower it.

| Mode | What happens on a prompt the guard acts on | Recorded |
|---|---|---|
| `off` | nothing | nothing |
| `shadow` | nothing you can see | `would_block` (after a cold resume) or `would_advise` (after an idle gap) |
| `on`, `warn` | a `systemMessage` with the cost; the prompt goes through | `advised` |
| `on`, `ask` | the first prompt after a cold resume is stopped once, with the cost, two options and a note; after an idle gap, a warning as above | `blocked`, then `overridden` or `cleared` |

**When it acts.** It acts at most once per session, even across a resume: an empty marker file named by the hashed
session key, kept 30 days, records that it did. It acts in two cases:

- **The first prompt after a cold resume.** At `SessionStart` with `source` `resume` or `fork`, Claude Code says
  whether the cache has likely expired (`prompt_cache_likely_expired`). It also gives the tokens the first request
  will re-send (`context_tokens`) and its own cost estimate (`estimated_cache_write_usd`). `session_reset` arms the
  guard with those numbers, and the next prompt is checked.
- **A prompt after an idle gap** in the same session. The newest main-conversation request in the transcript must
  be older than the cache lifetime in effect. The lifetime comes from the status line's report (`prompt_cache.ttl`,
  from the spool) when there is one. Otherwise it comes from that request's 5-minute or 1-hour cache write. With
  neither, it is one hour, the longer one, so the guard calls a cache cold late rather than early. The rebuild size
  is that request's input, cache-read and cache-write tokens. This case is **only ever warned about**, even in
  `ask` mode: `UserPromptSubmit` also fires for turns Claude Code starts itself (a `/loop` iteration, a scheduled
  task, a background subagent reporting back, a message from another session), and nothing in the input says
  which a prompt is.

**What it shows.** Each figure is labeled:

- The tokens.
- About $X at API prices: Claude Code's estimate after a resume, Trove's own (pricing table, at the cache lifetime
  in effect) after an idle gap.
- The share of your 5-hour and weekly limits, with its 90% range and basis ("estimated from API prices" or
  "estimated from your meter", see [limit-fit.md](limit-fit.md)). With no limit fit yet, no share is shown.

In `ask` mode the reason offers two options:

- Send the prompt again to continue. Claude Code shows it under `Original prompt:`, and Trove will not stop the
  session again.
- `/clear`, which costs nothing, then paste the note.

The note holds your last three typed prompts (300 bytes each), up to ten edited file names and the end of the last
reply. It is at most 1,500 bytes, built in memory from the transcript's last 1 MiB, and is only ever in the hook's
reply: nothing writes it to disk (rule 7). Tool output, lines Claude Code wrote itself, slash-command wrappers and
subagent text are left out.

**Before `ask` can stop anything**, the installed Claude Code version must pass `trove hooks verify --prompt-block`
in your own session. It stores a `UserPromptSubmit` entry with `block: yes` in the capability record. Until then
`ask` acts as `warn`. The reason: nothing documented lets a hook tell an unwatched `claude -p` run from an
interactive session, and a stopped prompt there would end the run. The check needs three things:

- You saw the first prompt stopped.
- The hook saw `CLAUDE_CODE_ENTRYPOINT=cli` in the interactive session.
- It saw a different value under `-p`.

The variable is observed, not documented, and the check records it as such. At run time the guard stops a prompt
only when the variable says `cli`. Run the check again after updating Claude Code (`trove doctor` warns when the
recorded version differs).

**What it costs.** Nothing reaches the model on its own: a block's `reason` is "not added to context" (verified),
and a warning is a `systemMessage`, "shown to the user". Whether a `systemMessage` also reaches the model is not
documented, and `trove hooks verify --prompt-block --saw-system` records the answer. The note costs its tokens
once, if you paste it (at most about 450, estimated); the registry declares 450 per use and `trove report` subtracts
it from every cleared session.

The hook reads the snapshot and the session journal, and the transcript's tail from the end, parsing only the newest
lines. Measured on the Windows dev machine (`TestColdResumeTiming`): a warm prompt passes in p50 2.2 ms, p95 5 ms
inside the process. Acting on a 1 MiB transcript, note included, takes p95 65 ms. The safety net is 250 ms and the
install sets a 5 s timeout (the event's default is 30 s). Starting the `trove` process adds its usual cost to every
prompt, even with the guard off (see "How it stays fast").

**Guardrail** `cold_resume_override`: of the prompts it stopped, the share you sent again. The guard goes back to
shadow when that share is above 50% beyond chance, with at least 20 blocks (same sequential test as the rules
above). Stopping a prompt you then send anyway did not help you. The 50% is a placeholder, not set by a benchmark.
`trove report` shows what followed: see [measurement.md](measurement.md#cold-resume-guard).

## Routine steer

`hook_routine_steer` (B24, T2, added 2026-11-24) cuts thinking on turns that need little. When you send a routine
prompt such as "yes", "continue", "run the tests" or "commit", it adds the line "Answer directly without
deliberating." to that prompt's context through `UserPromptSubmit` `additionalContext`. It runs in the
`user_prompt_cold_check` hook, after the cold-resume guard. If the guard stops the prompt, the steer adds nothing.

```toml
[optimizers]
hook_routine_steer = "shadow"   # off (default) | shadow | on
```

**What was verified, and where (read 2026-11-24).**
- platform.claude.com/docs/en/build-with-claude/adaptive-thinking (served as "Steering thinking"),
  `/build-with-claude/effort` and `/build-with-claude/prompt-caching`:
  - On Opus 5.5 "Adaptive thinking is always on and can't be turned off, so effort is the primary control." The same
    holds for Fable 5.2. Haiku 5.5's thinking is "on by default" and can be disabled at high effort or below. The
    page makes no always-on statement for Sonnet 5.5.
  - "The resolved effort value is rendered into the prompt, so changing it between requests can invalidate cache
    breakpoints." This is why the steer never touches effort.
  - Per-message steering, appending "Answer directly without deliberating.", works "without touching the system
    prompt or changing any request parameters between turns" and leaves "earlier cache breakpoints intact". The page
    adds: "treat it like any other prompt change: measure before you ship" and "Steering Claude to think less often
    might reduce quality on tasks that benefit from reasoning."
  - "On Claude Haiku 5.5 and Claude Fable 5.2, the discouraging phrase doesn't reliably reduce thinking." Asking
    Sonnet 5.5 to think less in the system prompt is not reliable either, and nothing is said about the
    per-message phrase on Sonnet.
- code.claude.com/docs/en/hooks and `/hooks-guide`:
  - `additionalContext` is "String added to Claude's context alongside the submitted prompt". It must sit inside
    `hookSpecificOutput`, or it is "silently ignore[d]".
  - Claude Code wraps it in a system reminder "at the point where the hook fired". It is saved in the transcript and
    replayed on resume rather than recomputed. Strings over 10,000 characters are replaced by a file path.
  - The `UserPromptSubmit` input has `prompt` but no model. Only `SessionStart` sends `model`, and `PostModelSwitch`
    sends `to_model`.
  - Neither page says outright that the text lands inside the new user turn, after the cached prefix.

**Models.** Only `claude-opus-5-5`, including its `[1m]` variant, is steered. The model is pinned at every
`SessionStart` and updated at every `PostModelSwitch`, whatever started the switch, as a fixed word. A session on
any other model, or one whose model Claude Code did not send, is left alone silently: nothing is recorded and the
prompt goes through untouched (rule 4). Sonnet 5.5 is added only if a trial of its own shows the phrase works there.

**What counts as routine.** `internal/routine` decides, in memory, from the prompt text. It is a pure function, so a
replayed prompt gets the same label. A prompt is routine only when all of it, after normalising case, spacing and
end punctuation, is a sequence of listed phrases: confirmations, "continue" and the like, running tests, builds,
linters and formatters, and committing or pushing. Each of these makes a prompt not routine:
- a qualifier ("yes but fix the race first");
- a question mark, a number, a path, code or markup, or a slash command;
- a second line;
- more than 8 words or 80 bytes.

The classifier also reads a prompt as a correction ("no", "try again", "that didn't work", "you forgot ..."). The
prompt is never stored, logged or hashed; only the label leaves memory.

**Cache safety.** The steer changes no request parameter and touches nothing already sent. Its text goes beside the
new prompt, at the end of the conversation, and stays in the transcript, so every later turn's prefix is byte for
byte the one before. Because the documentation does not say this outright, `on` and the trial's "on" arm only record
`would_steer` until `trove hooks verify --prompt-context` passes on the installed Claude Code version.

That check runs in a session you start yourself. The test hook adds a marker plus about 300 tokens of padding to the
second of four short prompts. It passes only when all of these hold:
- the text sits after turn 1's last request and before turn 2's first in the transcript;
- turn 2's first request read all of turn 1's prompt from the cache (within 50 tokens), so adding the text broke
  nothing;
- turn 3's first request read all of turn 2's prompt, text included, so the text stays put.

The test hook records line positions and token counts only. Shadow mode works without the check.

**Overhead (rule 15).** At most about 30 tokens per steered prompt (estimated): the 37-byte phrase plus up to 68 bytes
of Claude Code's wrapper, at 3.5 bytes per token. Each `steered` event is charged that figure. Every later request of
the session carries the line again as cached input, and that cost is inside what the trial measures.

**Modes.**
- `shadow` counts routine prompts on Opus 5.5 (`would_steer`) and changes nothing.
- `on` steers every routine prompt on Opus 5.5 once the live check has passed. It is an explicit opt-in.
- The trial (`trove autopilot trial hook_routine_steer`) needs all of these to start:
  - the steer in `shadow`;
  - the live check passed;
  - at least 20 routine prompts counted;
  - A/B on;
  - your yes to a consent text.

  Each new session then gets an arm from a hash of its key. The "on" arm is steered. The "off" arm only counts its
  routine prompts, so the two arms are compared turn for turn.
- The kill switch and a guardrail rollback apply at once. A state change waits for the next session (rule 5).

**Events** (`hook_events`, migration 0028):
- `steered` and `would_steer`;
- `turn_end`: the next prompt after a routine one, which closes the routine turn;
- `reprompted`: that next prompt read as a correction.

**Outcome and guardrails.**
- **Outcome:** thinking tokens per routine turn (measured). These are the `thinking_tokens` the history parser reads
  from Claude Code's log, summed over the main-conversation requests between a routine prompt and the next prompt.
  A turn with no thinking figure is left out, not counted as zero. Each session contributes its mean.
- **Share of the limits:** the difference is also shown as a share of the 5-hour and weekly limits per routine turn,
  from the limit fit (estimated, as output tokens on Opus 5.5).
- **Verdict:** the sequential, always-valid test the rollback checks use, tuned for a 25% effect.
- **Stops (`rolled_back`):**
  - the test_fail_rate guardrail;
  - sessions coming out worse in the hook trial's comparison, which includes turns per session and tokens per
    session;
  - more thinking per routine turn with the steer;
  - the re-prompt guardrail: corrections after a steered turn beyond the off arm's rate plus 10 points, at every
    look.
- **Ends (`off`):**
  - switching the steer on for everyone;
  - the live check no longer passing.
- **Never turns the steer on:** a clean result reports the figures, and you decide. T2 needs a benchmark verdict
  before Autopilot may switch anything on.

## Model-switch and cold-compact guards

Two more actions make the next request reprocess the whole context. Both guards use the cold-resume guard's
thresholds (`optimize.cold_resume_min_tokens`, `optimize.cold_resume_min_week_pct`) and its modes. Both are pinned per
session, act once per session, are never turned on by Autopilot, and are measured with their own guardrail.

```toml
[optimizers]
hook_model_switch = "shadow"   # off (default) | shadow | on
hook_cold_compact = "shadow"   # off (default) | shadow | on
```

**`hook_model_switch` (B22).** "Each model has its own cache", so switching a session to another model makes the next
request write the whole context again.
- **When it acts:** on `PreModelSwitch` with `prompt_cache_warm` true, a different `to_model`, and `context_tokens` at
  or above the threshold (or the weekly share's low end at or above its threshold).
- **`on`:** a `systemMessage` (at most 420 bytes) with the tokens, Claude Code's own `estimated_cache_write_usd` and
  the share of the 5-hour and weekly limits (estimated, with range and basis).
- **Never a decision:** Claude Code already asks you to confirm a switch while the cache is warm, and that
  confirmation decides. The guard never answers `allow`, `deny` or `ask`, because `"ask"` is a refusal outside an
  interactive `/model`.
- **Speed:** a `PreModelSwitch` hook that times out blocks the switch. The guard reads no transcript and runs under
  the 150 ms safety net, with a 5 s timeout set. Measured on the Windows dev machine: p95 39 ms
  (`TestModelSwitchTiming`).
- **Outcome:** `post_model_switch` records `overridden` when the switch went ahead (not for `auto` or `resume`
  switches). Guardrail `model_switch_override`: rolled back to shadow when more than 50% of warnings are followed by
  the switch beyond chance, with at least 20 warnings (a placeholder).
- **opusplan:** plan-mode toggles under the `opusplan` setting are not warned about (whether they reach
  `PreModelSwitch` is not documented). `trove report` shows a finding instead when your `model` setting is
  `opusplan`: the Opus↔Sonnet switches in your history, the tokens they rebuilt, and their cost and share of the
  limits. A `/model` switch between the same families looks the same in the log, so it is an upper bound.

**`hook_cold_compact` (B23).** After the cache expired, a `/compact` sends the whole history to be summarized "as
uncached input".
- **When it acts:** on a manual `/compact` (`trigger` `manual`) when the newest main-conversation request is older
  than the cache lifetime in effect (as for the idle case above) and its prompt tokens reach the thresholds.
- **`on`:** exits 2 with a message on stderr, which is shown to you for a manual `/compact`. The message gives the
  cost (priced as uncached input), says "type /compact again to compact anyway", or offers `/clear` and the same
  in-memory note as the cold-resume guard.
- **Second `/compact`:** goes through and records `overridden`. A `/clear` while the stop is pending records
  `cleared`.
- **Never `auto`:** blocking an auto compaction that recovers from a context-limit error fails the request.
- **No print-mode gate:** a stopped manual compaction is only skipped, and the session goes on.
- **The one non-zero exit:** this is the only hook that exits non-zero on purpose. Every error, panic and the 250 ms
  safety net still end in exit 0, and without a stderr to write to it never stops anything. Guardrail
  `cold_compact_override`, same rule as above.

## Measurement

Each decision is one row in `hook_events`: hook, kind, bytes, estimated tokens, hashed session key.

| Kind | Meaning | Label |
|---|---|---|
| `blocked` | the read was denied (for `hook_cold_resume`: a prompt was stopped) | measured count |
| `would_block` | shadow mode: it would have been denied (or stopped) | measured count |
| `advised` / `would_advise` | a hint was added / would have been | measured count |
| `reread_anyway` | the agent retried a denied read | measured count |
| `rewritten` / `would_rewrite` | an input or output was changed / would have been | measured count |
| `reissued` | the agent ran a quieted command again as first written | measured count |
| `fetched_back` | the agent fetched back an output a hook trimmed | measured count |
| `extra_read` | the agent read more of a file after `hook_read_limit` limited a read of it (once per file) | measured count |
| `adopted` | the agent used `trove_run` (once per session) | measured count |
| `overridden` | you sent a prompt again after the cold-resume guard stopped one | measured count |
| `cleared` | the session ended with `/clear` after the cold-resume guard stopped a prompt | measured count |

For `hook_cold_resume` the tokens column holds the rebuild size: Claude Code's `context_tokens` after a resume,
the last request's prompt tokens after an idle gap.

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

- Model switches and compaction, for the rebuild guards (read 2026-11-24 at `code.claude.com/docs/en/hooks`,
  `/prompt-caching` and `/model-config`):
  - `PreModelSwitch` input: `from_model`, `to_model`, `requested_model`, `source` (`"command"` for `/model <name>`,
    the Model setting in `/config` or turning on fast mode; `"picker"`; `"sdk"`), `context_tokens` (the last
    response's input, cache read, cache creation and output tokens; `0` before the first response),
    `prompt_cache_warm`, `cache_ttl` (`"5m"` or `"1h"`), `estimated_cache_write_usd` ("on `to_model` at the
    `cache_ttl` rate, excluding the next response") and `pricing`. The matcher compares the canonical name of
    `to_model`.
  - It answers `permissionDecision` `allow`, `deny` or `ask`. "Only `/model` in an interactive session can show the
    `"ask"` prompt. On every other surface, including non-interactive mode with the `-p` flag, `/config`, and
    `set_model` requests, Claude Code treats `"ask"` as a refusal." "Claude Code shows the user any `systemMessage`
    your hook returns regardless of the decision." Claude Code already "asks you to confirm the switch only while
    the cache is still warm and the new model isn't the one that produced the last response" (prompt-caching).
  - Its default timeout is 30 s, and "A PreModelSwitch hook that doesn't respond before its timeout blocks the
    switch." An exit code other than 0 or 2 without JSON lets the switch go ahead.
  - `PostModelSwitch` gets the same fields and two more sources, `"auto"` and `"resume"`. It fires for "A setting such
    as `opusplan` entering or leaving plan mode" and cannot block. Whether `PreModelSwitch` fires for those toggles is
    not stated. "each plan-mode toggle is a model switch and starts a fresh cache" (prompt-caching).
  - `PreCompact` input: `trigger` (`manual` for `/compact`; `auto` for the auto-compact window and idle compaction)
    and `custom_instructions`. No token or cache field. "Exit with code 2 to block compaction. For a manual
    `/compact`, the stderr message is shown to the user." It can also block with JSON `decision: "block"`; whether
    that reason is shown is not stated, so Trove uses exit 2. Its `systemMessage` and `continue` are discarded. A
    blocked auto compaction is skipped when proactive, but when it was "triggered to recover from a context-limit
    error … the current request fails", so Trove never blocks `auto`. Default timeout 600 s; what a timeout does is
    not stated.
  - Compaction while the cache is cold: "the summarization request reprocesses that history as uncached input"
    (prompt-caching). `PostCompact` gets `trigger` and `compact_summary` and has no decision control.

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

### Live check of the routine steer (manual)

Not run yet. Use a throwaway data dir.

1. `TROVE_DATA_DIR=/tmp/trove-probe trove hooks verify --prompt-context` and follow its steps: one session on Opus
   5.5, four short prompts within a few minutes. Record the result, the version and the four token figures here. If
   `context` is not `yes`, stop: the steer records only on that version.
2. Check that the finished check says the session's model was one the steer acts on. If it does not, SessionStart's
   `model` field is not in the form the allowlist expects, and the steer would stay off silently.
3. Set `hook_routine_steer = "on"`, `trove hooks install`, start a session on Opus 5.5 and send "yes" after a reply
   that asks a question. Check that the transcript has the line after the prompt, that the next turn's
   `cache_read_input_tokens` covers the previous prompt, and that `hook_events` has `steered` then `turn_end`.
4. Switch to Haiku 5.5 with `/model` and send "yes": nothing is added and nothing is recorded.

### Live check of the cold-resume guard (manual)

Not run yet. It needs a Claude Code version that passes `trove hooks verify --prompt-block`, a large session and a
wait longer than its cache lifetime. Use a throwaway data dir.

1. `TROVE_DATA_DIR=/tmp/trove-probe trove hooks verify --prompt-block` and follow its steps (one interactive
   session, one `claude -p` run). Record the result and the version here. If `block` is not `yes`, stop: `ask`
   stays a warning on that version.
2. Set `hook_cold_resume = "on"` and `cold_resume_action = "ask"`, `trove hooks install`, then build a session of
   at least 150,000 tokens and exit.
3. After more than the cache lifetime (one hour on a subscription), `claude --resume` it and send a prompt. Check:
   it is stopped once, the reason shows the tokens and the costs with their labels, and the note reads sensibly.
   Note whether the prompt went back to the input box (not documented).
4. Send it again: it goes through, and `hook_events` has `blocked` then `overridden`. Repeat with a `/clear`
   instead: `blocked` then `cleared`.
5. `claude -p --resume <id> "continue"` on a cold session: it must go through with no block.

## Known gaps

- The routine steer has not run in a real session yet (see the live check above). Some details rest on transcripts
  as they are observed to be, not as documented:
  - where Claude Code writes the `additionalContext` line in the transcript;
  - that `thinking_tokens` is in the log for Opus 5.5.

  Without `thinking_tokens` in the log the trial cannot measure anything and keeps running until its time limit.
  `UserPromptSubmit` also fires on turns Claude Code starts itself (a `/loop` iteration, a subagent reporting back);
  their text is never routine, so they only close a routine turn. A routine prompt that Claude Code expands (a
  custom slash command) is never seen as routine, because it starts with a slash. The correction list is English
  only, so re-prompts in other languages are not counted.

- The model-switch guard cannot tell a plan-mode toggle from a `/model` switch, and the opusplan finding counts both.
  The cold-compact guard's session_reset neighbour still forgets the session's reads when a `/compact` is stopped,
  so a later repeat read is allowed (never a wrong block). Neither guard has been tried in a real session yet:
  check that the cost appears beside Claude Code's switch confirmation, that a cold `/compact` is stopped once and
  goes through the second time, and whether `PreModelSwitch` fires on an opusplan toggle.
- The cold-resume guard never stops a prompt after a mid-session idle gap, only warns, because self-started turns
  look the same. After a block, a turn Claude Code starts itself would count as `overridden`. The first prompt
  after a resume could itself be a self-started turn (for example a message from another session); it is stopped
  once like a typed one. Whether `--resume` keeps the session id is not documented, so the once-per-session marker
  is kept for 30 days by hashed key. The hour-after comparison in `trove report` is not randomized.

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
