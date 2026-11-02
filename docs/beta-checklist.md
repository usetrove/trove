# Private beta checklist

Goal: learn whether a stranger can install Trove, see value and uninstall using only the README, and whether the numbers match real invoices. Target 30 to 50 users.

Trove sends nothing on its own by default, so every metric below comes from something the tester chooses to send: a `trove feedback` issue, a `trove reconcile --export` file, a `trove contribute --export` file, or an answer in the check-in. Say this to every tester up front. Do not collect anything else.

The weekly anonymous upload (`trove contribute enable`) is off by default and sends nothing until a tester runs `enable` and answers yes after seeing the exact text. It goes to the endpoint in `[contribute] endpoint` (built-in default: contribute.troverelay.com). Offer it as an option, never as a requirement, and tell testers what it holds and that their random install id lets you delete their rows on request. The export file stays the fallback for anyone who prefers to send it by hand.

## 1. Who to recruit

Aim for a mix, and record the mix (not names) in the results sheet.

- [ ] Claude Code users on API keys (gateway path)
- [ ] Claude Code users on a subscription (history, fixes, hooks, MCP path)
- [ ] Codex CLI users
- [ ] At least some on macOS and some on Linux
- [ ] At least a few who use more than one agent
- [ ] A few heavy users (large monthly bill) and a few light ones
- [ ] At least 5 people who have never seen the project (the "stranger" test)

## 2. Before sending the build

- [ ] The release candidate passes [install-verification.md](install-verification.md) on a clean macOS and a clean Linux machine
- [ ] `go run ./scripts/pricingage --strict --max-age-days 7` passes
- [ ] The tester guide is just the README plus the one-paragraph note below. If a tester needs more, that is a README bug: log it.

Tester note: "Install Trove from the README. Run it. Tell us what happened. Nothing is sent to us unless you choose to send it (`trove contribute` shows the optional summary and `--export` saves it to a file). You can remove everything with `trove uninstall`."

## 3. What to ask each tester

Day 0 (after install):
- [ ] Which install path did you use? Did anything fail or confuse you?
- [ ] How long from starting the install to seeing your first useful output? (Ask them to note the time; do not guess.)
- [ ] Was the first number believable? Did it surprise you?
- [ ] Did you apply a fix on day one? Which? Did you read the diff first?

Day 7:
- [ ] Are you still using it? What did you change because of it?
- [ ] Did anything break or slow your agent down? (Ask for `trove feedback`.)
- [ ] Did you roll anything back, and why?

Day 30 (or at the first invoice):
- [ ] Run `trove reconcile` for the invoice period and send the `--export` file if you are willing.
- [ ] Run `trove contribute --export summary.json`, read it, and send it if you are willing. It has counts and whole-number percents only.
- [ ] Are you still using it? (Retention.)
- [ ] Would you be unhappy if it were gone? Why?
- [ ] Uninstall test (a few testers): run `trove uninstall --purge-data` and check your agent's config is as before.

## 4. Metrics

Record each with its source. A metric with fewer than the number of responses you expected is reported as "n = X of Y testers", never as a bare percentage.

| Metric | How it is obtained | Notes |
|---|---|---|
| Time to first insight | Tester's own note of install start and first report | Target under 2 minutes for the history report; the gateway path is within the first session. These are targets, not results. |
| Fixes applied on day one | `trove fix` list in the day-0 answer, or `fixes` count in `trove feedback` | Count fixes applied and fixes reverted separately |
| 30-day retention | Day-30 answer: still has Trove installed and used it in the last 7 days | Self-reported; say so |
| Reconciliation accuracy | `trove reconcile --export` files | Per tester: diff %, unpriced count, models involved. Summarize as the share within 2% and the spread of the rest |
| Optimizer effect and waste share | `trove contribute --export` files | Self-selected, so report "n = X of Y testers", not a rate for all users. Percents are rounded and request counts are bucketed |
| Rollback events | `guardrail_trips` and `fixes_reverted` from `trove feedback`, plus interviews | Separate automatic rollbacks (guardrails) from user reverts |
| Archive downloads | `go run ./scripts/downloads` (GitHub release counters; needs `GITHUB_TOKEN` while the repo is private) | Downloads, not installs or users. Covers the install script, Homebrew and npx. `go install` builds from source and is not counted, so ask about it in the day-0 install-path question. Uninstalls cannot be counted. |
| Install failures | Issues labeled `beta-feedback` | By install path and OS |

## 5. Reconciliation process

1. Each tester runs `trove reconcile --provider <p> --from <first day> --to <last day> --invoice <total>` for a completed billing period. Dates should match the invoice period and time zone (`--utc` if the provider bills in UTC).
2. With permission, they send the `--export` file. It holds totals and per-model token counts, no content.
3. For every result outside 2%, find the cause before launch: unpriced model, missing price entry, traffic outside the gateway, credit on the invoice, wrong period, or a real pricing or counting bug.
4. Record each cause. Fix real bugs, add a regression test, and re-run reconcile on the same export.
5. If a cause is outside Trove's control (credits, other machines), document it in [measurement.md](measurement.md) section 5.

## 6. Exit criteria (agree these before the beta starts)

Fill in the thresholds together; they are intentionally blank so nobody invents them.

- [ ] Reconciliation: at least ___% of testers with an invoice are within 2%, and every outlier has a documented cause.
- [ ] No open bug that loses data or breaks an agent (Trove must fail open).
- [ ] `trove uninstall` restored the agent config for every tester who ran it.
- [ ] At least ___ strangers completed install, first report and uninstall with only the README.
- [ ] Time to first insight was measured for at least ___ testers and the median is published as "measured, n = X".

## 7. Results sheet (one row per tester, no names)

Columns: agent, plan type (API key or subscription), OS, install path, minutes to first report, fixes applied day 1, fixes reverted, guardrail trips, still active at day 30, reconcile diff %, reconcile cause if outside 2%, free-text issues.
