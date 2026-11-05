# Settings profiles (internal/settings)

Per-project, per-task-type tuning of each harness's built-in token controls.
This file is the verification log and the status of the work. The settings
themselves are data in `internal/settings/catalog/*.toml`, not code.

## Rule for what Trove may write

CLAUDE.md rule 8 says to verify provider and agent details against official
docs. The fetch tools used here can return a model summary of a page instead of
its raw text, and summaries are not evidence. So each catalog entry records:

- `verified = "verbatim"`: the key was read as raw page text.
- `verified = "extractor"`: seen only in a summarized view. Shown to the user,
  suggested as a command, **never written**.
- `verified_values`: the values seen verbatim. A profile may only write those.

The validator rejects any `applyable = true` entry that is not verbatim, is
shell-only, or has no verified values or pattern.

## Verification log (read 2026-11-05)

Newest Claude Code version named in the docs: 2.1.314. Newest Codex version
named: 0.134.0 (the profile change). Neither site states a "latest" version.

### Claude Code (code.claude.com/docs/en/)

| Setting | Where | Status |
|---|---|---|
| `env` block sets session env vars | settings-reference, env-vars | verbatim |
| `CLAUDE_CODE_AUTO_COMPACT_WINDOW` | env, 100000..1000000 | verbatim, applyable |
| `CLAUDE_AUTOCOMPACT_PCT_OVERRIDE` | env, 1..100, only lowers | verbatim, applyable; default not stated |
| `claudeMdExcludes` | settings, any layer, arrays merge | verbatim, applyable |
| `outputStyle` | settings; Proactive, Concise, Explanatory, Learning (case-sensitive) | verbatim, applyable |
| `effortLevel` | settings; low, medium, high, xhigh | key verbatim, values extractor: suggest `/effort` only |
| `MAX_THINKING_TOKENS` | env | its env-vars entry was beyond the raw text read: suggest only |
| `ENABLE_TOOL_SEARCH`, per-server `alwaysLoad` | env, MCP entry | values unconfirmed: suggest only |
| `MAX_MCP_OUTPUT_TOKENS` | shell export, default 25000 (extractor) | suggest only |
| `model`, `fallbackModel` | settings | extractor; `model` already handled by `model_default` |
| Custom compaction instructions | none found | **gap**, not offered |

Managed settings (settings.md, managed-settings.md, extractor-quoted for paths):
managed values rank above every other layer. File locations:
macOS `/Library/Application Support/ClaudeCode/managed-settings.json`,
Linux/WSL `/etc/claude-code/managed-settings.json`,
Windows `C:\Program Files\ClaudeCode\managed-settings.json`, plus
`managed-settings.d/*.json`, MDM/registry, and server-managed delivery. The
`maxEffortLevel` cap is an exception that applies from any layer. Trove cannot
read server-managed or MDM values from a file, and `/status` does not show
per-key sources, so detection is best effort (see Known gaps).

### Codex CLI (learn.chatgpt.com/docs/config-file/, reached from developers.openai.com/codex/)

- Profiles (verbatim): `--profile name` loads `~/.codex/config.toml`, then
  overlays `~/.codex/name.config.toml`. Use top-level keys in the profile file.
  From Codex 0.134.0, `[profiles.name]` tables and the top-level `profile =`
  selector are no longer read. Project-local `.codex/config.toml` ignores
  `profile` and `profiles`.
- `model_reasoning_effort`: verbatim example `"medium"`. The value set is "advertised by the selected model", so it is open.
- `model_verbosity`: verbatim example `"low"`; Responses API providers only.
- `model_reasoning_summary`: verbatim example `"none"`.
- `tool_output_token_limit`, `model_auto_compact_token_limit` (and `_scope`),
  `compact_prompt`: table rows seen only in a summary: suggest only.
- No default is stated on any page for these settings.
- Managed layers (extractor-quoted): `requirements.toml` at `/etc/codex/` or
  `%ProgramData%\OpenAI\Codex\`; `managed_config.toml` at `/etc/codex/` or
  `~/.codex/` on Windows. Managed layers override CLI `--config`. The full layer
  order was not obtained.

## Follow-ups needed to widen what Trove can apply

1. Read the raw `effortLevel`, `MAX_THINKING_TOKENS` and `ENABLE_TOOL_SEARCH`
   sections (settings-reference and env-vars are 450 KB and 190 KB; the fetch
   tool only returns the first ~40 KB raw). Until then those levers are
   suggestions.
2. Read the raw Codex settings table for the three extractor-only keys.
3. Benchmark evidence (the "Prompt 25" results referenced by the task) is not in
   the repo: `bench/results/` holds only smoke runs. Profiles therefore ship
   without lowering any setting below the harness default.

## How it works

- `trove settings` lists the catalog checked against the installed versions.
- `trove settings plan --task <type>` shows the diff and everything Trove will not do, and why. Changes nothing.
- `trove settings apply --task <type>` goes through the Fixer: diff, approval, backup, `trove undo`, and queued while an agent session looks active (the queue remembers the task name and switch only).
- Claude Code values are written to the project's `.claude/settings.json` (env block or native key). Codex values go in a new `~/.codex/trove-<task>.config.toml`; start Codex with `codex --profile trove-<task>`. The user's own `config.toml` is never touched.
- Never over managed settings: a value that a readable managed file already sets, or that a higher-priority project file sets, is shown as "not changed" with the reason. Server-managed and MDM policy cannot be read, and the plan says so.
- Profiles ship with values that cannot hurt (Codex `model_reasoning_summary = "none"`). Anything that changes behavior without evidence (`outputStyle = Concise`, `model_verbosity = low`) is left out unless `--allow-unproven` or `[settings] allow_unproven = true`.

## Token overhead (rule 15)

Profiles change settings values only. They add no tool definitions and inject
no text into any prompt, so their own overhead is 0 tokens. If a future profile
adds an `outputStyle` or instruction text, that text must be counted and
charged against its measured saving.

## Measurement

Reported by `trove settings status` and in `trove report`. Output tokens per session, prompt tokens per request, and outcome counts (tool errors, turns, abandoned sessions) are compared across sessions before and after the change. Figures are "estimated" until both sides have `[measure] min_sessions`, then "measured". Limits, stated in every result:

- Trove records no project or task on a session, so all sessions of the agent are compared.
- Trove does not know whether tests passed. Outcome counts are the only success proxy.
- A Codex profile is "not measurable": Trove cannot tell which sessions used `--profile`.
- When outcomes clearly worsen, or output grows, the report suggests `trove undo <id>`. It does not undo automatically.

## Known gaps

- No benchmark evidence exists yet, so no profile lowers a setting below the agent default and no numeric thinking or compaction value ships.
- No shadow estimate: history does not split thinking from other output, so Trove cannot honestly estimate a saving before applying.
- A concurrent session-level A/B (arms chosen at session start) is not built; before/after is the only measurement.
- No automatic rollback, only a recommendation.
- Per-project value overrides in config are not built; per-project tuning is the project the plan targets.
- Managed-settings detection is best effort (see above).

## Status

All planned steps are implemented: catalog, profiles, fixer wiring, managed-override blocking, measurement, report, CLI.
