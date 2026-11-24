package technique

// registry is the code form of the tables in docs/techniques.md. Every ID there
// must be here and the other way round; TestRegistryMatchesTheDocument checks it.
// Sources and licences were verified on 2026-11-13 (docs/techniques.md, "What was
// verified"). When an entry changes, change the document in the same commit.

// init derives every entry's Tier from its Class, so an entry with no class has no tier and a test fails.
func init() {
	for i := range registry {
		registry[i].Tier = TierOfClass(registry[i].Class)
	}
}

var zero = 0

func none() *int { return &zero }

func use(n int) *int { return &n }

// trimmerToolDefTokens is what the Trove MCP server's four tool definitions add to the context in a session
// that has it registered, which the output trimmer cannot work without (it needs trove_fetch_full). It is charged
// to the trimmer in full, even for a user who registered the server for other reasons, so a promotion never rests
// on a saving the definitions already ate. A test in internal/mcp keeps it equal to `trove mcp schema-size`.
const trimmerToolDefTokens = 351

// mcpSnippetTokens is the estimated size of the MCP tool-preference snippet, sent with every request once it is in
// CLAUDE.md. A test in internal/fixer keeps it equal to mcp.SnippetTokens().
const mcpSnippetTokens = 70

// verbositySteerTokens is the most verbosity_steer adds to a request (its longest note as a new system block). A
// test in internal/optimize keeps it equal to VerbositySteer{}.OverheadTokens().
const verbositySteerTokens = 85

// coldResumeNoteTokens is the most the cold-resume guard's note can cost if the user pastes it after /clear: its
// 1,500-byte cap at 3.5 bytes per token, rounded up. Tests in internal/hooks and internal/measure keep them in step.
const coldResumeNoteTokens = 450

const (
	urlRTK       = "https://github.com/rtk-ai/rtk"
	urlHeadroom  = "https://github.com/headroomlabs-ai/headroom"
	urlCaveman   = "https://github.com/JuliusBrussee/caveman"
	urlLLMLingua = "https://github.com/microsoft/LLMLingua"
	urlSavior    = "https://github.com/Mibayy/token-savior"
	urlSuperComp = "https://github.com/Supercompress/Supercompress"
	urlCCUsage   = "https://github.com/ccusage/ccusage"
	urlPonytail  = "https://github.com/DietrichGebert/ponytail"
	urlZTK       = "https://github.com/codejunkie99/ztk"
	ownerTrove   = "trove"
	tierLossless = "lossless"
	tierRecover  = "recoverable"
	tierBehavior = "behavior"
	// tierRouting sends the request to another model (optimize.TierRouting).
	tierRouting   = "routing"
	noticeInCount = "the text it adds is counted inside the tokens it removes"
)

var gatewayClaim = []Claim{{ResourceBaseURL, ownerTrove}}

var registry = []Technique{
	// ---- Built ---------------------------------------------------------------
	{ID: "B1", Class: ClassLossless, Title: "Strip colour codes, progress frames and repeated blank lines", Names: []string{"noise_normalizer"},
		Layers: []Layer{LayerToolBoundary, LayerRequest}, Route: RouteNative, Status: StatusBuilt, Effect: tierLossless,
		Subscription: true, Auto: true, Overhead: none(), HookNeeds: []string{"Bash:output"}, Claims: []Claim{{ResourceShellCompressor, ownerTrove}},
		Licence: "Trove project", Note: "lossless; acts on new tool results only"},
	{ID: "B2", Class: ClassLossless, Title: "Replace a repeated identical tool result with a pointer", Names: []string{"duplicate_result_collapser"},
		Layers: []Layer{LayerRequest}, Route: RouteNative, Status: StatusBuilt, Effect: tierLossless,
		Gateway: true, Auto: true, Overhead: none(), OverheadNote: noticeInCount, Claims: gatewayClaim,
		Licence: "Trove project", Note: "deterministic, so a resent history gives the same bytes"},
	{ID: "B3", Class: ClassRecoverable, Title: "Keep failures and the first and last lines of long test and build output", Names: []string{"output_trimmer"},
		Layers: []Layer{LayerToolBoundary, LayerRequest}, Route: RouteNative, Status: StatusBuilt, Effect: tierRecover,
		Subscription: true, AddsText: true, Overhead: use(trimmerToolDefTokens), UseOverhead: none(), UseNetted: true,
		OverheadNote: noticeInCount + "; the four MCP tool definitions it relies on (about 351 tokens, `trove mcp schema-size`) are charged to it as a per-session cost, although the server is also B9's",
		HookNeeds:    []string{"Bash:output"},
		Requires:     []string{CapMCPServer, CapRecoveryStore}, Claims: []Claim{{ResourceShellCompressor, ownerTrove}},
		Licence: "Trove project", Note: "full output is kept locally and can be fetched back; Bash output only in hooks"},
	{ID: "B4", Class: ClassCacheControl, Title: "Move changing lines out of the stable prompt", Names: []string{"volatile_prefix_mover"},
		Layers: []Layer{LayerRequest}, Route: RouteNative, Status: StatusBuilt, Effect: tierLossless, Prefix: true,
		Gateway: true, Overhead: none(), Requires: []string{CapCustomAgent}, Claims: gatewayClaim,
		Licence: "Trove project", Note: "custom agents only"},
	{ID: "B5", Class: ClassCacheControl, Title: "Place cache markers on the final prompt layout", Names: []string{"cache_breakpoints"},
		Layers: []Layer{LayerRequest}, Route: RouteNative, Status: StatusBuilt, Effect: tierLossless, Prefix: true,
		Gateway: true, Overhead: none(), Requires: []string{CapCustomAgent}, Claims: gatewayClaim,
		Licence: "Trove project", Note: "custom agents only; at most 3 of the 4 allowed markers"},
	{ID: "B6", Class: ClassRecoverable, Title: "Deny a repeat read of an unchanged file", Names: []string{"hook_repeat_read"},
		Layers: []Layer{LayerToolBoundary}, Route: RouteNative, Status: StatusBuilt, Effect: tierRecover,
		Subscription: true, AddsText: true, Overhead: none(), UseOverhead: use(68), OverheadNote: "a one-line denial (about 68 tokens) each time it blocks a read; the saving estimate does not subtract it",
		Licence: "Trove project"},
	{ID: "B7", Class: ClassRecoverable, Title: "Add a line limit to a large file read", Names: []string{"hook_read_limit"},
		Layers: []Layer{LayerToolBoundary}, Route: RouteNative, Status: StatusBuilt, Effect: tierRecover,
		HookNeeds:    []string{"Read:input"},
		Subscription: true, AddsText: true, Overhead: none(), UseOverhead: use(42), UseNetted: true, OverheadNote: "a short note (about 42 tokens) that the read was partial; the saving estimate already subtracts it",
		Licence: "Trove project", Note: "rewrites the whole tool input (updatedInput replaces it)"},
	{ID: "B8", Class: ClassLossless, Title: "Hint to use trove_run for known-noisy commands", Names: []string{"hook_large_output"},
		Layers: []Layer{LayerToolBoundary}, Route: RouteNative, Status: StatusBuilt, Effect: tierLossless,
		Subscription: true, AddsText: true, NoSavingEstimate: true, Overhead: none(), UseOverhead: use(54), OverheadNote: "one hint (about 54 tokens) per noisy command; it has no saving estimate, so none is netted",
		Requires: []string{CapMCPServer}, Claims: []Claim{{ResourceShellCompressor, ownerTrove}},
		Licence: "Trove project"},
	{ID: "B15", Class: ClassRecoverable, Title: "Add one output-quieting flag to an allowlisted command", Names: []string{"hook_quiet_commands"},
		Layers: []Layer{LayerToolBoundary}, Route: RouteNative, Status: StatusBuilt, Effect: tierRecover, NoSavingEstimate: true,
		HookNeeds:    []string{"Bash:input"},
		Subscription: true, Overhead: none(), Claims: []Claim{{ResourceShellCompressor, ownerTrove}}, Licence: "Trove project",
		Note: "rewrites the Bash command the way RTK and ZTK do, so it holds the shell-compressor slot: one owner per Bash call (plan 9.1)"},
	{ID: "B17", Class: ClassRecoverable, Title: "Replace a repeated identical tool output with a pointer at the hook", Names: []string{"hook_output_dedup"},
		Layers: []Layer{LayerToolBoundary}, Route: RouteNative, Status: StatusBuilt, Effect: tierRecover,
		Subscription: true, AddsText: true, Overhead: use(trimmerToolDefTokens), UseOverhead: none(), UseNetted: true,
		OverheadNote: noticeInCount + "; the four MCP tool definitions it relies on (about 351 tokens) are charged to it as a per-session cost, as they are to B3",
		HookNeeds:    []string{"Bash:output"},
		Requires:     []string{CapMCPServer, CapRecoveryStore},
		Licence:      "Trove project", Note: "the full output is kept locally and can be fetched back; Bash, Grep and Glob; no time-based expiry"},
	{ID: "B18", Class: ClassRecoverable, Title: "Show the outline of a very large file read instead of the whole file", Names: []string{"hook_read_outline"},
		Layers: []Layer{LayerToolBoundary}, Route: RouteNative, Status: StatusBuilt, Effect: tierRecover,
		Subscription: true, AddsText: true, Overhead: use(trimmerToolDefTokens), UseOverhead: none(), UseNetted: true,
		OverheadNote: noticeInCount + "; the four MCP tool definitions it relies on (about 351 tokens) are charged to it as a per-session cost, as they are to B3",
		HookNeeds:    []string{"Read:output"},
		Requires:     []string{CapMCPServer, CapRecoveryStore},
		Licence:      "Trove project", Note: "full reads of 40 KB or more of a file the outliner knows; the full text is kept locally and a second full Read goes through; stays in shadow until a live probe sees Read honour updatedToolOutput"},
	{ID: "B21", Class: ClassGuard, Title: "Warn, or ask once, before a prompt rebuilds a large session's expired cache", Names: []string{"hook_cold_resume"},
		Layers: []Layer{LayerToolBoundary}, Route: RouteNative, Status: StatusPartial, Effect: tierLossless, NoSavingEstimate: true,
		Subscription: true, AddsText: true, Overhead: none(), UseOverhead: use(coldResumeNoteTokens),
		OverheadNote: "nothing reaches the model unless the user pastes the resume note after /clear (at most about 450 tokens, once); the block reason is shown to the user and not added to context (code.claude.com/docs/en/hooks, 2026-11-23)",
		Licence:      "Trove project",
		Note:         "built: shadow, warn, and ask on the first prompt after a cold resume. Ask acts as warn until a live check or the user's own session shows a block reaches a typed prompt and print-mode runs can be told apart; a mid-session idle gap is only ever warned about, because self-started turns cannot be told from typed ones"},
	{ID: "B9", Class: ClassLossless, Title: "Trove MCP tools: trove_read, trove_search, trove_run, trove_fetch_full", Names: []string{"trove_mcp"},
		Layers: []Layer{LayerToolBoundary}, Route: RouteNative, Status: StatusBuilt, Effect: tierLossless,
		Subscription: true, AddsText: true, OverheadNote: "tool definitions go into every request; measured by `trove mcp schema-size`",
		Claims: []Claim{{ResourceCodeNav, ownerTrove}}, Licence: "Trove project"},
	{ID: "B10", Class: ClassConfig, Title: "Config fixes with a diff and an undo", Names: []string{"mcp_prune", "context_slim", "cache_breaker_fix", "model_default", "retyped_command"},
		Layers: []Layer{LayerConfig}, Route: RouteConfig, Status: StatusBuilt, Effect: tierBehavior,
		Subscription: true, Overhead: none(), Licence: "Trove project", Note: "each fix shows its evidence and is applied only on a yes"},
	{ID: "B16", Class: ClassBehavior, Title: "Remove generic context-file sections that duplicate nothing", Names: []string{"context_slim_generic"},
		Layers: []Layer{LayerConfig}, Route: RouteConfig, Status: StatusBuilt, Effect: tierBehavior,
		Subscription: true, Overhead: none(), Licence: "Trove project",
		Note: "split from context_slim, which now removes only provable duplicates; a suggestion only, never part of --all"},
	{ID: "B19", Class: ClassBehavior, Title: "MCP tool-preference snippet in CLAUDE.md, with consent", Names: []string{"mcp_snippet"},
		Layers: []Layer{LayerInstructions}, Route: RouteConfig, Status: StatusBuilt, Effect: tierBehavior,
		Subscription: true, AddsText: true, Overhead: use(mcpSnippetTokens),
		OverheadNote: "the snippet is sent with every request (about 70 tokens, estimated); `trove fix` multiplies it by the project's measured requests per session",
		Licence:      "Trove project", Note: "offered only when Trove's MCP server is registered; a suggestion applied by id, never part of --all; adoption before and after is shown by `trove fix`"},
	{ID: "B20", Class: ClassBehavior, Title: "Ask for shorter replies with a fixed note at the end of the system prompt", Names: []string{"verbosity_steer"},
		Layers: []Layer{LayerRequest}, Route: RouteNative, Status: StatusBuilt, Effect: tierBehavior,
		Gateway: true, AddsText: true, Overhead: use(verbositySteerTokens), UseOverhead: none(),
		OverheadNote: "the note is sent with every request (at most about 85 tokens, estimated); each request records the exact figure",
		Claims:       append([]Claim{{ResourceBehaviorRules, ownerTrove}}, gatewayClaim...),
		Licence:      "Trove project", Note: "API-key gateway only, Anthropic Messages; appended after every existing system byte, so each turn stays the previous one's prefix; levels concise and terse"},
	{ID: "B11", Class: ClassSubagentRouting, Title: "Scout subagent and test-gated cascade", Names: []string{"task_scout", "task_cascade"},
		Layers: []Layer{LayerInstructions, LayerConfig}, Route: RouteNative, Status: StatusBuilt, Effect: tierBehavior,
		Subscription: true, AddsText: true, OverheadNote: "shown by `trove tasks plan`", Licence: "Trove project"},
	{ID: "B12", Class: ClassGuard, Title: "Autopilot: promotion and rollback at session boundaries", Names: []string{"autopilot"},
		Layers: []Layer{LayerConfig, LayerToolBoundary, LayerRequest}, Route: RouteNative, Status: StatusBuilt,
		Subscription: true, Overhead: none(), Licence: "Trove project", Note: "a controller, not a saving of its own"},
	{ID: "B13", Class: ClassConfig, Title: "Flag and remove a cost-raising setting you already have", Names: []string{"settings_check"},
		Layers: []Layer{LayerConfig}, Route: RouteConfig, Status: StatusBuilt, Effect: tierBehavior,
		Subscription: true, Overhead: none(), Licence: "Trove project", Note: "a suggestion only, never part of --all"},
	{ID: "B14", Class: ClassConfig, Title: "Catalog of the agent's documented token controls, with version checks and profiles", Names: []string{"settings"},
		Layers: []Layer{LayerConfig}, Route: RouteConfig, Status: StatusBuilt, Effect: tierBehavior,
		Subscription: true, Overhead: none(), Licence: "Trove project"},

	// ---- Candidates and partly built -----------------------------------------
	{ID: "C1", Class: ClassCacheControl, Title: "Cache lifetime: 5 minutes or 1 hour", Names: []string{"cache_ttl", "cache_ttl_main", "cache_ttl_subagents"},
		Layers: []Layer{LayerConfig, LayerRequest}, Route: RouteConfig, Status: StatusPartial, Effect: tierBehavior,
		Subscription: true, Overhead: none(), Licence: "Trove project",
		Note: "built: a per-user estimate (the cache_ttl suggestion) and a `trove fix` that writes the verified setting; not built: a per-breakpoint split in the gateway. A blanket downgrade is not a default (docs/techniques.md, finding 2)"},
	{ID: "C2", Class: ClassCacheControl, Title: "Hold sibling subagent requests until the first cache entry exists", Names: []string{"fan_out"},
		Layers: []Layer{LayerRequest}, Route: RouteNative, Status: StatusCandidate, Effect: tierLossless,
		Gateway: true, Claims: gatewayClaim, Licence: "Trove project",
		Note: "verified 2026-11-14 and not recommended now: the provider mechanism is real, Claude Code already holds same-prefix siblings in workflow fan-outs, and on real data the remaining saving was tiny against the latency it adds."},
	{ID: "C3", Class: ClassLossless, Title: "Let identical simultaneous requests share one upstream call", Names: []string{"inflight_dedup"},
		Layers: []Layer{LayerRequest}, Route: RouteNative, Status: StatusPartial, Effect: tierLossless,
		Gateway: true, Overhead: none(), Claims: gatewayClaim, Licence: "Trove project",
		Note: "built: shadow mode, which counts identical requests that arrive while one is still waiting for the provider and estimates the saving; not built: sharing the call, so \"on\" runs as shadow. Design and exit criteria in docs/inflight-dedup.md"},
	{ID: "C4", Class: ClassBehavior, Title: "Answer identical requests from a local response cache", Names: []string{"exact_cache"},
		Layers: []Layer{LayerRequest}, Route: RouteNative, Status: StatusCandidate, Effect: tierBehavior,
		Gateway: true, Claims: gatewayClaim, Licence: "Trove project", Note: "a correctness risk with agents; opt-in only"},
	{ID: "C5", Class: ClassGuard, Title: "Hard spend cap checked against the ledger", Names: []string{"spend_cap"},
		Layers: []Layer{LayerRequest}, Route: RouteNative, Status: StatusPartial, Effect: tierBehavior,
		Gateway: true, Overhead: none(), Claims: gatewayClaim, Licence: "Trove project",
		Note: "built: shadow mode, which marks requests made after spending reached the budget cap and estimates what a hard cap would have stopped; not built: refusing anything, so \"on\" runs as shadow. Design and exit criteria in docs/spend-cap.md"},
	{ID: "C14", Class: ClassCacheControl, Title: "Keep an idle API-key session's 5-minute cache warm with pings", Names: []string{"keep_warm"},
		Layers: []Layer{LayerRequest}, Route: RouteNative, Status: StatusPartial, Effect: tierLossless,
		Gateway: true, Overhead: none(), Claims: gatewayClaim, Licence: "Trove project",
		Note: "built: shadow estimates and on-mode pings (max_tokens 0, the documented pre-warm request), each recorded as its own billed row, with a ping cap, a daily spend cap and a spend guardrail. Approved with its limits in docs/decisions/keep-warm.md; checked live 2026-11-17 on one model with curl (docs/keep-warm.md); not yet checked inside a real agent session"},
	{ID: "C6", Class: ClassEffort, Title: "Effort level per task", Names: []string{"effortLevel", "effort_tool_turns"},
		Layers: []Layer{LayerConfig}, Route: RouteConfig, Status: StatusPartial, Effect: tierBehavior, Prefix: true,
		Subscription: true, Overhead: none(), Licence: "Trove project",
		Note: "the setting is verified and writable; no profile sets a value without a benchmark. A change of effort can invalidate cached message blocks, so it applies to new sessions only"},
	{ID: "C7", Class: ClassSubagentRouting, Title: "Choose the model per subagent",
		Layers: []Layer{LayerConfig, LayerInstructions}, Route: RouteConfig, Status: StatusPartial, Effect: tierBehavior,
		Subscription: true, Overhead: none(), Licence: "Trove project", Note: "the scout and cascade (B11) use it; no general setting is written yet"},
	{ID: "C8", Class: ClassModelRouting, Title: "Step down to a cheaper model on tool turns in the gateway, with a stickiness rule", Names: []string{"model_router"},
		Layers: []Layer{LayerRequest}, Route: RouteNative, Status: StatusPartial, Effect: tierRouting, Prefix: true,
		Gateway: true, Overhead: none(), Claims: gatewayClaim, Licence: "Trove project",
		Note: "built: shadow estimates of each switch's cache-write cost and the stickiness rule (stay at least k = (W-R_to)/(R_from-R_to) turns, about 11.5r/(1-r)); not done: the session A/B under the T3 guardrails, which needs live API-key sessions (docs/model-router.md)"},
	{ID: "C9", Class: ClassConfig, Title: "Output size limits for commands, MCP results and file reads",
		Names:  []string{"bashOutputMaxChars", "MAX_MCP_OUTPUT_TOKENS", "CLAUDE_CODE_FILE_READ_MAX_OUTPUT_TOKENS"},
		Layers: []Layer{LayerConfig}, Route: RouteConfig, Status: StatusPartial, Effect: tierBehavior,
		Subscription: true, Overhead: none(), Licence: "Trove project", Note: "verified and writable; no profile sets a value without a benchmark"},
	{ID: "C10", Class: ClassConfig, Title: "Compaction timing", Names: []string{"autoCompactWindow", "idleCompaction"},
		Layers: []Layer{LayerConfig}, Route: RouteConfig, Status: StatusPartial, Effect: tierBehavior, Prefix: true,
		Subscription: true, Overhead: none(), Licence: "Trove project", Note: "compaction rewrites history by design, so earlier cached prefixes are lost"},
	{ID: "C11", Class: ClassConfig, Title: "Tool search for MCP tool definitions", Names: []string{"ENABLE_TOOL_SEARCH"},
		Layers: []Layer{LayerConfig}, Route: RouteConfig, Status: StatusPartial, Effect: tierBehavior,
		Subscription: true, Overhead: none(), Licence: "Trove project", Note: "verified and writable; settings_check flags a stray =false"},
	{ID: "C12", Class: ClassRecoverable, Title: "Replace tool output for more tools than Bash", Names: []string{"tool_output_rewrite"},
		Layers: []Layer{LayerToolBoundary}, Route: RouteNative, Status: StatusCandidate, Effect: tierRecover,
		Subscription: true, Licence: "Trove project", Note: "the hooks reference says updatedToolOutput applies to any tool; Trove's hooks use it for Bash, Grep and Glob"},
	{ID: "C13", Class: ClassBehavior, Title: "Ask for terse replies and minimal code", Names: []string{"terse_replies"},
		Layers: []Layer{LayerInstructions}, Route: RouteNative, Status: StatusCandidate, Effect: tierBehavior,
		Subscription: true, AddsText: true, OverheadNote: "the rules text is carried in every session; its effect must be measured, not assumed",
		Claims: []Claim{{ResourceBehaviorRules, ownerTrove}}, Licence: "Trove project"},

	// ---- Other people's tools ------------------------------------------------
	{ID: "W1", Class: ClassRecoverable, Title: "RTK: rewrite shell commands so their output is smaller", Names: []string{"rtk"},
		Layers: []Layer{LayerToolBoundary}, Route: RouteWrap, Status: StatusWrapped, Effect: tierBehavior,
		Subscription: true, OverheadNote: "declared in its manifest (`trove external show rtk`)",
		Claims:  []Claim{{ResourceShellCompressor, "rtk"}},
		Licence: "Apache-2.0 (Apache-only build); BUSL-1.1 in the default build", Source: urlRTK,
		Note: "Trove installs only the Apache-only build at a pinned tag"},
	{ID: "W2", Class: ClassRecoverable, Title: "token-savior: MCP server for code navigation", Names: []string{"token-savior"},
		Layers: []Layer{LayerToolBoundary}, Route: RouteWrap, Status: StatusWrapped, Effect: tierBehavior,
		Subscription: true, AddsText: true, OverheadNote: "declared in its manifest (`trove external show token-savior`)",
		Claims:  []Claim{{ResourceCodeNav, "token-savior"}},
		Licence: "MIT", Source: urlSavior, Note: "its benchmark is self-reported and its harness is not public"},
	{ID: "W3", Class: ClassBehavior, Title: "terse-rules: a short rules block", Names: []string{"terse-rules"},
		Layers: []Layer{LayerInstructions}, Route: RouteWrap, Status: StatusWrapped, Effect: tierBehavior,
		Subscription: true, AddsText: true, OverheadNote: "declared in its manifest (`trove external show terse-rules`)",
		Claims: []Claim{{ResourceBehaviorRules, "terse-rules"}}, Licence: "Trove project"},
	{ID: "W4", Class: ClassBehavior, Title: "Caveman: a skill that makes replies terse", Names: []string{"caveman"},
		Layers: []Layer{LayerInstructions}, Route: RouteWrap, Status: StatusWrapped, Effect: tierBehavior,
		Subscription: true, AddsText: true, OverheadNote: "counted from the rules block it adds (`trove external show caveman`)",
		Claims:  []Claim{{ResourceBehaviorRules, "caveman"}},
		Licence: "Apache-2.0 (the skill text at v4.1.0)", Source: urlCaveman,
		Note: "wraps its skill text at a pinned tag only, never its proxy, CLI or Auto mode; \"Caveman\" is that project's trademark"},
	{ID: "W5", Class: ClassRecoverable, Title: "Headroom: compress tool output, logs and files", Names: []string{"headroom"},
		Layers: []Layer{LayerRequest, LayerToolBoundary}, Route: RouteIdeas, Status: StatusReference,
		Claims:  []Claim{{ResourceBaseURL, "headroom"}},
		Licence: "Apache-2.0", Source: urlHeadroom,
		Note: "its proxy and `headroom wrap` claim the agent's API endpoint, so they are never chained with Trove's gateway; its Python library (`compress()`, in a custom agent's own code) and MCP server do not, and Trove does not install them. It has no CLI that shortens Bash output without its proxy, and its usage beacon is on by default (verified 2026-11-18)"},
	{ID: "W6", Class: ClassBehavior, Title: "LLMLingua: prompt compression with a model", Names: []string{"llmlingua"},
		Layers: []Layer{LayerRequest}, Route: RouteIdeas, Status: StatusReference,
		Licence: "MIT", Source: urlLLMLingua, Note: "Python and a model; prompt-level compression is not Autopilot-eligible"},
	{ID: "W7", Class: ClassBehavior, Title: "SuperCompress: query-aware context compression, hosted", Names: []string{"supercompress"},
		Layers: []Layer{LayerRequest}, Route: RouteNone, Status: StatusExcluded,
		Licence: "MIT repository; the hosted API receives your text", Source: urlSuperComp,
		Note: "it sends your prompts off the machine, which PRINCIPLES.md rule 1 does not allow (name match not fully verified)"},
	{ID: "W8", Class: ClassConfig, Title: "ccusage: usage analysis from local logs", Names: []string{"ccusage"},
		Layers: []Layer{LayerConfig}, Route: RouteIdeas, Status: StatusReference,
		Licence: "MIT", Source: urlCCUsage, Note: "`trove logs` and `trove report` already do this"},
	{ID: "W9", Class: ClassBehavior, Title: "Ponytail: rules that make the agent write less code", Names: []string{"ponytail"},
		Layers: []Layer{LayerInstructions}, Route: RouteWrap, Status: StatusWrapped, Effect: tierBehavior,
		Subscription: true, AddsText: true, OverheadNote: "counted from the rules block it adds (`trove external show ponytail`)",
		Claims:  []Claim{{ResourceBehaviorRules, "ponytail"}},
		Licence: "MIT", Source: urlPonytail,
		Note: "wraps its AGENTS.md rules text at a pinned tag only, never its plugin, skill or hooks"},
	{ID: "W10", Class: ClassRecoverable, Title: "ZTK: rewrite shell commands so their output is smaller", Names: []string{"ztk"},
		Layers: []Layer{LayerToolBoundary}, Route: RouteWrap, Status: StatusWrapped, Effect: tierBehavior,
		Subscription: true, OverheadNote: "declared in its manifest (`trove external show ztk`)",
		Claims:  []Claim{{ResourceShellCompressor, "ztk"}},
		Licence: "MIT", Source: urlZTK,
		Note: "Trove writes its PreToolUse Bash hook itself at a pinned version and never runs `ztk init`"},
}
