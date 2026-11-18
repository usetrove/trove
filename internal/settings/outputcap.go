package settings

import (
	"fmt"
	"strings"
)

// Units an output cap is counted in.
const (
	CapChars  = "chars"
	CapTokens = "tokens"
)

// OutputCap is a limit an agent puts on one kind of tool output before the model
// sees it. Shadow estimates must not count output past the limit as saved: the
// agent would have cut it anyway. Each cap names the agent version the docs
// described and the day they were read, so a stale figure can be found and fixed.
type OutputCap struct {
	// Tool is "Bash" (shell commands), "mcp" (any MCP tool) or "Read" (file reads).
	Tool  string `toml:"tool" json:"tool"`
	Unit  string `toml:"unit" json:"unit"`
	Limit int64  `toml:"limit" json:"limit"`
	// SettingKey is the catalog setting that changes the limit. Trove does not
	// read the user's value, so a raised limit makes estimates lower, not higher.
	SettingKey string `toml:"setting_key" json:"setting_key,omitempty"`
	// DocsVersion is the newest agent version the docs page described.
	DocsVersion string `toml:"docs_version" json:"docs_version"`
	DocURL      string `toml:"doc_url" json:"doc_url"`
	VerifiedOn  string `toml:"verified_on" json:"verified_on"` // YYYY-MM-DD
	Notes       string `toml:"notes" json:"notes,omitempty"`
}

// Validate rejects a cap Trove must not use in an estimate.
func (c OutputCap) Validate() error {
	var bad []string
	add := func(ok bool, msg string) {
		if !ok {
			bad = append(bad, msg)
		}
	}
	add(c.Tool != "", "tool is required")
	add(c.Unit == CapChars || c.Unit == CapTokens, "unit must be chars or tokens")
	add(c.Limit > 0, "limit must be positive")
	_, err := ParseVersion(c.DocsVersion)
	add(err == nil, "docs_version is not a version")
	add(strings.HasPrefix(c.DocURL, "https://"), "doc_url must be https")
	add(dateRE.MatchString(c.VerifiedOn), "verified_on must be YYYY-MM-DD")
	if len(bad) > 0 {
		return fmt.Errorf("output cap %q is invalid: %s", c.Tool, strings.Join(bad, "; "))
	}
	return nil
}

// OutputCap returns the verified output cap for a tool, if the harness has one.
func (c *Catalog) OutputCap(harness, tool string) (OutputCap, bool) {
	f, ok := c.File(harness)
	if !ok {
		return OutputCap{}, false
	}
	for _, oc := range f.OutputCaps {
		if oc.Tool == tool {
			return oc, true
		}
	}
	return OutputCap{}, false
}
