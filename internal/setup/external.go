package setup

import (
	"bytes"
	"strings"
)

// ExternalPrefix starts the Agent name of every change made for a third-party
// tool by `trove external` (external_<tool>). Those changes are undone by change
// ID, never by fix ID, so they stay out of `trove fix` listings.
const ExternalPrefix = "external_"

// IsExternal reports whether an Agent name belongs to a `trove external` change.
func IsExternal(agent string) bool { return strings.HasPrefix(agent, ExternalPrefix) }

// ExternalMarkers returns the comment lines that fence a tool's block in a
// Markdown rules file. They are HTML comments so they do not reach the model
// as visible text in rendered views, and are unique per tool.
func ExternalMarkers(tool string) (open, close string) {
	return "<!-- >>> trove external " + tool + " (managed by `trove external`; remove with `trove external remove " + tool + "`) -->",
		"<!-- <<< trove external " + tool + " -->"
}

// NewExternalEdit builds an Edit for a third-party tool. old is the file as
// planned (nil if it does not exist) and updated is what should replace it.
// openMarker, when set, is the opening fence of a block Trove added; if the user
// edits the file later, undo removes just that block. Without it the file is
// left alone after later edits, because Trove cannot tell which lines are its own.
func NewExternalEdit(tool, path, show, summary, openMarker string, old, updated []byte) *Edit {
	return &Edit{Agent: ExternalPrefix + tool, Path: path, Show: show, Summary: summary,
		old: old, updated: updated, value: openMarker, NoOp: bytes.Equal(old, updated) && old != nil}
}

// stripExternal removes a tool's fenced block from a file edited since Trove wrote it.
func stripExternal(cur []byte, tool, openMarker string) ([]byte, bool) {
	open, close := ExternalMarkers(tool)
	if openMarker == "" || openMarker != open {
		return nil, false
	}
	next, err := stripBetween(cur, open, close)
	if err != nil {
		return nil, false
	}
	return next, true
}
