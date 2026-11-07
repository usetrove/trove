# The Trove plugin for Claude Code

The plugin is how most people use Trove. It works with **every Claude Code login** (Free, Pro, Max, Team,
Enterprise, API key, Bedrock, Vertex, Foundry), because it never touches credentials or traffic. It only wires
Claude Code to the `trove` program: hooks, slash commands and a first-run report. All logic lives in the
program, so the plugin, the gateway and the other adapters behave the same.

Facts about Claude Code below were checked against code.claude.com/docs on **2026-11-05** (pages named in each
section). Re-check them before a release ([release-checklist.md](release-checklist.md)).

## Install

```
claude plugin marketplace add usetrove/trove
claude plugin install trove@trove
```

Start Claude Code. On the first session Trove reads your existing Claude Code history (read-only, metadata
only) and shows the headline report: what you spent, the waste it found, and what it would suggest. On a
typical history this takes seconds (the history audit read 460 MB in 6 s; see [logs.md](logs.md)); the hook
allows up to 90 s once. If your Claude Code version does not show the message, type `/trove:report`.

**Nothing is turned on.** The hooks are registered but do nothing until you set an optimizer to `shadow`
(estimate only) or `on` in `config.toml`. `/trove:setup` walks you through each choice, says what it costs, and
changes nothing without your yes.

macOS and Linux only for now (the wrapper is a POSIX shell script; Windows is a later item).

## What is in it

| Piece | What it is |
|---|---|
| Hooks (`hooks/hooks.json`) | The same handlers `trove hooks install --rewrite` writes, generated from the same code. Inert until an optimizer is enabled. Details: [hooks.md](hooks.md). |
| SessionStart command | `trove plugin start`: the one-time report, and the capability check below. Always exits 0. |
| `/trove:report` | `trove report` |
| `/trove:status` | Which features are on, in shadow mode, or held back. |
| `/trove:setup` | The approval flow: repeat-read check in shadow mode, status line, Trove tools (MCP), scout subagent, live hook test. |
| `/trove:uninstall` | Undoes everything Trove changed, then tells you to remove the plugin. |
| `bin/trove` | A small wrapper that finds or fetches the `trove` program (below). |

The MCP server and the scout subagent are **not** bundled. A plugin cannot ship either switched off, and both
cost tokens in every session (tool definitions, an agent description), which would break "nothing enabled
without approval" and rule 15. `/trove:setup` offers them through `trove mcp install` and `trove tasks apply
scout`, shows their estimated token overhead first, and records them so `trove uninstall` removes them.
The plugin's slash commands set `disable-model-invocation`, so their descriptions add nothing to your context.

The status line cannot be shipped by a plugin either (only `agent` and `subagentStatusLine` take effect in a
plugin's settings; components docs, 2026-11-05). `/trove:setup` runs `trove plugin status-line`, which shows the
diff, backs up `settings.json`, never replaces an existing status line, and is removed by `trove uninstall`.

There is no `/trove:handoff` yet; nothing in Trove implements it.

## The program

Plugin hooks and MCP processes do not get the plugin's `bin/` on PATH (docs, 2026-11-05), so every command calls
`sh "${CLAUDE_PLUGIN_ROOT}/bin/trove"`. The wrapper runs, in order:

1. the copy it downloaded earlier into `${CLAUDE_PLUGIN_DATA}` (pinned to the plugin's `VERSION`);
2. a `trove` on PATH that reports exactly that version (so a Homebrew or npm install is reused);
3. a download of that version from the GitHub release, **SHA-256 checked against the release's
   `checksums.txt`** (the same check as `install.sh`; a mismatch installs nothing).

Hooks never download (they have a 3 to 5 s budget); the session-start command fetches the program first. If the
program cannot be found, a hook exits 0 and Claude Code is unaffected. The only network call is that release
download, at install time. A `VERSION` of `dev` (a source checkout) never downloads.

## Capability check

Whether a hook may *rewrite* a tool call is a fact about the installed Claude Code version, not an assumption
([hooks.md](hooks.md), "What your agent version allows"). On each session start the plugin compares
`claude --version` with the version the record was made for. When it changed (an update), the record is replaced
by a documentation-only one: nothing is run, nothing costs money, and every rewriting optimizer drops to shadow
until a test passes. You are told once. To re-test, run `/trove:setup`, which shows `trove hooks verify`: Trove writes a
settings file and a short prompt, you start `claude --settings <file>` yourself and paste the prompt, and Trove starts
no model request. That works on any plan. `trove hooks probe --live` (Trove runs the sessions for you, a few cents) is
refused unless Claude Code uses your own API key.

## Uninstall

A plugin uninstall cannot run code, so do it in two steps:

1. `/trove:uninstall` (or `trove uninstall`). It restores every file Trove changed, byte for byte when you have
   not edited it since, and otherwise removes only Trove's own lines. It keeps your data unless you add
   `--purge-data`.
2. `claude plugin uninstall trove`. This removes the plugin's hooks and commands, and (when it is the last
   scope) the plugin's data folder including the downloaded program (CLI reference, 2026-11-05).

Do step 1 first. If you only run step 2, the plugin's hooks and commands are gone and your own settings are
untouched, but anything you approved in `/trove:setup` stays behind (the status line, an MCP registration, the
scout subagent, config edits), and the downloaded program that could undo it went with the plugin's data folder.
To finish: install `trove` again (brew, npm or `install.sh`) and run `trove uninstall`, or delete the
`statusLine` entry from `~/.claude/settings.json`, run `claude mcp remove trove`, and delete
`.claude/agents/trove-scout.md` by hand. If you never approved those items there is nothing left behind.

## Company rollout

Sources: code.claude.com/docs/en/plugins/org, managed-settings, server-managed-settings,
llm-gateway-rollout (read 2026-11-05).

Put this in the managed settings file, which overrides user and project settings:

| OS | Path |
|---|---|
| macOS | `/Library/Application Support/ClaudeCode/managed-settings.json` |
| Linux, WSL | `/etc/claude-code/managed-settings.json` |
| Windows | `C:\Program Files\ClaudeCode\managed-settings.json` |

```json
{
  "extraKnownMarketplaces": {
    "trove": { "source": { "source": "github", "repo": "usetrove/trove" } }
  },
  "enabledPlugins": { "trove@trove": true }
}
```

Notes from the docs:

- `enabledPlugins` with `true` force-enables the plugin; `false` blocks it at every scope.
- If you use an allowlist (`strictKnownMarketplaces`), it registers nothing by itself: keep
  `extraKnownMarketplaces`, and add `{ "source": "github", "repo": "usetrove/trove" }` to the allowlist.
- macOS and Windows can deliver the same JSON through MDM (`com.anthropic.claudecode` preferences domain;
  `HKLM\SOFTWARE\Policies\ClaudeCode`, value `Settings`).
- Third-party marketplaces are not reviewed by Anthropic and do not auto-update by default. Set
  `"autoUpdate": true` on the `extraKnownMarketplaces` entry if you want updates, or pin a `ref`/`sha` in the
  `source` to control exactly which version your engineers get.
- Fleets without outbound git: the docs describe `CLAUDE_PLUGIN_SEED_DIR` with
  `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1` to pre-seed plugins.
- The program: either pre-install `trove` at the plugin's version with your package tooling (the wrapper reuses
  it), or allow the wrapper's one download from `github.com/usetrove/trove/releases`.

**With a gateway** (the Claude apps gateway, LiteLLM or another proxy via `ANTHROPIC_BASE_URL`, or Bedrock,
Vertex, Foundry): server-managed settings (set in the claude.ai admin console) are **skipped** when a custom
`ANTHROPIC_BASE_URL`, Bedrock, Vertex or Foundry is in use (server-managed-settings page). The gateway rollout
guide says gateway deployments use the file-based managed path, which enforces the same keys. So put the JSON
above in the managed settings **file or MDM policy**, not the admin console. The plugin does not depend on how
requests are routed: it never sees them. LiteLLM is not named in Anthropic's gateway pages (which say any gateway
with a supported API format works and that they do not endorse or audit third-party gateways); nothing here is
LiteLLM-specific, and we have not tested against it.

Trove is also a separate local gateway for API-key traffic ([gateway.md](gateway.md)). Do not point Claude Code's
`ANTHROPIC_BASE_URL` at both a company gateway and Trove's; the plugin needs neither.

## Known gaps

- Whether hooks run for every login type is **not stated** in the hooks documentation. The plugin never reads
  the login, so it should, but this must be confirmed on a Pro/Max, a Team and an API-key login
  ([plugin-verification.md](plugin-verification.md)).
- Whether `systemMessage` from a SessionStart hook is displayed is **not stated** for that event; the report is
  always available as `/trove:report`.
- The slash commands are namespaced `/trove:<name>` by Claude Code (components docs, 2026-11-05).
- Submission to Anthropic's official or community marketplaces is not covered: their acceptance criteria are
  not documented. The plugin ships from this repository's own marketplace.
- Each hook is a process start even while inert (about the cost of running the wrapper and `trove`). They cannot
  be turned on for a session already running (rule 5), so they are registered up front.
