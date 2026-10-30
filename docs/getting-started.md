# Getting started

Detailed steps that are too long for the README. Commands are shown for macOS and Linux; on Windows read `./trove` as `.\trove.exe`. Windows is not a supported 0.1.0 release platform, so treat it as experimental.

## Try it from source with nothing installed

Until 0.1.0 is published. This builds one binary in the repo folder and keeps Trove's data in a throwaway folder, so it cannot touch an existing `~/.trove` or any agent config. Requires [Go 1.27.3 or newer](https://go.dev/dl/).

macOS / Linux:

```sh
go build -o trove ./cmd/trove
export TROVE_DATA_DIR="$(mktemp -d)"   # throwaway data folder for this shell
./trove doctor                         # checks your setup; changes nothing
./trove                                # report from your agent's local history
./trove fix                            # evidence-based fixes with estimated savings; changes nothing
```

Windows (PowerShell):

```powershell
go build -o trove.exe ./cmd/trove
$env:TROVE_DATA_DIR = Join-Path $env:TEMP "trove-demo"
.\trove.exe doctor
.\trove.exe
.\trove.exe fix
```

Setting `TROVE_DATA_DIR` (or passing `--data-dir <path>`) matters: without it Trove uses `~/.trove`, and if that folder already holds data from another tool, Trove may open or modify its database. Add `--no-history` to any command to stop it reading your agent's session history for that run, and `--json` for machine-readable output.

What you should see:

- `doctor` lists which agents it found and whether prices are fresh.
- The report shows token use and waste, each number labeled **measured** or **estimated**. The first run reads the last 30 days of history once; it only reads, and stores counts.
- `fix` lists proposals with an id each. It looks at the project in your current folder, so run it from a project you use with your agent. Run from a folder with no agent history it says "No fixes to suggest", which is normal.

If a report is empty, your agent has probably not written history yet, or lives somewhere `doctor` did not list. Please [report it](../README.md#reporting-bugs).

## Route Claude Code through the gateway (API key only)

Skip this if you use a Claude Pro/Max subscription. The gateway refuses subscription traffic on purpose. It is the only part that edits your real agent config (`~/.claude/settings.json`, backed up first), so it is the one place the throwaway data folder does not protect you. Only the curl part of this path has been verified end to end; the Claude Code part has not been completed yet. Full steps, expected results and troubleshooting: [gateway-manual-test.md](gateway-manual-test.md).

1. Have `ANTHROPIC_API_KEY` set in the terminal you will start `claude` from. Trove never reads or stores it.
2. Terminal A: `./trove gateway start` (stays running; Ctrl-C stops it). Default address is `127.0.0.1:8787`.
3. Terminal B: `./trove setup`. It shows the exact change, asks, and backs up the file.
4. Start `claude` from the terminal with the key. If it offers to use the API key, say yes. Run `/status` and check the base URL is `http://127.0.0.1:8787/anthropic`.
5. Use your agent, then `./trove gateway status` (request count should rise) and `./trove` (the report now includes gateway-measured numbers).
6. To go back: `./trove undo`, then Ctrl-C the gateway. Background service mode (`--background`) works on macOS and Linux only.

If you get a 401 "API-key authentication only", Claude Code is still using your claude.ai login; see the troubleshooting section of [gateway-manual-test.md](gateway-manual-test.md).

## Apply a fix, add the status line

```sh
trove fix <id> --apply         # shows the exact diff and asks first
trove setup --status-line      # spend in Claude Code's status line (opt-in, separate step)
```

Each change is backed up and reversible; see [What Trove changes](../README.md#what-trove-changes-and-how-to-undo-it).

## Configuration

`~/.trove/config.toml` is optional; defaults are in `internal/config` (`DefaultTOML`). Any setting can be overridden with `TROVE_` variables, for example `TROVE_GATEWAY_PORT=9000`.

## Check the numbers against your bill

```sh
trove reconcile --provider anthropic --from 2026-10-01 --to 2026-10-31 --invoice 123.45
```

A difference within 2% is reported as a match. Dollar figures are list-price estimates, not invoices. See [measurement-method.md](measurement-method.md) and [pricing.md](pricing.md).

## Verify a download

```sh
sha256sum -c --ignore-missing checksums.txt
cosign verify-blob \
  --certificate-identity 'https://github.com/usetrove/trove/.github/workflows/release.yml@refs/tags/vX.Y.Z' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  --bundle checksums.txt.sigstore.json checksums.txt
```

Replace `vX.Y.Z` with the version you downloaded. The cosign step is optional but is the only one that proves the checksums came from this project's release workflow. The install script and the npm wrapper check the SHA-256 only, not the signature.
