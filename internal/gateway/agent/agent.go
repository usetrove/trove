// Package agent labels the calling tool coarsely. Only the label and a
// version string are kept; the raw User-Agent is never stored.
package agent

import (
	"net/http"
	"regexp"
	"strings"
)

// Labels.
const (
	ClaudeCode = "claude_code"
	Codex      = "codex"
	SDK        = "sdk"
	Other      = "other"
)

var version = regexp.MustCompile(`[/ ]v?(\d+\.\d+(?:\.\d+)?)`)

// Label returns (label, version). The x-claude-code-session-id header is
// documented (code.claude.com/docs/en/llm-gateway-protocol, verified
// 2026-10-28) and is the strongest Claude Code signal. The User-Agent
// patterns below are heuristics: the docs I verified do not specify UA
// formats, so unknown agents fall back to "other".
func Label(h http.Header) (label, ver string) {
	ua := strings.ToLower(h.Get("User-Agent"))
	switch {
	case h.Get("X-Claude-Code-Session-Id") != "" || strings.Contains(ua, "claude-cli") || strings.Contains(ua, "claude-code"):
		label = ClaudeCode
	case strings.Contains(ua, "codex"):
		label = Codex
	case strings.HasPrefix(ua, "anthropic/") || strings.HasPrefix(ua, "openai/") ||
		strings.HasPrefix(ua, "anthropic-") || strings.HasPrefix(ua, "openai-") ||
		strings.Contains(ua, "anthropic-sdk") || strings.Contains(ua, "openai-python") || strings.Contains(ua, "openai-node"):
		label = SDK
	default:
		return Other, ""
	}
	if m := version.FindStringSubmatch(ua); m != nil {
		ver = m[1]
	}
	return label, ver
}
