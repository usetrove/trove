package probe

import (
	"context"
	"strings"

	"github.com/usetrove/trove/internal/hooks"
)

// CodexDocs is what Codex CLI's documentation says hooks can change. It is not a
// measurement: Codex could not be run here, so it counts only for Bash input
// (hooks.Capabilities.Supports), never for output.
//
// Read 2026-11-04 at learn.chatgpt.com/docs/hooks: PreToolUse can rewrite a call
// by returning permissionDecision "allow" with updatedInput, and for Bash the
// updatedInput must include a string command; PostToolUse "updatedMCPToolOutput
// and suppressOutput are parsed but not supported yet".
func CodexDocs(ctx context.Context, o Options, now string) hooks.Capability {
	ver := ""
	if o.Exec != nil {
		if out, err := o.Exec.Run(ctx, "", orEnv(o.Env), "codex", "--version"); err == nil {
			if f := strings.Fields(string(out)); len(f) > 0 {
				ver = f[len(f)-1]
			}
		}
	}
	return hooks.Capability{Agent: "codex", AgentVersion: ver, Tool: "Bash", Method: "docs", ProbedAt: now,
		InputRewrite: hooks.CapYes, OutputRewrite: hooks.CapNo,
		Evidence: "documentation only (not run): PreToolUse updatedInput works for Bash with a string command; PostToolUse output replacement is parsed but not supported"}
}
