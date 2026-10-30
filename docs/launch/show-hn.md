# Show HN draft

Status: draft for your edit. Do not post until `go run ./scripts/checkclaims --publish` passes.

## Title options

- Show HN: Trove – find where your AI coding agent wastes tokens, and prove the fix didn't hurt
- Show HN: Trove – a local tool that shows your coding agent's token waste (open source, no telemetry)

## Text

I built Trove because I couldn't answer a simple question: where do my coding agent's tokens actually go?

Run `trove` and it reads the session history Claude Code already keeps on disk and shows your own number. In my history, [MEASURED: share of input tokens that were repeated or avoidable, author's own history — source: docs/launch/evidence/<date>/author-report.json]. Yours will differ, which is the point.

Then `trove fix` lists specific, evidence-based changes to your agent's config, with the exact diff. You approve each one, and `trove undo` reverses it. Estimated savings are labeled estimated; once something is on, Trove measures it and rolls it back if outcome signals get worse.

On our benchmark, the safe optimizers cut cost by [MEASURED: median cost reduction, all_safe vs direct — source: bench/results/<run>/summary.json] with a task success rate of [MEASURED: success rate with vs without — source: bench/results/<run>/outcomes.json] ([MEASURED: number of task pairs — source: bench/results/<run>/summary.json] pairs, one agent version, [list tasks]). I'd treat that as one data point, not a promise.

How it's built:

- Single Go binary, no cgo. Everything is stored in `~/.trove` as metadata only (counts, sizes, hashes, never prompts or code). No telemetry; the only network traffic is your own API requests, if you opt into the local gateway.
- Without the gateway there's no proxy at all: history audit, config fixes, hooks, and an MCP server. This works on a subscription plan too.
- The optional gateway (127.0.0.1) is for API-key users. It never touches subscription or OAuth traffic and fails open: if Trove errors, your request goes through unchanged.
- Optimizations are off by default, have a shadow mode, and are switched only at session boundaries so prompt caches stay valid.

Limitations, honestly:

- Tested with Claude Code and Codex CLI only; see the supported-agents page for the exact versions. Codex support is less tested.
- Dollar figures are list-price estimates. `trove reconcile` compares them with your invoice; in our beta, [MEASURED: share of beta invoices within tolerance — source: docs/beta-results/<date>/reconcile-summary.json].
- Some optimizers can't be measured with some agents, and we say so rather than guess.
- macOS and Linux only for now.

Repo: https://github.com/usetrove/trove. I'd most like to hear where your number doesn't match your bill.

## Be ready for these comments

- "Does it send my data anywhere?" Point to PRIVACY.md and the no-egress CI test.
- "Why not just a proxy?" Most of the value works without one; the gateway is optional.
- "Benchmarks on one agent?" Agree, link the limitations, and say what is next.
