package hooks

import (
	"encoding/json"

	"github.com/usetrove/trove/internal/hookcaps"
)

// What a hook may change is a fact about the installed agent version, not something to assume
// (see internal/hookcaps, which holds the record's types and the queries on it). This file keeps
// the names the rest of the program already uses, and owns writing the record.

// Capability results.
const (
	CapYes      = hookcaps.Yes
	CapNo       = hookcaps.No
	CapUntested = hookcaps.Untested
)

// Capability is what was observed for one tool of one agent.
type Capability = hookcaps.Capability

// Capabilities is the stored probe result.
type Capabilities = hookcaps.Capabilities

const capabilitiesVersion = hookcaps.Version

// CapabilitiesPath is where the probe result is stored.
func CapabilitiesPath(dir string) string { return hookcaps.Path(dir) }

// LoadCapabilities reads the stored probe result. A missing or unreadable file is the empty
// result, under which nothing is supported.
func LoadCapabilities(dir string) Capabilities { return hookcaps.Load(dir) }

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
	RewriteInput  = hookcaps.RewriteInput
	RewriteOutput = hookcaps.RewriteOutput
)
