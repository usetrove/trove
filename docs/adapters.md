# Adapters and the shared engine

Trove has one optimizer engine (`internal/optimize`) and several adapters that reach it. An adapter holds no
optimization logic. It turns what its host gives it into the engine's form, calls the engine, and turns the answer
back into what the host expects. Optimizers are written once, so a fix or a new optimizer reaches every adapter that
can run it.

## The engine API

Every adapter calls one of three entry points:

| Entry point | Works on | Used by |
|---|---|---|
| `TransformToolInput(call, session)` | a tool call, before it runs | hooks, plugins |
| `TransformToolOutput(call, output, session)` | a tool's output, after it ran | hooks, plugins, and inside requests for the gateway |
| `TransformRequest(request, session)` | a whole provider request | gateway, plugins |

The tool-level calls use a neutral form (`ToolCall`, `ToolOutput`). The request level does not: its codec is
Anthropic Messages, behind the engine, because byte-exact span editing is what keeps cached prefixes valid. Adapters
pass request bytes plus the provider and path, and never parse them. OpenAI requests are still forwarded unchanged.

What every call guarantees:

- **Pure and deterministic.** The same input gives the same output. (Tool-level optimizers that need the file system,
  such as `hook_repeat_read`, read it through a stated interface and are not part of the cache-byte contract.)
- **Fail open.** A panic, error or timeout returns the original input and records a failure. Nothing an adapter does
  can make the engine the reason an agent stops.
- **Time budget.** Each tool-level call has the `budget_ms` budget (default 25 ms). A request has one budget for the
  whole pipeline.
- **Shadow mode.** A shadow optimizer computes its report and changes nothing.
- **States are resolved by the caller.** Pins, guardrail rollbacks and the kill switch differ per adapter, so the
  adapter passes a state resolver. An optimizer it does not name is off. The kill switch always applies.

Each optimizer declares where it acts (`Points`), its tier (`lossless`, `recoverable`, `lossy`), and its own token
overhead (`OverheadTokens`: tool definitions and fixed injected text). Net savings are tokens removed minus
overhead. Today every optimizer declares zero. The output trimmer's real overhead is the MCP tool definitions that
let the agent fetch trimmed output back; filling that in is a known gap (see the end).

## Capability matrix

This table is generated from the optimizers' declarations by `go test ./internal/optimize -run TestAdaptersDoc
-update`, and a test fails if it drifts. `trove doctor --capabilities` shows the same data (add `--json` for
machine-readable output). The harness facts behind the hooks column were checked against the official hooks
documentation (code.claude.com/docs/en/hooks) on 2026-11-04, through a summary of the page rather than the full
text, so treat the hooks column as provisional until the page is read in full.

<!-- BEGIN GENERATED: capability matrix (go test ./internal/optimize -run TestAdaptersDoc -update) -->

| Optimizer | Tier | Acts at | Overhead (tokens) | gateway | hooks | plugin |
|---|---|---|---|---|---|---|
| `volatile_prefix_mover` | lossless | request | 0 | **supported**: request: sees and can rewrite every outgoing API request; only for agents the user marked as custom, never Claude Code or Codex | **impossible**: request (no hook receives the model request (none documented as of 2026-11-04)) | **partial**: request: only in harnesses that expose the outgoing request to plugins; only for agents the user marked as custom, never Claude Code or Codex |
| `noise_normalizer` | lossless | request, tool_output | 0 | **supported**: request: sees and can rewrite every outgoing API request | **partial**: tool_output: PostToolUse documents updatedToolOutput, but the value must match each tool's output shape and built-in tools silently ignore a mismatch, so the adapter emits it only for tools a live probe has seen honour it (`trove hooks probe`); whether later requests keep the replaced output is unanswered (docs/non-gateway-optimization.md, Q2) | **supported**: tool_output: reachable over `trove engine serve`; what a harness lets a plugin change is checked per harness |
| `output_trimmer` | recoverable | request, tool_output | 0 | **supported**: request: sees and can rewrite every outgoing API request; stays in shadow unless the full output can be fetched back (recovery store and the Trove MCP server) | **partial**: tool_output: PostToolUse documents updatedToolOutput, but the value must match each tool's output shape and built-in tools silently ignore a mismatch, so the adapter emits it only for tools a live probe has seen honour it (`trove hooks probe`); whether later requests keep the replaced output is unanswered (docs/non-gateway-optimization.md, Q2); stays in shadow unless the full output can be fetched back (recovery store and the Trove MCP server) | **supported**: tool_output: reachable over `trove engine serve`; what a harness lets a plugin change is checked per harness; stays in shadow unless the full output can be fetched back (recovery store and the Trove MCP server) |
| `duplicate_result_collapser` | lossless | request | 0 | **supported**: request: sees and can rewrite every outgoing API request | **impossible**: request (no hook receives the model request (none documented as of 2026-11-04)) | **partial**: request: only in harnesses that expose the outgoing request to plugins |
| `cache_breakpoints` | lossless | request | 0 | **supported**: request: sees and can rewrite every outgoing API request; only for agents the user marked as custom, and only for known models above the cache minimum | **impossible**: request (no hook receives the model request (none documented as of 2026-11-04)) | **partial**: request: only in harnesses that expose the outgoing request to plugins; only for agents the user marked as custom, and only for known models above the cache minimum |
| `hook_repeat_read` | recoverable | tool_input, tool_output | 0 | **impossible**: needs the file as it is on disk before the Read runs; the gateway only sees finished history | **supported**: tool_input: PreToolUse can deny a call, add context or rewrite its input (code.claude.com/docs/en/hooks, 2026-11-04) | **supported**: tool_input: reachable over `trove engine serve`; what a harness lets a plugin change is checked per harness |
| `hook_read_limit` | recoverable | tool_input | 0 | **impossible**: the limit has to be set before the Read runs; the gateway only sees finished history | **supported**: tool_input: PreToolUse can deny a call, add context or rewrite its input (code.claude.com/docs/en/hooks, 2026-11-04) | **supported**: tool_input: reachable over `trove engine serve`; what a harness lets a plugin change is checked per harness |
| `hook_quiet_commands` | recoverable | tool_input | 0 | **impossible**: the flag has to be added before the command runs; the gateway only sees finished history | **supported**: tool_input: PreToolUse can deny a call, add context or rewrite its input (code.claude.com/docs/en/hooks, 2026-11-04) | **supported**: tool_input: reachable over `trove engine serve`; what a harness lets a plugin change is checked per harness |
| `hook_large_output` | lossless | tool_input | 0 | **impossible**: advice has to reach the agent before the command runs; the gateway only sees finished history | **supported**: tool_input: PreToolUse can deny a call, add context or rewrite its input (code.claude.com/docs/en/hooks, 2026-11-04) | **supported**: tool_input: reachable over `trove engine serve`; what a harness lets a plugin change is checked per harness |

<!-- END GENERATED -->

How to read it: **supported** means the adapter can run the optimizer as designed. **Partial** means it can run it
with a stated limit. **Impossible** means the adapter cannot reach the point the optimizer acts at, so it is not
pretended.

## The adapters

**Gateway** (`internal/gateway`). Calls `TransformRequest` on API-key traffic. The request-level optimizers for noise,
trimming and duplicates reach tool results through the same functions the tool-level API uses, so the text is
identical either way (`TestToolLevelMatchesRequestLevel`).

**Hooks** (`trove hook <event>`, `internal/hooks`). Parses the agent's hook JSON, calls `TransformToolInput` and
`TransformToolOutput`, and renders the engine's decision: `permissionDecision: deny` or `additionalContext`, plus the
spool counts. It keeps the session journal as storage for the engine's read ledger. It does not yet emit
`updatedToolOutput`; see the partial rows above.

**Plugins** (`trove engine serve`, `internal/adapter/plugin`). For harness plugins that are not written in Go. The
plugin starts one long-lived child process and speaks JSON lines over stdin and stdout.

### Plugin protocol

One JSON object per line in each direction, replies in order. This is protocol version 1.

```
-> {"id":1,"method":"hello"}
<- {"id":1,"ok":true,"protocol":1,"version":"0.1.0","recoverable":false,"capabilities":[...]}
-> {"id":2,"method":"tool_output","session":{"key":"s1","harness":"opencode","login":"api_key","cannot":["deny"]},
    "call":{"name":"Bash"},"output":{"parts":[{"text":"..."}]}}
<- {"id":2,"ok":true,"output":{"parts":[{"text":"..."}]},"reports":[{"name":"noise_normalizer","state":"on",...}]}
```

Methods: `hello`, `capabilities`, `session`, `tool_input`, `tool_output`, `request`, `observe`. A `request` carries
`provider`, `path`, `content_encoding` and `body` (base64, so bytes are exact).

- **Handshake.** `hello` returns the protocol version. A plugin that does not speak it does nothing (fails open).
- **Fail open.** If `ok` is false, or the process died, use your original input. A reply never contains anything the
  plugin did not send.
- **No credentials.** The protocol has no header or key field, so none can reach the engine (a test checks the
  types). Do not send any. The login is a three-valued label the plugin derives from non-secret fields:
  `api_key`, `copilot` or `subscription_or_unknown`. Only the first two may have content changed; for the third
  every optimizer runs in shadow (it counts, changes nothing) and a `request` comes back untouched with
  `skipped: "login_not_eligible"`. An empty login counts as the third.
- **States.** A plugin that names a `harness` and sends no `states` gets them from the server: the configured state,
  lowered to shadow by a guardrail rollback (read again on every call, so a rollback acts at once) and by the control
  arm of the A/B experiment, and pinned for the session at its first call (rule 5). `session` returns them. A plugin
  that sends `states` is used as before (hooks-style callers and tests). The baseline gate that the gateway applies
  is not applied here, as for hooks.
- **What the harness cannot do.** `cannot` lists `deny`, `rewrite_input`, `rewrite_output` and `rewrite_request`.
  Where a plugin cannot carry out what an optimizer does, the engine counts in shadow instead (a "blocked" that the
  harness never enforced would be a wrong number).
- **Agent label.** A harness plugin that names none is labelled `other`, the label the gateway gives an unknown
  client, so `custom_agents = ["other"]` in `config.toml` enables `volatile_prefix_mover` and `cache_breakpoints`
  for it exactly as on the gateway. Only `sdk` and `other` can be marked custom.
- **Recording.** With a `harness`, the server hashes the session key and writes decisions and `observe` signals to
  `adapter_events` (metadata only; see PRIVACY.md). `observe` takes a `usage` or an `outcome` with a short fixed
  detail; free text is rejected. Provider and model names must match a strict pattern or they are dropped.
- **Recovery.** `output_trimmer` may be on only when the Trove MCP server is registered and the recovery store opens;
  `hello` says which (`recoverable`).
- **Stdout** carries only protocol lines. Logs go to stderr.
- `trove engine serve` refuses to run from a terminal. Closing its stdin makes it write out what it recorded and exit.

### Per-call overhead of the plugin path

Measured with `go test ./internal/adapter/plugin -bench .`, and `TROVE_BENCH_BIN=<built trove>` for the subprocess
numbers. Machine: AMD Ryzen 5 5500U, Windows 11, Go benchmark with 300 iterations, 2026-11-04. These are one
machine's timings, not a guarantee, and a laptop with antivirus is slow at starting processes; re-measure before
quoting them.

| Call | Payload | In-process (protocol + engine) | Real subprocess (OS pipes) |
|---|---|---|---|
| `tool_input` | small | 0.05 ms | 0.48 ms |
| `tool_output` | 1 KB | 0.13 ms | 0.57 ms |
| `tool_output` | 100 KB | 2.3 ms | 3.2 ms |
| `request` | 190 KB | 2.6 ms | 3.1 ms |

Cold start (spawn to first reply): about 46 ms (mean of 20 spawns). Pay it once per session, not per call.

The call cost is dominated by payload size, not by the process boundary: pipes add roughly 0.4 to 0.5 ms to a small
call. The gateway's 5 ms p50 target is a different measurement (a request through the proxy), checked by
`TestEngineStaysWithinTheGatewayBudget`.

## Adding an optimizer

1. Implement `Capable` (`Name`, `Tier`, `Points`, `OverheadTokens`) plus `Optimizer` (request level),
   `ToolInputOptimizer` or `ToolOutputOptimizer`, whichever points it acts at.
2. Register it in `Pipeline()` (request level, order is a cache contract) or `ToolPipeline()`.
3. Add `SupportOverrider` or `Caveater` if an adapter's answer differs from the default for a reason of its own.
4. Run `go test ./internal/optimize -run TestAdaptersDoc -update` and commit the regenerated table. The parity tests
   pick the new optimizer up from the matrix and fail until it has a fixture:
   `internal/gateway/parity_test.go` (gateway against hooks, tool output),
   `internal/gateway/parity_plugin_test.go` (plugin against gateway and hooks, request and tool output; add a tool
   fixture to `parityFixtures` or a request fixture to `requestOnlyBody`) and
   `internal/gateway/parity_plugin_tool_test.go` (plugin against hooks, tool input; add one to `toolInputFixtures`).
   Then run `go test ./internal/gateway -run PluginParity -update-plugin-fixtures` to refresh
   `plugins/opencode/test/fixtures/parity.json`, which the OpenCode plugin's own tests replay through the real engine.

## Known gaps

- The request level is Anthropic Messages only.
- `output_trimmer` declares zero overhead; its MCP tool definitions should be charged to it (`trove mcp
  schema-size` has the number).
- Hooks do not emit `updatedToolOutput`: per-tool output shapes are unverified, and whether later requests keep the
  replaced output (Q2 in `non-gateway-optimization.md`) is unanswered, which matters for cache safety.
- The plugin server keeps read ledgers in memory only, and pins session states in memory (the experiment arm is
  stored, so it survives a restart; the rest is recomputed from config, which can differ if config changed).
- The plugin server does not apply the gateway's baseline period before an optimizer may be "on" (hooks do not
  either); the A/B experiment and guardrail rollbacks are applied.
- Request-level transforms in a plugin are only for harnesses that expose the request body; see `opencode.md`.
- `trove_run` in the MCP server still has its own head-and-tail fallback trimming outside the engine.
- Hooks now count a prompt on every `Bash` call, not only on noisy ones, so the "prompt #N" in a repeat-read message
  can differ by a little from before.
