# The Trove MCP server

`trove mcp` is a local [MCP](https://modelcontextprotocol.io) server over stdio. It gives Claude Code and
Codex CLI four tools that return less text than the built-in equivalents, and it makes the output Trove
shortens recoverable. It works for subscription users too, because it needs no gateway.

Nothing leaves your machine. The server makes no network calls.

## Install and remove

```
trove mcp install              # shows what will change, asks, then registers
trove mcp install --dry-run    # show only
trove mcp install --scope project   # Claude Code: add to .mcp.json in this project instead
trove mcp status
trove mcp uninstall            # reverses everything
```

| Agent | What Trove does | Where |
|---|---|---|
| Claude Code, user scope (default) | runs Claude Code's own `claude mcp add --transport stdio --scope user trove -- <trove> mcp` | Claude Code's user settings |
| Claude Code, `--scope project` | adds a `trove` entry to `.mcp.json` (backed up; Claude Code asks you to approve it) | the project |
| Codex CLI | adds `[mcp_servers.trove]` to `config.toml` between marker comments (backed up) | `~/.codex/config.toml` |

Trove never edits `~/.claude.json`: it holds account details. That is why user scope goes through the
`claude` command, which must be on your `PATH`. Codex's project-level config is only read for trusted
projects, so Trove edits Codex at user level only.

Every edited file is backed up under `~/.trove/backups`. `trove mcp uninstall` (or `trove undo`) restores a
file you have not touched since byte for byte; if you edited it, only Trove's own lines are removed.

## The tools

Their definitions are sent with every request, so they are short. `trove mcp schema-size` prints the size
(estimated; about 350 tokens for all four).

| Tool | What it does |
|---|---|
| `trove_read(path, symbol?, start?, end?)` | Files up to 8 KB and 200 lines come back whole. Larger files return an outline (functions and types with line ranges) plus the symbol or lines you ask for. Outlines exist for Go, Python, JavaScript/TypeScript, Rust, Java, Kotlin, C#, Swift; other types show the first 60 lines. |
| `trove_search(query, glob?)` | Case-insensitive text search of the project. Ranked (definitions and file-name matches first), identical lines merged, at most 40 results and 5 per file, 6 KB, with a count of what was left out. Skips `.git`, `node_modules`, `vendor`, build folders and `.gitignore`d paths. |
| `trove_run(name)` | Runs a command from `.trove/commands.toml`. Returns pass or fail, failing test names, errors and stack traces, a short summary, and an `output_id`. |
| `trove_fetch_full(output_id, start?, end?)` | Returns the full stored output, or a line range of it. |

### Safety

- `trove_read` and `trove_search` are read-only and confined to the project folder (the folder the agent
  starts the server in, or `--root`). Absolute paths, `..`, and symlinks that lead outside are refused.
  Binary files and files over 2 MB are refused with a message saying so.
- They also refuse files that usually hold secrets (`.env` and `.env.*` except `.env.example`-style
  templates, `*.pem`, `*.key`, `id_rsa`, `.npmrc`, `.netrc`, `.git/config`, anything under `.ssh/` or `.aws/`,
  and similar), and `trove_search` skips them. The reason: Claude Code documents its `Read` deny rules as
  applying, best effort, to its built-in file-reading tools; it does not document them covering files an MCP
  server reads (checked 2026-10-30 at code.claude.com/docs/en/permissions), so Trove does not rely on them.
  It matches on names only, so a secret in a file with an ordinary name is not caught. You can also deny
  the tools themselves by name (`mcp__trove__trove_read`) in your agent's permission settings.
- `trove_run` runs only the names in the allowlist, directly (no shell), in the project folder, with
  variables that look like credentials (`*KEY*`, `*TOKEN*`, `*SECRET*`, `*PASSWORD*`, `*COOKIE*`, `*_PAT`,
  `*_DSN`, `ANTHROPIC_*`, `OPENAI_*`, `AWS_*`, `AZURE_*`, `SSH_AUTH_SOCK`, `DATABASE_URL`) removed. This is a
  name-based denylist, so an unusually named secret can still get through. A command that exceeds
  `timeout_sec` is killed.
- **The allowlist lives in the project, so it needs your approval.** A repository you clone could ship a
  `.trove/commands.toml` naming any program. `trove_run` therefore refuses the file until you run
  `trove mcp trust` in the project folder, which shows every command and asks. Approval is stored as
  hashes in `~/.trove/mcp_trust.json` (no paths) and applies to that exact version of the file: any edit,
  such as a `git pull`, withdraws it. `trove mcp trust` works only at a real terminal and has no `--yes`
  flag, so an agent cannot approve a file through its own shell. `trove mcp trust --revoke` withdraws it.

### `.trove/commands.toml`

```toml
[commands.test]
run = ["go", "test", "./..."]
timeout_sec = 300        # optional; default 300, at most 3600

[commands.lint]
run = ["golangci-lint", "run"]
```

Names use letters, digits, `-` and `_`. `run` is a program followed by its arguments; shell syntax such as
`&&` or `$(...)` is passed to the program as plain text, not interpreted.

## The recovery store

Full outputs are kept in `~/.trove/recovery`: owner-only files (0600) in an owner-only folder, one file
per output, named by its ID (the first 16 hex characters of the sha256 of the text). This is the only place
tool output is written to disk. Limits are in `config.toml`:

```toml
[recovery]
max_mb = 200     # oldest outputs are removed first
ttl_days = 7
```

An output that cannot be stored is not shortened. An ID that has expired or been evicted returns a clear
message.

## Turning on the output trimmer for gateway users

`output_trimmer` (see `docs/optimize.md`) shortens large test and build output in requests that pass
through the gateway. It can be `on` only once the MCP server is registered; before that, `on` runs as
`shadow` (estimates only). Each shortened output is stored first, and its ID is in the marker line the agent
sees, so the agent can call `trove_fetch_full`. If a store write fails the request is forwarded unchanged.
Like every optimizer it applies from the next session.

## Getting agents to use the tools

Agents may prefer their built-in tools. `trove mcp snippet` prints a short optional text (about 70
tokens, estimated) for `CLAUDE.md` or `AGENTS.md` telling the agent when to prefer these tools. Trove does
not add it for you.

## Measuring adoption

`trove report` has a "Trove tools (MCP)" section once the server is registered or has been used:

- **Use** (estimated, from your agents' own session logs): the share of Claude Code's file reads and searches
  that used a Trove tool instead of `Read`, `Grep` or `Glob`. `Bash` is shown as context only, because most
  Bash calls are not test runs. For Codex, Trove only counts its own tool calls: there is no verified list of
  Codex's built-in tool names to compare against.
- **Fetch-back** (measured, from gateway requests): `trove_fetch_full` calls per trimmed output. It is shown
  after 20 trimmed outputs. At or above `measure.refetch_rate_percent` (25 by default) the report says
  trimming may be too aggressive and suggests setting `output_trimmer = "shadow"`. The report advises; it
  does not change the setting.
- **Schema size** (estimated): what the tool definitions add to each request.

## What was checked, and where

Verified on 2026-10-29 against current documentation:

- MCP Go SDK: `github.com/modelcontextprotocol/go-sdk` (v1.8.0 is what `go.mod` pins), stdio transport and typed
  tools.
- Claude Code: `claude mcp add` syntax, scopes and where they are stored, `.mcp.json` format, tool names
  `mcp__<server>__<tool>`, project-scope approval (code.claude.com/docs/en/mcp); built-in tool names `Read`,
  `Grep`, `Glob`, `Bash` (code.claude.com/docs/en/tools-reference). Note that `Grep` and `Glob` are absent by
  default on macOS, Linux and WSL, where Claude Code searches through `Bash`, so the use figure undercounts
  searches there.
- Codex CLI: `[mcp_servers.<name>]` fields and `codex mcp add` (learn.chatgpt.com/docs/extend/mcp). That page
  does not say how tools from a server are named, so see `docs/mcp-verification.md`.
