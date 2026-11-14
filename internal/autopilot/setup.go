package autopilot

import (
	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/hookcaps"
	"github.com/usetrove/trove/internal/mcp/install"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/technique"
)

// The user's real setup, read from Trove's own files and database. Nothing here runs the agent,
// reads a credential or touches the network.
//
// Why it matters: an optimizer can be set to "on" while the program quietly runs it as shadow
// because something it needs is missing. A hook that rewrites a tool call, for example, only acts
// for an agent version a probe has seen honour that rewrite; otherwise the hook stays in shadow.
// If Autopilot records "on" in that case, the report says something is saving money that is not.
// So before it promotes anything, and again at every check after, it asks whether the optimizer can
// really act here.

// Setup is what Autopilot knows about where optimizers can run.
type Setup struct {
	// Traffic says which channels, the gateway and the hooks, recorded anything recently.
	Traffic measure.Traffic
	// MCP is true when Trove's MCP server is registered with an agent. It implies the recovery store.
	MCP bool
	// CustomAgents are the agents the user marked as custom, whose prompt Trove may lay out.
	CustomAgents []string
	// Hooks is the stored hook probe result: which tool rewrites an agent version was seen to honour.
	// The plugin replaces it with a docs-only record when Claude Code is updated, so reading the file is
	// reading the current fact; nothing here needs to run `claude`.
	Hooks hookcaps.Capabilities
}

// detectSetup reads the setup for a data directory and configuration.
func detectSetup(dir string, cfg config.Config, traffic measure.Traffic) Setup {
	return Setup{Traffic: traffic, MCP: install.Registered(dir), CustomAgents: cfg.Optimize.CustomAgents, Hooks: hookcaps.Load(dir)}
}

// hookTools are the tools whose rewrites the registry can ask for.
var hookTools = []string{"Bash", "Read", "Grep", "Glob"}

// context turns the setup into the registry's terms.
func (s Setup) context() technique.Context {
	ctx := technique.Context{Login: technique.LoginUnknown, GatewayOn: s.Traffic.Gateway}
	if s.Traffic.Gateway {
		// The gateway refuses every login except an API key, so traffic through it is an API key.
		ctx.Login = technique.LoginAPIKey
	}
	if s.MCP {
		ctx.Have = append(ctx.Have, technique.CapMCPServer, technique.CapRecoveryStore)
	}
	if len(s.CustomAgents) > 0 {
		ctx.Have = append(ctx.Have, technique.CapCustomAgent)
	}
	for _, tool := range hookTools {
		for _, kind := range []string{hookcaps.RewriteInput, hookcaps.RewriteOutput} {
			if s.Hooks.SupportsAny(tool, kind) {
				ctx.HookCaps = append(ctx.HookCaps, tool+":"+kind)
			}
		}
	}
	return ctx
}

// prerequisiteFor says why the named optimizer cannot really act in this setup, in plain English, or
// returns "" when it can. With no traffic recorded on either channel it returns "", because the setup
// is not known yet and the optimizer is still collecting.
func prerequisiteFor(name string, s Setup) string {
	if !s.Traffic.Gateway && !s.Traffic.Hooks {
		return ""
	}
	if r := technique.Check([]string{name}, s.context()); len(r.Unmet) > 0 {
		return r.Unmet[0].Problem
	}
	return ""
}

// whyUnavailable is the one reason, if any, an optimizer cannot run here: first what the traffic shows
// about the channels, then what the setup is missing.
func whyUnavailable(name string, s Setup) string {
	if r := unavailableFor(name, s.Traffic); r != "" {
		return r
	}
	return prerequisiteFor(name, s)
}
