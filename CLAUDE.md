# Trove project brief

You are helping build Trove, "the efficiency layer for AI coding agents."

## What Trove is
- A local, open-source tool, shipped as a single Go binary, that cuts the tokens AI coding agents waste, automatically, and proves results did not suffer.
- Phase A (current): developers who pay per usage with their own API keys (Anthropic, OpenAI). Trove runs an opt-in LOCAL gateway on 127.0.0.1. The user's coding agent (Claude Code, Codex CLI, or a custom agent) points at it using that agent's documented custom-endpoint setting. Trove records metadata for every request, detects waste, and applies proven, cache-safe optimizations.
- Phase B (later): config fixes, an MCP tool server and hooks that work for everyone, including subscription users, with no gateway.
- Positioning: neutral and cross-tool. One place across all of a developer's agents.

## Non-negotiable rules
1. Local-first. All data lives in ~/.trove (SQLite plus files). No telemetry, no analytics, no network calls except forwarding the user's own API requests to the provider they configured.
2. Credentials. API keys pass through in memory, untouched. Never log, store, print or persist them. Redact auth headers everywhere: logs, errors, fixtures, crash output.
3. Subscriptions. Never handle subscription or OAuth credentials. Never intercept, reroute or modify subscription traffic. Never spoof a client identity or headers. Gateway mode is for API-key authentication only; anything else is rejected with a clear message explaining why.
4. Fail open. If Trove hits an internal error while optimizing, it forwards the original request unmodified. Trove must never be the reason an agent stops working.
5. Cache safety. Transformations are pure, deterministic functions of the request, so the same history always produces byte-identical output and cached prefixes stay valid. Optimizers are switched on or off only at session boundaries, never mid-session.
6. Prove before acting. Every optimization is opt-in, has a shadow mode (estimate only, change nothing), is measured once enabled, and rolls back automatically if guardrails trip. Every number shown to users is labeled "measured" or "estimated".
7. Metadata only. Prompt text, code, file paths and tool output may be processed in memory but are never written to the database. Store token counts, sizes, hashes, model names, timings and outcome signals. The only exception is the opt-in local recovery store for trimmed output (Phase B): local, size-capped, auto-expiring, permissions 0600.
8. Verify, don't assume. For any provider API detail (endpoints, streaming event formats, usage fields, caching rules, limits, pricing) or agent behavior (config locations, custom-endpoint settings, hooks, MCP registration, log formats), check current official documentation. Tell the user what you verified, where, and when. Never rely on memory for these.
9. Accuracy over features. A wrong number shown to a user is worse than no number.

## Engineering standards
- Go (current stable), pure-Go dependencies (no cgo), one static binary. macOS and Linux first; Windows later.
- Code under internal/ in small packages with narrow interfaces. Table-driven tests, golden files for parsers and reports, recorded and redacted fixtures for API traffic. No live API calls in CI.
- The gateway adds under 5 ms p50 overhead and never buffers a stream.
- Errors are plain English and say what to do next. Every command supports --json.

## How we work
- Before coding: restate the task, list assumptions and open questions, and propose a plan. Wait for approval on anything ambiguous, irreversible, or touching files outside the repo.
- Work in small, reviewable commits with tests.
- At the end: what changed, how to test it, known gaps, and what to verify next.

## Layout
cmd/trove (entry), internal/{cli,config,store,redact} implemented in the foundation; pricing, gateway, analyze, optimize, measure, report, logs (history parsers and audit, see docs/logs.md), fixer and mcp (see docs/mcp.md; also recovery) are implemented; hooks is a placeholder for a later prompt; bench/ (Python, later).
