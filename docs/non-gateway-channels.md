# Reaching developers without the gateway: research notes

Written 2026-11-03, updated 2026-11-04 (section 5). Status: Step 2 of the research plan is done for three channels (OpenTelemetry export,
hooks, statusLine). Steps 1, 3, 4 and 5 are not done. Not legal advice; re-verify before relying on it
(CLAUDE.md rule 8).

Why this exists: the gateway only accepts API-key traffic (rule 3). Developers who sign in with a claude.ai
login, including Team and Enterprise through SSO, send an OAuth credential and are refused. The share of
developers in that group is **not measured**; "about 97%" is an unverified guess.

Nothing here changes how credentials are handled. Each channel below is a documented, credential-free path.

## 1. Claude Code OpenTelemetry export (verified 2026-11-03)

Source: https://code.claude.com/docs/en/monitoring-usage (first 100,000 characters read, then a search of the
rest for the OAuth wording; the page is about 173,000 characters).

- Enabled with `CLAUDE_CODE_ENABLE_TELEMETRY=1` plus `OTEL_METRICS_EXPORTER` / `OTEL_LOGS_EXPORTER=otlp`,
  `OTEL_EXPORTER_OTLP_PROTOCOL` and `OTEL_EXPORTER_OTLP_ENDPOINT` (the docs' example is `http://localhost:4317`).
- The `claude_code.api_request` event carries, per request: `model`, `input_tokens`, `output_tokens`,
  `cache_read_tokens`, `cache_creation_tokens`, the 5m/1h cache-write split (v2.1.300+), `cost_usd`,
  `duration_ms`, `request_id`, `speed`, `query_source`, `effort`, and attribution to the agent, skill, MCP
  server and MCP tool.
- It works for OAuth logins. The page shows an `auth_method` attribute with values such as `"oauth"`, and says
  `user.email` is included "when authenticated via OAuth", sent only to the endpoint you configure.
- Prompt text, tool arguments, file paths and raw API bodies are **off by default** and each has its own opt-in
  variable. Trove must never ask users to turn those on.
- Managed settings can lock the OTLP endpoint, protocol and credentials. **An Enterprise admin may therefore
  stop a developer from pointing telemetry at a local Trove receiver.** Not yet checked per plan.
- Claude Code does not pass `OTEL_*` variables to hooks, Bash or MCP subprocesses.

What it would give Trove: near-real-time per-request usage and cost for OAuth users, the same token figures the
history files already hold (docs/logs.md), plus `cost_usd`, `query_source` and `effort`. **It measures; it
cannot change a request.**

Open points before building: a loopback-only OTLP receiver is a new inbound listener (no outbound call, so
probably consistent with rule 1, but it needs a rule-1 review); it must drop `user.email`, `user.id` and any
content attribute before storing (rule 7); the receiver and Claude Code versions must be tested together.

## 2. Hooks (verified 2026-11-03)

Source: https://code.claude.com/docs/en/hooks (about 200,000 of 286,000 characters read).

- 33 events. Every event gets `session_id`, `transcript_path`, `cwd`, `prompt_id` and more.
- Output options that matter for token savings:
  - `PreToolUse`: allow, deny, ask, or `updatedInput` (rewrite the tool's arguments before it runs).
  - `PostToolUse`: `updatedToolOutput` **replaces what Claude sees as the tool's output**, for all tools. For
    built-in tools the value must match the tool's output shape (for `Bash`: `stdout`, `stderr`,
    `interrupted`, `isImage`); a mismatch is ignored and the original is used.
  - `additionalContext` on many events, over 10,000 characters is saved to a file with a 2,000-character preview.
  - `PreCompact` and `UserPromptSubmit` can block, but `UserPromptSubmit` cannot rewrite the prompt.
- The tool has already run when `PostToolUse` fires, and OpenTelemetry records the original output.
- Not documented in what I read: whether replaced output affects the prompt cache, and any size limit.
  `tool_response` shape varies per tool.
- Hook input has **no usage, token or cost fields** in what I read.

Trove today (`internal/hooks/run.go`): only `PreToolUse` deny (repeat reads) and an `additionalContext` hint.
**`updatedToolOutput` is not used.** This is the largest untapped lever found so far: trimming noisy tool
output for OAuth users with no gateway. It still has to pass rule 6 (shadow first, measured, rollback) and rule 5
(a deterministic function of the input, so the cached prefix stays valid; the cache effect is unverified).

## 3. statusLine (verified 2026-11-03)

Source: https://code.claude.com/docs/en/statusline (searched the saved page for the field list).

- stdin JSON includes `cost.total_cost_usd` (an estimate at list price, "may differ from your actual bill"),
  `context_window.used_percentage`, `context_window.context_window_size`, and `context_window.current_usage`
  (input, output, cache creation, cache read for the last call).
- `rate_limits.*` appears only for Pro, Max and Ultra subscribers, or behind a Claude apps gateway with a spend
  limit. It does not appear for Team or Enterprise logins.
- Display only. It cannot change a request.

## 4. Prior art: Beacon (asymptote-labs/agent-beacon), source read 2026-11-03

Source: https://github.com/asymptote-labs/agent-beacon, shallow clone at commit `a2c3c80`, MIT licence, Go.
Only read, never built or run. `pkg/asymptoteobserve` (the redaction code) was not read in depth.

What it is: a memory and observability layer for coding agents, not an optimizer or a gateway. It records
sessions from about 30 tools into a normalized event log and reuses reviewed knowledge later.

How it captures Claude Code, with no proxy and no credentials:
- **Local OpenTelemetry collector** (`beacon-otelcol`, a custom OpenTelemetry Collector build) on 127.0.0.1,
  default gRPC 4317 and HTTP 4318, with redaction and filter steps before a JSONL writer.
- **It writes Claude Code's `settings.json` `env` block** (`ConfigureClaude`, `cli/beacon/internal/endpoint/harness/harness.go`):
  `CLAUDE_CODE_ENABLE_TELEMETRY=1`, OTLP logs and metrics exporters, the local endpoint, delta temporality.
  It also sets `OTEL_LOG_TOOL_DETAILS=1` and `OTEL_LOG_USER_PROMPTS=1` (content on).
  Raw API bodies (`OTEL_LOG_RAW_API_BODIES`) only with an explicit flag, and even then it keeps only a few fields.
- **Hooks** through a separate `beacon-hooks` binary on `PreToolUse`, `PostToolUse`, `PostToolUseFailure`,
  `PermissionRequest` and `SessionEnd`, matcher `*` for most.
- **Polling** `~/.claude/projects/*.jsonl` (`beacon endpoint claude sync`), with per-file cursors and a
  rotating local log (live file plus five archives, 10 MiB each).
- It cancels any per-signal endpoint, header or beta-tracing variable the user set, so telemetry can only go
  to its collector.
- No use of `updatedToolOutput` or `updatedInput` found in non-test Go code. It observes (and can deny via an
  optional policy hook); it does not trim tool output.

What it means for Trove:
- Confirms the OpenTelemetry route works in a shipped product for OAuth users, alongside hooks and polling.
- **Do not copy its content policy.** It turns prompts and tool details on by default. Under rule 7 Trove would
  set only the telemetry-on, endpoint and protocol variables, never the `OTEL_LOG_*` content switches, and the
  receiver must drop `user.email`, `user.id` and any content attribute before storing.
- Its code guards against a user's redirect variables; nothing I read handles admin-locked managed settings.
  Whether Enterprise admins can block a local receiver is still unverified.
- Worth borrowing: the sweep lock and cursor design for polling, and a golden-file test for the settings it writes.
- Not worth borrowing: direct rewriting of `settings.json` without a confirm step. Trove's installers show
  the change, ask, and back the file up.

## Not yet researched

Settings and CLAUDE.md fixes with proof, the MCP tool ideas beyond the four tools, other agents with API-key
paths, segment sizing (step 1), mapping each optimization to the data it needs (step 3), and measuring what the
history files cover on real installs (step 4).

## Early reading (estimated, not measured)

1. OpenTelemetry gives better live measurement for OAuth users but no new power to optimize.
2. `PostToolUse` `updatedToolOutput` is the strongest candidate for optimizing without the gateway, and is
   unused today.
3. Request-body rewriting and cache-aware trimming remain gateway-only.

## 5. Gateways and OAuth logins (verified 2026-11-04)

Sources, all read in full or searched as noted: code.claude.com/docs/en/llm-gateway (whole page),
/gateways (whole page), /llm-gateway-protocol (searched for OAuth, subscription and header wording, not read
in full), /claude-apps-gateway (searched for plan, auth and loopback wording, not read in full), and
/legal-and-compliance (whole page).

### What the docs say

- **Setting only `ANTHROPIC_BASE_URL`, with no gateway credential, keeps the saved claude.ai login active.**
  Requests go through the gateway, and the subscription's limits and billing still apply. The docs say a
  gateway that passes this traffic to Anthropic "must forward the OAuth capability in `anthropic-beta`";
  stripping it fails the request with `401` (protocol page, request headers).
- Setting `ANTHROPIC_AUTH_TOKEN` or `apiKeyHelper`, or signing in to Claude apps gateway, turns subscription login
  off for that session and bills API rates to whoever owns the forwarded credential.
- **Claude apps gateway** is Anthropic's own gateway, built into the `claude` binary. It is for organizations that
  route through their own cloud, and a gateway sign-in *replaces* a Team or Enterprise login. It does not serve
  claude.ai subscription traffic as such, and no plugin or extension point for third-party request
  modification was found in what I searched. It accepts plain `http://` only for a loopback host at `/login`.
- Gateways must forward `anthropic-beta` verbatim and the `anthropic-ratelimit-unified-*` response headers, which
  Claude Code uses to show plan limits to claude.ai users.
- A gateway also gets `x-claude-code-session-id` on every request, which gives per-session grouping without
  reading bodies.

### The legal page, which decides whether this is allowed (quoted, not paraphrased)

"Anthropic does not permit third-party developers to offer Claude.ai login into their own applications, or to
route requests through Free, Pro, Max, or Ultra plan credentials on behalf of their users. Moreover, developers
may not collect, store, or intermediate Claude.ai credentials or session tokens."

It also says OAuth authentication is "intended exclusively for purchasers of ... subscription plans and is
designed to support ordinary use of Claude Code and other native Anthropic applications", and that the
restriction does not stop "an end user from signing in to the unmodified Claude Code binary with their own
Claude subscription".

### Reading (estimated, not legal advice)

1. A local proxy in front of an OAuth login is **technically documented** (`ANTHROPIC_BASE_URL` only). It is
   **not clearly permitted**. The OAuth bearer token would pass through Trove's process, and "intermediate Claude.ai
   credentials or session tokens" is the wording that most plausibly covers that. The text is about third-party
   developers routing on behalf of users, so a user running a local tool on their own machine is a grey area.
   Only Anthropic can settle it.
2. CLAUDE.md rule 3 already forbids this. Nothing here argues for changing it. If you want it reconsidered,
   the question to put to Anthropic (sales contact is named on the legal page) is: may a user-run local tool
   observe or modify requests carrying that user's own OAuth token, with the token never stored or logged?
3. Even if allowed, rule 3 also says "never modify subscription traffic", so it could at most be an
   observe-only mode, and that is already covered by OpenTelemetry (section 1) with no credential in the path.
4. Enterprise: an admin can pin `ANTHROPIC_BASE_URL` and set `allowedProviders` to `["customEndpoint"]`
   (v2.1.285+). A user-local Trove gateway would then be refused. A company gateway would have to be Trove's
   target instead, and that is a different product (operator-run, API-key or gateway credential).

### Verdict for this step

No new route to gateway-level control for OAuth users. The gateway route stays out under rule 3 and the legal
page. The credential-free channels (OpenTelemetry, hooks, statusLine, history files) remain the only candidates.

### Open items

- Whether Anthropic would give a written answer on the question in point 2. Not asked; needs you to decide.
- The rest of /claude-apps-gateway ("Availability and limitations", what it forwards) and
  /claude-apps-gateway-config were not read in full.
- Whether an operator-run Trove gateway for Enterprise (company key or gateway credential) is a product you want.
  That is a rule-3-compatible variant, since the credential is an API key, not OAuth.

### Decision (2026-11-04, owner)

The OAuth gateway route is **closed**. The owner will not ask Anthropic for permission, so no proxy, interception or
modification of claude.ai-login traffic will be built or researched further. Rule 3 stands. The operator-run
Enterprise gateway variant (company API key) is not pursued unless the owner reopens it.
