# Gateway (pass-through mode)

`trove gateway start` runs an opt-in local gateway on 127.0.0.1. `trove setup` points Claude Code and
Codex CLI at it (with your consent); `trove undo` reverses that exactly. Nothing in the request or
response is modified in this mode; Trove only records metadata.

## Routes
- `/anthropic/...` forwards to `upstreams.anthropic`, `/openai/...` to `upstreams.openai`. Any path
  under those prefixes is forwarded verbatim, so new API features keep working.
- `/trove/health` returns status JSON.

## Guards
- Binds to loopback only (`--listen` with a non-loopback address is refused).
- Requests whose `Host` is not `localhost`, `127.0.0.1` or `[::1]` are refused (DNS rebinding), as is any
  request with an `Origin` header (web pages).

## API keys only
- Anthropic: the credential must be in `x-api-key`. An `Authorization` header is allowed only if it
  repeats the same value (Claude Code's `apiKeyHelper` sends both). A bearer-only request, a different
  bearer, or an OAuth value in `anthropic-beta` is refused.
- OpenAI: `Authorization: Bearer <key>`. A JWT-shaped token (a ChatGPT/OAuth session token) is refused.
  This is a heuristic; API keys are not documented to be JWTs.
- Refusals are HTTP 401 in the provider's own error shape with a plain message. Credentials are only
  compared in memory, never logged, stored or echoed.

## What is recorded (metadata only)
One row per response: provider, model, endpoint (no query string), streamed, status, latency, time to
first byte (`ttft_ms`; for streams, the first SSE bytes, not the first generated token), token buckets,
`usage_raw` (the provider's usage object, numbers only), `service_tier`, cost from `internal/pricing`
(only when every part was priced exactly; otherwise empty), agent label and version, session key.

## Session key
1. Claude Code's `x-claude-code-session-id` header, hashed.
2. Otherwise a SHA-256 of the system prompt / instructions plus the first user message.
   Limits: identical prompts in different runs share a key (split only by the idle gap); an agent
   that rewrites its first message or system prompt each turn fragments into many sessions.
3. A gap longer than `gateway.idle_minutes` (default 30) starts a new session.

## Known gaps
- Brotli/zstd-compressed responses are forwarded untouched but their usage is recorded as unavailable
  (gzip/deflate are decoded on the side copy).
- OpenAI Responses usage, the Anthropic per-TTL cache-write split and the Anthropic non-streamed usage
  field list are awaiting official schema text (see `usage.New`). Until then those requests are recorded
  without token counts, and cache writes are left unpriced.
- Fixtures are hand-built from the documented event shapes, not recorded from live traffic.
- Go may reorder or re-case header names; bodies are byte-identical.
