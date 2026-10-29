# MCP server: what to verify on a real install

Automated tests cover the tools, the safety checks, the recovery store, install and undo (with a fake
`claude`), the trimmer round trip and the report. They do not run a real agent. Check these by hand.

Use a throwaway data folder so you never touch a real `~/.trove`:

```
export TROVE_DATA_DIR=$(mktemp -d)
```

## 1. Claude Code

1. `trove mcp install --dry-run`, then `trove mcp install`. Confirm `claude mcp list` shows `trove` as connected.
2. In a scratch project add `.trove/commands.toml` with a `test` command. Start Claude Code there.
3. Run `/mcp` and check the four tools are listed. Ask it to read a file over 200 lines and to run the tests.
4. Confirm the tool names in the transcript are `mcp__trove__trove_read` and so on (adoption counting keys on this).
5. `trove mcp install --scope project --project <dir>` writes `.mcp.json`; Claude Code should ask to approve it.
6. `trove mcp uninstall`; `claude mcp list` no longer shows `trove`.

## 2. Codex CLI

1. `trove mcp install --agent codex`. Check the `[mcp_servers.trove]` block in `~/.codex/config.toml` and that your
   other settings are untouched.
2. Start Codex in a scratch project and ask it to list its tools; ask it to call `trove_read`.
3. **Open question:** the Codex docs do not say how a server's tools are named. Look in
   `~/.codex/sessions/**/rollout-*.jsonl` for the `function_call` `name` of a Trove call. `ClassifyTool`
   (`internal/measure/adoption.go`) accepts a server name `trove` followed by the tool name after `__`, `.`, `/`
   or `:`. If Codex uses another form, add it there and add a case to `TestClassifyTool`.
4. Codex's built-in tool names are not verified, so no adoption rate is computed for Codex.

## 3. The trimmer round trip (gateway users)

1. With the MCP server registered, set `output_trimmer = "on"` under `[optimizers]` and start a new session through
   the gateway. `trove optimize status` should show effective `on`.
2. Run a test command with more than about 6 KB of output. In the next request the output should contain
   `[Trove: N lines trimmed; full output sha256:<id>]`.
3. Ask the agent for the rest. It should call `trove_fetch_full` with that id and get the original text. Compare
   with `~/.trove/recovery/<id>.out`.
4. `trove report` shows the fetch-back rate after 20 trimmed outputs.

## 4. Limits

- Fill the store past `[recovery] max_mb` (set it to 1) and confirm the oldest outputs go and a fetch of one
  says it may have expired.
- On macOS or Linux, check `ls -l ~/.trove/recovery` shows `-rw-------` files in a `drwx------` folder.

## Known gaps

- The symlink-escape tests skip on machines where creating symlinks needs a privilege (default Windows). Run
  them on macOS or Linux, or from an elevated Windows shell.
- On Windows, a timed-out `trove_run` command is stopped, but processes it started may keep running (no process
  groups).
- The fetch-back rate is reported, not enforced: it does not trigger the automatic rollback guardrails.
- The `trove_read` outline for JavaScript, Rust, Java and similar languages is regex-based and can be wrong for
  unusual formatting; Go uses the real parser.
