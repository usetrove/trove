# Techniques: one list, one order, one way to combine them

Status: **inventory and plan.** No code changes come with this document. It lists every token-saving technique Trove
has, could build, or could wrap; says where each one acts, how Trove would get it, and what it conflicts with; and sets
the rules for making them work together. Every external fact is marked with a source key (see "What was verified"),
and anything not verified is marked. Checked 2026-11-13.

## Why this exists

"All the techniques should work together" is a composition problem, not a code-import problem. Techniques act at
different layers, some touch the cached prompt, some overlap, and two good ones can cost more together than apart.
Before choosing where code comes from, each technique needs the same few facts written down. This table is that.

## Routes: where a technique comes from

| Route | What it means | When to use it | Limits |
|---|---|---|---|
| **native** | Trove implements it in Go, in the optimizer engine, hooks or gateway | It must compose inside the request or tool path; it needs deterministic output (rule 5) | Most work |
| **config** | Trove sets a documented setting or environment variable of the agent, with a diff and an undo (`trove fix`, `trove settings`) | The agent already offers the control; works for subscription users | Only what the agent documents; applies at session start |
| **wrap** | Another project's tool, installed and judged by `trove external`, with the user's approval | The tool lives outside the request path (shell output, an MCP server, a rules block) | Needs its own runtime; Trove can detect conflicts but not control the tool's inside |
| **vendor** | Copy a small piece of someone else's code into this repository | Only a Go file under a permissive license, with attribution | You own it afterwards. Python or Rust cannot go into a pure-Go, no-cgo binary |

### Licence and provenance rules

1. Vendor or port only from MIT or Apache-2.0 sources, and keep the notice (`NOTICE` or a header comment naming
   source, version and licence). Never from GPL, AGPL, BUSL-1.1 or any source-available licence, or from anything whose
   licence is unclear or mixed.
2. A closed or hosted product is never copied. Rule 11 forbids reverse engineering. Public documentation and published
   benchmarks are for ideas only.
3. A hosted service that receives prompt or code text is out: rule 1 allows no other outbound call.
4. Pin a version and record where it was verified, as `trove external show` does.
5. Names: no "Claude", "Anthropic", "Codex" or "OpenAI" in any name we ship (rule 13). Other projects' names may be
   used as plain text; "Caveman" is that project's trademark, so it is never ours.
6. Techniques that need a model call of their own count that cost as overhead (rule 15).

## The layers, in the order they act

1. **Config** (before a session): settings and environment variables the agent reads at start.
2. **Instructions** (start of a session): rules text, skills, subagent definitions, output styles.
3. **Tool boundary** (during a session): hooks that rewrite a tool call or its output; MCP tools.
4. **Request** (the gateway): the API request body and its cache layout. API-key traffic only (rules 3 and 10).
5. **Provider**: caching and billing, which Trove observes but does not control.

A technique at an earlier layer cannot be undone by a later one, and every change at layers 3 and 4 can change bytes
the provider has cached. That is why layers 3 and 4 are session-pinned and deterministic.

## The matrix

"Prefix" means the technique can change bytes that are already part of the cached prompt. "Subscription" means it works
for a user with a Claude.ai login (no gateway). "Proxy" in the Route column means it needs the gateway.

### Already built

| ID | Technique | Layer | Route | Prefix | Subscription | Notes |
|---|---|---|---|---|---|---|
| B1 | `noise_normalizer`: strip colour codes, progress frames, repeated blank lines | tool boundary, request | native | no (new results only) | yes (hook) | lossless; Autopilot-eligible |
| B2 | `duplicate_result_collapser`: replace a repeated identical tool result with a pointer | request | native | no, deterministic | no (gateway) | lossless; Autopilot-eligible |
| B3 | `output_trimmer`: keep failures and the first and last lines of long test and build output | tool boundary, request | native | no | yes (hook, Bash only) | recoverable: full text kept locally; needs the MCP server and recovery store. Not Autopilot-eligible |
| B4 | `volatile_prefix_mover`: move changing lines out of the stable prompt | request | native | **yes** | no | custom agents only |
| B5 | `cache_breakpoints`: place cache markers on the final layout | request | native | **yes** | no | custom agents only; at most 3 of the 4 allowed |
| B6 | `hook_repeat_read`: deny a repeat read of an unchanged file | tool boundary | native | no | yes | S7 `PreToolUse` deny |
| B7 | `hook_read_limit`: add `limit: 400` to a large read | tool boundary | native | no | yes | needs `updatedInput` (S7); it replaces the whole input |
| B8 | `hook_large_output`: hint to use `trove_run` for known-noisy commands | tool boundary | native | no | yes | needs the MCP server |
| B15 | `hook_quiet_commands`: add one output-quieting flag to an allowlisted command | tool boundary | native | no | yes | rewrites the Bash command like RTK does but is not refused beside it today; whether it should be is open |
| B9 | MCP tools `trove_read`, `trove_search`, `trove_run`, `trove_fetch_full` | tool boundary | native | no | yes | tool definitions are added to every request: an overhead (rule 15) |
| B10 | `trove fix`: `mcp_prune`, `context_slim`, `cache_breaker_fix`, `model_default` | config | config | no | yes | diff and undo |
| B13 | `trove fix` `settings_check`: flag and remove a cost-raising setting you already have (caching off, tool search off, a one-hour cache lifetime with one-hour writes in your history) | config | config | no | yes | suggestion only, never in `--all`, no saving figure; added 2026-11-13 |
| B14 | `trove settings`: catalog of the agent's documented token controls with version checks, profiles and measurement | config | config | no | yes | existed before this document; an earlier draft left it out. Catalog extended 2026-11-13 |
| B11 | Scout subagent and test-gated cascade (`trove tasks`) | instructions | native + config | no | yes | cheaper model for easy work |
| B12 | Autopilot (promotion and rollback at session boundaries) | all | native | no | yes | see [autopilot.md](autopilot.md) |

### Candidates

| ID | Technique | Layer | Route | Prefix | Subscription | Notes and sources |
|---|---|---|---|---|---|---|
| C1 | **Cache lifetime**: choose 5 minutes or 1 hour | config; request | config, then proxy | the setting: no. A per-breakpoint split: yes | config: yes; split: no | See findings 1 and 2: measured once, a blanket 5-minute default would have cost more for that developer. S2, S4. The per-user estimate is built (`cache_ttl` suggestion) |
| C2 | Fan-out serialisation: hold sibling subagent requests until the first cache entry exists | request | native, proxy | no | no | needs a latency budget (up to 3 s is Probe0's figure, unverified). Not verified against provider docs |
| C3 | In-flight dedup: identical simultaneous requests share one upstream call | request | native, proxy | no | no | fail open on any doubt |
| C4 | Exact response cache for identical requests | request | native, proxy | no | no | correctness risk with agents; opt-in only |
| C5 | Hard spend cap checked against the ledger | request | native, proxy | no | no | |
| C6 | Effort level per task | config; request | config; proxy | a mid-session change **can invalidate the cache** (S1, summary) | config: yes | session start only (rule 5). S4, S6 |
| C7 | Model per subagent | config, instructions | config | no | yes | `CLAUDE_CODE_SUBAGENT_MODEL`, frontmatter `model`. S5 |
| C8 | Per-request model swap in the gateway | request | proxy | **yes** (a different model has a different cache) | no | not recommended; same-family only, if ever |
| C9 | Output size limits: `BASH_MAX_OUTPUT_LENGTH` or `bashOutputMaxChars`, `MAX_MCP_OUTPUT_TOKENS`, `CLAUDE_CODE_FILE_READ_MAX_OUTPUT_TOKENS` | config | config | no | yes | defaults 30,000 chars, 25,000 tokens, 25,000 tokens. S3, S8. **Catalog entries verbatim 2026-11-13** (`trove settings`, `internal/settings/catalog`); no profile sets a value yet, since lowering one changes behavior and there is no benchmark |
| C10 | Compaction timing: `autoCompactWindow`, `CLAUDE_AUTOCOMPACT_PCT_OVERRIDE`, `idleCompaction`; guidance for `/compact` | config | config | compaction rewrites history by design | yes | S3, S4. **Catalog entries verbatim 2026-11-13**; no profile ships a number |
| C11 | Tool search: `ENABLE_TOOL_SEARCH`, per-server `alwaysLoad` | config | config | no | yes | S3, S8; Headroom sets it too. **`ENABLE_TOOL_SEARCH` verbatim 2026-11-13**; `trove fix` flags a stray `=false` (see B13) |
| C12 | `updatedToolOutput` for more tools than Bash | tool boundary | native | no | yes | S7 says it applies to any tool; Trove's hooks use it only for Bash, Grep, Glob |
| C13 | Terse replies and minimal code (rules text or a skill) | instructions | wrap or native text | no | yes | costs tokens to carry (rule 15); effect measured, not assumed |

### Wrapped tools (already in `trove external`, or candidates)

| ID | Tool | What it does | Licence (verified) | Route | Overlap |
|---|---|---|---|---|---|
| W1 | RTK | Rewrites shell commands so their output is smaller | Apache-2.0 for the Apache-only build; a BUSL-1.1 engine in the default build (L1) | wrap, Apache-only build, pinned tag | B3, B8: all three act on Bash output |
| W2 | token-savior | MCP server for code navigation | MIT (L5) | wrap | B9: both offer read and search tools |
| W3 | terse-rules | A short rules block | Trove's own | wrap | C13, and Caveman if installed |
| W4 | Caveman | Skill and proxy that make replies terse | Apache-2.0 from v3.0.0; earlier parts were BUSL-1.1 (L3) | candidate: wrap the skill only | C13, W3. Do not wrap its proxy |
| W5 | Headroom | Compresses tool output, logs and files; library, proxy, CLI, MCP server | Apache-2.0 (L2) | candidate: ideas only | B1 to B3. Its proxy and Trove's gateway both claim `ANTHROPIC_BASE_URL`, so they cannot be chained |
| W6 | LLMLingua | Prompt compression with a model | MIT (L4) | idea only; Python and a model | prompt-level compression, not Autopilot-eligible |
| W7 | SuperCompress | Query-aware context compression, hosted | MIT repo; the hosted API receives your text (L6, match not fully verified) | **excluded** | sends prompts off the machine (rule 1) |
| W8 | ccusage | Usage analysis from local logs | MIT (L7) | reference only | `trove analyze` and `trove logs` already do this |

## Findings that change the plan

1. **Claude Code documents its own cache TTL controls.** S2 and S4: `promptCacheTtl` (main conversation) and
   `subagentPromptCacheTtl`, each `5m` or `1h`; env vars `CLAUDE_CODE_PROMPT_CACHE_TTL`,
   `CLAUDE_CODE_SUBAGENT_PROMPT_CACHE_TTL`, `ENABLE_PROMPT_CACHING_1H`, `FORCE_PROMPT_CACHING_5M`; a subagent
   frontmatter `experimental.cacheTtl`. The docs list a precedence order, and the settings need Claude Code v2.1.242
   or later (subagent frontmatter v2.1.248, both from a summary of the page). So choosing a TTL needs no proxy.
2. **The default TTL depends on the login.** S2 (raw table): the main conversation uses one hour on a Claude
   subscription within plan usage, and five minutes on usage credits, an API key or a cloud provider. If that holds,
   an API-key user is already on 5 minutes for the main conversation, and a one-hour entry costs twice the base price
   (S1) only when a user opts in. That would shrink the case for a TTL optimizer a lot.
   **Measured once, on one developer's own Claude Code history (2026-11-13; about six weeks, about 100 sessions, no
   figures kept here because this repository is public).** Main-conversation requests wrote their cache at the
   one-hour rate; subagent requests wrote only five-minute entries, which matches the defaults in S2. Almost all
   follow-on main-conversation requests came within five minutes, so most one-hour writes were never needed. But the
   few that came after a pause of five to sixty minutes read back a large amount of cached prompt that a five-minute
   entry would have lost and rewritten at the five-minute write price. At list prices (an estimate), the
   one-hour premium paid was well under half of the rewrites it avoided, so **a blanket downgrade to five minutes
   would have cost this developer more, not less.** Subagents paused five to sixty minutes too rarely for a
   one-hour lifetime to pay. This is one history on one machine and it assumes a missed entry is rewritten in full, so
   it does not generalize; it shows that the answer depends on how a person pauses. Method: for each main-conversation
   request, take the gap since the previous request in the same session; sum cache-read tokens where the gap is
   over five and up to sixty minutes (reads only a one-hour entry could serve) and compare their rewrite cost at the
   five-minute write price with the one-hour premium on the one-hour writes, per model, using
   `trove pricing show`. Also not known: the plan behind the traffic. Plan detection used an API-key variable in
   the shell, while the one-hour default in S2 is the subscription default, so the dollar figures are list-price
   equivalents and not necessarily a bill.
   **Consequence:** do not ship a TTL downgrade as a default. If anything is built for C1, it is a per-user estimate
   computed from the user's own history, shown first and recommending a change only when five minutes is cheaper for
   that user.
   **Built 2026-11-13:** the `cache_ttl` suggestion (`internal/optimize/suggest/cachettl.go`, shown by `trove report` and
   `trove optimize suggest`) does exactly this, in both directions, and is silent unless the other lifetime is at least
   25% cheaper by at least $1. On the history above it correctly says nothing.
   It is advice only; it does not change any setting.
3. **A per-breakpoint split still needs the gateway.** The API accepts a TTL on each cache marker, and a longer TTL
   must come before a shorter one (S1, summary only). Claude Code's settings pick one TTL per bucket, not one per
   breakpoint.
4. **Effort changes are not cache-safe mid-session.** S1 (summary): changing `output_config.effort` can invalidate
   message blocks. That supports setting effort only at session start, through config.
5. **Hooks can do more than Trove uses.** S7: `PostToolUse` `updatedToolOutput` works for any tool;
   `PreToolUse` `updatedInput` replaces the whole input and needs `allow` or `ask`.
6. **Documented output and compaction controls are cheap wins** for subscription users (C9, C10, C11): no proxy, no
   credentials, with a diff and an undo.

## Known conflicts

| Pair | Why | Rule |
|---|---|---|
| B3, B8, W1 | All shorten shell output; each can rewrite the same Bash call, and `updatedInput` replaces the whole input | One owner per tool call. `trove external` already flags RTK against Trove's Bash hooks |
| B9, W2 | Two read and search tool sets with different output shapes | Choose one; measure first |
| C13, W3, W4 | Stacked brevity rules add overhead and can fight each other | At most one |
| W5 proxy, the gateway | Both want `ANTHROPIC_BASE_URL` | Never chain; pick one |
| B4, B5, C1 | All change cache layout | Only through the fixed pipeline order, together, at session start |
| C6, C8 against any cached prefix | A change of effort or model breaks cache hits | Session start only |
| C10 and everything at layer 4 | Compaction rewrites history, so cached prefixes are lost by design | Do not tune both at once |

## What "work together" requires

1. **A registry entry per technique** with: layer, route, prefix flag, declared overhead (rule 15), conflicts,
   whether it needs the gateway, the login types it works for, and its source and licence. **Built 2026-11-13:**
   `internal/technique` (`registry.go`) is the code form of this document's tables. Tests keep the two in step (same
   IDs), tie every name to the code that implements it, and enforce the rules above: a technique that adds text
   declares its overhead, only built, lossless, prefix-safe techniques are marked for Autopilot, vendored code is MIT or
   Apache-2.0, and a candidate's name must not already exist in the code. `trove techniques` lists it.
2. **A fixed, deterministic order** (rule 5). Layers 3 and 4 already have one (`docs/optimize.md`). Config and
   instruction techniques apply once, at session start.
3. **One conflict check for everything.** Today `internal/external/conflict.go` covers wrapped tools. Extend it to
   the built-in techniques so Autopilot can refuse an incompatible pair. **Built 2026-11-13:** `technique.Check` and
   `technique.CanAdd` answer it for built-in and wrapped techniques alike, from claims on scarce resources (the
   agent's API endpoint, who shortens shell output, who provides read and search tools, who asks for terse replies)
   and explicit pairs. `trove external` now takes its built-in overlap from the registry, with a test that it still
   refuses exactly what it refused before, and Autopilot holds an optimizer back, with the reason, when something
   already on cannot run beside it. Not yet done: refusing `hook_quiet_commands` beside RTK (both rewrite the same
   Bash input; `trove external` has never refused it, so the registry does not either until that is decided), and
   checks for the pairs in the conflicts table that need measurement, not a rule.
4. **Measurement of sets, not only of single optimizers.** `measure.NetGuard` judges one at a time. Sets need
   session-level A/B comparisons, and every number is labeled "measured" or "estimated" (rule 6).
5. **A chooser** (the planner) that picks compatible sets, and acts only after such an A/B shows it beats the fixed
   list ([autopilot.md](autopilot.md)).
6. **A test per pair on the conflict list**, run in the stress suite: the same history gives the same bytes, and a
   resent history costs a lookup, not a rescan.

## Suggested order of work

1. **Done 2026-11-13 (one history):** the 5-minute and 1-hour cache-write split was measured (finding 2). It argues against a blanket TTL downgrade. The per-user estimate for C1 is now built as the `cache_ttl` suggestion (advice only).
2. Config-route techniques. **Done 2026-11-13 (first pass):** the settings catalog now holds the verbatim entries for C9, C10, C11 and the cache lifetime keys, and `trove fix` has `settings_check` (B13). **Not done:** any profile that sets a number (needs a benchmark), and C1 as a recommendation (needs step 1). They help subscription users and need no proxy.
3. **Done 2026-11-13:** the registry and the shared conflict check (requirements 1 and 3).
4. Build the gateway-only techniques for API-key users, one at a time and shadow first: C3, C5, then C2.
5. Decide W4 and W5 only after steps 1 to 4, from measured overlap with what already exists.
6. Run the benchmark, then the A/B of sets, before an active planner.

## What was verified

Read on 2026-11-13 through a page-fetching agent. No page showed a last-updated date, so re-check before relying on
any of it. Quotes marked "summary" came from the fetch tool's own summary of part of a page, not from raw text; they
are not verbatim.

| Key | Source | What it covers |
|---|---|---|
| S1 | https://platform.claude.com/docs/en/build-with-claude/prompt-caching | TTLs and prices (raw), breakpoint limit 4 (raw), lookback 20 blocks (raw), minimum lengths (raw), TTL ordering and invalidation rules (**summary only**) |
| S2 | https://code.claude.com/docs/en/prompt-caching | TTL controls, precedence, default TTL by login (raw) |
| S3 | https://code.claude.com/docs/en/env-vars | environment variables (parts of the page read) |
| S4 | https://code.claude.com/docs/en/settings-reference | settings index (raw); some entries from a summary |
| S5 | https://code.claude.com/docs/en/sub-agents | subagent model and frontmatter (first part read) |
| S6 | https://code.claude.com/docs/en/model-config | effort and model precedence (first part read) |
| S7 | https://code.claude.com/docs/en/hooks | `updatedInput`, `updatedToolOutput`, `updatedMCPToolOutput` (parts read; not checked for other events) |
| S8 | https://code.claude.com/docs/en/mcp and /costs | tool search, output limits (mcp: first part read) |
| S9 | https://code.claude.com/docs/en/settings-reference and /env-vars (second read, 2026-11-13) | raw text of `effortLevel`, `modelSettings`, `promptCacheTtl`, `subagentPromptCacheTtl`, `autoCompactWindow`, `autoCompactEnabled`, `bashOutputMaxChars`, `idleCompaction`, `env`, and the env-var rows for `BASH_MAX_OUTPUT_LENGTH`, `CLAUDE_CODE_AUTO_COMPACT_WINDOW`, `CLAUDE_AUTOCOMPACT_PCT_OVERRIDE`, `CLAUDE_CODE_FILE_READ_MAX_OUTPUT_TOKENS`, `ENABLE_TOOL_SEARCH`, `MAX_MCP_OUTPUT_TOKENS`; minimum versions where the entry states one. The `model-config` page was not read |

Licences, from each repository's LICENSE file:

| Key | Repository | Licence |
|---|---|---|
| L1 | https://github.com/rtk-ai/rtk | Apache-2.0 root; the `rtk-engine` and `rtk-proxy` parts are BUSL-1.1; an Apache-only build exists. `engine/LICENSE` not read |
| L2 | https://github.com/headroomlabs-ai/headroom | Apache-2.0 |
| L3 | https://github.com/JuliusBrussee/caveman | Apache-2.0 from v3.0.0; earlier engine and proxy parts were BUSL-1.1; "Caveman" is a trademark |
| L4 | https://github.com/microsoft/LLMLingua | MIT |
| L5 | https://github.com/Mibayy/token-savior | MIT |
| L6 | https://github.com/Supercompress/Supercompress | MIT repo. **Name match not confirmed**: two other repositories with similar names were not read. Whether the hosted service falls under the MIT licence is not verified |
| L7 | https://github.com/ccusage/ccusage | MIT (`apps/ccusage/LICENSE`) |

Not verified: latest release tags and primary languages of the repositories above; the 3-second hold and any
percentage in Probe0's own materials; how the Claude Code TTL settings behave on a real login; the middle section of S1
(TTL ordering and the invalidation table), which should be re-read directly before building C1 or C6.
