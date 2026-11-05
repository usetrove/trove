package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// What a hook may change is a fact about the installed agent version, not
// something to assume: a release has shipped where PostToolUse output was
// silently ignored for some built-in tools. So a rewrite is only emitted for an
// agent and tool that a probe (`trove hooks probe`, scripts/hookprobe) has seen
// work. Anything unknown counts as unsupported; the optimizer then stays in
// shadow and `trove doctor` says why.

// Capability results.
const (
	CapYes      = "yes"
	CapNo       = "no"
	CapUntested = "untested"
)

// Capability is what was observed for one tool of one agent.
type Capability struct {
	Agent         string `json:"agent"`
	AgentVersion  string `json:"agent_version"`
	Tool          string `json:"tool"`
	InputRewrite  string `json:"input_rewrite"`
	OutputRewrite string `json:"output_rewrite"`
	// Method is "live" (a real session showed the model the rewritten value) or
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

const capabilitiesVersion = 1

// CapabilitiesPath is where the probe result is stored.
func CapabilitiesPath(dir string) string { return filepath.Join(dir, "hooks", "capabilities.json") }

// LoadCapabilities reads the stored probe result. A missing or unreadable file
// is the empty result, under which nothing is supported.
func LoadCapabilities(dir string) Capabilities {
	b, err := os.ReadFile(CapabilitiesPath(dir)) //nolint:gosec // path under Trove's data dir
	if err != nil {
		return Capabilities{}
	}
	var c Capabilities
	if json.Unmarshal(b, &c) != nil || c.V != capabilitiesVersion {
		return Capabilities{}
	}
	return c
}

// SaveCapabilities stores c atomically.
func SaveCapabilities(dir string, c Capabilities) error {
	c.V = capabilitiesVersion
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(CapabilitiesPath(dir), append(b, '\n'))
}

// Kinds of rewrite.
const (
	RewriteInput  = "input"
	RewriteOutput = "output"
)

// Supports reports whether a hook may rewrite the given tool's input or output
// for the agent. Only a "yes" from a live probe counts.
func (c Capabilities) Supports(agent, tool, kind string) bool {
	for _, e := range c.Entries {
		if e.Agent != agent || e.Tool != tool {
			continue
		}
		// Codex is not available to probe, and its documentation states Bash input
		// rewriting (learn.chatgpt.com/docs/hooks, read 2026-11-04), so a docs
		// result counts there for input only. Everything else needs a live probe.
		if e.Method != "live" && (e.Method != "docs" || agent != "codex" || kind != RewriteInput) {
			continue
		}
		switch kind {
		case RewriteInput:
			return e.InputRewrite == CapYes
		case RewriteOutput:
			return e.OutputRewrite == CapYes
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
