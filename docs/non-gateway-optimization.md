# Optimizing without the gateway: research notes

Written 2026-11-03. Status: desk questions Q1, Q3, Q4 and Q7 have first answers below. Q2 (is hook-replaced output
cache safe), Q5 (how to prove savings) and Q6 (how big is the prize) are NOT done. Nothing here is a verdict yet.
Not legal advice; re-verify before relying on it (CLAUDE.md rule 8). Companion to
[non-gateway-channels.md](non-gateway-channels.md).

Every claim below is from Claude Code's official docs unless marked **estimated** (my reading, not tested) or
**unverified**.

## Sources and how much was read (2026-11-03)

| Page | Read |
|---|---|
| code.claude.com/docs/en/costs | whole page |
| code.claude.com/docs/en/prompt-caching | whole page |
| code.claude.com/docs/en/hooks | about 200,000 of 286,000 characters read; the rest was searched, not read |
| code.claude.com/docs/en/managed-settings | searched for key names only, not read in full |
| code.claude.com/docs/en/monitoring-usage, statusline | see non-gateway-channels.md |

## Q4. Levers that cut inference cost with no credentials

Anthropic documents these itself (costs and prompt-caching pages):

| Lever | Setting or command | Documented effect |
|---|---|---|
| Preprocess data in hooks | `PreToolUse` `updatedInput`, `PostToolUse` `updatedToolOutput` | The costs page's own example: a hook greps a 10,000-line log for errors "reducing context from tens of thousands of tokens to hundreds". Anthropic's example rewrites the command (`updatedInput`) |
| Main-conversation cache TTL | `promptCacheTtl` setting or `CLAUDE_CODE_PROMPT_CACHE_TTL` (`5m` or `1h`, v2.1.242+) | Default is 1 hour only on a subscription within plan usage; API key or cloud provider defaults to 5 minutes |
| Subagent and other-request cache TTL | `subagentPromptCacheTtl` or `CLAUDE_CODE_SUBAGENT_PROMPT_CACHE_TTL` | Subagents, compaction and workflows default to 5 minutes |
| Cache controls | `ENABLE_PROMPT_CACHING_1H`, `FORCE_PROMPT_CACHING_5M`, `DISABLE_PROMPT_CACHING*` | See precedence list on the prompt-caching page |
| Effort and thinking | `/effort`, `MAX_THINKING_TOKENS` (fixed-budget models only) | Thinking is billed as output; adaptive models ignore nonzero budgets |
| Model choice | `/model`, subagent `model: haiku` | Switching model mid-session loses the cache |
| CLAUDE.md size | keep under about 200 lines; move workflows to skills | Skills load on demand |
| MCP overhead | tool search defers tool definitions by default | Disabling servers mid-session can invalidate the cache when tools are not deferred |
| Idle behavior | `idleCompaction`, prompt suggestions, session recap | Background requests while idle |
| Compaction timing | `/compact` at natural breaks | Compaction invalidates the conversation layer by design |

What the docs say about cache safety of changes, relevant to Rule 5:
- The cache matches an exact prefix. Claude Code orders requests system prompt, then project context, then
  conversation, and **appends** new content at the end.
- Changing the set of tool definitions, model, speed, or (on most models) effort mid-session invalidates the cache.
  Editing CLAUDE.md mid-session does not, because it is only read at session start.
- Claude Code itself already rewrites history in two cases it calls "expected rebuilds": compaction and
  clearing old tool results.

**Overlap to be aware of:** on Pro, Max, Ultra, Team and Enterprise plans, `/usage` already shows a breakdown of
usage by skill, subagent, plugin and MCP server, plus recommendations such as "Trim CLAUDE.md" with a token
figure, and `f` can apply some of them (v2.1.305+). Trove's fixer for OAuth users overlaps with this. Its value
there is cross-tool coverage, history, hooks and measurement, not the recommendations themselves.

**Open discrepancy:** the costs page says Team and Enterprise usage "draws from a per-seat allowance", with usage
credits beyond it. Anthropic's help center page on usage-based Enterprise plans says seats include no usage and
tokens are billed at API rates. Both may be true for different plan versions. Which applies decides whether an
Enterprise user pays per token, so it decides how valuable savings are. **Unverified; ask users which plan they
have.**

## Q3. The hook contract (partly verified)

- `PostToolUse` `updatedToolOutput` replaces what Claude sees, for all tools. For built-in tools the value must
  match the tool's output shape (Bash: `stdout`, `stderr`, `interrupted`, `isImage`); a mismatch is ignored and
  the original is used. `updatedMCPToolOutput` is the MCP-only field. No size limit is stated.
- The tool has already run when `PostToolUse` fires, and OpenTelemetry records the original output.
- `PreToolUse` `updatedInput` rewrites arguments before the tool runs (Anthropic's own cost example).
- Hook input includes `transcript_path`, `session_id`, `prompt_id`, `cwd`, tool name, input and (post) response;
  it has no token or cost fields. The `tool_response` shape differs per tool.
- Timeouts: `SessionEnd` defaults to 1.5 seconds (raisable); other defaults not captured. **Unverified:** ordering
  of several matching hooks, and the exact `tool_response` shape for Read, Grep and Glob.
- Whether the transcript stores the replaced or the original output: **not stated in what I read. This is Q2.**

## Q1. Which gateway optimizers could move to hooks or MCP (estimated, pending Q2 and Q3)

| Gateway optimizer (docs/optimize.md) | Acts on | Hook/MCP equivalent | Reading |
|---|---|---|---|
| `noise_normalizer` | tool results | `PostToolUse` replace, Bash stdout | Plausible; depends on Q2/Q3 |
| `output_trimmer` | tool results, recoverable | `PostToolUse` replace plus the existing recovery store (`trove_run`) | Plausible; depends on Q2/Q3 |
| `duplicate_result_collapser` | tool results across turns | Hooks ledger already keeps hashes; replace a repeat with a pointer | Partial; needs cross-turn state |
| `volatile_prefix_mover` | system prompt | none | Custom agents only, so not applicable to Claude Code even with the gateway |
| `cache_breakpoints` | request body | none | Custom agents only; Claude Code controls its own breakpoints |

Estimated: the tool-result optimizers are the part that can follow users to a hook. Request-body rewriting of
history or system prompt is gateway-only.

## Q7. Compliance and Enterprise fit

Anthropic's own costs page recommends hooks for preprocessing and shows how, so using documented hook outputs
looks consistent with the legal page. It still does not give permission for anything touching credentials.

Managed settings can limit what Trove can install on a managed machine (key names confirmed, effects **not read
in full**):
- `allowManagedHooksOnly` (a lock; the strictest value from any admin source wins) restricts which hooks run.
  The exact list of what still runs is on the settings-reference page and was not read.
- `disableAllHooks`, `allowedHttpHookUrls`, `allowManagedMcpServersOnly` and `allowedMcpServers` also exist.
- Admins can pin the telemetry endpoint through a managed `env` block (monitoring-usage page).

So on a locked-down Enterprise machine, hooks, MCP and a local telemetry receiver may all be blocked. This needs
checking per company, and the employer's policy applies on top of Anthropic's terms.

## Not done

- **Q2**, the make-or-break test: whether Claude Code keeps the replaced output in later requests and whether
  cache reads stay stable. The prompt-caching page suggests it should (hook output is appended before the result
  is first sent, not rewritten later), but that is **estimated**, not tested. Needs a manual run on a real
  Claude Code session.
- **Q5**: how to prove savings and no harm without matched gateway requests.
- **Q6**: where input tokens actually go in real sessions, to size the prize.
- Remaining parts of the hooks and managed-settings pages.

## Phase 0 remainder: hooks and managed settings (verified 2026-11-04)

Sources: code.claude.com/docs/en/hooks (characters 0-200,000 of 286,000 read by summary; the last 86,000 were
searched for `updatedToolOutput`, `allowManagedHooksOnly` and `tool_response` with no match, so those are NOT
in that tail), code.claude.com/docs/en/managed-settings (saved, searched for key names), settings-reference
(first 100,000 of 448,000 characters, index table only). Third-party items are labeled.

### Verified in the official docs
- **Timeouts:** defaults are 600 s for `command`, `http` and `mcp_tool`, 30 s for `prompt`, 60 s for `agent`.
  Lowered to 30 s on `UserPromptSubmit`, `PreModelSwitch`, `PostModelSwitch`, and 10 s on `MessageDisplay`.
  `SessionEnd` shares a 1.5 s budget, raised to match a longer per-hook timeout, up to 60 s. `PostToolUse` has no
  special override. A slow trimming hook delays every tool result, so Trove's hook must stay far under these.
- **Several hooks on one event run in parallel.** The same handler defined in several settings files runs once.
- **`updatedToolOutput`:** "Replaces the tool's output with the provided value before it is sent to Claude." For
  built-in tools a value that does not match the output schema "is ignored and the original output is used".
  MCP output is not schema-checked. No size limit is stated. Docs warn that "Stripping error details that Claude
  needs can cause it to proceed on a false assumption."
- **OpenTelemetry records the original output** "before the hook runs", so OTel cannot show the saving.
- `PreToolUse` conflicts resolve as `deny` > `defer` > `ask` > `allow`.
- **Hook merging:** `hooks` is a "Lists" key, so entries from every managed source combine. `allowManagedHooksOnly`
  is a "Locks" key: the strictest value from any source applies. `allowManagedMcpServersOnly` and
  `allowedMcpServers` also exist; an invalid `disableAllHooks` is dropped with a warning.
- `allowManagedHooksOnly` one-line description: "Run only the hooks your organization deploys". Managed scope
  only. `disableAllHooks` turns off hooks, a custom status line and the `@` file suggestion command together.

### Still NOT answered (so still open)
- **Order and conflict resolution when two hooks both return `updatedToolOutput` or `updatedInput`.** Not in the
  pages read. Since hooks run in parallel, Trove must assume another tool's hook could also rewrite output, and
  Q2 must test that case.
- **Whether the transcript or later requests keep the original or the replaced output (Q2).** Not stated.
- **`tool_response` shapes for Read, Grep, Glob, Edit and WebFetch.** Only Write
  (`filePath`, `type`) and Bash (`stdout`, `stderr`, `interrupted`, `isImage`, `bashEditDiff`) were found. The
  schema "depends on the tool". These have to be captured from a real session, not assumed.
- **Exact list of what `allowManagedHooksOnly` still lets run** (plugin hooks, statusLine). Settings-reference
  entry was beyond the part read.

### Third-party reports (unverified, not Anthropic)
- A GitHub issue (claudeissues.com #86042, closed "not planned") says server-managed `allowManagedHooksOnly: true`
  also silently disables a custom statusLine, with no debug message. Another (#51123, resolved) says the
  restriction was bypassed when `ANTHROPIC_BASE_URL` pointed at a local proxy.
- Blog summaries say it blocks user, project, local and most plugin hooks while managed hooks still run.
  Treat as a hint to test, not a fact.

### Effect on the plan
- On a locked-down Enterprise machine Trove's user-level hooks and statusLine may silently not run. Trove must
  detect that (no hook events arrive in a session) and say so in plain English instead of showing zero savings.
- Parallel hooks mean output trimming can collide with other tools' hooks. This is a Q2 test case.
- Phase 1 needs real captured `tool_response` samples for Read, Grep and Glob before any trimmer is written.
