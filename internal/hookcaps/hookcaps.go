// Package hookcaps holds what a probe observed about which tool calls an agent's hooks may
// rewrite, and reads and queries that record. It is the pure core of what internal/hooks stores
// in <data dir>/hooks/capabilities.json, split out so that internal/autopilot can read the same
// record without importing internal/hooks (which imports autopilot).
//
// What a hook may change is a fact about the installed agent version, not something to assume: a
// release has shipped where PostToolUse output was silently ignored for some built-in tools. So a
// rewrite is only emitted for an agent and tool that a probe (`trove hooks probe`,
// scripts/hookprobe) has seen work, either in a live probe on the user's API key or in a session
// the user started themselves (`trove hooks verify`). Anything unknown counts as unsupported.
package hookcaps

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Capability results.
const (
	Yes      = "yes"
	No       = "no"
	Untested = "untested"
)

// Kinds of rewrite.
const (
	RewriteInput  = "input"
	RewriteOutput = "output"
)

// Version is the schema version of the stored record.
const Version = 1

// Capability is what was observed for one tool of one agent.
type Capability struct {
	Agent         string `json:"agent"`
	AgentVersion  string `json:"agent_version"`
	Tool          string `json:"tool"`
	InputRewrite  string `json:"input_rewrite"`
	OutputRewrite string `json:"output_rewrite"`
	// Method is "live" (Trove ran an agent session on the user's API key and it showed
	// the model the rewritten value), "session" (the user ran the test in their own
	// session with `trove hooks verify`; the output result is what they reported) or
	// "docs" (the agent's documentation says so; nothing was run).
	Method   string `json:"method"`
	Evidence string `json:"evidence"`
	ProbedAt string `json:"probed_at"` // RFC 3339
}

// Capabilities is the stored probe result.
type Capabilities struct {
	V       int          `json:"v"`
	Entries []Capability `json:"entries"`
}

// Path is where the probe result is stored under a data directory.
func Path(dir string) string { return filepath.Join(dir, "hooks", "capabilities.json") }

// Load reads the stored probe result. A missing, unreadable or newer-version file is the empty
// result, under which nothing is supported.
func Load(dir string) Capabilities {
	b, err := os.ReadFile(Path(dir)) //nolint:gosec // path under Trove's data dir
	if err != nil {
		return Capabilities{}
	}
	var c Capabilities
	if json.Unmarshal(b, &c) != nil || c.V != Version {
		return Capabilities{}
	}
	return c
}

// Supports reports whether a hook may rewrite the given tool's input or output for the agent. Only
// a "yes" from a live probe or a verified session counts.
func (c Capabilities) Supports(agent, tool, kind string) bool {
	for _, e := range c.Entries {
		if e.Agent != agent || e.Tool != tool {
			continue
		}
		// Codex is not available to probe, and its documentation states Bash input rewriting
		// (learn.chatgpt.com/docs/hooks, read 2026-11-04), so a docs result counts there for
		// input only. Everything else needs a live probe.
		if e.Method != "live" && e.Method != "session" && (e.Method != "docs" || agent != "codex" || kind != RewriteInput) {
			continue
		}
		switch kind {
		case RewriteInput:
			return e.InputRewrite == Yes
		case RewriteOutput:
			return e.OutputRewrite == Yes
		}
	}
	return false
}

// SupportsAny reports whether any agent in the record is supported for the tool and kind. The
// hooks' events do not say which agent produced them, so a check made on behalf of the whole
// install asks whether any probed agent can do it.
func (c Capabilities) SupportsAny(tool, kind string) bool {
	seen := map[string]bool{}
	for _, e := range c.Entries {
		if seen[e.Agent] {
			continue
		}
		seen[e.Agent] = true
		if c.Supports(e.Agent, tool, kind) {
			return true
		}
	}
	return false
}

// Find returns the entry for an agent and tool.
func (c Capabilities) Find(agent, tool string) (Capability, bool) {
	for _, e := range c.Entries {
		if e.Agent == agent && e.Tool == tool {
			return e, true
		}
	}
	return Capability{}, false
}
