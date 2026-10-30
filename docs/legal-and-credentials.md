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
Possible mitigation: make `trove setup` refuse unless `ANTHROPIC_API_KEY` is set (check existence only,
never read the value). Not implemented.

## Not yet checked

- Anthropic Commercial Terms, Consumer Terms and Usage Policy in full (only the Claude Code page above).
- OpenAI terms for Codex CLI and the API. Same question applies to ChatGPT-login traffic; Trove rejects
  JWT-shaped bearer tokens as a heuristic (`internal/gateway/auth`).

## Update 2026-10-30

The sources above were re-read and extended (Commercial Terms, Usage Policy, the hooks, MCP, permissions and
gateway pages, and the Codex pages) in [compliance.md](compliance.md), which also lists what could not be read
(OpenAI's policy pages) and the open questions for a lawyer. Keep that file current; this one is the original
working note.
