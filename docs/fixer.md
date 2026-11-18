# trove fix: evidence-based config fixes

`trove fix` proposes edits to your agent's own config files, shows the exact diff,
and changes nothing until you approve. Every changed file is backed up and
`trove undo` restores it byte for byte. All savings shown are **estimated**; an
applied fix gets a **measured** line in `trove report` only after enough sessions.

```
trove fix                      list fixes with estimated savings (changes nothing)
trove fix <id>                 show one fix and its diff
trove fix <id> --apply         apply it (asks first; --yes skips the question)
trove fix --all --apply        apply every ready fix except suggestions (summary, one question)
trove fix --apply-queued       apply fixes that were queued while an agent was active
trove undo <id>                restore one fix exactly
trove undo --all               restore everything Trove changed (setup and fixes)
```

Every command takes `--json`. Under `--json`, `--apply` without `--yes` applies nothing.
`--project <dir>` picks the project (default: the current folder).

## The fixes

| id prefix | kind | what it does | evidence required |
|---|---|---|---|
| `mcp-` | `mcp_prune` | Adds an unused server to `disabledMcpjsonServers` in `.claude/settings.local.json` (your machine only; `.mcp.json` is not edited) | at least `fixer.min_sessions` recent sessions in this project's Claude Code history, none calling `mcp__<server>__*` |
| `ctx-` | `context_slim` | T1. Deletes only provable duplicates: whole sections of `CLAUDE.md` / `.claude/CLAUDE.md` / `AGENTS.md` whose every non-blank line also appears in the README or `docs/`, or that repeat an earlier section of the same file. The evidence lists each deleted line beside the matching source line (`"Deployments" line 7 = README.md line 3: "..."`) | file larger than `fixer.context_min_tokens` (estimated); at least 4 substantial lines for a README/docs copy, 3 for a repeated section |
| `ctxg-` | `context_slim_generic` | **Suggestion, T2 (behavioural).** Deletes generic overview/boilerplate sections (Overview, About, Introduction, License, ...) that hold no rule words, commands or code and are not copies of anything. Nothing proves them redundant, so removing them changes what the agent is told: never part of `--all`, apply it by id | file larger than `fixer.context_min_tokens` (estimated); a whole-word generic heading |
| `cache-` | `cache_breaker_fix` | Deletes lines that state the current date/time or a generated/updated stamp | the line matches the optimizer's volatile-line classifier and says it holds the current time |
| `ttl-`, `ttlsub-` | `cache_ttl_main`, `cache_ttl_subagents` | **Suggestion, T1 (cache_control).** Sets `"promptCacheTtl"` or `"subagentPromptCacheTtl"` to `"5m"` or `"1h"` in `~/.claude/settings.json` (Claude Code 2.1.242 or later; verified on code.claude.com/docs/en/settings-reference, 2026-11-13). Turned into advice when the project's `.claude/settings.json` or `.claude/settings.local.json` sets the same key | the `cache_ttl` suggestion's thresholds: at least 100 requests with cache activity in that bucket, the other lifetime at least 25% cheaper and at least $1 cheaper at list prices over `fixer.lookback_days` |
| `model-` | `model_default` | **Suggestion.** Sets `"model"` in `~/.claude/settings.json`, or `model = ` in `~/.codex/config.toml` for Codex | at least 20 small-task requests, 40% or more of a frontier model's requests |
| `chk-` | `settings_check` | **Suggestion.** Deletes one cost-raising setting you already have in your own Claude Code settings: `DISABLE_PROMPT_CACHING` (and the per-model forms), `ENABLE_TOOL_SEARCH=false`, or a one-hour cache lifetime (`ENABLE_PROMPT_CACHING_1H`, `CLAUDE_CODE_PROMPT_CACHE_TTL`, `CLAUDE_CODE_SUBAGENT_PROMPT_CACHE_TTL`, `promptCacheTtl`, `subagentPromptCacheTtl`). The tool-search fix also suggests setting `"alwaysLoad": true` on just the server that must stay upfront (verified on code.claude.com/docs/en/mcp, 2026-11-17) | the setting is present with the flagged value in `~/.claude/settings.json`, `.claude/settings.json` or `.claude/settings.local.json`; for a one-hour setting also tokens written at the one-hour rate in your Claude Code history |
| `run-` | `retyped_command` | **Suggestion, T1 (config).** Adds a shell command the agent keeps typing out to the project's `.trove/commands.toml` as a named `trove_run` command (`[commands.<name>] run = [...]`), creating the file if needed. Only plain commands (words and `'...'`/`"..."` quoting) are added; a command that needs a shell (pipes, `&&`, redirects, `$`, globs, here-documents, `cd`, `VAR=x`) becomes a manual step, because `trove_run` never runs a shell. Applying it does not let `trove_run` run anything: approve the file with `trove mcp trust` | the same command (spacing ignored, grouped by hash) at least `fixer.retyped_min_count` times in at least `fixer.retyped_min_sessions` sessions of this project's Claude Code history within `fixer.lookback_days`, and at least 20 bytes long; at most 10 per run, most frequent first |
| `snip-` | `mcp_snippet` | **Suggestion, T2 (behavioural).** Appends the fixed MCP tool-preference snippet (`trove mcp snippet`, about 70 tokens) to the project's `CLAUDE.md`, creating it if needed | Trove's MCP server is registered (`trove mcp status`) and `CLAUDE.md` does not already contain the snippet |

Trove never writes new prose and never rewrites a file: edits are text splices that
keep comments, key order, indentation and line endings. A result that fails to
re-parse as JSON/TOML is discarded.

`settings_check` is also never part of `--all`, carries no saving figure (Trove cannot know how many sessions a setting affects), says you may have set it on purpose, and edits only by deleting the one key: formatting, key order and line endings are kept, and a file that is not strict JSON is not edited. Managed settings and your shell are never read or changed.

`cache_ttl_main` and `cache_ttl_subagents` are never part of `--all`: the saving rests on you pausing the way you did in the lookback window. They carry a per-session dollar estimate only when every request in the bucket was on an API key. While one is in place, `settings_check` does not offer to delete the setting it wrote. In `trove report` they are judged on where the cache writes went, not on prompt size: the share of lifetime-split writes at one hour in sessions that started before and after the fix (`cache_lifetime` in JSON). It reads **measured** with at least `measure.min_sessions` sessions with cache writes on each side, and "moved as predicted" when at least 80% of writes after the fix are at the lifetime it set.

`retyped_command` is never part of `--all`: each command is one you choose to let an agent run by name. The estimate counts only the command text the agent stops writing (bytes over about 4 bytes per token, less the `trove_run` call), times how often it was typed, over all sessions in the window; output shortening by `trove_run` is not included, and there is no dollar figure. The command text is read from the history in memory for the run and is never stored. Adoption is measured with the existing `trove_run` counter: the `post_tool_run` hook records an `adopted` event the first time a session calls `trove_run`, and `trove fix` shows how many sessions did so since the first `retyped_command` fix was applied in this project, out of the sessions the hooks saw (`trove_run_adoption` in JSON, measured). Hook events carry no project, so both counts cover all projects. That hook is installed with `trove hooks install --large-output` (or `--rewrite`); without it `trove fix` says use cannot be seen and shows no number.

`mcp_snippet` is never part of `--all`: it changes what the agent is told, so it is applied only by id, which is your consent. Its text is Trove's fixed snippet, never generated prose. It claims no saving. Its overhead is charged instead: about 70 tokens on every request (estimated, bytes over 3.5 bytes per token), and, once applied, that figure times the project's measured requests per session. Adoption is the share of Claude Code's file reads and searches (`Read`, `Grep`, `Glob`) that used `trove_read` or `trove_search`, from the session history, in the `fixer.lookback_days` before the snippet was applied and since (`mcp_snippet_use` in JSON, estimated because it comes from agent logs). History rows carry no project, so adoption covers all projects. It is a before/after comparison, not a randomized A/B, so other changes in the same period are not separated out. `trove undo <id>` removes the snippet and stops the measurement.

`model_default` is never part of `--all`, is turned into advice if `ANTHROPIC_MODEL`
is set, the project sets its own model, or (Codex) the project's `.codex/config.toml` sets one or your config selects a `profile`, and carries no saving estimate, because
Trove cannot know how many tasks a cheaper model would handle as well.

## Safety rules

- **Approval:** nothing changes without `--apply` plus an answer of yes (or `--yes`).
- **No active session:** if the gateway or an agent's history files show activity within
  `fixer.activity_minutes` (default 30), approved fixes are queued in `~/.trove/fixqueue.json`
  (ids and file hashes only) and applied by `trove fix --apply-queued` once idle. A queued fix is dropped if
  its evidence is gone or its file changed after you approved it.
- **Allowlist:** only `CLAUDE.md`, `CLAUDE.local.md`, `AGENTS.md`, `AGENTS.override.md`,
  `.claude/{CLAUDE.md,settings.json,settings.local.json,rules/*.md}`, `.mcp.json`, `.codex/config.toml`, `.trove/commands.toml` in the project, and
  `CLAUDE.md`, `settings.json` in `~/.claude` (or `CLAUDE_CONFIG_DIR`), `AGENTS.md`, `config.toml` in `~/.codex`
  (or `CODEX_HOME`). Symlinks are resolved first.
- **Refused always:** managed/policy files (`managed-settings.json`, `managed-mcp.json`, anything under
  `/Library/Application Support/ClaudeCode`, `/etc/claude-code`, `C:\Program Files\ClaudeCode`, `/etc/codex`),
  and credential files (`.credentials.json`, `auth.json`, `~/.claude.json`, `.env*`).
- **Stale plans:** a file that changed between planning and writing is not written.
- **Undo:** a fix whose file you edited afterwards is left alone (its backup stays recorded);
  undo later changes first or merge by hand.
- **Privacy:** the database records only fix id, kind, agent, a hash of the path, timestamps and the estimates
  (`fixes` table). No path, no file text.

## How results are measured

An applied fix is recorded in the `fixes` table (migration 0007). `trove report` compares sessions that
started before the fix with sessions that started after it, on provider-reported prompt tokens per request.
It says **measured** only with at least `measure.min_sessions` sessions on each side; otherwise it prints
"not enough data yet" next to the estimate. Sessions differ, so a reduction supports the estimate and does
not prove it; when several fixes are in place their effects are mixed and the report says so.
Reverting a fix (`trove undo`) stops it being reported.

This is not wired into the automatic guardrail rollback: a fix that looks harmful is a recommendation to run
`trove undo <id>`, not a silent edit of your files.

## Verified (2026-10-29) and not verified

Verified against official docs:

- Claude Code (code.claude.com/docs/en/{memory,settings,mcp,managed-settings}): CLAUDE.md locations and load order,
  imports, managed paths per OS; settings files and precedence (managed > CLI > local > project > user), strict JSON,
  `model`, `ANTHROPIC_MODEL` wins over files; `.mcp.json` project scope; `disabledMcpjsonServers`; tool search defers
  MCP schemas by default.
- Codex CLI (learn.chatgpt.com/docs/config-file/config-basic, /docs/extend/mcp, /docs/agent-configuration/agents-md):
  `~/.codex/config.toml`, trusted-project `.codex/config.toml`, `[mcp_servers.<name>] enabled = false`, `model` in `~/.codex/config.toml` and precedence (CLI flags > trusted project `.codex/config.toml` > `--profile` > user > cloud > `/etc/codex`), AGENTS.md
  discovery and the 32 KiB `project_doc_max_bytes` default.

Not verified, and what Trove does about it:

- **Claude Code's project folder naming** under `~/.claude/projects` (non-alphanumerics become `-`) is not documented.
  If no folder matches, there is no history and no `mcp_prune` fix is proposed.
- **MCP tool-definition weight per server** is not knowable without connecting. It is measured from your gateway
  traffic: for each request the analyzer splits the provider-reported prompt total across the prompt by byte share and
  records each server's tool-definition tokens (`tool_servers`, server names only). Only calibrated requests count. The
  fix shows the average per request ("measured") times the requests per session, measured from the project's Claude Code
  history (or gateway sessions if there is none). With no gateway data the fix shows no number. Context files and
  changing lines are file bytes over about 4 bytes per token, so they are labeled "estimated".
- **Servers in `~/.claude.json`** (user and local scope) are not read: the file holds account and OAuth state that Trove
  must not handle. Use `/mcp` to disable those.
- **Codex MCP usage** is not detected: Codex's official docs (checked 2026-10-29) do not say how MCP tool calls are named in
  session files, and Trove does not guess. Codex servers get no `mcp_prune` fix; the TOML splice helper exists but is not yet used by a detector.
- **Codex model default** is proposed from Codex history rows, whose parser is unverified on real installs. Trove cannot see CLI flags or whether a project is trusted, so a project `model` is reported as advice either way.
- **Bash commands in Claude Code history**: the hook input names the command `tool_input.command` (verified on
  code.claude.com/docs/en/hooks, 2026-11-17); the history file reuses the tool's input, but its format is not
  documented. If the field is missing, no `retyped_command` fix is proposed.
- Token counts for context files are bytes / 4, an estimate.
- Managed settings in effect cannot be seen by Trove; a managed `model` would override the `model_default` edit.

## Config

```toml
[fixer]
min_sessions = 5          # sessions seen before a server is called unused (3 to 1000)
lookback_days = 30
context_min_tokens = 1500
activity_minutes = 30
retyped_min_count = 5     # times a command was typed before it is offered (2 to 1000)
retyped_min_sessions = 3  # in at least this many sessions (1 to 1000)
```
