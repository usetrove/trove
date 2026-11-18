# Trove plan index

Read this file first, then only the workstream file for your task (docs/plan/W<n>.md). Don't read the other workstream files.

## Tier rules (enforced by internal/technique/registry.go after 0.1)
- T1 Safe (transform_lossless, transform_recoverable, config_overhead, cache_control, guard): deterministic, visible diff, lossless or recoverable, mechanical checker, shadow then A/B. Autopilot after a shadow run and an A/B verdict.
- T2 Behavioural (behavior_instructions): T1 rules + per-request overhead charged, test-pass guardrail on, confounder tag recorded, explicit user opt-in. Autopilot only after a benchmark verdict, off by default.
- T3 Routing (model_routing, effort, subagent_routing, local_offload): T2 rules + cache-stickiness rule, a benchmark verdict per agent/model family, gateway never sees subscription logins. Strictest rollback threshold.
- All tiers: measurable on the real bill, undoable through the Fixer, records which quality signals were checked.

## Hard constraints
- Never point Claude Code at the gateway without an API key (keep the trove setup rule).
- New tables: update PRIVACY.md and store privacy tests (store names, never values).
- Keep the naming lint passing (no vendor names in file, folder or command names).
- Never chain Headroom's proxy with Trove's gateway.

## Order
W0 -> (W1, W2) -> (W6, W7) -> W9. W3, W4, W5 and W8 run in parallel. W10 and W11 are continuous.

## Items
Owner: agent = Claude Code can do it alone; human = needs real installs, paid benchmark runs or a sign-off; agent+human = agent builds it, a human runs or verifies it.
Status: todo | doing | done | blocked(<reason>). Update the Status column when you finish an item.

| ID | Item | Rel | Size | Owner | Needs | Status |
|---|---|---|---|---|---|---|
| 0.1 | Class+Tier fields, registry enforcement | 0.5 | S | agent | - | done |
| 0.2 | Scout/cascade as T3 + session A/B | 0.5 | M | agent | 0.1 | done |
| 0.3 | Confounder tag at session start | 0.5 | S | agent | 0.1 | done |
| 0.4 | Tier+evidence labels on suggestions | 0.5 | S | agent | 0.1 | done |
| 1.1 | Cost/turn + cost/uncached token metrics | 0.5 | M | agent | 0.1 | done |
| 1.2 | CUPED/regression adjustment | 0.6 | M | agent | 1.1, pre-registered method | done |
| 1.3 | Sequential always-valid CIs for rollback | 0.6 | M | agent | 1.1 | done |
| 1.4 | Fill priors file | 0.6 | M | agent+human | bench results | blocked(bench results) |
| 1.5 | Agent output caps in shadow estimates | 0.5 | S | agent | - | done |
| 1.6 | Learned bytes/token per model family | 0.5 | S | agent | - | done |
| 1.7 | Subscription usage numbers + status line | 0.6 | M | agent | 1.1 | done |
| 2.1 | Test-pass signal (PostToolUse Bash exit code) | 0.6 | M | agent+human | live probe of Bash PostToolUse | done |
| 2.2 | test_fail_rate guardrail | 0.6 | M | agent | 2.1 | done |
| 2.3 | Per-session outcomes rollup | 0.6 | S | agent | 2.1 | done |
| 2.4 | Turn-inflation guardrail | 0.6 | S | agent | 1.1 | done |
| 2.5 | CI checker per optimizer | 0.6 | S | agent | - | done |
| 3.1 | Fixed-overhead findings first | 0.5 | S | agent | - | done |
| 3.2 | Measured overhead per fix | 0.6 | M | agent | - | done |
| 3.3 | alwaysLoad advice | 0.6 | S | agent | - | done |
| 3.4 | context_slim: provable duplicates only | 0.5 | S | agent | 0.1 | done |
| 4.1 | cache_ttl as a trove fix | 0.6 | S | agent | - | done |
| 4.2 | Per-marker cache TTL at gateway | 0.7 | M | agent | - | done |
| 4.3 | Keep-warm pings (API key, opt-in) | 0.7 | M | agent+human | - | done |
| 4.4 | In-flight dedup decision from shadow | 0.7 | S | agent | - | done |
| 5.1 | Hook output-hash dedup | 0.7 | M | agent | - | done |
| 5.2 | Outline-first large Read | 0.7 | M | agent+human | live probe of Read output | done |
| 5.3 | JSON/log output trimmer | 0.7 | M | agent | 2.5 | done |
| 5.4 | Retyped scripts -> trove_run | 0.7 | M | agent | - | done |
| 5.5 | MCP snippet with consent (T2) | 0.7 | S | agent | 0.1, 2.2 | done |
| 5.6 | Cursor + Gemini CLI adapters | 0.8 | L | agent+human | per-agent probe | blocked(per-agent live probe, docs/hooks-verification.md) |
| 5.7 | Finish Codex output rewriting | 0.8 | M | agent+human | Codex live probe | blocked(Codex live probe; no documented output-replacement field) |
| 6.1 | Ponytail prompt as T2 | 0.7 | S | agent | 0.1, 0.3, 2.2 | done |
| 6.2 | Caveman skill as T2 | 0.7 | S | agent | 6.1 | done |
| 6.3 | terse-rules as T2 + conflict check | 0.7 | S | agent | 6.1 | done |
| 6.4 | Gateway verbosity steering | 0.7 | M | agent | 2.5 | done |
| 6.5 | Learned verbosity level | 0.7 | M | agent | 6.4 | done |
| 6.6 | Autopilot trials for rule sets | 0.7 | M | agent | 1.1-1.3, 6.1 | done |
| 7.1 | Cascade/scout/settings studies | 0.8 | L | agent+human | paid benchmark runs | todo |
| 7.2 | Sticky model step-down at gateway | 0.8 | L | agent | 2.2, 2.4 | blocked(A/B needs live API-key sessions; shadow built) |
| 7.3 | Model-per-subagent fix | 0.8 | M | agent | 2.2 | blocked(override probed live and session A/B built; data comes from real sessions with the fix applied) |
| 7.4 | Lower effort on tool turns | 0.8 | M | agent | cache-break check | blocked(check done: keeps on Sonnet/Haiku 5.5; optimizer built; A/B is bench condition alone:effort_tool_turns, needs a study run) |
| 7.5 | model_default with measured estimate | 0.8 | S | agent | 1.4 | done |
| 7.6 | Ollama offload for subagents | 0.8 | L | agent+human | open-weight study | todo |
| 7.7 | Verify Codex/OpenCode subagents | 0.8 | M | human | real installs | todo |
| 7.8 | Batch API suggestion re-labeled T1 | 0.8 | S | agent | 0.1 | done |
| 8.1 | Spend cap warn 80% then stop | 0.8 | M | agent+human | refused-request probe | blocked(evidence: Claude Code stops after one refusal; a person must record it with `trove budget probe`) |
| 8.2 | Subscription budget warning | 0.8 | S | agent | 1.7 | done |
| 9.1 | RTK, ZTK, Headroom lib/CLI | 0.8 | M | agent | - | done |
| 9.2 | Ponytail + Caveman on referee list | 0.8 | S | agent | 6.1, 6.2 | done |
| 9.3 | Randomized hand-off A/B for external tools | 0.9 | L | agent+human | probe | todo |
| 9.4 | Claim vs verdict side by side | 0.9 | S | agent | - | done |
| 10.1 | Real installs macOS/Linux/Windows | cont. | - | human | - | todo |
| 10.2 | Codex real install | cont. | - | human | - | todo |
| 10.3 | Live hook probe + hooks-vs-gateway bench | cont. | - | human | API key | todo |
| 10.4 | Run per-hook studies | cont. | - | human | API key | todo |
| 10.5 | Long-session task suite | cont. | M | agent | - | done |
| 10.6 | Benchmark output_trimmer, hook_large_output | cont. | - | agent+human | API key | blocked(conditions built with the MCP server registered; needs a study run) |
| 10.7 | Benchmark-derived Autopilot thresholds | 0.8 | S | agent | 10.3-10.6 | todo |
| 10.8 | Commit SWE-bench results + fix flaky test | 0.9 | S | agent+human | - | blocked(flaky test fixed; swebench-gateway-2 result files are on another machine) |
| 11.1 | Docs around the three tiers | each | S | agent | 0.1 | todo |
| 11.2 | CHANGELOG + PRIVACY.md updates | each | S | agent | - | todo |
| 11.3 | Naming lint on new code | each | S | agent | - | todo |
