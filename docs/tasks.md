# Delegation subagents: scout, routine agents, cascade (internal/tasks)

Opt-in delegation features for Claude Code projects, installed with
`trove tasks plan|apply scout|agents|explore|cascade|subagent-model` and removed with
`trove tasks remove <feature>`. Files are written only through the
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

## Model per subagent

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

## Routine agents

`trove tasks plan|apply agents` moves mechanical work into subagents on a cheaper model at low effort. The main
conversation keeps its own model and effort level, and the subagent's output stays out of its context.

| File | What it does | Tools | Model, effort | Turn limit |
|---|---|---|---|---|
| `.claude/agents/trove-test-runner.md` | Runs the tests once and reports pass or fail. Never edits files | Bash, Read, Grep, Glob | `routine_model` (haiku), `routine_effort` (low) | 15 |
| `.claude/agents/trove-lint-fixer.md` | Runs the formatter, then the linter. Applies only fixes that cannot change what the code does, and lists the rest | Bash, Read, Edit, Grep, Glob | the same | 20 |
| `.claude/skills/trove-test/SKILL.md` | `/trove-test`: forks into `trove-test-runner` | (the agent's) | (the agent's) | |
| `.claude/skills/trove-lint/SKILL.md` | `/trove-lint`: forks into `trove-lint-fixer` | (the agent's) | (the agent's) | |

- **Return format.** Each agent's reply starts with `STATUS: done`, `STATUS: failed` or `STATUS: unsure`,
  followed by at most 8 short lines with no pasted output. The test runner adds `TESTS: <n> passed, <n> failed`
  and one line per failing test. The scout uses the same format.
- **Commands.** With `test_cmd` and `lint_cmd` under `[tasks]` (or `--test-cmd`, `--lint-cmd`), the commands are
  written into the agents. Without them, the agents look in the README, Makefile, package.json, go.mod or
  CLAUDE.md, and reply `STATUS: failed` if they find nothing.
- **Skills.** The skills set `context: fork` and `agent:` and no `model` or `effort` of their own. Without a
  fork, a skill's `model` and `effort` would apply to the main conversation for the rest of its turn.
  `disable-model-invocation: true` keeps the model from loading them on its own; you run them as `/trove-test`
  and `/trove-lint`. The agents' own descriptions ask the main agent to use them proactively.
- **The scout** now has `effort: low` and the status format too. An installed scout picks this up on the next
  `trove tasks apply scout`.

### Explore override (separate opt-in)

`trove tasks plan|apply explore` writes `.claude/agents/trove-explore.md` with `name: Explore`. It replaces
Claude Code's built-in Explore in this project with one that has read-only tools (Read, Grep, Glob), runs on
`routine_model` at `routine_effort`, and replies in the status format. The built-in Explore runs on the main
conversation's model (on Opus when the main model is Fable). The file is not offered when you already have your
own Explore in the project's or your user agents folder. Trove's version has no Bash, which the built-in may use
for read-only commands.

### Overrides and environment

- `[tasks]` in config.toml: `routine_model`, `routine_effort`, `scout_effort`, `lint_cmd`. The flags
  `--routine-model`, `--routine-effort`, `--scout-effort` and `--lint-cmd` win over config. Values are checked
  against the catalog.
- `CLAUDE_CODE_SUBAGENT_MODEL_FORCE` blocks the install, because Claude Code then ignores every subagent's
  model.
- `CLAUDE_CODE_EFFORT_LEVEL` wins over a subagent's `effort`. The plan says so, the install still goes ahead
  (the cheaper model still applies), and the report shows the effort each request actually ran at.
- `maxEffortLevel` (managed settings) caps effort. That does not affect `low`.
- Effort works only on models that support it. Haiku 5.5 does, and needs Claude Code v2.1.293 or later. On a
  model without effort support, Claude Code ignores the field.
- A file you wrote is never overwritten. `trove tasks remove agents|explore|scout|cascade|subagent-model` undoes
  the recorded changes of that feature in this project. A file edited after Trove wrote it is left alone and
  reported. Removing is refused while a session looks active, so a session never loses a subagent partway
  through.

### Measuring per agent

The `subagent_record` hook (Claude Code `SubagentStart` and `SubagentStop`, added by `trove hooks install` and the
plugin) records one `subagent_runs` row per run. A run has the hashed agent id, which joins the run to its requests
in the history (`history_requests.agent_key`). The agent type is stored as Trove's own names and the built-in
names; any other name is stored as `user`. The status is the STATUS word of the reply, read in memory. The reply
itself is never stored.

`trove report` ("Per agent", `agents` in `--json`) shows, for each agent type:

- tokens and output tokens per run (measured, from the agent's logs);
- thinking as a share of output, over requests whose log reports thinking (measured);
- the mean run as a share of the 5-hour and weekly limits (estimated from the limit fit; absent without one);
- requests by the effort level the log recorded, which shows whether `effort: low` took effect;
- runs by status, the failure rate (failed or unsure over runs with a known status), and how many failed or
  unsure runs were followed by a general-purpose run in the same session (re-delegation).

**Before and after** compares runs of the same agent type before and after the install. This is observational, not
an A/B: anything else that changed in that time is mixed in. Explore and the scout have a "before" (the built-in
Explore, the scout before its effort line). The routine agents did not exist before, so they show "after" only.

**Session A/B.** With `[experiment] enabled`, each new session is assigned an arm, as for the scout. In the off arm,
`pre_tool_subagent` declines calls to `trove-test-runner` and `trove-lint-fixer`, so the main agent does the work
itself, as it did before the install. For the Explore override, the off arm sets the call's per-invocation model to
the main conversation's model alias. The built-in Explore cannot be restored while the project file exists, so this
arm still has low effort and the read-only prompt, and the comparison measures the model change only. The results go
through `experiment.AnalyzeHookTrial` (tokens per session, turns, tool errors, intention to treat) and its rollback,
which Autopilot reads.

**Guardrails.**
- Re-delegation: each routine agent's description tells the main agent not to retry it after `failed` or
  `unsure`, and to do the work itself or use general-purpose.
- Failure rate: above 25% of runs with a known status, once there are at least `[measure] min_sessions` such runs,
  the report suggests `trove tasks remove agents` or a stronger `routine_model`.
- The trial's rollback stops the off-arm control. It cannot delete files, which needs your consent, so the report
  says what to run.
- The test-pass signal (`post_tool_test_result`) and the trial's tool-error and turn counts apply as for every hook
  trial.

### Verified for this section

Read as raw page text on 2026-11-24:

| Detail | Source |
|---|---|
| Subagent `effort`: "Overrides the session effort level, but not the `CLAUDE_CODE_EFFORT_LEVEL` environment variable. Options: `low`, `medium`, `high`, `xhigh`, `max`; available levels depend on the model." | sub-agents.md, Frontmatter reference |
| A subagent with no `effort` runs at `CLAUDE_CODE_EFFORT_LEVEL`, then its spawner's or the turn's level, then `CLAUDE_CODE_SUBAGENT_EFFORT`; otherwise the session level applies only on the main model | sub-agents.md, "When a subagent has no effort field or parameter" |
| The per-invocation `effort` parameter needs v2.1.292 or later. The hooks page's Agent input table does not list it, so Trove does not write it | sub-agents.md, hooks.md |
| `maxEffortLevel` caps effort on the client; models not in the effort table do not support effort; Haiku 5.5 needs v2.1.293 | model-config.md |
| "A user or project subagent named `Explore` overrides the built-in and keeps its own `model` field" | sub-agents.md, Explore |
| Same-name priority: managed, `--agents`, `.claude/agents/`, `~/.claude/agents/`, plugin | sub-agents.md |
| Skill `model` and `effort` apply "for the rest of the current turn"; with `context: fork` they set the forked subagent's values. `context`, `agent`, `disable-model-invocation`; project skills at `.claude/skills/<name>/SKILL.md` | skills.md |
| `SubagentStart`: `agent_id`, `agent_type`. `SubagentStop`: also `agent_transcript_path`, `last_assistant_message`; with `SubagentHandback` (v2.1.271+), that field is only the closing text | hooks.md |

Not verified: the default agent of a forked skill when `agent` is omitted (summary only), so Trove always sets
`agent`. A transcript's `agentId` has no `agent-` prefix (seen on 2026-11-24 in local transcripts), while the hooks
page shows ids both with and without it. Trove strips the prefix before hashing. A live check that the hook's id
equals the transcript's is still to do.

## Safety

- Only `trove-*.md` directly inside the project's `.claude/agents` or
  `.claude/commands`, and `SKILL.md` directly inside a `trove-*` folder in the project's `.claude/skills`, are
  writable. A file with the same name that Trove did not
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

- **Routine agents: no benchmark yet.** Whether Haiku at low effort runs tests and fixes lint as reliably as the
  main model is not benchmarked. The failure rate and the session A/B are the only evidence, and no saving is
  claimed. As T3, Autopilot never installs files; it only reads the trial.
- **Status is often unknown on newer Claude Code.** With `SubagentHandback` (v2.1.271+), `last_assistant_message`
  holds only the closing text, so a STATUS line in the handed-back report is not seen. The failure rate then
  covers fewer runs.
- **Skills are not controlled in the A/B.** `/trove-test` and `/trove-lint` fork without an Agent tool call, so the
  off arm cannot decline them. Using them in an off session dilutes the comparison, as with the cascade.

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
  outbound network call, and PRINCIPLES.md rule 1 says any other outbound call needs
  a new rule. Proposed rule for approval: *the scout's summarizing step may call
  a model server on a loopback address (127.0.0.1 or ::1) that the user
  configured, off by default, never in the gateway request path, sending only
  what the user's own agent already read; its quality effect is measured
  separately.* No network code exists for it.
