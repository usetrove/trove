# Measurement method (summary)

The full description is [measurement.md](measurement.md). This page is the short version for readers who want to know what a number means before trusting it.

## Two labels, always

- **Measured**: counted from usage the provider reported (token counts in the response) or from Trove's own observations of a session.
- **Estimated**: derived from list prices, an assumption, or a shadow-mode projection of what an optimizer would have done.

A dollar figure is always an estimate: tokens are measured, but the price is a list price, not your invoice (discounts, credits and taxes are not known to Trove).

## How an optimizer earns its place

1. **Shadow.** It computes what it would have saved and changes nothing.
2. **On.** Only at a session boundary, never mid-session, so cached prompt prefixes stay valid.
3. **Measured.** Savings are compared with a baseline or with sessions where it was off. Outcome signals (retries, refetches) watch for harm.
4. **Rollback.** If a guardrail trips, the optimizer is turned off and the report says why.

## How accuracy is checked

- **Reconciliation.** `trove reconcile` compares Trove's recorded total for a period with the total on your provider invoice. A difference within 2% (adjustable with `--tolerance`) is a match. Beta results and any fixed discrepancies are recorded in [beta-checklist.md](beta-checklist.md).
- **Benchmarks.** `bench/` runs fixed tasks with and without Trove and compares cost and task success. Results are published only from real runs, with the run's files as the source ([bench/README.md](../bench/README.md)). As of 2026-10-30 no benchmark run has been completed.
- **Pricing freshness.** Prices are compiled into the binary with a verified date per model. `trove doctor` warns when any is older than 30 days; releases require all of them within 7 days ([release-checklist.md](release-checklist.md)).

## What Trove cannot know

Traffic that does not go through the gateway, usage on other machines, invoice credits, and subscription usage (which is not billed per token). See section 5 of [measurement.md](measurement.md).
