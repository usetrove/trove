# Optimizers (`internal/optimize`)

Optimizers rewrite the request the gateway forwards, to remove tokens that carry
no information. They are **off by default**, apply to **Anthropic Messages
requests only** (`/v1/messages`), and are built around three promises:

- **Fail open.** Any panic, error, malformed request or time-budget overrun and
  your original request is forwarded, byte for byte. Trove is never the reason
  an agent stops working.
- **Cache safe.** An optimizer is a pure function of the request bytes. Your
  agent resends its whole history every turn, so each old block must come out
  identical every time or the provider's cached prefix is invalidated.
- **Proven before acted on.** Every optimizer has a *shadow* state that runs it,
  records what it would have removed, and forwards the original.

## States and sessions

Set each optimizer under `[optimizers]` in `~/.trove/config.toml`:

```toml
[optimizers]
noise_normalizer = "shadow"   # off | shadow | on
```

| State | The request sent upstream | Recorded |
|---|---|---|
| `off` (default) | original | nothing |
| `shadow` | original | tokens it would have removed (estimated) |
| `on` | optimizer's output | tokens removed (estimated), estimated cost difference |

**A state is fixed when a session starts.** The first request of a session pins
the optimizer states, the custom-agent list and the bytes-per-token ratio
(`session_pins` table). Edits to `config.toml` apply to the next session, and a
gateway restart in the middle of a session reloads the pin, so output does not
change. Sessions are identified as described in `docs/gateway.md`; the key is
derived from the original body, before any transform.

## Pipeline order

Fixed, because order changes bytes:

| # | Optimizer | Tier | Applies to |
|---|---|---|---|
| 1 | `volatile_prefix_mover` | lossless (position changes) | custom agents only |
| 2 | `noise_normalizer` | lossless | tool results |
| 3 | `output_trimmer` | recoverable | tool results (**shadow until `trove mcp install`**) |
| 4 | `duplicate_result_collapser` | lossless | tool results |
| 5 | `cache_breakpoints` | lossless | custom agents only |

The system prompt is rearranged first, tool output is cleaned before duplicates
are compared (so two results that differ only in ANSI codes still match), and the
breakpoint decision is made last, on the final prompt.

A **shadow** optimizer runs on the request as the earlier `on` optimizers left
it, so its figure is exactly what it would remove if turned on with the same
neighbors. Shadow and `on` report the same `tokens_removed` for the same request.

## The optimizers

**`noise_normalizer`** strips ANSI escape codes, drops all but the last frame of
a progress bar or braille spinner, resolves carriage-return redraws the way a
terminal would, and collapses three or more identical consecutive lines into the
line plus `(repeated N times)` (N is how many times it appeared in a row). It
never removes a line that differs from its neighbors, and bullet lists, tables
and diffs are never treated as spinners. Shared patterns live in
`internal/noise`, which the waste detector also uses.

**`duplicate_result_collapser`** replaces a tool result that is byte-identical to
an earlier one (at least 200 bytes, successful, text only) with a short note
naming the earlier message and tool call. The first occurrence is never changed.
Near-misses, error results and mixed content are left alone.

**`cache_breakpoints`** is for agents that do not set caching markers themselves.
It only runs when the agent is marked custom, the request has no `cache_control`
anywhere, the model is known and the prompt is at least the model's minimum
cacheable size. It adds a top-level automatic `cache_control` (so no message is
edited), plus markers on the last tool definition and the last system block with
no volatile line: at most 3 of the provider's 4 allowed breakpoints, default
5-minute lifetime. Claude Code and Codex manage their own caching and are never
touched.

**`volatile_prefix_mover`** moves lines that change often (ISO dates and
timestamps, UUIDs, Unix epoch values, "today's date" phrases) from the system
prompt into a final system block, so the stable text in front of them can be
cached. Nothing is deleted. It decides from a single request, so it uses a
stateless line classifier rather than the cross-request comparison the cache-miss
analysis uses. Custom agents only.

**`output_trimmer`** keeps failures, stack traces, a little context and the first
and last lines of large test and build output, and replaces the rest with a
marker naming the line count and the output's ID (the first 16 hex characters of
the sha256 of the full output). Before a trimmed request is forwarded, the gateway
saves the full output in the local recovery store (`~/.trove/recovery`, owner-only
files, 200 MB and 7 days by default; see `[recovery]` in `config.toml`), and the
agent gets it back with the `trove_fetch_full` tool of the Trove MCP server. The saved
copy is the output exactly as the tool printed it, byte for byte. When an earlier optimizer
such as `noise_normalizer` has cleaned the text the model sees, the stored copy and the
marker's hash still name the raw output. If the output cannot be saved, the request is
forwarded unchanged.

The trimmer can be `on` only when the MCP server is registered with an agent
(`trove mcp install`; see `docs/mcp.md`). Until then `on` runs as `shadow`, which
only estimates. Like every optimizer it is pinned per session, so registering the
server or changing the setting applies from the next session.

### Marking a custom agent

```toml
[optimize]
custom_agents = ["sdk"]   # allowed labels: "sdk", "other"
budget_ms = 25
```

Labels come from `gateway/agent`. `claude_code` and `codex` cannot be listed.

## Safety and the kill switch

- Each optimizer runs behind a recover and the time budget (`budget_ms`,
  default 25). The whole pipeline falls back to the original request if any `on`
  optimizer fails; a failing shadow optimizer is recorded and skipped.
- Edits are applied to the original bytes, not by re-serializing, so keys,
  whitespace and existing `cache_control` markers outside an edited span are
  untouched. The final body is checked once before it can be forwarded.
- Requests are skipped untouched when they are not `/v1/messages`, carry a
  `Content-Encoding`, or exceed 16 MiB.
- **Kill switch:** `trove optimize off` (creates `~/.trove/optimize.off`) or
  `TROVE_OPTIMIZE=off` in the gateway's environment. A running gateway checks on
  every request. Switching off mid-session changes what the provider sees, so
  expect one cache re-write; `trove optimize resume` undoes it.

Baselines, measured savings and automatic rollback to shadow are described in
[measurement.md](measurement.md). While the baseline runs, `on` is held at shadow.

## What is recorded

Per request, in the existing `requests` row (metadata only): `optimizations_applied`
(optimizer, state, bytes and tokens removed, or the failure class),
`tokens_removed` (optimizers in `on` state), and `cost_diff_usd`.

Both numbers are **estimated**. `tokens_removed` is removed bytes divided by a
bytes-per-token ratio (3.5, pinned per session). `cost_diff_usd` prices the
removed tokens as if they had still been sent, spread over input, cache reads
and cache writes in the same proportion the request was billed
(`pricing.CostWithAdded`). It is empty when the request could not be priced
exactly.

## Suggestions (never applied)

`trove optimize suggest [--days N]` reads recorded metadata and offers:

- **Model choice**: small tasks on a frontier model, from the analyzer's
  `expensive_model_small_task` findings. Low confidence; Trove cannot check that
  the cheaper model would cope.
- **Batch API**: non-streaming traffic from scripts and custom agents. The
  Message Batches API is 50% off with most batches done within an hour (24-hour
  limit); cache hits in batches are best-effort.
- **Reasoning effort**: models where output tokens are a large share of spend.
  Anthropic publishes no savings figure for lower effort, so the suggestion
  carries a "per 10% fewer output tokens" scenario and no savings estimate.

## Provider facts checked (2026-10-29)

| Fact | Source |
|---|---|
| At most 4 explicit `cache_control` breakpoints; top-level automatic caching exists and uses one slot; minimum cacheable tokens by model (512 / 1,024 / 2,048 / 4,096) | https://platform.claude.com/docs/en/build-with-claude/prompt-caching |
| OpenAI caching is automatic; explicit markers only on GPT-5.6+ (why `cache_breakpoints` is Anthropic-only) | https://developers.openai.com/api/docs/guides/prompt-caching |
| Batch API: 50% discount, most batches under 1 hour, 24-hour expiry | https://platform.claude.com/docs/en/build-with-claude/batch-processing |
| `output_config.effort`: low / medium / high / xhigh / max; default high except Opus 5.5 and Haiku 5.5 (medium) | https://platform.claude.com/docs/en/build-with-claude/effort |

The prompt-caching page was read only through its first 100,000 of about 166,000
characters. Re-check the minimums table before changing it, and update
`minCacheTokens` (with its date) in `breakpoints.go`.

## Performance

`go test ./internal/optimize -bench .` and `TestEngineStaysWithinTheGatewayBudget`
(skipped under `-short`). On the development machine (AMD Ryzen 5 5500U), with
every optimizer on and changing the request on every call (the worst case):

| History size | Per request |
|---|---|
| ~63 KB | ~1.0 ms |
| ~217 KB | ~2.5 ms (p50), under the 5 ms gateway budget |
| ~2 MB | ~21 ms |

Most of that is re-reading the body after each optimizer that changes it. The
default install (all off) pays nothing: the gateway skips the engine. Tool-result
strings that were seen on an earlier request are memoized by content hash; the
memo only saves time and never changes output (`TestMemoNeverChangesOutput`).

## Known limits

- Anthropic Messages only. OpenAI requests pass through and keep their analyze
  shadow estimates.
- The volatile-line classifier is narrower than the cross-request detection in
  `analyze`: it knows dates, UUIDs, epoch values and a few phrases.
- Switching the kill switch on, or an agent compacting its history, costs one
  cache miss.
- The cache-breakpoint decision depends on prompt size. It cannot flip back
  within a session, but the request on which it first turns on has a different
  prefix from the one before it.
- Guardrail trips roll the optimizer back to shadow automatically. The gateway
  checks every 5 minutes (`measure.Run` in `internal/cli/gateway.go`), so a trip
  takes effect within that interval. See [measurement.md](measurement.md) section 4.
- Stored `shadow_savings` rows written before these optimizers existed use the
  provisional names `dedupe_tool_results`, `strip_noise` and
  `truncate_oversized_output`.
