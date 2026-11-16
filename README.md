<div align="center">

# Trove

**The efficiency layer for AI coding agents.**

Find the tokens your agent wastes, fix them with changes you approve one at a time, and check that your results did not suffer.

![License](https://img.shields.io/badge/license-Apache--2.0-blue?style=flat-square) ![Go](https://img.shields.io/badge/Go-1.27.3%2B-00ADD8?style=flat-square) ![Platforms](https://img.shields.io/badge/macOS%20%7C%20Linux-supported-brightgreen?style=flat-square) ![Status](https://img.shields.io/badge/status-pre--release-orange?style=flat-square)

[Get started](#get-started) · [How it works](#how-it-works) · [Measured results](#measured-results) · [Docs](docs/getting-started.md) · [FAQ](#faq)

</div>

---

Trove reads the session history your agent already keeps on disk and finds where tokens go to waste: repeated reads, bloated context, cache breakers, unused tool definitions. It proposes fixes, shows you the exact change, and only applies it when you say yes. Then it measures whether the change helped.

One small program. No account. Nothing to sign up for.

- **Local-first.** Everything lives in `~/.trove`. Nothing is sent anywhere unless you opt in.
- **Honest numbers.** Every figure is labeled **measured** (counted from usage the provider reported) or **estimated** (derived from list prices or an assumption). Dollar figures are estimates, not invoices.
- **Proves before acting.** Every optimization is opt-in, estimated in shadow mode first, measured once on, and rolled back if its guardrails trip.
- **Works with** Claude Code and Codex CLI, on a subscription plan or with your own API keys.

<!-- DEMO GIF: record from docs/launch/demo-script.md, save as docs/assets/demo.gif, then replace this comment with: <img src="./docs/assets/demo.gif" /> -->

## Get started

> [!NOTE]
> Trove 0.1.0 has not been published yet, so the install commands below will not work until it is. Until then, [try it from source](#try-it-from-source).

### 1. Install

Every path checks the download against the release's SHA-256 checksums before it runs anything.

**macOS / Linux (supported):**
```bash
curl -fsSL https://raw.githubusercontent.com/usetrove/trove/main/install.sh | TROVE_VERSION=0.1.0-beta.1 sh
```
During the beta, pin the version as shown: a `-beta` release is a pre-release, so "latest" does not pick it.

**Homebrew (macOS / Linux):**
```bash
brew install usetrove/trove/trove
```

<details>
<summary><b>Windows (experimental)</b></summary>

Download the `trove_<version>_windows_amd64.zip` from the release page, check it, then unzip it. In PowerShell (replace the version with your release's):

```powershell
$v = "0.1.0-beta.1"
Invoke-WebRequest "https://github.com/usetrove/trove/releases/download/v$v/trove_${v}_windows_amd64.zip" -OutFile trove.zip
Invoke-WebRequest "https://github.com/usetrove/trove/releases/download/v$v/checksums.txt" -OutFile checksums.txt
$want = (Select-String -Path checksums.txt -Pattern "trove_${v}_windows_amd64.zip").Line.Split(" ")[0]
if ((Get-FileHash trove.zip -Algorithm SHA256).Hash.ToLower() -ne $want.ToLower()) { throw "checksum mismatch: do not use this file" }
Expand-Archive trove.zip -DestinationPath "$HOME\trove"
& "$HOME\trove\trove.exe" doctor
```

The Claude Code plugin's hooks on Windows also need `sh` (Git Bash) on your `PATH`; `trove doctor` warns when it is missing.

</details>

### 2. Add the Claude Code plugin

```
claude plugin marketplace add usetrove/trove
claude plugin install trove@trove
```

Then start a session and run **`/trove:setup`**. It shows each change, says what it costs, and asks before anything is turned on.

> [!IMPORTANT]
> **Rewriting stays off until you opt in.** The hooks are installed and only record what happens. No tool call, file or output is changed until you choose a mode (shadow, which only estimates, or on) in `/trove:setup`.

### 3. See your report

```bash
trove          # your report, from the history your agent already keeps
trove fix      # evidence-based fixes with estimated savings; changes nothing
```

To apply a fix, run `trove fix <id> --apply`. You see the exact diff and are asked first.

That is all you need to start. You do not need the gateway for this, and it works on a subscription plan. The gateway is an optional extra, turned on only with `trove setup --gateway`.

### Try it from source

Builds one binary in the repo folder and keeps all data in a throwaway folder, so it cannot touch an existing `~/.trove` or any agent config. Requires [Go 1.27.3 or newer](https://go.dev/dl/).

```bash
go build -o trove ./cmd/trove
export TROVE_DATA_DIR="$(mktemp -d)"
./trove doctor
./trove
```

<details>
<summary>On Windows (experimental), in PowerShell</summary>

```powershell
go build -o trove.exe ./cmd/trove
$env:TROVE_DATA_DIR = Join-Path $env:TEMP "trove-demo"
.\trove.exe doctor
.\trove.exe
```

</details>

Always set `TROVE_DATA_DIR` (or pass `--data-dir`) when experimenting. Without it Trove uses `~/.trove`. What to expect, the API-key gateway walkthrough, configuration and checksum verification are in [docs/getting-started.md](docs/getting-started.md). If you have Go and want the program on your `PATH`, `go install github.com/usetrove/trove/cmd/trove@latest` builds it from source (no checksum check).

## How it works

Two layers. You can use the first without the second.

```
 Your agent (Claude Code, Codex CLI)
      │
      │  1. For everyone, no proxy
      ├──► session history on disk ──► Trove: report, fixes, hooks, MCP tools
      │
      │  2. Optional, for API-key users
      └──► Trove gateway on 127.0.0.1 ──► your provider
                  records metadata, applies optimizers you switched on
```

1. **For everyone, no proxy.** Trove reads local session history, finds waste, proposes fixes to your agent's config files, and can add hooks and an MCP tool server. Nothing sits between your agent and the provider.
2. **Optional local gateway, for people who pay with their own API keys.** `trove setup` points Claude Code or Codex CLI at a gateway on `127.0.0.1` using the agent's documented custom-endpoint setting. The gateway records metadata per request (token counts, sizes, hashes, model names, timings) and can apply optimizations switched on one at a time. It never handles subscription or OAuth credentials and rejects them with an explanation. `trove setup` will not point Claude Code at it unless it will use an API key (an `ANTHROPIC_API_KEY`, an `apiKeyHelper`, or `plan = "api_key"` in config.toml); with a Claude.ai login, use the plugin.

### The optimizers

All are off by default, run in shadow mode first, and fail open: any error forwards your original request byte for byte. See [docs/optimize.md](docs/optimize.md).

| Optimizer | What it does | Loses information? |
|---|---|---|
| `noise_normalizer` | Strips ANSI codes, spinner frames and repeated lines from tool output | No |
| `duplicate_result_collapser` | Replaces a tool result identical to an earlier one with a short note | No |
| `output_trimmer` | Keeps failures, stack traces and a little context from large test and build output; the full text is saved locally and fetchable | Recoverable |
| `cache_breakpoints` | Adds prompt-caching markers for agents that set none (custom agents only) | No |
| `volatile_prefix_mover` | Moves timestamps and other changing lines out of the cacheable prefix (custom agents only) | No |

### Principles that hold everywhere

- **Local-first.** Everything lives in `~/.trove`. Nothing is sent unless you opt in to the anonymous weekly summary.
- **Credentials.** API keys pass through and are never logged or stored.
- **Metadata only.** No prompts, code, paths or tool output in the database.
- **Fail open.** An internal error forwards your request unchanged.
- **Cache safe.** Transformations are deterministic, so the same history always produces the same bytes and cached prefixes stay valid.
- **Prove before acting.** See [measurement](docs/measurement.md).

## Measured results

> [!IMPORTANT]
> **Bottom line.** Trove has **not yet shown that it saves money on real coding tasks.** Two pre-registered studies found no saving that can be told apart from noise, and the short tasks they used leave little for an optimizer to remove. Scripted sessions show where it does help: output-heavy work and agents that do not cache their prompts. Trove makes no savings promise.

Trove's benchmark harness (`bench/`) runs real coding tasks in Docker with your own API key. For each run it records whether the task passed and what it cost. Each study is pre-registered: the question, the decision rule and the budget are locked before the first run.

**Decision rule.** Trove recommends an optimizer for automatic use only if its cost reduction is at least 5% and the whole 95% interval lies at or below −5%, with the success rate not falling by more than 5 points and at least 30 pairs. Otherwise the verdict is NEED MORE DATA or DON'T SHIP. A result is never rounded up.

### 1. Real tasks, two pre-registered studies

Both ask: does the gateway with its safe optimizers on cost less than the same gateway with every optimizer off (`passthrough`)? Costs are **estimated** (list price on the token counts the agent reports). Pass or fail is **measured**.

| | Aider polyglot, 30 tasks | SWE-bench Verified, 35 tasks |
|---|---|---|
| Study | `gateway-cost-2` | `swebench-gateway-2` |
| Model, agent | `claude-haiku-5-5`, Claude Code 2.1.318 | `claude-sonnet-5-5`, Claude Code 2.1.319 |
| Runs | 180 (90 pairs) | 140 (35 pairs per comparison) |
| Median paired cost change, `all_safe` vs `passthrough` | **−3.0%** (95% interval −9.5% to +3.7%) | **+4.4%** (95% interval −7.0% to +13.8%) |
| Success | 90 of 90 under both | 28 of 35 vs 29 of 35 |
| Mean cost per run, estimated | about $0.0026 | about $0.02 to $0.03 |
| Verdict | **NEED MORE DATA** | **NEED MORE DATA** |

The two studies point in opposite directions, which shows how wide the noise is. In the SWE-bench study the two single optimizers were also within noise: `alone_noise_normalizer` 0.0% (−11.0% to +20.0%) and `alone_duplicate_result_collapser` +1.9% (−11.6% to +26.0%), both against the same `passthrough` baseline.

**Why nothing shows up.** The gateway's own records say the optimizers had almost nothing to remove. In the SWE-bench study the median session was 5 turns, and `all_safe` removed about 0.2% of input tokens (estimated). A saving of that size cannot move a bill. A longer, tool-heavy session is a different case, which is what the scripted workflows below explore.

Study files and locks are committed in `bench/studies/`; the method is in [benchmark methodology](docs/benchmark-methodology.md). The SWE-bench study ran on a separate bench machine, and its result files are not yet in this repository. To reproduce the first study, fetch the tasks with `python -m trovebench fetch polyglot -n 30 --seed 20261108`, then from `bench/` run `python -m trovebench run --study studies/gateway-cost-2.yaml --tasks <that folder> --batch <folder> --yes`.

### 2. Scripted everyday sessions, replayed through the real engine

Short benchmark tasks cannot show what happens in a long working day, so `trove bench workflows` replays hand-scripted Claude Code sessions through the real optimizer engine, offline: no API call, no spend. The sessions are scripted (how often a file is re-read, how noisy a shell is, how long a session runs are assumptions written in [`internal/stress/workflows.go`](internal/stress/workflows.go)), so these figures say what the optimizers do on these patterns. **They are not a forecast of anyone's bill.**

| Kind of session | Median saving (range) | Label |
|---|---:|---|
| Everyday: fix a failing test, rename across a codebase, debug with logs, explore a repo, a ~90-turn feature session (7 workflows) | **3.7%** (0.0% to 8.6%) | estimated |
| Output-heavy: a verbose test loop, a failing container build, incident logs (3 workflows) | **19.3%** (2.5% to 50.7%) | estimated |
| A custom agent or script that sends no cache markers (the 7 everyday workflows again) | **72.0%** (51.6% to 79.8%) | estimated |

How to read the rows:

- **Everyday.** Claude Code already caches its own prompt, so the optimizers can only trim what it sends. Most of a session is the system prompt, the model's replies and the files it read, none of which a safe optimizer may remove. Even an oracle that deleted every tool output, including the files the model needs, would save between 6.5% and 56.9% depending on the session. Making outputs twice or four times larger moves the median only to 4.9% and 4.6%.
- **Output-heavy.** These sessions were chosen because large, repetitive tool output dominates them. Quote this tier as a ceiling for that kind of work, never as a typical day, and never average it with the everyday row. One workflow does most of the lifting (a monorepo test loop at 50.7%); incident logs, which the trimmer does not recognize, save only 2.5%.
- **No caching.** This saving is Anthropic's prompt caching, which Trove switches on for agents that do not use it. It is zero for an agent that already caches, and it does not apply to Claude Code. It assumes a 5-minute cache lifetime and a 10% chance of a cold cache on any turn.

All three rows price the same way: list prices on `claude-sonnet-5-5` as of 2026-11-08, token counts from the engine's bytes-per-token ratio rather than provider counts, declared optimizer overhead charged to the optimized side, and the same cache model on both sides.

**Nothing is lost for the saving (measured, from the checkers in the same run):** every one of 698 failure and error lines in the original tool output was still visible to the model; trimmed output is stored byte for byte and fetchable; all 400 requests kept the cached prefix byte-identical; 0 broken invariants; and a repeat run produced identical bytes. The engine adds about 2.5 ms per request at the median (p99 about 17 ms), measured in-process on one machine; that excludes the gateway's own forwarding time.

Reproduce with `bash bench/scripts/95_workflows.sh` (add `ANALYZE=1` for the ceiling and what-if analysis). The saved reports are in [`bench/published/workflows/`](bench/published/workflows/) and the method in [docs/workflows.md](docs/workflows.md).

### Caveats

- Two models and two Claude Code versions across the real-task studies. Results may differ for other models, versions or longer sessions.
- The real-task studies cover only the gateway. They do not test the Claude Code plugin's hooks, the path most people will use. Study files for each hook alone exist; none has been run.
- The scripted sessions are not real users. For your own sessions, `trove` reports estimates from the history your agent keeps.
- The first study overlaps an earlier 10-task study by nine tasks, so it is not fully held out. That earlier study (2026-11-08, 30 pairs) showed +9.7%.

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

<details>
<summary><b>Does it work with a subscription (Claude Pro/Max, ChatGPT)?</b></summary>

The history report, fixes, hooks and MCP server do. The gateway is for API-key use only; Trove will not touch subscription or OAuth traffic.

</details>

<details>
<summary><b>Which agents and versions?</b></summary>

Claude Code and Codex CLI. See [supported agents](docs/supported-agents.md) for what has and has not been tested.

</details>

<details>
<summary><b>Can an optimization make my agent worse?</b></summary>

Optimizers are off by default, run in shadow mode first, and are measured. If guardrails trip, Trove rolls them back and tells you. `trove optimize off` stops all of them at once.

</details>

<details>
<summary><b>How accurate are the dollar figures?</b></summary>

They are list-price estimates from usage counts. `trove reconcile` compares them with a real invoice; a difference within 2% counts as a match. See [measurement method](docs/measurement-method.md) and [pricing](docs/pricing.md).

</details>

<details>
<summary><b>How much will I actually save?</b></summary>

It depends on how your agent works, and the honest answer is that it is not yet proven for typical Claude Code use. The savings are largest where there is real waste to remove: output-heavy sessions, and agents that do not cache their prompts. Run `trove` and `trove fix` to see estimates for your own history, and see [Measured results](#measured-results).

</details>

## Uninstall

```bash
trove uninstall --dry-run      # shows what would happen
trove uninstall                # stops the gateway, restores every agent file, removes the MCP server
trove uninstall --purge-data   # also deletes what Trove stored in ~/.trove
```

Then remove the program itself: `brew uninstall trove`, or delete the binary the install script placed on your `PATH`. Trove never deletes data if an agent file could not be restored, because the backups are then still needed.

## Reporting bugs

We welcome your feedback. Run `trove feedback`: it builds a GitHub issue with versions, agent names, counts and error classes, shows you the full text, and opens it only if you say yes. It never includes prompts, code, paths or keys. You can also file a [GitHub issue](https://github.com/usetrove/trove/issues) directly.

## Data collection, usage, and retention

Trove collects nothing by default. It has no analytics or telemetry that runs on its own, and the only network calls it makes are the ones below. In gateway mode it forwards your own API requests to the provider you configured, as your agent would have. What it stores locally (token counts, sizes, hashes, model names, timings, outcome signals) is listed in [PRIVACY.md](PRIVACY.md); you can delete all of it with `trove uninstall --purge-data`.

If you want to help check Trove's savings claims, `trove contribute` shows a small anonymous summary (counts, whole-number percents and fixed labels; no prompts, paths, model names or dollar amounts), and `trove contribute --export file.json` saves it so you can share it yourself. Trove sends nothing unless you also run `trove contribute enable`, which shows the exact text, asks, and then shares that same summary at most once a week until you run `trove contribute disable`. It carries a random install id so you can ask for your data to be deleted. Details are in [PRIVACY.md](PRIVACY.md).

## Documentation

| | |
|---|---|
| **Using Trove** | [Getting started](docs/getting-started.md) · [Reports](docs/report.md) · [Gateway](docs/gateway.md) · [Optimizers](docs/optimize.md) · [Supported agents](docs/supported-agents.md) |
| **Trusting Trove** | [Measurement method](docs/measurement-method.md) · [Benchmark methodology](docs/benchmark-methodology.md) · [Scripted workflows](docs/workflows.md) · [Pricing and updates](docs/pricing.md) |
| **Safety and privacy** | [Privacy](PRIVACY.md) · [Security](SECURITY.md) · [Threat model](docs/threat-model.md) · [Compliance](docs/compliance.md) |
| **Contributing** | [Contributing](CONTRIBUTING.md) · [Beta checklist](docs/beta-checklist.md) |

## License

Apache-2.0
