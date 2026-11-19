# Supported agents and versions tested

This page says what has actually been tested, not what we hope works. Update the table whenever a check is run; a blank means not tested.

Last reviewed: 2026-11-18.

| Agent | Feature | Version tested | OS tested | Status |
|---|---|---|---|---|
| Claude Code | History parser (`trove logs`) | 2.1.302 (format inspected on a local install) | Windows 11 | Parsed against real files |
| Claude Code | Gateway via custom endpoint | 2.1.319 | Linux amd64 (Docker) | Ran in all 90 benchmark sessions of `launch-3` (2026-11-18), API-key auth, recording every request |
| Claude Code | Model routing (`model_router`) | 2.1.319 | Linux amd64 (Docker) | Ran in 30 `launch-3` sessions: `claude-sonnet-5-5` routed to `claude-haiku-5-5`, 30 of 30 tasks passed |
| Claude Code | Plugin hooks | 2.1.319 | Linux amd64 (Docker) | Live hook probe passed (2026-11-17); hooks ran in 60 `launch-3` sessions and acted in 4 of them |
| Claude Code | MCP server | 2.1.319 | Linux amd64 (Docker) | Registered by the plugin in 60 `launch-3` sessions; whether the agent called its tools was not recorded |
| Claude Code | Spend-cap refusal | 2.1.327 | | The agent printed Trove's refusal and stopped after one request, with no retry |
| Claude Code | Status line | | | Not yet run by a person |
| Claude Code | `trove fix` | | | Not yet tested on a real install |
| Claude Code | `/trove:setup`, plugin install from the marketplace | | | Not yet run by a person on a real machine |
| OpenCode 1.x | Plugin (`plugins/opencode`), tool-level | | | Not yet run on a real OpenCode. Hook shapes read from 1.19.5; engine side tested against a real `trove engine serve` |
| OpenCode 2.x | Plugin (`plugins/opencode`), tool-level and request-level (Anthropic) | | | Not yet run on a real OpenCode. Hook shapes read from 2.2.5; engine side tested against a real `trove engine serve` |
| Codex CLI | History parser | | | Format not verified on a real install ([logs.md](logs.md)) |
| Codex CLI | Gateway, hooks, MCP | | | Not yet tested |
| Cursor | Hooks (experimental, `--agent cursor` only) | | | Built from the documentation; not yet run on a real install |
| Gemini CLI | Hooks and history (experimental, `--agent gemini` only) | | | Built from the documentation; not yet run on a real install |

The benchmark sessions ran Claude Code inside Linux containers, not on a person's machine. macOS has not been tested on a real machine; CI runs the Go test suite on macOS and Linux and tests the Windows installer on Windows; the full suite also runs on Windows 11 during development.

Other versions of Claude Code are read on a best-effort basis; files from outside the 2.x line are flagged rather than guessed at.

## How a row gets filled in

1. Run the feature on a clean machine with the agent version you name, following the steps in its doc (for example [hooks.md](hooks.md) or [gateway.md](gateway.md)).
2. Record the agent version (`claude --version`, `codex --version`), the OS, the date and who ran it.
3. Change the status only to what the run showed. If anything failed, say so in the Status column and link the issue.

Agent behavior (config locations, custom-endpoint settings, hooks, log formats) must be re-checked against the agent's current official documentation before each release. See [doctor.go](../internal/cli/doctor.go) for the sources and dates already verified.
