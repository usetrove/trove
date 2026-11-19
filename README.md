<div align="center">

<img src="docs/assets/trove-banner.svg" alt="Trove: the efficiency layer for AI coding agents" width="720">

Find the tokens your agent wastes, fix them with changes you approve one at a time, and check that your results did not suffer.

![Release](https://img.shields.io/github/v/release/usetrove/trove?style=flat-square) ![License](https://img.shields.io/badge/license-Apache--2.0-blue?style=flat-square) ![Go](https://img.shields.io/badge/Go-1.27.3%2B-00ADD8?style=flat-square) ![Platforms](https://img.shields.io/badge/macOS%20%7C%20Linux-supported-brightgreen?style=flat-square) ![Status](https://img.shields.io/badge/status-pre--release-orange?style=flat-square)

[Get started](#get-started) · [How it works](#how-it-works) · [Measured results](#measured-results) · [Docs](docs/getting-started.md) · [FAQ](#faq)

</div>

---

Trove reads the session history your agent already keeps on disk and finds where tokens go to waste: repeated reads, bloated context, cache breakers, unused tool definitions. It proposes fixes, shows you the exact change, and only applies it when you say yes. Then it measures whether the change helped.

One small program. No account. Nothing to sign up for.

- **Local-first.** Everything lives in `~/.trove`. Nothing is sent anywhere unless you opt in.
- **Honest numbers.** Every figure is labeled **measured** (counted from usage the provider reported) or **estimated** (derived from list prices or an assumption). Dollar figures are estimates, not invoices.
- **Proves before acting.** Every optimization is opt-in, estimated in shadow mode first, measured once on, and rolled back if its guardrails trip.
- **Works with** Claude Code and Codex CLI, on a subscription plan or with your own API keys. Claude Code is the best tested; see [Supported agents](#supported-agents) for exactly what works where.

## Get started

> [!NOTE]
> **Latest release: [v0.4.0](https://github.com/usetrove/trove/releases/tag/v0.4.0)** (2026-11-14). See [what's new](#whats-new-in-v040). Trove is still early: read [Measured results](#measured-results) before you expect a saving; the one confirmed saving is model routing for API-key users.

### 1. Install

Every path checks the download against the release's SHA-256 checksums before it runs anything.

**macOS / Linux, with Homebrew:**
```bash
brew install usetrove/trove/trove
```

**Windows (PowerShell, experimental):**
```powershell
irm https://github.com/usetrove/trove/raw/main/install.ps1 | iex
```
That is all there is to it. It installs `trove.exe` for your user (no administrator needed) and adds it to your user `PATH`, so `trove` works straight away in that window and in new ones. It checks the download against the release's SHA-256 checksums (and its cosign signature, if cosign is installed) and refuses to install on a mismatch. To leave your `PATH` alone, set `$env:TROVE_NO_PATH = "1"` first; to pin a version, set `$env:TROVE_VERSION = "0.4.0"`. To undo the `PATH` change, remove the folder under Settings, System, About, Advanced system settings, Environment Variables.

<details>
<summary>macOS or Linux without Homebrew</summary>

```bash
curl -fsSL https://github.com/usetrove/trove/raw/main/install.sh | sh
```
This installs the latest release (v0.4.0) into `~/.local/bin`. To pin a version, set `TROVE_VERSION=0.4.0` before `sh`. If you have [cosign](https://github.com/sigstore/cosign) installed, the script also verifies the release's signature; set `TROVE_REQUIRE_SIGNATURE=1` to refuse an install it cannot verify. It never edits your shell profile: if `~/.local/bin` is not on your `PATH`, it tells you to add it.

</details>

<details>
<summary>Windows: prefer to download and check it yourself?</summary>

Download `trove_0.4.0_windows_amd64.zip` (or `windows_arm64`) from the [release page](https://github.com/usetrove/trove/releases/tag/v0.4.0), check it, then unzip it. In PowerShell (replace the version with your release's):

```powershell
$v = "0.4.0"
Invoke-WebRequest "https://github.com/usetrove/trove/releases/download/v$v/trove_${v}_windows_amd64.zip" -OutFile trove.zip
Invoke-WebRequest "https://github.com/usetrove/trove/releases/download/v$v/checksums.txt" -OutFile checksums.txt
$want = (Select-String -Path checksums.txt -Pattern "trove_${v}_windows_amd64.zip").Line.Split(" ")[0]
if ((Get-FileHash trove.zip -Algorithm SHA256).Hash.ToLower() -ne $want.ToLower()) { throw "checksum mismatch: do not use this file" }
Expand-Archive trove.zip -DestinationPath "$HOME\trove"
& "$HOME\trove\trove.exe" doctor
```

</details>

The Claude Code plugin's hooks on Windows also need `sh` (Git Bash) on your `PATH`; `trove doctor` warns when it is missing.

### 2. Add the Claude Code plugin

A Claude Code plugin is an add-on for Claude Code, like a browser extension. It is how Trove gets inside your sessions: it registers small **hooks** (commands Claude Code runs when a session starts, around tool calls and when a session ends), adds the `/trove:` slash commands, and includes a launcher that finds the `trove` program. Type these in a terminal:

```
claude plugin marketplace add usetrove/trove
claude plugin install trove@trove
```

The launcher uses the program you installed in step 1. If it finds none, it downloads the matching release itself and checks its SHA-256 first. So for Claude Code on its own, this step is enough; step 1 is what gives you the `trove` command in a terminal.

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

## What using Trove looks like

Once it is set up there is nothing to run day to day:

1. **Install and add the plugin** (the two steps above).
2. **Start Claude Code.** On the first session Trove reads your existing history (read-only, metadata only) and shows a report: what you spent, the waste it found, and what it would suggest.
3. **Run `/trove:setup` once.** It shows one screen, in plain English, and asks you to choose **Turn on Autopilot** or **Just show me reports**. It also offers optional extras (a status line, Trove's MCP tools, a scout subagent) and shows what each costs in tokens first. **Nothing is switched on until you say yes.**
4. **Use Claude Code as normal.** With Autopilot on, each optimization starts in **shadow**, where it runs on a copy and only records what it would have saved, after paying for its own overhead. It is promoted only if it clears the bar over enough of your own sessions, and only at the start of a new session, never in the middle of one. Once on, it is measured, and it drops back to shadow by itself if the net saving goes negative, outcomes get worse, or errors rise. If anything fails inside Trove, your original request is forwarded unchanged.
5. **Glance at it now and then.** A status line, a one-line receipt when a session ends, and a weekly summary. You are only interrupted when a decision is needed, such as a rollback.

Most people need three commands: `/trove:setup` once, `trove` for the report, and `trove status` to see what is on. To step back, use `trove autopilot pause`, `trove autopilot off`, `trove undo` or `trove uninstall`. The other commands are optional: finer control (`trove fix`, `trove hooks install`, `trove mcp install`), the API-key gateway, and developer tools (`trove bench`, `trove stress`).

> [!NOTE]
> Autopilot is a first version: its thresholds are unvalidated placeholders, and the riskier optimizations need a separate, second yes (`trove autopilot trials on`). The plugin has run on real Claude Code only inside the benchmark (`launch-3`, Claude Code 2.1.319), where its hooks acted in 2 of 30 sessions; it has not yet been checked across everyday use. See [Supported agents](#supported-agents) and [Measured results](#measured-results).

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

The full, numbered rules are in [PRINCIPLES.md](PRINCIPLES.md).

## How much proof each change needs

Trove sorts every technique into three tiers by what it changes, and asks more proof of each tier:

- **T1 Safe**: lossless or recoverable edits to tool output and config (duplicate collapsing, noise cleanup, trimming
  with a recovery copy, cache control). Shadow first, then a session A/B.
- **T2 Behavioural**: asking the model to act differently (shorter replies, rule sets). Its added tokens are charged,
  test failures are watched, and you opt in explicitly.
- **T3 Routing**: changing which model, effort or agent does the work (model routing, lower effort on tool turns, a
  cheaper subagent model). A cache-stickiness rule, a benchmark verdict per model family and the strictest rollback.

See [docs/techniques.md](docs/techniques.md) for every technique and its tier.

## Supported agents

Trove works with **Claude Code** and **Codex CLI**, and ships an experimental **OpenCode** plugin. Claude Code is the best tested. The table says what exists and how well it has been checked; the full test record, with versions, is in [docs/supported-agents.md](docs/supported-agents.md).

| Feature | Claude Code | Codex CLI |
|---|---|---|
| History report and waste finding (`trove`) | Yes. Parsed against real session files (Claude Code 2.1.302) | Yes, but the parser has **not been verified on a real Codex install** |
| Fixes (`trove fix`) | Yes | Some: the default-model suggestion (`~/.codex/config.toml`), and cache-breaker and context-size findings in `AGENTS.md`. Pruning unused MCP servers is Claude Code only |
| Hooks | All of them, including repeat-read prevention and output rewrites | Session reset and session-end receipt; large-output advice on request. No repeat-read prevention (Codex names no file-read tool to match) |
| MCP tool server (`trove mcp install`) | Yes (needs `claude` on your `PATH`) | Yes, at user level (`~/.codex/config.toml`) |
| Optional gateway | Yes, **API-key use only**. Optimizers rewrite Anthropic Messages requests | Yes, **API-key use only** (`OPENAI_API_KEY`). Trove records metadata; the optimizers do **not** rewrite OpenAI-format requests |
| Subscription logins (Claude Pro/Max, ChatGPT) | Reports, fixes, hooks and MCP work. The gateway refuses them | Same. A ChatGPT or Codex login is refused by the gateway, and Trove never opens `~/.codex/auth.json` |

> [!NOTE]
> Codex CLI support is built from its documentation and open-source code but **has not been run on a real Codex install**. Treat it as experimental. If you try it, `trove feedback` is the fastest way to tell us what broke. The test record (last reviewed 2026-10-30) also lists macOS and Linux as not yet tested on real machines; its automated tests ran on Windows 11 and in CI.

## What's new in v0.4.0

Released 2026-11-14. Full notes in the [changelog](CHANGELOG.md) and on the [release page](https://github.com/usetrove/trove/releases/tag/v0.4.0).

- **Autopilot trial policy `lossless-v3`.** Adds three candidates to the two from v2 (`output_trimmer`, `hook_large_output`, `hook_read_limit`), tried one at a time. It needs its own yes (`trove autopilot trials on` shows what is added and asks again), and anyone who agreed to v2 keeps exactly v2 until then. The consent text is generated from the policy, so it cannot list fewer changes than the policy makes. All trial thresholds are still unvalidated placeholders. See [docs/autopilot-eligibility.md](docs/autopilot-eligibility.md).
- **New guardrails.** A trial candidate drops back to shadow on its own if `hook_read_limit` is followed by reads of more of the file at more than twice the shared rate, or if the `hook_large_output` hint is given and `trove_run` is then used in fewer than 10 percent of those sessions. If you installed hooks with v0.3.0 or earlier, run `trove hooks install --rewrite` again to get the counter.
- **`output_trimmer` declares its overhead.** Its marker is netted inside its estimate and the 351 tokens of Trove MCP tool definitions are charged per session, so Autopilot can trial it. It can be trialled through hooks alone, so subscription users can too.
- **A harder gateway.** A 5-minute whole-request read timeout (streamed responses are not limited), a 1 GiB cap on request bodies held in memory (an excess gets a plain 503 the agent retries), and free-text redaction that now also covers AWS, GitHub, Google, Slack, Stripe and npm keys and PEM private keys.
- **Security and supply chain.** A full [security document](docs/security.md) listing every control and what it does not protect against, a fuller [SECURITY.md](SECURITY.md) with how to verify a release, Dependabot, CodeQL and `go mod verify` in CI, cosign verification in `install.sh`, SLSA build provenance on release archives (`gh attestation verify`), a secret scan over tracked files, and a reproducible-build check. The provenance and rebuild checks are new and unproven on a release.

Known gaps are listed in the changelog. The main one: Autopilot's thresholds are placeholders, and `output_trimmer` and `hook_large_output` have no benchmark study, so their trials on your own sessions are their only evidence.

## Measured results

> [!IMPORTANT]
> **Bottom line.** In a pre-registered study of 30 long Claude Code sessions on Sonnet (`launch-3`, 2026-11-18), Trove's full setup with model routing on cut the estimated cost per session by **25%** (98% interval 21% to 29%), with no drop in tasks passed (measured). The saving comes from model routing, an opt-in option for API-key traffic that sends tool turns to a cheaper model; it never runs on a subscription. Model routing is on `main` and ships in the next release; v0.4.0 does not include it. The Claude Code plugin alone and the safe optimizers have not yet shown a saving on real tasks: on the tasks studied they had almost nothing to remove. Trove makes no savings promise beyond what a study shows.

Trove's benchmark harness (`bench/`) runs real coding tasks in Docker with your own API key. For each run it records whether the task passed and what it cost. Each study is pre-registered: the question, the decision rule and the budget are locked before the first run.

**Decision rule.** Trove recommends an optimizer for automatic use only if its cost reduction is at least 5% and the whole 95% interval lies at or below −5%, with the success rate not falling by more than 5 points and at least 30 pairs. Otherwise the verdict is NEED MORE DATA or DON'T SHIP. A result is never rounded up.

### 1. Long Claude Code sessions, every arm launched the same way (`launch-3`)

Does the Trove plugin cut cost, and does the plugin plus the gateway with model routing cut it further? 30 long-session Python exercises, Claude Code 2.1.319, `claude-sonnet-5-5`, routing to `claude-haiku-5-5`, API-key auth, 90 runs (30 pairs per comparison). Costs are **estimated** (list price on the usage the gateway recorded for each request, the same source for every arm). Pass or fail is **measured**. The study was staged: each look is tested at a 98% interval, and it stopped after the first look because both comparisons were decided.

| Compared with no Trove (`passthrough`) | Total cost change, estimated (98% interval) | Tasks passed | Verdict |
|---|---|---|---|
| Plugin + gateway + model routing (`trove_full`) | **−25.3%** (−29.2% to −21.4%) | 30 of 30 vs 30 of 30 | **SHIP** |
| Plugin alone (`passthrough_plugin`) | **+3.5%** (−0.6% to +7.7%) | 30 of 30 vs 30 of 30 | **DON'T SHIP** (even the best case saves less than 5%) |

- **Where the saving comes from.** Model routing. In `trove_full` the safe gateway optimizers removed no tokens, and input tokens rose (about 10.5M vs 7.7M, measured) because a model switch rewrites the cache; the cheaper model's price more than paid for that. `trove_full` also had brevity steering on. Routing (T3) and brevity steering (T2) are both off by default and need an explicit opt-in.
- **Why the plugin alone saved nothing here.** Its hooks acted in 2 of 30 runs; these tasks gave them almost nothing to do. That says the tasks had little waste, not that the hooks do nothing.
- **Limits.** Every arm passed every task, so the tasks were easy; the study shows no loss of success, not quality on harder work. Sessions with routing took a median 9.4 s longer (measured). One model, one Claude Code version, this suite only.
- An earlier launch study (`launch-1`) is published with a correction: its baseline ran Claude Code with `--bare` and the plugin arms could not, so its plugin comparison is confounded. `launch-3` launches every arm the same way.

Report, per-run metadata and the lock: [`bench/published/launch-3/`](bench/published/launch-3/report.md). Reproduce with the runbook in [`bench/scripts/launch/`](bench/scripts/launch/README.md).

### 2. Real tasks, two pre-registered studies

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

### 3. Scripted everyday sessions, replayed through the real engine

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
- The only confirmed saving (`launch-3`) is model routing, for API-key traffic. Subscription users get the plugin, which has not yet shown a saving. Study files for each hook alone exist (`components-1`); none has been run.
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
| Windows install script (`install.ps1`) | Adds its folder to your user `PATH` (opt out with `TROVE_NO_PATH=1`); changes no other setting | Remove the folder from Path under Environment Variables, and delete `%LOCALAPPDATA%\Programs\trove` |
| Normal use | Creates `~/.trove` (database, backups, settings) | `trove uninstall --purge-data` |

Shell rc files are never touched. Trove never reads or writes your API keys.

## FAQ

<details>
<summary><b>Does it work with a subscription (Claude Pro/Max, ChatGPT)?</b></summary>

The history report, fixes, hooks and MCP server do. The gateway is for API-key use only; Trove will not touch subscription or OAuth traffic.

</details>

<details>
<summary><b>Which agents and versions?</b></summary>

Claude Code and Codex CLI, plus an experimental OpenCode plugin. See [Supported agents](#supported-agents) above, and [docs/supported-agents.md](docs/supported-agents.md) for the test record. Codex support has not been run on a real install yet.

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

If you want to help check Trove's savings claims, `trove contribute` shows a small anonymous summary (counts, whole-number percents and fixed labels; no prompts, paths, model names or dollar amounts), and `trove contribute --export file.json` saves it so you can share it yourself. Trove sends nothing unless you also run `trove contribute enable`, which shows the exact text, asks, and then shares that same summary at most once a week until you run `trove contribute disable`. It carries a random install id so you can ask for your data to be deleted. The code of the server that receives these summaries is in [`server/worker`](server/worker/README.md), so you can read exactly what it does with them. It never sees your prompts, code, paths, keys or individual requests: Trove never sends those anywhere. Details are in [PRIVACY.md](PRIVACY.md).

## Documentation

| | |
|---|---|
| **Using Trove** | [Getting started](docs/getting-started.md) · [Reports](docs/report.md) · [Gateway](docs/gateway.md) · [Optimizers](docs/optimize.md) · [Supported agents](docs/supported-agents.md) |
| **Trusting Trove** | [Measurement method](docs/measurement-method.md) · [Benchmark methodology](docs/benchmark-methodology.md) · [Scripted workflows](docs/workflows.md) · [Pricing and updates](docs/pricing.md) |
| **Safety and privacy** | [Privacy](PRIVACY.md) · [Security](SECURITY.md) · [Threat model](docs/threat-model.md) |
| **Contributing** | [Principles](PRINCIPLES.md) · [Contributing](CONTRIBUTING.md) · [Changelog](CHANGELOG.md) |

## License

Apache-2.0
