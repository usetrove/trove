# Supported agents and versions tested

This page says what has actually been tested, not what we hope works. Update the table whenever a check is run; a blank means not tested.

Last reviewed: 2026-10-30.

| Agent | Feature | Version tested | OS tested | Status |
|---|---|---|---|---|
| Claude Code | History parser (`trove logs`) | 2.1.302 (format inspected on a local install) | Windows 11 | Parsed against real files |
| Claude Code | Gateway via custom endpoint | | | Not yet tested on a real install ([gateway-manual-test.md](gateway-manual-test.md)) |
| Claude Code | Hooks | | | Not yet tested on a real install ([hooks-verification.md](hooks-verification.md)) |
| Claude Code | MCP server | | | Not yet tested on a real install ([mcp-verification.md](mcp-verification.md)) |
| Claude Code | Status line | | | Not yet run by a person ([report-verification.md](report-verification.md)) |
| Claude Code | `trove fix` | | | Not yet tested on a real install ([fixer-verification.md](fixer-verification.md)) |
| Codex CLI | History parser | | | Format not verified on a real install ([logs.md](logs.md)) |
| Codex CLI | Gateway, hooks, MCP | | | Not yet tested |

macOS and Linux: not yet tested on real machines. Automated tests so far ran on Windows 11 and in CI.

Other versions of Claude Code are read on a best-effort basis; files from outside the 2.x line are flagged rather than guessed at.

## How a row gets filled in

1. Run the matching verification checklist on a clean machine with the agent version you name.
2. Record the agent version (`claude --version`, `codex --version`), the OS, the date and who ran it.
3. Change the status only to what the checklist showed. If anything failed, say so in the Status column and link the issue.

Agent behavior (config locations, custom-endpoint settings, hooks, log formats) must be re-checked against the agent's current official documentation before each release. See [doctor.go](../internal/cli/doctor.go) for the sources and dates already verified.
