# Request analysis (`internal/analyze`)

Analysis runs in the gateway's recording goroutine, after the response has been
forwarded. It never modifies a request and never adds latency to one. Every
number it produces is an **estimate** and is labeled that way. Nothing it
stores contains prompt text, code, file paths or tool output.

## What is stored (per request row)

| Column | Content |
|---|---|
| `segment_token_estimates` | `{label, calibrated, total_tokens, bytes_per_token, categories}`; categories are `system`, `context_files`, `tool_schemas`, `history`, `tool_results:<tool name>` and sum exactly to the provider-reported prompt total when `calibrated` is true |
| `findings` | list of `{detector, tokens, usd, confidence, label, detail}`; `detail` holds counts, tool names and reason codes only |
| `shadow_savings` | `{label, optimizers: {<name>: {tokens, usd}}}` |

Segment hashes are used in memory (cache-prefix comparison, duplicate
detection) and are **not** written to the database. In memory, per session,
the analyzer keeps hashes, sizes and per-line hashes plus lengths of
system-level segments (never text), for at most 3 recent requests, and it
evicts sessions idle for 2 hours or beyond 64 sessions.

## Token estimates

Bytes of each segment are converted to tokens using a per-model-family
bytes-per-token ratio that is learned from provider-reported totals, then the
segment estimates are rescaled (largest-remainder rounding) so they sum to the
reported total exactly. Context files are detected by markers (`Contents of …
CLAUDE.md`, `# claudeMd`, `<project_instructions>`, …); a bare mention of
CLAUDE.md in a system prompt is not enough. When the provider reported no
usage the estimate uses the prior ratio and `calibrated` is false.

## Cache misses

Each request is compared with the best-matching of the session's last three
requests (concurrent sub-agents share a session). Expected cache read is the
content identical to the earlier request, wherever it now sits (system-level
segments by unchanged lines), capped at what the provider had cached for it.
A miss is reported when the shortfall against the provider-reported cache
read exceeds a tolerance (512 tokens Anthropic, 2,048 OpenAI) and 10% of the
expectation. Reason codes are tried in this order:

1. `model_switched`
2. `below_minimum`: the shareable prefix is below the model's minimum
   cacheable length (only when the minimum is known)
3. `no_cache_markers`: Anthropic request with no `cache_control` anywhere
4. `ttl_expired`: gap since the earlier request longer than the cache lifetime
5. `tool_definitions_changed`: `reordered`, `added`, `removed` or `modified`
6. `volatile_content`: a few lines changed in a system-level segment;
   records only the category (`date`, `id`, `path`, `other`), segment index,
   token offset and changed-line count
7. `unknown`

The cost of a miss is the extra spend of billing the missed tokens as cache
writes or input instead of cache reads, from the pricing engine.

## Waste detectors

`duplicate_tool_result`, `noisy_output`, `oversized_tool_output` (counts only
tokens above the threshold, default 10,000), `unused_tool_schemas` (tools
defined in every request but never called, after 5 requests),
`context_file_weight` (≥ 500 tokens), `expensive_model_small_task`
(suggestion, low confidence, input ≤ 4,000 and output ≤ 500 tokens on a model
priced ≥ $5/M input; compares with the most capable same-provider model at a
third of the price or less), `long_session_drift` (≥ 10 requests, ≥ 100,000
prompt tokens, four rising prompt sizes, and errors/repeated calls not
decreasing; counts the failed and repeated exchanges). Findings overlap (a
noisy output can also be oversized) and **must not be added together**.
Dollars are priced by splitting each removed span across the part of the prompt
billed as cache read, cache write and uncached input
(`pricing.CostWithoutAll`).

## Shadow mode

Optimizer names match the optimizers in `internal/optimize` (see
`docs/optimize.md`); each is derived from the detector it would act on: `duplicate_result_collapser`,
`noise_normalizer`, `output_trimmer`, `prune_unused_tools`. Per request
the estimate equals the matching finding. Cache effects: tokens removed from
the cached prefix are valued at the cache-read price, from the written part at
the write price, from the rest at the input price. Optimizers change only at
session boundaries, so the steady-state cached prefix stays valid; no
one-off re-write is charged. Roll-ups per session and per UTC day are computed
from the stored rows (`SessionRollup`, `DayRollup`); each optimizer is summed
on its own, so rows are not additive across optimizers.

## Verified provider facts

Checked against the providers' own documentation on **2026-10-29**:

- Anthropic, <https://platform.claude.com/docs/en/build-with-claude/prompt-caching>
  - Minimum cacheable prompt: 512 tokens (Fable 5.1, Mythos 5.1, Opus 5.5,
    Opus 5, Sonnet 5.5, Fable 5, Mythos 5, Haiku 5.5); 1,024 (Opus 4.8,
    Sonnet 5, Sonnet 4.6, Sonnet 4.5, Opus 4.1, Opus 4, Sonnet 4); 2,048
    (Mythos Preview, Opus 4.7, Haiku 3.5); 4,096 (Opus 4.6, Opus 4.5,
    Haiku 4.5). Shorter prompts are processed without caching and no error.
  - TTL 5 minutes (default) or 1 hour (`"ttl": "1h"`), refreshed at no cost on
    each use, measured from the start of the request.
  - Up to 4 breakpoints; the hierarchy is tools → system → messages and a
    change invalidates that level and everything after it; lookback window of
    20 positions per breakpoint.
  - Usage: `cache_read_input_tokens`, `cache_creation_input_tokens`,
    `cache_creation.ephemeral_{5m,1h}_input_tokens`, `input_tokens`.
  - Only the first 100,000 characters of the page were read; the rest was not
    checked.
- OpenAI, <https://developers.openai.com/api/docs/guides/prompt-caching>
  - GPT-5.6 and later: 1,024 visible input tokens minimum; earlier models vary
    with request settings and the page gives no fixed number (so
    `below_minimum` is not reported for them).
  - Caching is automatic; implicit breakpoints every 2,048 tokens on GPT-5.5,
    `cached_tokens` rounded down to a multiple of 128 on earlier models.
  - Retention: `in_memory` typically 5 to 10 minutes of inactivity, up to one
    hour; `24h`; GPT-5.6+ `30m` (default) or `12h`. `ttl_expired` is therefore
    reported for OpenAI only past one hour.
  - Cached tokens are in `usage.input_tokens_details.cached_tokens`. The page
    does not mention `prompt_tokens_details.cached_tokens`; the gateway's
    usage adapters were verified separately.

Re-verify these before changing `facts.go`.

## History ledgers (Claude Code, added 2026-11-23)

Code: `internal/analyze/ledger.go`, `ledger_output.go`; loaded by
`internal/measure/ledger.go`; shown by `trove report` as **Cache rebuilds** and
**Output by kind**. Both ledgers are worked out when the report is built, from
numbers and codes already in `history_requests` and `history_tool_calls` (see
[logs.md](logs.md)). Nothing new is stored. They are pure functions: the same
rows always give the same result, whatever order they arrive in.

Requests are grouped by session and agent (the main conversation, or one
subagent) and ordered by time. Every request of a session that has a request in
the report period is loaded, so the period's first request is compared with its
real predecessor; only entries inside the period are counted.

### Rebuild ledger

- **New content** = this request's context minus the agent's previous request's
  context (never below 0), where context = uncached input + cache writes + cache
  reads. All three are measured counts from the log.
- **Rebuild**: a cache write over `RebuildMinTokens` (**20,000**) tokens **and**
  over `RebuildFactor` (**3**) times the new content. Below that, a write is the
  new turn's own content being cached. A test pins both constants.
- A first request has no predecessor, so it counts when its write is over
  20,000 tokens. For a subagent that is the `subagent_start` cause. For the main
  conversation it is `session_start`: listed, not counted as a rebuild, because
  it builds a cache rather than rebuilding one.
- **Cause**, the first that matches wins:

  | Code | Rule |
  |---|---|
  | `idle_expired` | Gap since the agent's previous request is longer than the cache lifetime in effect |
  | `model_changed` | Model differs from the agent's previous request |
  | `subagent_start` | A subagent's first request |
  | `compaction` | A compaction boundary came since the previous request, or the request follows a compaction summary |
  | `version_changed` | Claude Code version differs |
  | `effort_changed` | Effort differs (per-turn effort when known), only on models that keep one cache per effort level |
  | `fast_mode_changed` | Fast mode switched on or off |
  | `unexplained` | None of the above: a candidate bug, or a cause the log does not show |

- **Cache lifetime in effect**: from the agent's own writes in the log (a 1-hour
  write means 1 hour, a 5-minute write 5 minutes). Before the log has shown one,
  the documented default is used: 1 hour for the main conversation on a
  subscription, 5 minutes otherwise. Those verdicts are counted and the report
  says how many there were. The plan itself is estimated (see logs.md).
- `unexplained` also covers causes that are documented but not in the log:
  connecting or removing an MCP server, enabling or disabling a plugin, denying
  a whole tool, many images. The report says so.
- Labels: rebuild count and tokens are **measured** (a fixed rule applied to
  measured numbers). The split by cause is **estimated**, because the cause is
  inferred.

### Output ledger

- **Thinking**: `thinking_tokens` from the log, **measured**. It is already
  inside `output_tokens` and is never added to it. When the log has no count,
  thinking = output − estimated visible tokens (never below 0), **estimated**.
- **Visible text and tool input**: the rest of the output, split by the byte
  size of the response's text blocks and of its tool-call inputs. Always
  **estimated**.
- **Bytes per token**: calibrated per model family from requests with a measured
  thinking count (their non-thinking output against their text and tool-input
  bytes). It needs at least 20 such requests and 2,000 tokens; otherwise the
  prior of 3.5 bytes per token is used. The report names the ratio and where it
  came from.
- **Phase**: what came right before the request in the log: a typed prompt
  (`prompt`), a tool result (`tool_result`), a compaction summary (`compact`) or
  nothing recognizable (`other`). Lines Claude Code writes itself (`isMeta`)
  do not change it.
- Requests read before Trove recorded sizes (parser version 3) and with no
  thinking count cannot be split. Their output is counted in Output only. Their
  table rows show `-`, and the report says how many tokens that covers.
- **Thinking carried in context**: for each request, the agent's earlier
  thinking since the last compaction or model switch, summed over requests.
  Counted only on models that the preserved-thinking page lists as keeping
  earlier thinking (Fable and Mythos 5.1 and 5.2, Opus 5.5, Sonnet 5.5, Haiku
  5.5). It is an **estimated upper bound**: no page says the tokens carried
  equal the original thinking count, the API drops blocks a model cannot use,
  and Claude Code may trim what it sends back.

### Verified (2026-11-23)

- <https://code.claude.com/docs/en/prompt-caching>, read in full:
  - Cache lifetime: one hour for the main conversation on a Claude subscription
    within plan usage; five minutes on usage credits, an API key or a cloud
    provider. Subagents, workflows, compaction, forks and session titles get
    five minutes unless set otherwise (`promptCacheTtl`,
    `subagentPromptCacheTtl`, `FORCE_PROMPT_CACHING_5M`,
    `ENABLE_PROMPT_CACHING_1H`).
  - Actions that invalidate the cache: switching models; changing effort
    (except on Fable 5.1 and later, Opus 5.5, Sonnet 5.5 and Haiku 5.5);
    turning on fast mode; MCP server or plugin changes; denying a whole tool;
    compacting; many images; upgrading Claude Code.
  - "Its first request doesn't read the parent's cache" (subagents).
- <https://platform.claude.com/docs/en/build-with-claude/preserved-thinking>,
  read in full: "On models with preserved thinking, usable blocks stay in
  context and are billed as input." The extended-thinking page says Opus 4.5
  and models numbered 4.6 and higher also keep earlier thinking. The two pages
  disagree on Opus 5 and Sonnet 5, so only the preserved-thinking list is
  counted.
- Observed in local transcripts, not documented: compaction is a `system` line
  with subtype `compact_boundary`, followed by a user line with
  `isCompactSummary`. Subagent lines carry `agentId`. See logs.md.

## Known limits

- Context-file detection and the noise detectors are heuristics; confidence is
  `medium` or `low` accordingly.
- A restarted gateway forgets session state: the first request after a restart
  has no cache comparison, and `unused_tool_schemas` starts counting again.
- Request images and other non-text content have no segment bytes; their
  tokens are spread over the text segments by the calibration.
- Gemini requests are not analyzed.
- History ledgers: the compaction request itself does not appear to be written
  to the transcript, so its own cost is not visible; only the rebuild after it
  is. Causes the log does not record (MCP, plugin or tool-list changes) land in
  `unexplained`. Text blocks are summed per log line. On the versions checked,
  each line holds a different block, but a version that repeated blocks across
  lines would overstate visible text. The ledgers cover Claude Code only.
