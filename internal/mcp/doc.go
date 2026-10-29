// Package mcp is Trove's local MCP server: four tools that return less text to
// the agent (trove_read, trove_search, trove_run, trove_fetch_full) over stdio.
//
// Rules the code here holds to:
//   - Read-only access is confined to the project folder (see internal/mcp/root);
//     symlinks that leave it are refused.
//   - trove_run executes only commands named in .trove/commands.toml, directly
//     and never through a shell, with credential-looking environment variables
//     removed.
//   - Anything shortened is stored in full in the recovery store
//     (internal/recovery) first, and trove_fetch_full returns it. If it cannot be
//     stored it is not shortened.
//   - Nothing is sent over the network and nothing is logged to stdout, which
//     carries the protocol.
//
// See docs/mcp.md.
package mcp
