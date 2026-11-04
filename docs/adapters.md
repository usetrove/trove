# Adapters and the shared engine

placeholder

## Capability matrix

<!-- BEGIN GENERATED: capability matrix (go test ./internal/optimize -run TestAdaptersDoc -update) -->

| Optimizer | Tier | Acts at | Overhead (tokens) | gateway | hooks | plugin |
|---|---|---|---|---|---|---|
| `volatile_prefix_mover` | lossless | request | 0 | **supported**: request: sees and can rewrite every outgoing API request; only for agents the user marked as custom, never Claude Code or Codex | **impossible**: request (no hook receives the model request (none documented as of 2026-11-04)) | **partial**: request: only in harnesses that expose the outgoing request to plugins; only for agents the user marked as custom, never Claude Code or Codex |
| `noise_normalizer` | lossless | request, tool_output | 0 | **supported**: request: sees and can rewrite every outgoing API request | **partial**: tool_output: PostToolUse documents updatedToolOutput, but the value must match each tool's output shape; the adapter computes the result and does not emit it yet | **supported**: tool_output: reachable over `trove engine serve`; what a harness lets a plugin change is checked per harness |
| `output_trimmer` | recoverable | request, tool_output | 0 | **supported**: request: sees and can rewrite every outgoing API request; stays in shadow unless the full output can be fetched back (recovery store and the Trove MCP server) | **partial**: tool_output: PostToolUse documents updatedToolOutput, but the value must match each tool's output shape; the adapter computes the result and does not emit it yet; stays in shadow unless the full output can be fetched back (recovery store and the Trove MCP server) | **supported**: tool_output: reachable over `trove engine serve`; what a harness lets a plugin change is checked per harness; stays in shadow unless the full output can be fetched back (recovery store and the Trove MCP server) |
| `duplicate_result_collapser` | lossless | request | 0 | **supported**: request: sees and can rewrite every outgoing API request | **impossible**: request (no hook receives the model request (none documented as of 2026-11-04)) | **partial**: request: only in harnesses that expose the outgoing request to plugins |
| `cache_breakpoints` | lossless | request | 0 | **supported**: request: sees and can rewrite every outgoing API request; only for agents the user marked as custom, and only for known models above the cache minimum | **impossible**: request (no hook receives the model request (none documented as of 2026-11-04)) | **partial**: request: only in harnesses that expose the outgoing request to plugins; only for agents the user marked as custom, and only for known models above the cache minimum |
| `hook_repeat_read` | recoverable | tool_input, tool_output | 0 | **impossible**: needs the file as it is on disk before the Read runs; the gateway only sees finished history | **supported**: tool_input: PreToolUse can deny a call, add context or rewrite its input (code.claude.com/docs/en/hooks, 2026-11-04) | **supported**: tool_input: reachable over `trove engine serve`; what a harness lets a plugin change is checked per harness |
| `hook_large_output` | lossless | tool_input | 0 | **impossible**: advice has to reach the agent before the command runs; the gateway only sees finished history | **supported**: tool_input: PreToolUse can deny a call, add context or rewrite its input (code.claude.com/docs/en/hooks, 2026-11-04) | **supported**: tool_input: reachable over `trove engine serve`; what a harness lets a plugin change is checked per harness |

<!-- END GENERATED -->
