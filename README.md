# Trove

![License](https://img.shields.io/badge/license-Apache--2.0-blue?style=flat-square) ![Go](https://img.shields.io/badge/Go-1.27.3%2B-00ADD8?style=flat-square) ![Platforms](https://img.shields.io/badge/macOS%20%7C%20Linux-supported-brightgreen?style=flat-square) ![Status](https://img.shields.io/badge/status-pre--release-orange?style=flat-square)

Trove is the efficiency layer for AI coding agents. It reads the session history your agent already keeps on disk, finds the tokens it wastes (repeated reads, bloated context, cache breakers, unused tool definitions), proposes fixes you approve one at a time, and checks that your results did not suffer. One small program, no account, nothing to sign up for.

**Runs locally. Sends nothing. Open source.** Works with Claude Code and Codex CLI.

**Learn more in the [documentation](docs/getting-started.md)**.

<!-- DEMO GIF: record from docs/launch/demo-script.md, save as docs/assets/demo.gif, then replace this comment with: <img src="./docs/assets/demo.gif" /> -->

## Get started

> [!NOTE]
> Trove 0.1.0 has not been published yet, so the install commands below will not work until it is. Until then, [try it from source](#try-it-from-source).

1. Install Trove. Every path checks the download against the release's SHA-256 checksums before it runs anything.

    **macOS/Linux (Recommended):**
    ```bash
    curl -fsSL https://raw.githubusercontent.com/usetrove/trove/main/install.sh | sh
    ```

    **Homebrew (macOS/Linux):**
    ```bash
    brew install usetrove/trove/trove
    ```

    **No install (Node.js):**
    ```bash
    npx trove-cli doctor
    ```

    **Windows:** release builds are not part of 0.1.0. See [try it from source](#try-it-from-source).

2. Run it:

    ```bash
    trove          # your report, from the history your agent already keeps
    trove fix      # evidence-based fixes with estimated savings; changes nothing
    ```

That is all you need to start. You do not need the gateway for this, and it works if you use a subscription plan. Every number is labeled **measured** (counted from usage the provider reported) or **estimated** (derived from list prices or an assumption). Dollar figures are estimates, not invoices.

To apply a fix, run `trove fix <id> --apply`; you see the exact diff and are asked first.

### Try it from source

Builds one binary in the repo folder and keeps all data in a throwaway folder, so it cannot touch an existing `~/.trove` or any agent config. Requires [Go 1.27.3 or newer](https://go.dev/dl/).

```bash
go build -o trove ./cmd/trove
export TROVE_DATA_DIR="$(mktemp -d)"
./trove doctor
./trove
```

On Windows (experimental), in PowerShell:

```powershell
go build -o trove.exe ./cmd/trove
$env:TROVE_DATA_DIR = Join-Path $env:TEMP "trove-demo"
.\trove.exe doctor
.\trove.exe
```

Always set `TROVE_DATA_DIR` (or pass `--data-dir`) when experimenting. Without it Trove uses `~/.trove`. What to expect, the API-key gateway walkthrough, configuration and checksum verification are in [docs/getting-started.md](docs/getting-started.md).

If you have Go and want the program on your `PATH`, `go install github.com/usetrove/trove/cmd/trove@latest` builds it from source (no checksum check).

## How it works

Two layers; you can use the first without the second.

1. **For everyone, no proxy.** Trove reads local session history, finds waste, proposes fixes to your agent's config files, and can add hooks and an MCP tool server. Nothing sits between your agent and the provider.
2. **Optional local gateway, for people who pay with their own API keys.** `trove setup` points Claude Code or Codex CLI at a gateway on `127.0.0.1` using the agent's documented custom-endpoint setting. The gateway records metadata per request (token counts, sizes, hashes, model names, timings) and can apply optimizations switched on one at a time. It never handles subscription or OAuth credentials and rejects them with an explanation.

Principles that hold everywhere: local-first (everything lives in `~/.trove`, no telemetry), API keys pass through and are never logged or stored, metadata only (no prompts, code, paths or tool output in the database), fail open (an internal error forwards your request unchanged), and prove before acting (every optimization is opt-in, estimated in shadow mode first, measured once on, and rolled back if guardrails trip; see [measurement](docs/measurement.md)).

## What Trove changes, and how to undo it

Nothing changes until you approve it. Each change is shown first, the original file is backed up, and one command reverses it.

| Command | What it changes | Undo |
|---|---|---|
| `trove setup` | Claude Code `settings.json` (gateway address) or Codex `~/.codex/config.toml` | `trove undo` |
| `trove setup --status-line` | Claude Code `settings.json` (`statusLine`); never replaces one you already have | `trove undo` |
| `trove fix <id> --apply` | The agent config file named in the diff | `trove undo <id>` |
| `trove hooks install` | Hooks in Claude Code and Codex CLI settings | `trove hooks uninstall` |
| `trove mcp install` | Registers the Trove MCP server with your agents | `trove mcp uninstall` |
| `trove gateway start --background` | Installs a user background service | `trove gateway stop`, or `trove uninstall` |
| Normal use | Creates `~/.trove` (database, backups, settings) | `trove uninstall --purge-data` |

Shell rc files are never touched. Trove never reads or writes your API keys.

## FAQ

**Does it work with a subscription (Claude Pro/Max, ChatGPT)?** The history report, fixes, hooks and MCP server do. The gateway is for API-key use only; Trove will not touch subscription or OAuth traffic.

**Which agents and versions?** Claude Code and Codex CLI. See [supported agents](docs/supported-agents.md) for what has and has not been tested.

**Can an optimization make my agent worse?** Optimizers are off by default, run in shadow mode first, and are measured. If guardrails trip, Trove rolls them back and tells you. `trove optimize off` stops all of them at once.

**How accurate are the dollar figures?** They are list-price estimates from usage counts. `trove reconcile` compares them with a real invoice; a difference within 2% counts as a match. See [measurement method](docs/measurement-method.md) and [pricing](docs/pricing.md).

## Uninstall

```bash
trove uninstall --dry-run      # shows what would happen
trove uninstall                # stops the gateway, restores every agent file, removes the MCP server
trove uninstall --purge-data   # also deletes what Trove stored in ~/.trove
```

Then remove the program itself: `brew uninstall trove`, or delete the binary the install script placed on your `PATH`. If you ran it with `npx`, delete `~/.cache/trove-cli`. Trove never deletes data if an agent file could not be restored, because the backups are then still needed.

## Reporting bugs

We welcome your feedback. Run `trove feedback`: it builds a GitHub issue with versions, agent names, counts and error classes, shows you the full text, and opens it only if you say yes. It never includes prompts, code, paths or keys. You can also file a [GitHub issue](https://github.com/usetrove/trove/issues) directly.

## Data collection, usage, and retention

Trove collects nothing. It has no telemetry or analytics and makes no network calls of its own. In gateway mode it forwards your own API requests to the provider you configured, as your agent would have. What it stores locally (token counts, sizes, hashes, model names, timings, outcome signals) is listed in [PRIVACY.md](PRIVACY.md); you can delete all of it with `trove uninstall --purge-data`.

If you want to help check Trove's savings claims, `trove contribute` shows a small anonymous summary (counts, whole-number percents and fixed labels; no prompts, paths, model names or dollar amounts), and `trove contribute --export file.json` saves it so you can share it yourself. Trove sends nothing automatically.

## More documentation

[Getting started](docs/getting-started.md) · [Measurement method](docs/measurement-method.md) · [Privacy](PRIVACY.md) · [Security](SECURITY.md) · [Threat model](docs/threat-model.md) · [Compliance](docs/compliance.md) · [Pricing and updates](docs/pricing.md) · [Supported agents](docs/supported-agents.md) · [Reports](docs/report.md) · [Gateway](docs/gateway.md) · [Beta checklist](docs/beta-checklist.md) · [Contributing](CONTRIBUTING.md)

## License

Apache-2.0
