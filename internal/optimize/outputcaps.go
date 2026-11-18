package optimize

import (
	"strings"
	"sync"

	"github.com/usetrove/trove/internal/settings"
)

// Agents cut tool output before the model sees it (Bash 30,000 characters, MCP and
// file reads 25,000 tokens by default). Output past the cap was never going to cost
// tokens, so a shadow estimate must not count removing it as a saving. The limits
// live in the settings catalog, each tied to an agent version and a verification date.

var (
	capsOnce sync.Once
	caps     *settings.Catalog
)

// builtinCaps loads the embedded catalog once. If it cannot be read the estimates
// simply stay uncapped (the old behaviour); an estimate is never made from a
// missing cap.
func builtinCaps() *settings.Catalog {
	capsOnce.Do(func() { caps, _ = settings.Builtin() })
	return caps
}

// capTool maps a tool name to the catalog's tool key.
func capTool(name string) string {
	switch {
	case name == "Bash" || name == "PowerShell":
		return "Bash"
	case name == "Read":
		return "Read"
	case strings.HasPrefix(name, "mcp__"):
		return "mcp"
	}
	return ""
}

// outputCapBytes is the most bytes of a tool's output the agent passes on. ok is
// false when the tool has no verified cap. A token cap is turned into bytes at
// bytesPerToken, which is why the learned ratio is passed in where there is one:
// a ratio above the real one overstates the cap and so overstates the saving.
func outputCapBytes(tool string, bytesPerToken float64) (int64, bool) {
	c := builtinCaps()
	key := capTool(tool)
	if c == nil || key == "" {
		return 0, false
	}
	oc, ok := c.OutputCap(settings.ClaudeCode, key)
	if !ok {
		return 0, false
	}
	if oc.Unit == settings.CapTokens {
		return int64(float64(oc.Limit) * bytesPerToken), true
	}
	return oc.Limit, true
}

// cappedSaving is the bytes a rewrite really keeps out of the context: what the
// agent would have passed on (before, cut at the cap) minus what it passes on now
// (after, cut at the same cap). A tool with no cap is counted whole. It is never
// negative.
func cappedSaving(tool string, before, after int, bytesPerToken float64) int {
	if c, ok := outputCapBytes(tool, bytesPerToken); ok {
		before, after = min(before, int(c)), min(after, int(c))
	}
	return max(before-after, 0)
}
