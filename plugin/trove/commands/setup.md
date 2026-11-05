---
description: Choose which Trove features to turn on (everything starts off)
disable-model-invocation: true
---

Trove is installed and nothing is turned on. Help the user choose what to enable. Make no change without the user's explicit yes for that item, and never enable an item the user did not pick. Use `sh "${CLAUDE_PLUGIN_ROOT}/bin/trove"` for every command below, run through the Bash tool.

1. Run `optimize status` and `plugin status` and show the user what is on today.

2. Ask which of these they want (AskUserQuestion, multi-select). For each, say what it does and what it costs:

   - **Repeat-read check in shadow mode.** Sets `hook_repeat_read = "shadow"` under `[optimizers]` in `config.toml` (the path is in the output of `optimize status`). It only counts the re-reads it would have skipped and changes nothing; the first numbers appear in `/trove:report`. A session keeps the setting it started with, so it applies from the next session. Edit the file only after the user agrees, and show the one-line change first. If `config.toml` does not exist yet, create it containing only the `[optimizers]` section with that line. Afterwards run `hooks sync` so the hooks pick the setting up.
   - **Status line.** Shows Trove's figures in Claude Code's status line. Run `plugin status-line` first: it prints the exact change and applies nothing. If the user agrees, run `plugin status-line --yes`. It refuses to replace an existing status line.
   - **Trove tools (MCP server).** Gives the agent `trove_read`, `trove_search`, `trove_run` and `trove_fetch_full`. Their definitions are added to every request, so run `mcp schema-size` and tell the user that estimated token cost before asking. If they agree, run `mcp install --dry-run`, show it, then `mcp install`.
   - **Scout subagent.** A cheap read-only explorer the main agent can delegate searches to. Run `tasks plan scout`, show the output including its token overhead, and apply with `tasks apply scout` only on a yes.
   - **Live hook test.** Needed before any hook may rewrite tool calls. It runs a few tiny Claude Code sessions on the user's own login (a few cents, or a small slice of their plan's usage), capped at $0.25. Run `hooks probe --live --max-usd 0.25` only on a yes, then show the result.

3. Summarize exactly what was changed and where, and how to reverse it: `sh "${CLAUDE_PLUGIN_ROOT}/bin/trove" undo`, or `/trove:uninstall` for everything.

Never touch the user's API keys, login or gateway settings. This command does not use the gateway.
