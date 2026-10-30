# Trove

**The efficiency layer for AI coding agents.**

Trove cuts the tokens your coding agents waste, and checks that your results did not suffer. One small program, no account, nothing to sign up for.

> **Runs locally. Sends nothing. Open source.**

<!-- DEMO GIF: record from docs/launch/demo-script.md, save as docs/assets/demo.gif, then replace this comment with: ![Trove demo](docs/assets/demo.gif) -->
*(Demo GIF coming with the first release.)*

> **Status: pre-release.** Version 0.1.0 has not been published yet, so the install commands below will not work until it is. Until then, [try it from source](#try-it-now-from-source-nothing-installed).

## Try it now (from source, nothing installed)

Use this until 0.1.0 is published. It builds one binary in the repo folder and keeps all of Trove's data in a throwaway folder, so it cannot touch an existing `~/.trove` or any agent config. Requires [Go 1.27.3 or newer](https://go.dev/dl/).

macOS / Linux:

```sh
go build -o trove ./cmd/trove
export TROVE_DATA_DIR="$(mktemp -d)"   # throwaway data folder for this shell
./trove doctor                         # checks your setup; changes nothing
./trove                                # report from your agent's local history
./trove fix                            # evidence-based fixes with estimated savings; changes nothing
```

Windows (PowerShell; Windows is not a supported release platform yet, so treat it as experimental):

```powershell
go build -o trove.exe ./cmd/trove
$env:TROVE_DATA_DIR = Join-Path $env:TEMP "trove-demo"
.\trove.exe doctor
.\trove.exe
.\trove.exe fix
```

Setting `TROVE_DATA_DIR` (or passing `--data-dir <path>`) matters: without it Trove uses `~/.trove`, and if that folder already holds data from another tool, Trove may open or modify its database. Add `--no-history` to any command to stop it reading your agent's session history for that run, and `--json` for machine-readable output.

What you should see: `doctor` lists which agents it found and whether prices are fresh; the report shows token use and waste by session, each number labeled **measured** or **estimated**; `fix` lists proposals with an id each. If a report is empty, your agent has probably not written history yet, or lives somewhere `doctor` did not list. That is worth a [bug report](#faq).

`trove fix` looks at the project in your current folder, so run it from a project you use with your agent. Run from a folder with no agent history it says "No fixes to suggest", which is normal. The first report reads the last 30 days of history once; it only reads, and stores counts.

### Optional: route Claude Code through the gateway (API key only)

Skip this if you use a Claude Pro/Max subscription. The gateway refuses subscription traffic on purpose. It is the only part that edits your real agent config (`~/.claude/settings.json`, backed up first), so it is the one place the throwaway data folder does not protect you. Only the curl part of this path has been verified end to end; the Claude Code part has not been completed yet. Step-by-step with expected results and troubleshooting: [docs/gateway-manual-test.md](docs/gateway-manual-test.md).

On Windows, read `./trove` below as `.\trove.exe`.

1. Have `ANTHROPIC_API_KEY` set in the terminal you will start `claude` from. Trove never reads or stores it.
2. Terminal A: `./trove gateway start` (stays running; Ctrl-C stops it). Default address is `127.0.0.1:8787`.
3. Terminal B: `./trove setup`. It shows the exact change, asks, and backs up the file.
4. Start `claude` from the terminal with the key. If it offers to use the API key, say yes. Run `/status` and check the base URL is `http://127.0.0.1:8787/anthropic`.
5. Use your agent, then `./trove gateway status` (request count should rise) and `./trove` (the report now includes gateway-measured numbers).
6. To go back: `./trove undo`, then Ctrl-C the gateway. Background service mode (`--background`) works on macOS and Linux only.

If you get a 401 "API-key authentication only", Claude Code is still using your claude.ai login; see the troubleshooting section of the doc above.

Everything past this point (`fix --apply`, `setup`, `hooks install`, `mcp install`, `gateway start`) edits agent config files or starts a service. Read the diff it shows, and see [What Trove changes](#what-trove-changes-and-how-to-undo-it) for the undo for each.

## Install

Pick one. Every path checks the download against the release's SHA-256 checksums before it runs anything.

```sh
brew install usetrove/trove/trove                                                  # Homebrew
npx trove-cli doctor                                                               # no install; runs a checked copy
curl -fsSL https://raw.githubusercontent.com/usetrove/trove/main/install.sh | sh   # install script
```

Or download the archive for your system from the GitHub release page and check it yourself:

```sh
sha256sum -c --ignore-missing checksums.txt
cosign verify-blob \
  --certificate-identity 'https://github.com/usetrove/trove/.github/workflows/release.yml@refs/tags/vX.Y.Z' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  --bundle checksums.txt.sigstore.json checksums.txt
```

Replace `vX.Y.Z` with the version you downloaded. The cosign step is optional but is the only one that proves the checksums came from this project's release workflow. The install script and the npm wrapper check the SHA-256 only, not the signature.

Supported: macOS and Linux (amd64, arm64). Windows builds are not part of 0.1.0.

## Quickstart

```sh
trove          # reads the history your agent already keeps on disk and shows your report
trove fix      # lists evidence-based fixes with their estimated savings; changes nothing
```

You do not need the gateway for this, and it works if you use a subscription plan. Every number is labeled **measured** (counted from usage the provider reported) or **estimated** (derived from list prices or an assumption). Dollar figures are estimates, not invoices.

To apply a fix (you see the exact diff and are asked first):

```sh
trove fix <id> --apply
```

To see spend in Claude Code's status line (opt-in, separate step):

```sh
trove setup --status-line
```

We aim for the first report to take under two minutes from install. That target has not been measured yet; the beta will record it ([docs/beta-checklist.md](docs/beta-checklist.md)).

## How it works

There are two layers, and you can use the first without the second.

1. **For everyone, no proxy.** Trove reads the session history your agent already stores locally, finds waste (repeated reads, bloated context, cache breakers, unused tool definitions), proposes fixes to your agent's config files, and can add hooks and an MCP tool server. Nothing sits between your agent and the provider.
2. **Optional local gateway, for people who pay with their own API keys.** `trove setup` points Claude Code or Codex CLI at a gateway on `127.0.0.1` using the agent's documented custom-endpoint setting. The gateway records metadata for each request (token counts, sizes, hashes, model names, timings) and can apply optimizations that are switched on one at a time. It forwards your own requests to the provider you configured, and nothing else leaves your machine. It does not handle subscription or OAuth credentials and rejects them with an explanation.

Principles that hold everywhere:

- **Local-first.** Everything lives in `~/.trove`. No telemetry, no analytics.
- **Keys pass through.** API keys are never logged, stored or printed.
- **Metadata only.** Prompt text, code, file paths and tool output are never written to the database. [PRIVACY.md](PRIVACY.md) lists exactly what is stored.
- **Fails open.** If Trove hits an internal error, your request goes through unchanged.
- **Prove before acting.** Every optimization is opt-in, has a shadow mode that only estimates, is measured once on, and rolls back if guardrails trip. See [docs/measurement.md](docs/measurement.md).

## What Trove changes, and how to undo it

Trove changes nothing until you approve it. Each change is shown first, the original file is backed up, and one command reverses it.

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

**Does it send my code or prompts anywhere?** No. Trove makes no network calls of its own. In gateway mode it forwards your own API requests to the provider you configured, as your agent would have. See [PRIVACY.md](PRIVACY.md).

**Does it work with a subscription (Claude Pro/Max, ChatGPT)?** The history report, fixes, hooks and MCP server do. The gateway is for API-key use only; Trove will not touch subscription or OAuth traffic.

**Which agents and versions?** Claude Code and Codex CLI. See [docs/supported-agents.md](docs/supported-agents.md) for what has and has not been tested.

**How accurate are the dollar figures?** They are list-price estimates from usage counts. During the beta, `trove reconcile` compares them with real invoices; results are published in the docs. See [docs/measurement-method.md](docs/measurement-method.md) and [docs/pricing.md](docs/pricing.md).

**Can an optimization make my agent worse?** Optimizers are off by default, run in shadow mode first, and are measured. If guardrails trip, Trove rolls them back and tells you. `trove optimize off` stops all of them at once.

**How do I report a problem?** `trove feedback` builds a GitHub issue with versions, agent names, counts and error classes, shows you the full text, and opens it only if you say yes. It never includes prompts, code, paths or keys.

**How do I check Trove's numbers against my bill?** `trove reconcile --provider anthropic --from 2026-10-01 --to 2026-10-31 --invoice 123.45`. A difference within 2% is reported as a match.

## Uninstall

```sh
trove uninstall --dry-run      # shows what would happen
trove uninstall                # stops the gateway, restores every agent file, removes the MCP server
trove uninstall --purge-data   # also deletes what Trove stored in ~/.trove
```

Then remove the program itself: `brew uninstall trove`, or delete the binary the install script placed on your `PATH`. If you ran it with `npx`, delete `~/.cache/trove-cli`. Trove never deletes data if an agent file could not be restored, because the backups are then still needed.

## Documentation

[Measurement method](docs/measurement-method.md) · [Privacy](PRIVACY.md) · [Security](SECURITY.md) · [Threat model](docs/threat-model.md) · [Compliance](docs/compliance.md) · [Pricing and updates](docs/pricing.md) · [Supported agents](docs/supported-agents.md) · [Reports](docs/report.md) · [Gateway](docs/gateway.md) · [Beta checklist](docs/beta-checklist.md)

## Build from source

See [Try it now](#try-it-now-from-source-nothing-installed) to build and run safely. To run the tests:

```sh
go test ./...
```

More: [CONTRIBUTING.md](CONTRIBUTING.md), [docs/release-checklist.md](docs/release-checklist.md).

## Configuration

`~/.trove/config.toml` is optional; defaults are in `internal/config` (`DefaultTOML`). Any setting can be overridden with `TROVE_` variables, for example `TROVE_GATEWAY_PORT=9000`.

## License

Apache-2.0
