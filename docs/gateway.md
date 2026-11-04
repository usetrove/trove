# Gateway (pass-through mode)

`trove gateway start` runs an opt-in local gateway on 127.0.0.1. `trove setup` points Claude Code and
Codex CLI at it (with your consent); `trove undo` reverses that exactly. Nothing in the request or
response is modified in this mode; Trove only records metadata.

## Routes
- `/anthropic/...` forwards to `upstreams.anthropic`, `/openai/...` to `upstreams.openai`. Any path
  under those prefixes is forwarded verbatim, so new API features keep working.
- `/trove/health` returns status JSON.
- Optional routes, off until configured under `[upstreams]` in `~/.trove/config.toml` (or
  `TROVE_UPSTREAMS_VERTEX`, `_FOUNDRY`, `_BEDROCK`):
  - `/vertex/...` forwards to `upstreams.vertex` (an origin such as `https://aiplatform.googleapis.com`).
  - `/foundry/...` forwards to `upstreams.foundry`, a base URL that keeps its path
    (`https://<resource>.services.ai.azure.com/anthropic`).
  - `/bedrock/...` forwards to `upstreams.bedrock` (an origin such as
    `https://bedrock-runtime.us-east-1.amazonaws.com`).
  - `/compat/<name>/...` forwards to the `[[upstreams.compat]]` entry with that name (see below).
  Point the matching Claude Code variable at the route: `ANTHROPIC_VERTEX_BASE_URL`,
  `ANTHROPIC_FOUNDRY_BASE_URL`, `ANTHROPIC_BEDROCK_BASE_URL`, each set to
  `http://127.0.0.1:8787/<route>` (names per the Claude Code docs, checked 2026-11-04). The cloud
  routes are tested with fake servers only, not live (see `docs/compliance.md`).

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
- Enterprise and Team organizations: supported when developers use an API key (a Claude Console key,
  `ANTHROPIC_API_KEY`, or `apiKeyHelper`), because that is an ordinary `x-api-key` request. Developers who
  sign in to Claude Code with a claude.ai account use an OAuth login, even on Enterprise, and are refused.
  (Per code.claude.com/docs/en/authentication, checked 2026-11-03.)
- Every claude.ai login is refused on every route, on any plan: Free, Pro, Max, Ultra, Team and Enterprise.
  Signals: an OAuth value in `anthropic-beta`, a credential starting `sk-ant-oat` (from `/login` or
  `claude setup-token`; the prefix is an assumption, kept as a second signal), and on `/anthropic` a bearer
  token with no `x-api-key` (this also refuses Console OAuth and federation tokens). The message says gateway
  mode needs an API key or cloud credential, and to use a subscription remove the custom endpoint
  (`trove undo`) and use the Trove plugin.
- `/vertex`: a Google bearer token. `/foundry`: `x-api-key`, `api-key` or an Entra ID bearer token (a JWT is
  normal there, so the JWT heuristic does not apply). `/compat/...`: the agent's bearer key, or none when the
  entry says `auth = "none"`; keys there are opaque, so only a ChatGPT `chatgpt-account-id` header is refused.
- `/bedrock`: an Amazon Bedrock API key (`AWS_BEARER_TOKEN_BEDROCK`) is forwarded. An AWS SigV4-signed request is
  refused with an explanation. SigV4 signs the request body hash and the `Host` header, so any change breaks it, and
  a request signed for `127.0.0.1` would not match AWS's own host anyway. Trove never holds AWS credentials to
  re-sign.
- Refusals are HTTP 401 in the provider's own error shape with a plain message. Credentials are only
  compared in memory, never logged, stored or echoed.

## OpenAI-compatible servers (self-hosted and open-weight)
For OpenAI-format agents such as Codex CLI, OpenCode or your own code. Claude Code is not supported against
non-Claude models through any gateway (Anthropic's docs, checked 2026-11-04).

```toml
[[upstreams.compat]]
name = "vllm"                         # the route is /compat/vllm/...
base_url = "http://10.0.0.5:8000/v1"  # https, or http for localhost and private networks
auth = "bearer"                       # or "none" for an open server
input_per_mtok = "0.60"               # optional custom rates, USD per million tokens
output_per_mtok = "2.40"
cache_read_per_mtok = "0.15"          # optional
gpu_hourly_usd = "2.50"               # optional: your machine's hourly cost, for a GPU-hour estimate
```

The request path after `/compat/vllm` is appended to `base_url`. If your agent's base URL is
`http://127.0.0.1:8787/compat/vllm/v1`, set `base_url` to the server origin without `/v1`.

- Pricing: custom rates price each row (labeled estimated, version `custom:<hash>`). Rows without rates have no
  cost. `gpu_hourly_usd` is applied in `trove report`, not per row: your hourly cost divided by the tokens measured
  over busy time and over the whole span, with utilization. Both are estimates.
- Usage: from Chat Completions responses. For a streamed reply, usage appears only if the agent sets
  `stream_options.include_usage`; otherwise the row has no token counts (never a guess).
- `trove report` adds latency (p50, p95), time to first byte (streamed requests only; first response byte, not
  first generated token), and output tokens per second, all measured.

## Session kind
Add the header `X-Trove-Kind: automated` (or `interactive`) to label a session; anything else is `unknown`. Claude
Code: `ANTHROPIC_CUSTOM_HEADERS="X-Trove-Kind: automated"`. The header is removed before forwarding. Trove does not
infer kind from the User-Agent. `trove report` shows spend by kind when any request declares one.

See [legal-and-credentials.md](legal-and-credentials.md) for the provider terms behind these rules.

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

## Healthcare
Anthropic's HIPAA configuration does not cover Claude Code routed through a third-party cloud provider or a
gateway (legal-and-compliance page, checked 2026-11-04). If you rely on a BAA, use the Trove plugin only.

## Known gaps
- Cloud backends (Vertex, Foundry, Bedrock) have no list prices in `pricing.json`, so their cost is empty. The
  Bedrock event stream decoder is tested on hand-built frames; confirm against a real capture.
- Brotli/zstd-compressed responses are forwarded untouched but their usage is recorded as unavailable
  (gzip/deflate are decoded on the side copy).
- OpenAI Responses usage and the exact Anthropic non-streamed usage field list are awaiting official
  schema text (see `usage.New`). Until then those requests are recorded without token counts.
- Anthropic cache writes are priced from the `cache_creation` per-TTL split (verified against the
  prompt-caching docs on 2026-11-03). If the split is missing, or its parts do not add up to
  `cache_creation_input_tokens`, the request is left unpriced rather than guessed.
- Fixtures are hand-built from the documented event shapes, not recorded from live traffic.
- Go may reorder or re-case header names; bodies are byte-identical.
