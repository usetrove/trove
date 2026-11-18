# Scout subagent and cascade workflow (internal/tasks)

Two opt-in delegation features for Claude Code projects, installed with
`trove tasks plan|apply scout|cascade`. Files are written only through the
Fixer: diff, approval, backup, `trove undo`, and queued while a session looks
active. Nothing is on by default.

## Verification log (rule 8)

Read as raw page text on 2026-11-05 (append `.md` to a code.claude.com docs URL;
the fetch tool's summaries are not evidence). Claude Code 2.1.318 is pinned in
`bench/`. The machine-readable form is `internal/tasks/catalog/claude_code.toml`;
a field not marked `verified = "verbatim"` there is never written.

| Detail | Source | Result |
|---|---|---|
| Subagent files live in `.claude/agents/` (project), `~/.claude/agents/` (user), a plugin's `agents/` | sub-agents.md | verbatim |
| Frontmatter: `name`, `description` required; `tools`, `disallowedTools`, `model`, `maxTurns` (needs 2.1.246+), others optional. Unknown fields are ignored without an error | sub-agents.md | verbatim |
| `model`: `sonnet`, `opus`, `haiku`, `fable`, a full model ID, or `inherit` | sub-agents.md | verbatim |
| `tools` omitted means every tool is inherited, so Trove always sets it | sub-agents.md | verbatim |
| The main agent delegates from the `description` text; "use proactively" encourages it | sub-agents.md | summary-level; wording is ours, behavior is measured, not assumed |
| `CLAUDE_CODE_SUBAGENT_MODEL_FORCE` makes Claude Code ignore the `model` field of every subagent | sub-agents.md | verbatim. The cascade is blocked while it is set |
| `.claude/commands/x.md` still works and gives `/x`; `$ARGUMENTS` is replaced by the arguments; the `model:` override of a command lasts one turn | slash-commands.md | verbatim |
| Hooks receive the subagent's name as `agent_type`; `SubagentStart`/`SubagentStop` exist | sub-agents.md | verbatim, **not used yet** (see gaps) |

**Not yet verified:** Codex CLI and OpenCode subagent or model-switching
features. Neither is offered. `trove tasks` writes for Claude Code only.

## The scout

`.claude/agents/trove-scout.md`: `tools: Read, Grep, Glob` (Bash is excluded
because it can write), `model: haiku` by default, `maxTurns: 20`, and a
description that asks the main agent to use it proactively for exploration and
log summaries. A short marked block in `CLAUDE.md` says when to delegate. The
block sits between `<!-- trove:scout -->` markers; text outside them is never
changed.

## The cascade

`/trove-cascade <task>` is a command file plus two subagents
(`trove-worker` on the cheap model, `trove-escalate` on the strong one). The
command tells the main agent to: delegate to the worker; run the project's test
command itself (it does not trust the worker's report); on failure, or when the
worker stopped at its turn limit, write a summary of at most 10 lines (what was
tried, failing test names, first error line each; no code, no history) and hand
it to the strong model; then say in one sentence which model finished and why.

Model switching is done by subagent tiers because a command's `model:` override
lasts one turn only. Because the steps are instructions to the main agent, Trove
cannot guarantee it follows them; it can only measure what happened.

The cascade will not install without `test_cmd` (`[tasks]` in config.toml, or
`--test-cmd`): escalating on a guess is not allowed.

## Model per subagent (plan item 7.3)

`trove tasks plan|apply subagent-model --model <alias or model ID>` adds `model: <model>` to each
subagent file directly in the project's `.claude/agents` that names no model. A file that already
has a model line, including `inherit`, is left alone, and so are Trove's own files. It is blocked
while `CLAUDE_CODE_SUBAGENT_MODEL` or `CLAUDE_CODE_SUBAGENT_MODEL_FORCE` is set. There is no default
model. The edit is a suggestion, backed up and undone with `trove undo`. Overhead: none, because
Claude Code reads the frontmatter and does not send it to the model.

Verified 2026-11-17 on code.claude.com/docs/en/sub-agents.md: when `model` is omitted, Claude Code
uses the per-invocation model, then `CLAUDE_CODE_SUBAGENT_MODEL`, then the main conversation's model.
Each subagent runs in its own context window.

The model line Trove writes has a comment line just above it (`# trove:subagent-model ...`), so
the hooks can tell it from one the user wrote by reading the file; nothing about the file is
stored.

Measurement: `trove report` shows before-and-after figures (`task_subagent_model`), and with
experiments on, the hooks run a session A/B. Each new Claude Code session is assigned an arm by a
hash of its key, as for the scout and cascade. In the "off" arm, a PreToolUse rewrite of an `Agent`
call to a subagent whose model line Trove wrote sets the call's per-invocation `model` to the main
conversation's model, read from the newest main-thread reply in the session transcript, so the
subagent runs as it did before the fix. Anything uncertain goes through untouched: a call that
names a model itself, a subagent the user configured, an unreadable transcript, a model with no
alias, a rolled-back feature.

Checked live on 2026-11-18 on Claude Code 2.1.327 with an API key:
- a PreToolUse `updatedInput` that adds `model: "sonnet"` to an `Agent` call ran the subagent on
  Sonnet even when its file said `model: haiku`, so the per-invocation model wins over the file;
- `"inherit"` and a full model ID (`claude-haiku-5-5`) as the per-invocation model made the call
  fail, so only the family aliases (`sonnet`, `opus`, `haiku`, `fable`) are used. An alias names
  the newest model of its family, so a main conversation on an older model gets that family's
  newest model in the off arm;
- a YAML comment line in the frontmatter does not change how the file is read;
- hook inputs carry no model field in a print-mode session (SessionStart, UserPromptSubmit and
  PreToolUse were logged), which is why the transcript is read.

## Safety

- Only `trove-*.md` directly inside the project's `.claude/agents` or
  `.claude/commands` are writable. A file with the same name that Trove did not
  write (no `<!-- trove:managed -->` line) is never overwritten.
- A damaged `<!-- trove:scout -->` block (opening marker, no close) is left alone.
- Rendering is a pure function of the options, so the same options give
  byte-identical files.
- Rule 13: names are `trove-*`; no provider or product brand.

## Overhead (rule 15)

Declared per file and shown in every plan: the subagent descriptions and the
`CLAUDE.md` block are in every request; the file bodies load only when used.
Estimated at 3.5 bytes per token. `trove report` prints the figures next to the
result so they are charged against it.

## Measurement

`task_installs` (migration 0014) stores the feature, the file's role, the model
aliases and the overhead estimates; never a path or the test command. The report
section "Scout and cascade you installed" compares sessions before and after
the install (as `trove settings` does) and shows:

- delegation: the share of sessions since the install with any subagent request
  (estimated: Trove does not parse which subagent ran);
- the share of those sessions' tokens that ran in subagents (estimated), which
  is what stayed out of the main conversation;
- cost per session, before vs after (measured once each side has
  `[measure] min_sessions`, else "not enough data");
- for the cascade, the share of sessions with subagents on two or more models
  (a sign of escalation) and the model of each session's last subagent request.

A clear cost rise or error rise produces a suggestion to run `trove undo`. It
does not undo anything itself.

## Known gaps

- **The session-level A/B is a partial control.** While a scout or cascade is
  installed, the plugin's hooks assign each new session an arm (a hash of the
  session key, recorded in `experiment_assignments`) and `trove report` compares
  the arms on tokens, turns and tool errors from the agent's history, with
  every assigned session counted (intention to treat). In the off arm a
  `pre_tool_subagent` hook (PreToolUse on `Agent|Task`) declines a call to the
  scout, so the session is a real control. The cascade is started by you from a
  slash command, which no hook can decline, so its off sessions are recorded and
  compared but not blocked; if you run the cascade anyway in an off session the
  comparison is diluted. Both are tier T3 (routing). Needs the plugin hooks or
  `trove hooks install`; without them no arms are assigned and only the
  before/after figures exist, which mix in everything else that changed.
- **No test-pass signal.** Trove does not know whether tests passed. Cost and
  error counts are proxies. A `PostToolUse` Bash handler could record only an
  exit status, but that needs the hook payload verified first.
- **Delegation is not scout-specific**: subagent type is not parsed from logs.
- **The agent may not follow the command.** The cascade is instructions, not
  enforcement; the "which model finished" statement is the agent's own words,
  which Trove does not read.
- **No benchmark comparison yet.** `bench/` has one model per run and no
  cascade condition. Cascade vs strong-only on cost and success needs a
  per-condition model and a fixture where the cheap model fails. It costs API
  money to run, so it has not been run. No savings figure is claimed anywhere.
- **Codex CLI and OpenCode** are not covered (unverified).
- **Local-model offload (Ollama) is deferred.** A call to a local server is an
  outbound network call, and CLAUDE.md rule 1 says any other outbound call needs
  a new rule. Proposed rule for approval: *the scout's summarizing step may call
  a model server on a loopback address (127.0.0.1 or ::1) that the user
  configured, off by default, never in the gateway request path, sending only
  what the user's own agent already read; its quality effect is measured
  separately.* No network code exists for it.
