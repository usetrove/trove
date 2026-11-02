# X thread draft

Status: draft for your edit. Each post stays under the platform limit; check before posting.

1. I ran a tool on my own Claude Code history to see where my tokens went. [MEASURED: share of input tokens that were repeated or avoidable, author's own history — source: docs/launch/evidence/<date>/author-report.json]. Yours will be different. Here's how to find out. 🧵

2. Trove is a local, open-source tool: "the efficiency layer for AI coding agents." Runs locally. Sends nothing by default. One binary. `trove` reads the session history your agent already keeps and shows the number.

3. The part I care about most: it doesn't just tell you, it offers fixes. `trove fix` lists specific changes to your agent's config with the exact diff. You approve each one. `trove undo` reverses it.

4. Savings are labeled. "Measured" means counted from provider-reported usage. "Estimated" means list prices or an assumption. Dollar figures are always estimates; `trove reconcile` checks them against your invoice.

5. Once a fix is on, Trove measures it and rolls it back if outcomes get worse. On our benchmark: [MEASURED: median cost reduction, all_safe vs direct — source: bench/results/<run>/summary.json], success rate [MEASURED: success rate with vs without — source: bench/results/<run>/outcomes.json]. One agent version, [N] tasks. A data point, not a promise.

6. Status line: add it to Claude Code and watch savings count up as you work (`trove setup --status-line`, opt-in).

7. Privacy: metadata only. No prompts, code or paths in the database. Sends nothing by default; an optional anonymous weekly summary stays off until you opt in, and you see the exact text first. The local gateway is API-key only and fails open.

8. Limits: Claude Code and Codex CLI only, macOS and Linux, one benchmark so far. Codex is less tested. Full list in the README.

9. Try it, then tell me where my number doesn't match your bill: github.com/usetrove/trove
