# Keep-warm pings (`keep_warm`)

Gateway behavior for API-key traffic only. Approved, with its limits, in [decisions/keep-warm.md](decisions/keep-warm.md) (2026-11-17). Plan item 4.3.

## What it does

The provider's default prompt cache lives 5 minutes, counted from the start of the request that wrote or last read it, and every read restarts that lifetime. If an API-key session goes quiet for longer, its next request writes the whole prefix again at 1.25x the input price. Keep-warm re-reads the prefix about 4.5 minutes after the last request (or ping) started, so the cache is still there when you come back.

A ping is the session's own last request, sent again to the same endpoint with the same key and the same prompt, with `max_tokens` set to `0` and `stream` removed. `max_tokens: 0` is the provider's documented pre-warm request: it reads the prompt, refreshes the cache and bills no output.

Provider details checked on 2026-11-17 against platform.claude.com/docs/en/build-with-claude/prompt-caching and platform.claude.com/docs/en/about-claude/pricing:
- a cache read restarts the 5-minute lifetime, and the lifetime runs from the request's start;
- 5-minute cache writes cost 1.25x base input, 1-hour writes 2x, reads 0.1x (0.05x on Opus 5.5 and Sonnet 5.5, 0.025x on Fable 5.x); Trove takes the rate for your model from its pricing table;
- a `max_tokens: 0` request is refused with `stream: true`, extended thinking (`thinking.type: "enabled"`), structured outputs or a `tool_choice` of `tool` or `any`; `max_tokens` and `stream` are not among the settings that invalidate the cache.

## When it pings

Only all of these:
- `keep_warm` is `on` (`keep_warm = "on"` under `[optimizers]` in `config.toml`), and was when the session's request arrived;
- the request went to the provider's own API (`/anthropic/v1/messages`) with an API key, and succeeded;
- it has a cache marker, none with the 1-hour lifetime, and none of the settings the provider refuses for `max_tokens: 0`;
- the response showed cache tokens written or read.

It stops, and drops the held key and prompt, after `gateway.keep_warm_max_pings` pings (default 2, at most 6), on any failed or unpriced ping, when the day's ping spend reaches `gateway.keep_warm_daily_cap_usd` (default and maximum $1), when the spend guardrail trips, when the session's next real request replaces it, when `keep_warm` is turned off or the kill switch is on, and when the gateway stops.

Spend guardrail: over the last 24 hours, once at least 6 pings were sent, pinging stops if the pings cost (measured) more than they are estimated to have saved (cache reads on the next real request that pings kept warm, priced as a 5-minute write minus a read). The guardrail and the daily cap are held in memory, so a gateway restart starts them afresh.

## Shadow mode

`keep_warm = "shadow"` holds no key and no prompt and sends nothing. When a session's next request arrives, its row gets a `keep_warm` shadow entry, labeled estimated: what pings would have saved across the gap (a 5-minute write minus a read, minus the pings' reads), or, when the gap was longer than the pings could bridge, what the wasted pings would have cost (negative).

## What is recorded

Each ping is a usage row on the same session, with agent `keep_warm` and `keep_warm` in `optimizations_applied`, so its cost is on your bill and in every spending figure. Known gap: a ping row also counts as a request in the session's request count.

## Manual check (needs an API key; not run in CI)

Last run: Live check 2026-11-17 (curl through the gateway, claude-haiku-5-5, 3,723-token cached system prompt, 5-minute lifetime): the first request wrote 3,723 cache tokens; two keep_warm pings at about 4.5 and 9 minutes each read 3,723 tokens with 0 output ($0.0000383 each); the same request sent 10.5 minutes after the first read all 3,723 tokens and wrote none. A request with adaptive thinking was also pinged: the provider accepted it (200) and it read the cache. Not yet checked with a real agent session (Claude Code or a custom agent) or on other models.

The "done when" condition is that the next real request after pings shows a cache read. To check it:

1. Use a fresh data dir (`export TROVE_DATA_DIR=$(mktemp -d)`), put `[optimizers]` / `keep_warm = "on"` in its `config.toml`, and run `trove gateway start`.
2. Point a custom agent or `curl` at `http://127.0.0.1:8787/anthropic/v1/messages` with your API key and a request whose system prompt is above the model's minimum cacheable length and carries `"cache_control": {"type": "ephemeral"}`.
3. Wait about 9 minutes without sending anything. The gateway sends two pings.
4. Send the same request again.
5. Check the rows: `sqlite3 "$TROVE_DATA_DIR/trove.db" "SELECT agent, cache_write_tokens, cache_read_tokens, cost_usd FROM requests ORDER BY id"`. Want: the first row writes the cache, the two `keep_warm` rows read it with 0 output tokens, and the last real row reads it (cache_read_tokens > 0, cache_write_tokens near 0).
6. For contrast, repeat with `keep_warm` off: the last row writes the cache again.
7. Also confirm with a request using adaptive thinking (`"thinking": {"type": "adaptive"}`) that the provider accepts the ping; the docs list only `"enabled"` as refused. Record the result, date and agent in [decisions/keep-warm.md](decisions/keep-warm.md).
