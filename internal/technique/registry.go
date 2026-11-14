package technique

// registry is the code form of the tables in docs/techniques.md. Every ID there
// must be here and the other way round; TestRegistryMatchesTheDocument checks it.
// Sources and licences were verified on 2026-11-13 (docs/techniques.md, "What was
// verified"). When an entry changes, change the document in the same commit.

var zero = 0

func none() *int { return &zero }

const (
	urlRTK        = "https://github.com/rtk-ai/rtk"
	urlHeadroom   = "https://github.com/headroomlabs-ai/headroom"
	urlCaveman    = "https://github.com/JuliusBrussee/caveman"
	urlLLMLingua  = "https://github.com/microsoft/LLMLingua"
	urlSavior     = "https://github.com/Mibayy/token-savior"
	urlSuperComp  = "https://github.com/Supercompress/Supercompress"
	urlCCUsage    = "https://github.com/ccusage/ccusage"
	ownerTrove    = "trove"
	tierLossless  = "lossless"
	tierRecover   = "recoverable"
	tierBehavior  = "behavior"
	noticeInCount = "the text it adds is counted inside the tokens it removes"
)

var gatewayClaim = []Claim{{ResourceBaseURL, ownerTrove}}

var registry = []Technique{
	// ---- Built ---------------------------------------------------------------
	{ID: "B1", Title: "Strip colour codes, progress frames and repeated blank lines", Names: []string{"noise_normalizer"},
		Layers: []Layer{LayerToolBoundary, LayerRequest}, Route: RouteNative, Status: StatusBuilt, Tier: tierLossless,
		Subscription: true, Auto: true, Overhead: none(), Claims: []Claim{{ResourceShellCompressor, ownerTrove}},
		Licence: "Trove project", Note: "lossless; acts on new tool results only"},
	{ID: "B2", Title: "Replace a repeated identical tool result with a pointer", Names: []string{"duplicate_result_collapser"},
		Layers: []Layer{LayerRequest}, Route: RouteNative, Status: StatusBuilt, Tier: tierLossless,
		Gateway: true, Auto: true, Overhead: none(), OverheadNote: noticeInCount, Claims: gatewayClaim,
		Licence: "Trove project", Note: "deterministic, so a resent history gives the same bytes"},
	{ID: "B3", Title: "Keep failures and the first and last lines of long test and build output", Names: []string{"output_trimmer"},
		Layers: []Layer{LayerToolBoundary, LayerRequest}, Route: RouteNative, Status: StatusBuilt, Tier: tierRecover,
		Subscription: true, AddsText: true, OverheadNote: noticeInCount + "; the MCP tool definitions it relies on are B9's overhead",
		Requires: []string{CapMCPServer, CapRecoveryStore}, Claims: []Claim{{ResourceShellCompressor, ownerTrove}},
		Licence: "Trove project", Note: "full output is kept locally and can be fetched back; Bash output only in hooks"},
	{ID: "B4", Title: "Move changing lines out of the stable prompt", Names: []string{"volatile_prefix_mover"},
		Layers: []Layer{LayerRequest}, Route: RouteNative, Status: StatusBuilt, Tier: tierLossless, Prefix: true,
		Gateway: true, Overhead: none(), Requires: []string{CapCustomAgent}, Claims: gatewayClaim,
		Licence: "Trove project", Note: "custom agents only"},
	{ID: "B5", Title: "Place cache markers on the final prompt layout", Names: []string{"cache_breakpoints"},
		Layers: []Layer{LayerRequest}, Route: RouteNative, Status: StatusBuilt, Tier: tierLossless, Prefix: true,
		Gateway: true, Overhead: none(), Requires: []string{CapCustomAgent}, Claims: gatewayClaim,
		Licence: "Trove project", Note: "custom agents only; at most 3 of the 4 allowed markers"},
	{ID: "B6", Title: "Deny a repeat read of an unchanged file", Names: []string{"hook_repeat_read"},
		Layers: []Layer{LayerToolBoundary}, Route: RouteNative, Status: StatusBuilt, Tier: tierLossless,
		Subscription: true, AddsText: true, OverheadNote: "a one-line denial that replaces a whole file read",
		Licence: "Trove project"},
	{ID: "B7", Title: "Add a line limit to a large file read", Names: []string{"hook_read_limit"},
		Layers: []Layer{LayerToolBoundary}, Route: RouteNative, Status: StatusBuilt, Tier: tierRecover,
		Subscription: true, AddsText: true, OverheadNote: "a short note that the read was partial and how to read more",
		Licence: "Trove project", Note: "rewrites the whole tool input (updatedInput replaces it)"},
	{ID: "B8", Title: "Hint to use trove_run for known-noisy commands", Names: []string{"hook_large_output"},
		Layers: []Layer{LayerToolBoundary}, Route: RouteNative, Status: StatusBuilt, Tier: tierLossless,
		Subscription: true, AddsText: true, OverheadNote: "one short hint per noisy command",
		Requires: []string{CapMCPServer}, Claims: []Claim{{ResourceShellCompressor, ownerTrove}},
		Licence: "Trove project"},
	{ID: "B15", Title: "Add one output-quieting flag to an allowlisted command", Names: []string{"hook_quiet_commands"},
		Layers: []Layer{LayerToolBoundary}, Route: RouteNative, Status: StatusBuilt, Tier: tierLossless,
		Subscription: true, Overhead: none(), Licence: "Trove project",
		Note: "rewrites the Bash command the way RTK does, but it does not claim the shell-compressor slot, matching what `trove external` refuses today; whether the two should be refused together is open"},
	{ID: "B9", Title: "Trove MCP tools: trove_read, trove_search, trove_run, trove_fetch_full", Names: []string{"trove_mcp"},
		Layers: []Layer{LayerToolBoundary}, Route: RouteNative, Status: StatusBuilt, Tier: tierLossless,
		Subscription: true, AddsText: true, OverheadNote: "tool definitions go into every request; measured by `trove mcp schema-size`",
		Claims: []Claim{{ResourceCodeNav, ownerTrove}}, Licence: "Trove project"},
	{ID: "B10", Title: "Config fixes with a diff and an undo", Names: []string{"mcp_prune", "context_slim", "cache_breaker_fix", "model_default"},
		Layers: []Layer{LayerConfig}, Route: RouteConfig, Status: StatusBuilt, Tier: tierBehavior,
		Subscription: true, Overhead: none(), Licence: "Trove project", Note: "each fix shows its evidence and is applied only on a yes"},
	{ID: "B11", Title: "Scout subagent and test-gated cascade", Names: []string{"task_scout", "task_cascade"},
		Layers: []Layer{LayerInstructions, LayerConfig}, Route: RouteNative, Status: StatusBuilt, Tier: tierBehavior,
		Subscription: true, AddsText: true, OverheadNote: "shown by `trove tasks plan`", Licence: "Trove project"},
	{ID: "B12", Title: "Autopilot: promotion and rollback at session boundaries", Names: []string{"autopilot"},
		Layers: []Layer{LayerConfig, LayerToolBoundary, LayerRequest}, Route: RouteNative, Status: StatusBuilt,
		Subscription: true, Overhead: none(), Licence: "Trove project", Note: "a controller, not a saving of its own"},
	{ID: "B13", Title: "Flag and remove a cost-raising setting you already have", Names: []string{"settings_check"},
		Layers: []Layer{LayerConfig}, Route: RouteConfig, Status: StatusBuilt, Tier: tierBehavior,
		Subscription: true, Overhead: none(), Licence: "Trove project", Note: "a suggestion only, never part of --all"},
	{ID: "B14", Title: "Catalog of the agent's documented token controls, with version checks and profiles", Names: []string{"settings"},
		Layers: []Layer{LayerConfig}, Route: RouteConfig, Status: StatusBuilt, Tier: tierBehavior,
		Subscription: true, Overhead: none(), Licence: "Trove project"},

	// ---- Candidates and partly built -----------------------------------------
	{ID: "C1", Title: "Cache lifetime: 5 minutes or 1 hour", Names: []string{"cache_ttl"},
		Layers: []Layer{LayerConfig, LayerRequest}, Route: RouteConfig, Status: StatusPartial, Tier: tierBehavior,
		Subscription: true, Overhead: none(), Licence: "Trove project",
		Note: "built: a per-user estimate (the cache_ttl suggestion) and the verified settings; not built: a per-breakpoint split in the gateway. A blanket downgrade is not a default (docs/techniques.md, finding 2)"},
	{ID: "C2", Title: "Hold sibling subagent requests until the first cache entry exists", Names: []string{"fan_out"},
		Layers: []Layer{LayerRequest}, Route: RouteNative, Status: StatusCandidate, Tier: tierLossless,
		Gateway: true, Claims: gatewayClaim, Licence: "Trove project", Note: "needs a latency budget; not verified against provider docs"},
	{ID: "C3", Title: "Let identical simultaneous requests share one upstream call", Names: []string{"inflight_dedup"},
		Layers: []Layer{LayerRequest}, Route: RouteNative, Status: StatusCandidate, Tier: tierLossless,
		Gateway: true, Claims: gatewayClaim, Licence: "Trove project", Note: "must fail open on any doubt"},
	{ID: "C4", Title: "Answer identical requests from a local response cache", Names: []string{"exact_cache"},
		Layers: []Layer{LayerRequest}, Route: RouteNative, Status: StatusCandidate, Tier: tierBehavior,
		Gateway: true, Claims: gatewayClaim, Licence: "Trove project", Note: "a correctness risk with agents; opt-in only"},
	{ID: "C5", Title: "Hard spend cap checked against the ledger", Names: []string{"spend_cap"},
		Layers: []Layer{LayerRequest}, Route: RouteNative, Status: StatusCandidate, Tier: tierBehavior,
		Gateway: true, Claims: gatewayClaim, Licence: "Trove project"},
	{ID: "C6", Title: "Effort level per task", Names: []string{"effortLevel"},
		Layers: []Layer{LayerConfig}, Route: RouteConfig, Status: StatusPartial, Tier: tierBehavior, Prefix: true,
		Subscription: true, Overhead: none(), Licence: "Trove project",
		Note: "the setting is verified and writable; no profile sets a value without a benchmark. A change of effort can invalidate cached message blocks, so it applies to new sessions only"},
	{ID: "C7", Title: "Choose the model per subagent",
		Layers: []Layer{LayerConfig, LayerInstructions}, Route: RouteConfig, Status: StatusPartial, Tier: tierBehavior,
		Subscription: true, Overhead: none(), Licence: "Trove project", Note: "the scout and cascade (B11) use it; no general setting is written yet"},
	{ID: "C8", Title: "Swap the model per request in the gateway", Names: []string{"model_swap"},
		Layers: []Layer{LayerRequest}, Route: RouteNative, Status: StatusCandidate, Tier: tierBehavior, Prefix: true,
		Gateway: true, Claims: gatewayClaim, Licence: "Trove project", Note: "not recommended: a different model has its own cache; same family only, if ever"},
	{ID: "C9", Title: "Output size limits for commands, MCP results and file reads",
		Names:  []string{"bashOutputMaxChars", "MAX_MCP_OUTPUT_TOKENS", "CLAUDE_CODE_FILE_READ_MAX_OUTPUT_TOKENS"},
		Layers: []Layer{LayerConfig}, Route: RouteConfig, Status: StatusPartial, Tier: tierBehavior,
		Subscription: true, Overhead: none(), Licence: "Trove project", Note: "verified and writable; no profile sets a value without a benchmark"},
	{ID: "C10", Title: "Compaction timing", Names: []string{"autoCompactWindow", "idleCompaction"},
		Layers: []Layer{LayerConfig}, Route: RouteConfig, Status: StatusPartial, Tier: tierBehavior, Prefix: true,
		Subscription: true, Overhead: none(), Licence: "Trove project", Note: "compaction rewrites history by design, so earlier cached prefixes are lost"},
	{ID: "C11", Title: "Tool search for MCP tool definitions", Names: []string{"ENABLE_TOOL_SEARCH"},
		Layers: []Layer{LayerConfig}, Route: RouteConfig, Status: StatusPartial, Tier: tierBehavior,
		Subscription: true, Overhead: none(), Licence: "Trove project", Note: "verified and writable; settings_check flags a stray =false"},
	{ID: "C12", Title: "Replace tool output for more tools than Bash", Names: []string{"tool_output_rewrite"},
		Layers: []Layer{LayerToolBoundary}, Route: RouteNative, Status: StatusCandidate, Tier: tierRecover,
		Subscription: true, Licence: "Trove project", Note: "the hooks reference says updatedToolOutput applies to any tool; Trove's hooks use it for Bash, Grep and Glob"},
	{ID: "C13", Title: "Ask for terse replies and minimal code", Names: []string{"terse_replies"},
		Layers: []Layer{LayerInstructions}, Route: RouteNative, Status: StatusCandidate, Tier: tierBehavior,
		Subscription: true, AddsText: true, OverheadNote: "the rules text is carried in every session; its effect must be measured, not assumed",
		Claims: []Claim{{ResourceBehaviorRules, ownerTrove}}, Licence: "Trove project"},

	// ---- Other people's tools ------------------------------------------------
	{ID: "W1", Title: "RTK: rewrite shell commands so their output is smaller", Names: []string{"rtk"},
		Layers: []Layer{LayerToolBoundary}, Route: RouteWrap, Status: StatusWrapped, Tier: tierBehavior,
		Subscription: true, OverheadNote: "declared in its manifest (`trove external show rtk`)",
		Claims:  []Claim{{ResourceShellCompressor, "rtk"}},
		Licence: "Apache-2.0 (Apache-only build); BUSL-1.1 in the default build", Source: urlRTK,
		Note: "Trove installs only the Apache-only build at a pinned tag"},
	{ID: "W2", Title: "token-savior: MCP server for code navigation", Names: []string{"token-savior"},
		Layers: []Layer{LayerToolBoundary}, Route: RouteWrap, Status: StatusWrapped, Tier: tierBehavior,
		Subscription: true, AddsText: true, OverheadNote: "declared in its manifest (`trove external show token-savior`)",
		Claims:  []Claim{{ResourceCodeNav, "token-savior"}},
		Licence: "MIT", Source: urlSavior, Note: "its benchmark is self-reported and its harness is not public"},
	{ID: "W3", Title: "terse-rules: a short rules block", Names: []string{"terse-rules"},
		Layers: []Layer{LayerInstructions}, Route: RouteWrap, Status: StatusWrapped, Tier: tierBehavior,
		Subscription: true, AddsText: true, OverheadNote: "declared in its manifest (`trove external show terse-rules`)",
		Claims: []Claim{{ResourceBehaviorRules, "terse-rules"}}, Licence: "Trove project"},
	{ID: "W4", Title: "Caveman: a skill that makes replies terse", Names: []string{"caveman"},
		Layers: []Layer{LayerInstructions}, Route: RouteWrap, Status: StatusCandidate, Tier: tierBehavior,
		Subscription: true, AddsText: true, OverheadNote: "the skill text, to be declared before it ships",
		Claims:  []Claim{{ResourceBehaviorRules, "caveman"}},
		Licence: "Apache-2.0 from v3.0.0; earlier engine and proxy parts were BUSL-1.1", Source: urlCaveman,
		Note: "wrap the skill only, never its proxy; \"Caveman\" is that project's trademark"},
	{ID: "W5", Title: "Headroom: compress tool output, logs and files", Names: []string{"headroom"},
		Layers: []Layer{LayerRequest, LayerToolBoundary}, Route: RouteIdeas, Status: StatusReference,
		Claims:  []Claim{{ResourceBaseURL, "headroom"}},
		Licence: "Apache-2.0", Source: urlHeadroom, Note: "its proxy and Trove's gateway both claim the agent's API endpoint"},
	{ID: "W6", Title: "LLMLingua: prompt compression with a model", Names: []string{"llmlingua"},
		Layers: []Layer{LayerRequest}, Route: RouteIdeas, Status: StatusReference,
		Licence: "MIT", Source: urlLLMLingua, Note: "Python and a model; prompt-level compression is not Autopilot-eligible"},
	{ID: "W7", Title: "SuperCompress: query-aware context compression, hosted", Names: []string{"supercompress"},
		Layers: []Layer{LayerRequest}, Route: RouteNone, Status: StatusExcluded,
		Licence: "MIT repository; the hosted API receives your text", Source: urlSuperComp,
		Note: "it sends your prompts off the machine, which CLAUDE.md rule 1 does not allow (name match not fully verified)"},
	{ID: "W8", Title: "ccusage: usage analysis from local logs", Names: []string{"ccusage"},
		Layers: []Layer{LayerConfig}, Route: RouteIdeas, Status: StatusReference,
		Licence: "MIT", Source: urlCCUsage, Note: "`trove logs` and `trove report` already do this"},
}
