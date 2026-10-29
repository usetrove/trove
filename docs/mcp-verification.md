# MCP server: what to verify on a real install

Automated tests cover the tools, the safety checks, the recovery store, install and undo (with a fake
`claude`), the trimmer round trip and the report. They do not run a real agent, and some checks are skipped
on Windows. Work through this list by hand. Tick each box as you go.

**Always use a throwaway data folder** so you never touch a real `~/.trove` (the real one belongs to another
product and has a different schema):

```
export TROVE_DATA_DIR=$(mktemp -d)        # PowerShell: $env:TROVE_DATA_DIR = (New-Item -ItemType Directory -Path "$env:TEMP\trove-test-$(Get-Random)").FullName
```

## 0. Before you start

- [ ] `go test ./...` passes (without `-short`, this also runs the real-subprocess MCP test).
- [ ] On macOS or Linux (or an elevated Windows shell), `go test ./internal/mcp/... -run Symlink -v` shows the
      symlink tests **running**, not skipped. They are the symlink-escape security checks.
- [ ] On macOS or Linux, `go test ./internal/recovery -run Permissions -v` runs (not skipped): files 0600, folder 0700.
- [ ] `CGO_ENABLED=0 go build -o trove ./cmd/trove` works; put `trove` on your `PATH`.
- [ ] `trove mcp schema-size` prints about 350 tokens (estimated) for 4 tools.

## 1. Claude Code

- [ ] `trove mcp install --dry-run` shows the `claude mcp add ...` command and changes nothing.
- [ ] `trove mcp install` asks first, then registers. `claude mcp list` shows `trove` connected.
- [ ] `~/.claude.json` is unchanged by Trove (only `claude mcp add` touched it).
- [ ] In a scratch project add `.trove/commands.toml` with a `test` command, start Claude Code there, run `/mcp`:
      four tools listed.
- [ ] Ask it to read a file over 200 lines: it gets an outline, then asks for a symbol or lines.
- [ ] Ask it to run the tests: it calls `trove_run` and gets a short result with an `output_id`.
- [ ] The transcript shows tool names `mcp__trove__trove_read` etc. (adoption counting keys on this).
- [ ] Ask it for the full output: it calls `trove_fetch_full` and the text matches `~/.trove/recovery/<id>.out`.
- [ ] `trove mcp install --scope project --project <dir>` writes `.mcp.json`; Claude Code asks you to approve it.
- [ ] `trove mcp uninstall`; `claude mcp list` no longer shows `trove`; `.mcp.json` is restored or removed.

## 2. Codex CLI

- [ ] `trove mcp install --agent codex` adds the `[mcp_servers.trove]` block to `~/.codex/config.toml`; your
      other settings are untouched; a backup exists under `$TROVE_DATA_DIR/backups`.
- [ ] Start Codex in a scratch project, ask it to list its tools, then to call `trove_read` on a file.
- [ ] **Open question: how does Codex name the tools?** The Codex docs do not say. Look in
      `~/.codex/sessions/**/rollout-*.jsonl` for the `function_call` `name` of a Trove call. `ClassifyTool`
      (`internal/measure/adoption.go`) accepts a server called `trove` followed by the tool name after `__`, `.`,
      `/` or `:`. If Codex uses another form, add it there and add a case to `TestClassifyTool`.
- [ ] Codex's built-in tool names are not verified, so Trove computes no adoption rate for Codex (only a count
      of Trove calls). Decide whether to verify them and add a rate.
- [ ] `trove mcp uninstall` restores `config.toml` byte for byte (or, if you edited it since, removes only the
      Trove block).

## 3. The trimmer round trip (gateway users, API key)

- [ ] With the MCP server registered, set `output_trimmer = "on"` under `[optimizers]` in
      `$TROVE_DATA_DIR/config.toml`, run `trove setup` and `trove gateway start`, and start a **new** agent session
      (optimizers apply from the next session).
- [ ] `trove optimize status` shows `output_trimmer` effective `on` (not `shadow`).
- [ ] Run a test command with more than about 6 KB of output. In the next request the output contains
      `[Trove: N lines trimmed; full output sha256:<id>]`.
- [ ] Ask the agent for the rest: it calls `trove_fetch_full` with that id and gets the original text.
- [ ] Unregister (`trove mcp uninstall`), start a new session: the trimmer is back to `shadow` and nothing is trimmed.
- [ ] Make the store unwritable (for example point `[recovery]` at a full or read-only disk, or delete the
      folder's write permission) and confirm requests still go through **unchanged** (fail open).
- [ ] After at least 20 trimmed outputs, `trove report` shows the fetch-back rate labeled `measured`.

## 4. Report

- [ ] `trove report` has a "Trove tools (MCP)" section once the server is registered.
- [ ] The adoption figure says "estimated from session logs"; the fetch-back figure says "measured".
- [ ] `trove report --json` has a `trove_tools` object with `adoption`, `schema_tokens` (label `estimated`).
- [ ] Force a high fetch-back (ask the agent to fetch most trimmed outputs): the report says trimming may be too
      aggressive and suggests `output_trimmer = "shadow"`. It must not change the setting itself.
- [ ] Spot-check that no prompt text, code, file paths or tool output appear in `$TROVE_DATA_DIR/trove.db`
      (only counts, sizes, hashes, names). The only stored output is under `recovery/`.

## 5. Limits and security (by hand)

- [ ] Set `[recovery] max_mb = 1`, produce several large outputs: the oldest are removed, and fetching one says
      it may have expired.
- [ ] Set `ttl_days = 1`, change the system date or touch the file's mtime back two days: fetch says expired.
- [ ] On macOS or Linux, `ls -l $TROVE_DATA_DIR/recovery` shows `-rw-------` files in a `drwx------` folder.
- [ ] Ask the agent to read `../something` and an absolute path outside the project: refused with a plain-English message.
- [ ] Create a symlink in the project pointing outside it; `trove_read` and `trove_search` do not follow it.
- [ ] Ask `trove_run` for a name not in `commands.toml`: refused, listing the allowed names.
- [ ] Put `ANTHROPIC_API_KEY` in your shell and run a command that prints its environment through `trove_run`:
      the key is not there.
- [ ] Read the "allowlist lives in the project" warning in `docs/mcp.md` and decide whether you are happy with it.

## Known gaps (not bugs to chase during the test)

- The symlink-escape tests skip on machines where creating symlinks needs a privilege (default Windows).
- On Windows, a timed-out `trove_run` command is stopped, but processes it started may keep running (no process groups).
- The fetch-back rate is reported, not enforced: it does not trigger the automatic rollback guardrails.
- The `trove_read` outline for JavaScript, Rust, Java and similar languages is regex-based and can be wrong for
  unusual formatting; Go uses the real parser.
- Claude Code's `Grep` and `Glob` are absent by default on macOS, Linux and WSL (it searches through `Bash`), so
  the adoption rate undercounts searches there.
- `trove undo` (the general command) reverses the Codex and `.mcp.json` edits but not the `claude mcp add`
  registration; use `trove mcp uninstall` for that.
