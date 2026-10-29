# Reading your agents' history

Written 2026-10-29. Code: `internal/logs`. Step-by-step check: [logs-verification.md](logs-verification.md).

`trove` can report without the gateway. It reads the session history that Claude Code (and Codex CLI) already keep on your disk, works out what the usage cost and where tokens were wasted, and shows the same report. Run `trove`, wait a few seconds, read the report.

## What it does and does not do

- **Read-only.** Trove never writes to an agent's directory.
- **Metadata only.** It stores token counts, sizes, hashes, model names, timestamps and tool *names*. It never stores prompts, code, file paths, tool output, raw session ids or raw request ids. The event type has no field that could hold text, and a test dumps every database table after a scan and checks that no sentinel text from the logs is in it.
- **No credentials.** Trove never opens `~/.claude.json`, `.credentials.json`, `auth.json` or the keychain.
- **Fails open.** If reading history goes wrong, `trove` prints one line on stderr and shows the report from what it already has.
- **Opt out.** `trove --no-history` for one run, or `[history] enabled = false` in `config.toml`.

## Commands

| Command | What it does |
|---|---|
| `trove` / `trove report` | Reads new history (first run: the last 30 days), then shows the report. |
| `trove logs scan [--all]` | Reads new history now. `--all` ignores the 30-day limit. |
| `trove logs status` | Shows files read, skipped lines, the detected plan and its evidence, and which waste checks can and cannot run on history. |

All support `--json`. A second run only reads files that changed, and only the bytes after the last checkpoint.

## Where the data comes from (verified 2026-10-29)

| | Claude Code | Codex CLI |
|---|---|---|
| Location | `~/.claude/projects/<project>/<session>.jsonl`, plus `<session>/subagents/`. `CLAUDE_CONFIG_DIR` moves it. | `~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl`. `CODEX_HOME` moves it. |
| Retention | Deleted after `cleanupPeriodDays` (default 30, minimum 1). | Not documented. |
| Format | JSON lines, `type` field. Verified from docs and a local install (v2.1.302). | JSON lines. **Not verified on a real install** (see below). |
| Usage | `message.usage` incl. cache write split into 5m/1h | `token_count` events, `last_token_usage` |
| Sources | code.claude.com/docs/en/claude-directory, `/settings` | openai/codex `codex-rs/protocol/src/protocol.rs`; developers.openai.com/codex/config-advanced |

Facts Trove relies on for Claude Code:

- One API response is written as several `assistant` lines (one per content block), each repeating the same usage. Trove merges them by `requestId`. Counting every line would overstate tokens about 2.3 times on the install checked.
- Tool results arrive on later `user` lines, matched by `tool_use_id`. Their size and a hash (for spotting repeats) are kept; the text is dropped.
- The cache write split (`ephemeral_5m` / `ephemeral_1h`) is present, so requests price exactly. A write with no split is left unpriced, as the gateway does.

**Codex is unverified.** Codex was not installed where this was written and OpenAI's docs do not describe the session files. The token fields and `session_meta` / `turn_context` fields were checked in the open-source repo. The `{timestamp, type, payload}` wrapper and the tool-call item names come from community write-ups only. Check it on a real install with the steps in `logs-verification.md` before trusting Codex numbers.

## Format changes between versions

Only Claude Code 2.1.302 was available to inspect. Trove accepts any version: unknown fields and unknown line types are ignored and counted, and a version outside `2.x` is parsed best-effort and flagged ("a Claude Code version Trove was not written for"). I could not verify how earlier versions differed, so older formats are tolerated, not tested against real files. Synthetic fixtures cover the current format and malformed input (garbage, truncated lines, oversized lines, wrong field types, unknown version).

## Plan: API key or subscription

Trove estimates it from, in order: `[history] plan` in `config.toml`; `ANTHROPIC_API_KEY` / `ANTHROPIC_AUTH_TOKEN` being set (name only, never the value); `apiKeyHelper` and `forceLoginMethod` in Claude Code's `settings.json`. For Codex: `OPENAI_API_KEY` being set. Conflicting or missing signals give `unknown`. A ChatGPT login or a Claude subscription login lives in files Trove does not open, so those users should set `plan = "subscription"`.

Limits: the signals are read from the shell running `trove`, which may not be the environment the agent ran in, and they describe now, not every past session. The plan is always labeled estimated.

What each plan shows:

- **api_key**: history counts as spend, at list prices (estimated, not an invoice).
- **subscription / unknown**: spend is *not* shown. The report shows **API value**: what the same usage would cost at API list prices, labeled estimated and "not a bill". Trove does not show a percentage of a subscription limit: plan limits are not available locally and change, so any such number would be a guess.

## What the audit can find

| Check | On history? |
|---|---|
| `oversized_tool_output` | Yes. Result size is turned into tokens at a prior ratio (3.5 bytes/token), not calibrated. |
| `duplicate_tool_result` | Yes, within one scan of a file. A repeat that straddles two scans is missed. |
| `expensive_model_small_task` | Yes. Uses the same rule as the gateway. |
| `noisy_output`, `context_file_weight`, `unused_tool_schemas`, `cache_miss` | No. They need prompt text, tool definitions or consecutive request bodies, which history does not keep. Skipped, not approximated. |
| `long_session_drift` | Not yet implemented for history. |

Dollar figures for oversized and duplicate results count only the first request that carried the result, so they are lower bounds. Findings use the same names, shadow-savings mapping and "estimated" label as gateway findings.

## Gateway and history together: no double counting

A Claude Code session that went through the gateway is in both places. The report counts it once, from the gateway:

- History requests are left out when their session key (a hash of the session id, same scheme as the gateway; the idle-gap `~N` suffix is ignored) has any gateway request. The report says how many sessions were left out.
- Codex sessions cannot be matched by key. History from the time of the first gateway Codex request onward is treated as gateway-seen and left out. If the gateway is stopped while Codex keeps running, that history is missed, so Codex history is a lower bound.
- A session only partly routed through the gateway loses its earlier history rows (per-session, not per-time-span).

## Accuracy check

On this machine's 30 days of real history (119 files, 460 MB, 4,889 requests) the first scan took 6.0 s and a re-run 0.4 s. Priced per session, the totals came out 1 to 8 percent below Claude Code's own `cost-state` totals in 7 of 8 sessions checked, and 23 percent below in one. They were never above. I did not confirm the cause; the likely one is that Claude Code's total includes calls that are not in the transcript. Treat history spend as "at least".

## Known gaps

- Codex parser unverified on a real install.
- No older-format fixtures from real versions.
- No drift check on history; no subscription limit estimates.
- History is read for the last 30 days of file changes. Anything Claude Code already deleted is gone.
- Duplicate-result detection only looks inside one scan of a file.
- Costs are list prices; fast/priority service tiers are left unpriced.
