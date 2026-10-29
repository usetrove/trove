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
| `ctx-` | `context_slim` | Deletes whole sections of `CLAUDE.md` / `.claude/CLAUDE.md` / `AGENTS.md` that repeat the README or `docs/`, are generic overview/boilerplate headings with no rules or commands, or repeat an earlier section | file larger than `fixer.context_min_tokens` (estimated) |
| `cache-` | `cache_breaker_fix` | Deletes lines that state the current date/time or a generated/updated stamp | the line matches the optimizer's volatile-line classifier and says it holds the current time |
| `model-` | `model_default` | **Suggestion.** Sets `"model"` in `~/.claude/settings.json` | at least 20 small-task requests, 40% or more of a frontier model's requests |

Trove never writes new prose and never rewrites a file: edits are text splices that
keep comments, key order, indentation and line endings. A result that fails to
re-parse as JSON/TOML is discarded.

`model_default` is never part of `--all`, is turned into advice if `ANTHROPIC_MODEL`
is set or the project sets its own model, and carries no saving estimate, because
Trove cannot know how many tasks a cheaper model would handle as well.

## Safety rules

- **Approval:** nothing changes without `--apply` plus an answer of yes (or `--yes`).
- **No active session:** if the gateway or an agent's history files show activity within
  `fixer.activity_minutes` (default 30), approved fixes are queued in `~/.trove/fixqueue.json`
  (ids and file hashes only) and applied by `trove fix --apply-queued` once idle. A queued fix is dropped if
  its evidence is gone or its file changed after you approved it.
- **Allowlist:** only `CLAUDE.md`, `CLAUDE.local.md`, `AGENTS.md`, `AGENTS.override.md`,
  `.claude/{CLAUDE.md,settings.json,settings.local.json,rules/*.md}`, `.mcp.json`, `.codex/config.toml` in the project, and
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
  `~/.codex/config.toml`, trusted-project `.codex/config.toml`, `[mcp_servers.<name>] enabled = false`, AGENTS.md
  discovery and the 32 KiB `project_doc_max_bytes` default.

Not verified, and what Trove does about it:

- **Claude Code's project folder naming** under `~/.claude/projects` (non-alphanumerics become `-`) is not documented.
  If no folder matches, there is no history and no `mcp_prune` fix is proposed.
- **MCP tool-definition weight per server** is not knowable without connecting. The estimate comes from your gateway's
  `unused_tool_schemas` findings; with no such data the fix shows no number. With tool search on, real savings may be
  lower than the estimate.
- **Servers in `~/.claude.json`** (user and local scope) are not read: the file holds account and OAuth state that Trove
  must not handle. Use `/mcp` to disable those.
- **Codex MCP usage** is not detected: the Codex history format is unverified and has no documented MCP tool-name
  convention. Codex servers get no `mcp_prune` fix; the TOML splice helper exists but is not yet used by a detector.
- **Codex model default** is not proposed (Codex history is unverified).
- Token counts for context files are bytes / 4, an estimate.
- Managed settings in effect cannot be seen by Trove; a managed `model` would override the `model_default` edit.

## Config

```toml
[fixer]
min_sessions = 5          # sessions seen before a server is called unused (3 to 1000)
lookback_days = 30
context_min_tokens = 1500
activity_minutes = 30
```
