---
description: Choose which Trove features to turn on (everything starts off)
disable-model-invocation: true
---

Trove is installed and nothing is turned on. Help the user choose what to enable. Make no change without the user's explicit yes for that item, and never enable an item the user did not pick. Use `sh "${CLAUDE_PLUGIN_ROOT}/bin/trove"` for every command below, run through the Bash tool.

1. Run `optimize status` and `plugin status` and show the user what is on today.

2. Ask which of these they want (AskUserQuestion, multi-select). For each, say what it does and what it costs:

   - **Repeat-read check in shadow mode.** Sets `hook_repeat_read = "shadow"` under `[optimizers]` in `config.toml` (the path is in the output of `optimize status`). It only counts the re-reads it would have skipped and changes nothing; the first numbers appear in `/trove:report`. A session keeps the setting it started with, so it applies from the next session. Edit the file only after the user agrees, and show the one-line change first. If `config.toml` does not exist yet, create it containing only the `[optimizers]` section with that line. Afterwards run `hooks sync` so the hooks pick the setting up.
   - **Autopilot.** Lets Trove turn on proven savings by itself, after one yes from the user. Run `autopilot` first and show the user what it says (it applies nothing). Then tell them, in your own words: it starts in shadow mode and changes nothing until there is enough evidence; only lossless optimizers that leave the cached prompt alone are eligible; a change reaches only sessions that start afterwards; anything they set themselves in `config.toml` is left alone; a guardrail turns an optimizer back to shadow if it stops paying off; it never touches their login or traffic; nothing leaves their machine. The thresholds are placeholders, not yet validated by a benchmark, so say so. If they agree, run `autopilot on --yes`. If they decline, run nothing. They can pause it with `autopilot pause` and withdraw with `autopilot off`.
   - **Status line.** Shows Trove's figures in Claude Code's status line. Run `plugin status-line` first: it prints the exact change and applies nothing. If the user agrees, run `plugin status-line --yes`. It refuses to replace an existing status line.
   - **Trove tools (MCP server).** Gives the agent `trove_read`, `trove_search`, `trove_run` and `trove_fetch_full`. Their definitions are added to every request, so run `mcp schema-size` and tell the user that estimated token cost before asking. If they agree, run `mcp install --dry-run`, show it, then `mcp install`.
   - **Scout subagent.** A cheap read-only explorer the main agent can delegate searches to. Run `tasks plan scout`, show the output including its token overhead, and apply with `tasks apply scout` only on a yes.
   - **Hook test.** Needed before any hook may rewrite tool calls. Trove never starts model requests on a Claude.ai login, so the user runs the test in a session they start themselves. Run `hooks verify` and show its output: it prints a `claude --settings <file>` command and one prompt per tool. The user starts that session, pastes the prompts, approves the tool calls, exits, and tells you which tools' replies began with `TROVE_PROBE_OUT_`; then run `hooks verify --finish --saw-output <those tools>` (`Tool=no` for the ones that did not) and show the result. Do not run `claude` yourself and do not run `hooks probe --live`: only offer that, as an alternative, when the user says Claude Code uses their own API key.

3. Summarize exactly what was changed and where, and how to reverse it: `sh "${CLAUDE_PLUGIN_ROOT}/bin/trove" undo`, or `/trove:uninstall` for everything. If they turned Autopilot on, say that `autopilot off` withdraws it.

Never touch the user's API keys, login or gateway settings. This command does not use the gateway.
