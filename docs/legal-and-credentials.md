# Credentials and provider terms: working notes

Not legal advice. These are notes on what the providers publish, so design decisions can be traced
back to a source. Re-verify before relying on them (CLAUDE.md rule 8); terms change.

## Anthropic / Claude Code (verified 2026-10-29)

Sources:
- https://code.claude.com/docs/en/legal-and-compliance (section "Authentication and credential use")
- https://code.claude.com/docs/en/gateways.md (section "Subscriptions and gateways")
- https://code.claude.com/docs/en/llm-gateway-connect.md (how `ANTHROPIC_BASE_URL` and credential variables behave)

What they say, in short:
- OAuth authentication is for subscription purchasers (Free, Pro, Max, Team, Enterprise). Anthropic does
  not permit developers to "route requests through Free, Pro, or Max plan credentials on behalf of their
  users", or to "collect, store, or intermediate Claude.ai credentials or session tokens".
- The restriction does not limit customers provisioning and using their own API keys, provided the
  usage is billed to the key owner and is not resold or intermediated.
- Gateways reached through `ANTHROPIC_BASE_URL` are a documented setup. Anthropic does not endorse,
  maintain or audit third-party gateway products.
- Setting only `ANTHROPIC_BASE_URL` (no API key or auth token) leaves a saved claude.ai login as the
  active credential, so subscription traffic would be sent to the gateway.
- Customers offering Claude Code inside their own products must not modify the binary or restrict its
  authentication methods. Trove does neither: it only sets a documented environment variable.
- Their stated contact for questions about permitted authentication methods is Anthropic sales.

## How Trove stays on the right side of that

- Gateway mode accepts API-key requests only (`internal/gateway/auth`). Subscription/OAuth requests get
  HTTP 401 with a plain message and are never forwarded (CLAUDE.md rule 3).
- Credentials are only compared in memory; never logged, stored or echoed (rule 2). Tests plant a fake
  key and scan logs and output for it.
- Trove runs locally for the person who owns the key; it does not resell or share access.

## Open gray area (decide before wider release)

If a subscription user runs `trove setup` and then starts Claude Code with no API key, the OAuth token
arrives at the local gateway in a request header before Trove rejects the request. Trove does not log,
store or forward it, but whether merely receiving it counts as "collect" is not settled by the docs.
Mitigation, implemented 2026-11-06: `trove setup` no longer points Claude Code at the gateway unless there is evidence it will
use an API key: `ANTHROPIC_API_KEY` is set (existence only, the value is never read), or `apiKeyHelper` or
`forceLoginMethod: console` is in its settings, or the user declared `plan = "api_key"` under `[history]` in config.toml. A
bearer-only `ANTHROPIC_AUTH_TOKEN` does not count, because the gateway refuses a bearer without an `x-api-key`. Otherwise setup
leaves Claude Code alone and says to use the plugin. Test: `internal/cli/setup_apikey_test.go`. **What it does not cover:** a user
who set the endpoint by hand, or who runs `trove setup` with a key and later logs in with Claude.ai, still sends the
login token to the gateway before it is refused. That residual case stays question 1 for a lawyer. Codex CLI is not affected:
Trove's provider entry reads the key from `OPENAI_API_KEY`.

## Not yet checked

- Anthropic Commercial Terms, Consumer Terms and Usage Policy in full (only the Claude Code page above).
- OpenAI terms for Codex CLI and the API. Same question applies to ChatGPT-login traffic; Trove rejects
  every credential that is not an `sk-` key, and any OpenAI-issued JWT on compat routes (`internal/gateway/auth`).

## Update 2026-10-31 (OpenAI pages read)

The OpenAI Terms of Use, Service terms, Services Agreement (business terms) and Usage policies were read from
text copied out of a browser; findings are in [compliance.md](compliance.md). In short: nothing read forbids a
local, single-user gateway on the user's own API key. The closest clause is the Usage policies' "model-access
intermediary" approval requirement (effective 2026-11-20), which we read as not applying to a loopback-only tool;
that is flagged for a lawyer. The DPA and Brand Guidelines are still unread.

## Update 2026-10-30 (second pass)

The Claude Code legal page now lists "Free, Pro, Max, Ultra, Team, and Enterprise" as the OAuth plans (Ultra is
new since the list above). The Consumer Terms were read for the first time; see [compliance.md](compliance.md).
The OpenAI terms are still unread (HTTP 403).

## Update 2026-10-30

The sources above were re-read and extended (Commercial Terms, Usage Policy, the hooks, MCP, permissions and
gateway pages, and the Codex pages) in [compliance.md](compliance.md), which also lists what could not be read
(OpenAI's policy pages) and the open questions for a lawyer. Keep that file current; this one is the original
working note.
