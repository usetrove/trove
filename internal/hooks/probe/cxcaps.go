package probe

import (
	"context"
	"strings"

	"github.com/usetrove/trove/internal/hooks"
)

// The entries in this file record what each agent's documentation says hooks can
// change. They are not measurements and unlock nothing (hooks.Capabilities.Supports
// counts only "live" and "session"); they are shown by `trove hooks status` and
// `trove doctor` so the user can see what a live probe would be checking.

// CodexDocs is what Codex CLI's documentation says.
//
// Re-read 2026-11-17 at learn.chatgpt.com/docs/hooks: PreToolUse can rewrite a call
// only by returning permissionDecision "allow" with updatedInput (Bash needs a string
// command), and Trove's hooks never send "allow". PostToolUse has no field that
// replaces a tool's output: decision "block" adds feedback, continue false stops
// processing, and updatedMCPToolOutput is listed as unsupported.
func CodexDocs(ctx context.Context, o Options, now string) hooks.Capability {
	return hooks.Capability{Agent: "codex", AgentVersion: agentVersion(ctx, o, "codex"), Tool: "Bash", Method: "docs", ProbedAt: now,
		InputRewrite: hooks.CapUntested, OutputRewrite: hooks.CapNo,
		Evidence: "documentation only (not run): updatedInput is accepted only with permissionDecision \"allow\", which Trove does not send, so input rewriting needs a live probe; no PostToolUse field replaces Bash output"}
}

// CursorDocs is what Cursor's documentation says.
//
// Read 2026-11-17 at cursor.com/docs/agent/hooks: preToolUse may return
// updated_input; postToolUse may replace output only for MCP tools
// (updated_mcp_tool_output), so a shell command's output cannot be replaced.
// The version is left empty: the hooks page names no command that prints it, and
// guessing an executable to run is not worth it for a record that unlocks nothing.
func CursorDocs(now string) hooks.Capability {
	return hooks.Capability{Agent: "cursor", Tool: "Bash", Method: "docs", ProbedAt: now,
		InputRewrite: hooks.CapUntested, OutputRewrite: hooks.CapNo,
		Evidence: "documentation only (not run): preToolUse documents updated_input; postToolUse replaces output for MCP tools only"}
}

// GeminiDocs is what Gemini CLI's documentation says.
//
// Read 2026-11-17 at geminicli.com/docs/hooks/reference/: BeforeTool's
// hookSpecificOutput.tool_input merges into the tool's arguments; AfterTool's
// decision "deny" replaces the result the model sees with reason.
func GeminiDocs(ctx context.Context, o Options, now string) hooks.Capability {
	return hooks.Capability{Agent: "gemini", AgentVersion: agentVersion(ctx, o, "gemini"), Tool: "Bash", Method: "docs", ProbedAt: now,
		InputRewrite: hooks.CapUntested, OutputRewrite: hooks.CapUntested,
		Evidence: "documentation only (not run): BeforeTool hookSpecificOutput.tool_input merges into the arguments; AfterTool decision \"deny\" replaces the result with reason"}
}

// agentVersion asks an installed agent for its version; "" when it is not installed.
func agentVersion(ctx context.Context, o Options, binary string) string {
	if o.Exec == nil {
		return ""
	}
	out, err := o.Exec.Run(ctx, "", orEnv(o.Env), binary, "--version")
	if err != nil {
		return ""
	}
	if f := strings.Fields(string(out)); len(f) > 0 {
		return f[len(f)-1]
	}
	return ""
}
