# Product Hunt listing draft

Status: draft for your edit. Check `scripts/checkclaims --publish` before submitting.

**Name:** Trove

**Tagline (60 characters max):** The efficiency layer for AI coding agents

**Short description:** See where your coding agent wastes tokens, apply a one-click fix, and watch proven savings count up. Runs locally. Sends nothing. Open source.

## Description

Trove reads the history your coding agent already keeps and shows you your own number: where your tokens went and what was avoidable. Your own history: [MEASURED: share of input tokens that were repeated or avoidable, author's own history — source: docs/launch/evidence/<date>/author-report.json].

Then it offers fixes you approve one at a time, each with the exact change shown first and a one-command undo. Once a fix is on, Trove measures it, and rolls it back automatically if outcomes get worse.

- **One place across your agents.** Claude Code and Codex CLI today.
- **Proven, not promised.** Every number is labeled measured or estimated. On our benchmark: [MEASURED: median cost reduction, all_safe vs direct — source: bench/results/<run>/summary.json] with success rate [MEASURED: success rate with vs without — source: bench/results/<run>/outcomes.json].
- **Local and private.** One binary. Metadata only. No telemetry.
- **Works on a subscription too.** The optional local gateway is only for API-key users.

## Honest limits

Two agents, macOS and Linux, one benchmark so far. Dollar figures are list-price estimates; `trove reconcile` checks them against your invoice.

## First comment (maker)

Why I built it, what surprised me in my own history, and a request: run `trove`, then tell me if your number looks wrong.

## Assets to prepare

- Gallery: the report screen, the fix diff, the status line, the share card (`trove share`)
- Demo GIF from [demo-script.md](demo-script.md)
- Topics: Developer Tools, Artificial Intelligence, Open Source
