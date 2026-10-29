package mcp

// Snippet is the optional text users can add to CLAUDE.md or AGENTS.md so the
// agent knows when to prefer Trove's tools. It is a few lines on purpose: context
// files are sent with every request.
const Snippet = `Trove MCP tools are available. Prefer trove_read for files over ~200 lines, trove_search for finding text, and trove_run for test, build and lint. If output says "Trove: N lines trimmed", call trove_fetch_full with its output id to get the rest.`

// SnippetTokens is the estimated token cost of the snippet.
func SnippetTokens() int { return estTokens(len(Snippet)) }
