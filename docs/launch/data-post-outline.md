# Data post outline: "Where AI coding tokens actually go"

Status: outline only. Every figure waits for a completed benchmark run and beta data.

## Thesis (edit once the data is in)

A coding agent's bill is mostly something other than the code you asked it to write. The data shows which parts, how much is avoidable without hurting results, and how we checked.

## 1. The hook

Lead with a reader's own number, not ours: run `trove` on your own history and see where your tokens went. Then ours:

- Across [N] sessions from [N] beta users, the largest share of input tokens went to [CATEGORY]: [MEASURED: share of input tokens by category, median across sessions — source: docs/beta-results/<date>/tokens-by-category.json]

## 2. Method (put this before the results)

- Where the data comes from: `bench/` tasks run on a pinned agent version, and, with permission, beta users' exported counts. State which figure comes from which.
- What is measured and what is estimated ([measurement-method.md](../measurement-method.md)).
- What is excluded: anything not seen by Trove, subscription usage not billed per token.
- How the benchmark compares conditions: `direct`, `passthrough`, `alone:<optimizer>`, `all_safe` ([bench/README.md](../../bench/README.md)), and the ship rule (minimum saving, success margin, minimum pairs).
- Reconciliation: how many beta users' totals matched their invoices within the tolerance, and what explained the rest: [MEASURED: share of invoices within tolerance — source: docs/beta-results/<date>/reconcile-summary.json]

## 3. Findings (one section each, one chart each)

| Section | Chart | Data needed | Source |
|---|---|---|---|
| Where input tokens go | Stacked bar by category (system prompt, tool definitions, file reads, tool output, history, other) | Per-category token counts per session | `bench/results/<run>/segments.json` |
| Cache behaviour | Cache read vs write vs uncached share, by agent | Usage fields from the gateway | `bench/results/<run>/cache.json` |
| Repeated work | Distribution of repeated reads per session | Hook / log counters | `bench/results/<run>/repeats.json` |
| What each optimizer saved | Bar: saving per optimizer alone and combined, with sample sizes | `alone:*` vs `direct` pairs | `bench/results/<run>/optimizers.json` |
| Did results suffer? | Success rate with vs without, with intervals | Task outcomes per condition | `bench/results/<run>/outcomes.json` |
| Estimate vs invoice | Scatter or table: recorded vs invoiced per beta user | `reconcile --export` files | `docs/beta-results/<date>/reconcile-summary.json` |

Charts: build them from the committed results files only, using one consistent palette, labeled axes, and the sample size on each. Label every chart "measured" or "estimated". Include a table of the numbers beneath each chart.

## 4. Limitations (must be in the post)

- Which agent and version, which tasks, which models. Results may not carry over to others.
- Optimizers that could not be measured with a given agent are reported as "not measurable with this agent", not omitted.
- Dollar figures are list-price estimates; invoices differ by credits and discounts.
- Beta users are self-selected.
- Anything that failed the guardrails or was rolled back, and why.

## 5. Call to action

Install, run `trove`, look at your own number, apply one fix if it makes sense, and tell us if our number did not match your bill (`trove reconcile`).

## 6. Appendix

Raw results files, the exact benchmark config and agent version, the pricing data version used, and the commit of Trove that produced them.
