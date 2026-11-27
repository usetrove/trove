# Trove principles

These are the rules Trove is built under. Code, tests and docs cite them by number
("rule 7"). A change that would break one needs the rule changed first, in a reviewed pull request.

## The rules

1. **Local-first.** All data lives in `~/.trove` (SQLite plus files). No telemetry, no analytics, no network calls except
   (a) forwarding the user's own API requests to the provider they configured,
   (b) the anonymous aggregate summary upload, which is off by default, happens only after the user runs `trove contribute enable`, sends exactly the text `trove contribute` shows, carries only the fields listed in PRIVACY.md, each approved in a decision under docs/decisions/ (the limits section additionally needs its own `trove contribute limits enable`, see [docs/decisions/limit-report.md](docs/decisions/limit-report.md)), and is never in the gateway request path,
   (c) a single request asking that same endpoint to delete what it holds under the user's own install id (`DELETE <endpoint>/<install id>`), which happens only when the user runs `trove contribute delete`, is never triggered by any other command or automatically, carries no body, credentials or cookies, follows no redirects, and is never in the gateway request path, and
   (d) keep-warm pings: when the user has enabled the keep-warm optimizer for API-key gateway traffic, the gateway may resend the session's last cached prefix, byte-identical, to the same provider endpoint with the same key, within the limits in [docs/decisions/keep-warm.md](docs/decisions/keep-warm.md). Pings are never sent for subscription or OAuth traffic, never in the request path, and never after the session ends.
   Any other outbound call needs a new rule.
2. **Credentials.** API keys pass through in memory, untouched. They are never logged, stored, printed or persisted. Auth headers are redacted everywhere: logs, errors, fixtures, crash output. The only exception is the keep-warm optimizer, which may hold a key in process memory for the lifetime of one idle session; it is still never logged, stored, printed or persisted.
3. **Subscriptions.** Trove never handles subscription or OAuth credentials, never intercepts, reroutes or modifies subscription traffic, and never spoofs a client identity or headers. Gateway mode is for API-key authentication only; anything else is rejected with a clear message explaining why.
4. **Fail open.** If Trove hits an internal error while optimizing, it forwards the original request unmodified. Trove must never be the reason an agent stops working.
5. **Cache safety.** Transformations are pure, deterministic functions of the request, so the same history always produces byte-identical output and cached prefixes stay valid. Optimizers are switched on or off only at session boundaries, never mid-session.
6. **Prove before acting.** Every optimization is opt-in, either one at a time or by the user agreeing once to a named policy ([Autopilot](docs/autopilot.md)). Each one has a shadow mode (estimate only, change nothing), is measured once enabled, and rolls back automatically if guardrails trip. A policy may only promote optimizers it names, only from shadow evidence, only at session boundaries, and never over a setting the user made. The one exception, approved by the project owner on 2026-11-24: a named policy may switch on, at the next session boundary and without shadow evidence, a T1 guard that never changes a request (it only warns the user, or stops one prompt once). It is still measured and rolled back by its own guardrail ([the optimize policy](docs/autopilot.md#the-optimize-policy)). Every number shown to users is labeled "measured" or "estimated".
7. **Metadata only.** Prompt text, code, file paths and tool output may be processed in memory but are never written to the database. Trove stores token counts, sizes, hashes, model names, timings and outcome signals. The only exceptions are the opt-in local recovery store for trimmed output (local, size-capped, auto-expiring, permissions 0600) and the keep-warm optimizer, which may hold one session's last request prefix in process memory between requests, never on disk.
8. **Verify, don't assume.** Provider API details (endpoints, streaming formats, usage fields, caching rules, limits, pricing) and agent behavior (config locations, custom-endpoint settings, hooks, MCP registration, log formats) are checked against current official documentation, and the docs say what was verified, where and when.
9. **Accuracy over features.** A wrong number shown to a user is worse than no number.
10. **No Claude.ai logins in the gateway.** Every Claude.ai subscription login is rejected, including Team and Enterprise. Claude.ai credentials and session tokens are never collected, stored or forwarded.
11. **No scraping, no reverse engineering.** Trove never scrapes provider web pages (for example usage meters) and never decompiles or reverse engineers an agent. It uses documented extension points and logs.
12. **No training on model output.** No model is trained on model output content (generated code or text), including in the benchmark lab. Only metadata, derived features and outcomes.
13. **Naming.** No "Claude", "Anthropic", "Codex" or "OpenAI" in any product, plugin, feature, package or command name. Plain-text "works with Claude Code" is fine.
14. **No reselling.** Trove never pays for, resells or intermediates model usage. Users always bring their own keys or subscriptions.
15. **Overhead is charged.** Every optimizer declares its own token overhead (tool definitions, injected text) and is charged for it.

## Technique tiers

Every optimization has a tier, enforced by `internal/technique/registry.go`. See [docs/techniques.md](docs/techniques.md).

- **T1 Safe:** lossless or recoverable, deterministic, visible diff, mechanical checker. Shadow first, then A/B. Autopilot may switch it on after shadow evidence and an A/B verdict.
- **T2 Behavioural** (changes what the agent is told, such as brevity rules or verbosity steering): everything T1 requires, plus its per-request overhead is charged, the test-pass guardrail is on, a confounder tag is recorded, and the user explicitly opts in. Off by default; Autopilot only after a benchmark verdict.
- **T3 Routing** (model, effort, subagent model, local offload): everything T2 requires, plus the cache-stickiness rule, a benchmark verdict per agent and model family, never on subscription or OAuth traffic, and the strictest rollback threshold.

## Measurement

Every optimization is judged against the actual bill and task outcomes using session-level A/B comparisons, never against a hypothetical raw-output baseline. See [docs/measurement.md](docs/measurement.md) and [docs/benchmark-methodology.md](docs/benchmark-methodology.md).

## Architecture

One optimizer engine, many adapters: the local gateway, Claude Code hooks, the OpenCode plugin and the settings writer. Adapters hold no optimization logic, and features are kept identical across adapters wherever the agent allows. See [docs/adapters.md](docs/adapters.md).

## Engineering standards

- Go (current stable), pure-Go dependencies (no cgo), one static binary.
- Code under `internal/` in small packages with narrow interfaces. Table-driven tests, golden files for parsers and reports, recorded and redacted fixtures for API traffic. No live API calls in CI.
- The gateway adds under 5 ms p50 overhead and never buffers a stream.
- Errors are plain English and say what to do next. Every command supports `--json`.
- Small, reviewable commits with tests.
