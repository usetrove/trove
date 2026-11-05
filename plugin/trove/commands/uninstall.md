---
description: Undo everything Trove changed, then tell the user how to remove the plugin
disable-model-invocation: true
---

Removing the plugin alone does not undo files Trove changed outside the plugin (a status line, config edits, an MCP registration, a scout subagent). Undo those first, then remove the plugin.

1. Run `sh "${CLAUDE_PLUGIN_ROOT}/bin/trove" uninstall --dry-run` with the Bash tool and show the user the plan.
2. Ask whether they also want Trove's stored data deleted (usage history, settings, backups). The default is to keep it.
3. If the user agrees to the plan, run `sh "${CLAUDE_PLUGIN_ROOT}/bin/trove" uninstall --yes`, adding `--purge-data` only if they asked for the data to go. Show the result. It restores each changed file byte for byte when the user has not edited it since, and otherwise removes only Trove's own lines.
4. Tell the user the last steps, which only they can do: run `claude plugin uninstall trove` (or `/plugin`, then Installed, then Trove), which removes the plugin's hooks and commands, and restart Claude Code.

Do not run `claude plugin uninstall` yourself. Do not touch any file other than through the commands above.
