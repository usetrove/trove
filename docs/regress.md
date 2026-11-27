# Regression watch

The regression watch catches a Claude Code release, or a change on the provider's side, that suddenly makes the
same work burn more. It runs on Claude Code's local session history (the same data as the rebuild and output
ledgers in [analyze.md](analyze.md) and the plan-limit estimate in [limit-fit.md](limit-fit.md)), compares the
newest versions and the last two days with your own earlier history, and says so in one sentence in `trove report`
and the status line.

It is advice only. Trove never pins a version, never turns auto-update off and never changes effort because of it.

## What it tracks

For each Claude Code version and each day:

| Metric | Value | Label |
|---|---|---|
| Unexplained rebuild rate | requests whose cache write was an `unexplained` rebuild, over all requests | measured |
| Thinking per request | thinking tokens over requests whose output could be split | measured, or estimated when some thinking was estimated from sizes |
| Requests with thinking | share of those requests with any thinking | as above |
| 5-hour limit per 1k output-equivalent tokens | meter movement (measured) over the output-equivalent tokens in the same clean 5-hour meter intervals | estimated from API prices |

Output-equivalent tokens are a request's tokens at API list prices, counted as output tokens of the same model
(its API-equivalent cost over the price of one output token). The meter movement is Claude Code's own number; the
denominator comes from list prices, so the metric is always labeled "estimated from API prices", whether or not
the per-user meter fit has reached its own gates. An interval that mixes versions counts for days only.

The two thinking metrics are compared within one model and effort level: the most common pair in the version or
day checked, against the baseline's requests on the same pair. Moving to a model or effort that thinks more is
your choice, not a regression, so it does not alert. The sentence names the pair, for example "thinking per
request (claude-opus-5-5, high effort) up 70%".

## The change test

The baseline is one value per day over the 28 days before the version first appeared (counting only older
versions) or before the day being checked. A day counts as a baseline block once it has 20 requests (2 meter
intervals for the plan-limit metric), and at least 5 such days are needed.

The new value is compared with the median of those days. The scale is 1.4826 times their median absolute
deviation, but never less than 5% of the median or a small per-metric floor (half a point of rebuilds, 2 points of
thinking share, 20 thinking tokens), so a flat baseline does not alert on one event. Only increases alert.

### Thresholds (starting values to tune)

All of these are named constants in `internal/regress/regress.go`. An alert needs every one.

| Constant | Starting value | Meaning |
|---|---|---|
| `MinRequests` | 200 | requests in the version or day (`MinIntervals` = 10 meter intervals for the plan-limit metric) |
| `MinSessions` | 3 | sessions in the version or day |
| `MinMADs` | 3.5 | robust deviations above the baseline median |
| `MinRelChange` | +30% | relative change over the baseline median |
| `MinBaselineBlocks` | 5 | baseline days with enough data |
| `BaselineDays` | 28 | days of baseline |
| `RecentDays`, `RecentVersions`, `VersionActiveDays` | 2, 3, 7 | what is checked: today and yesterday, the 3 newest versions while seen in the last 7 days |
| `MinActiveRequests` | 30 | requests for a version to count as active on a day |

A version or day under the minimums is listed as "too little data" (or "no baseline") and never alerts.

## Client or server

- **Version alert.** When older versions ran alongside the new one (at least 30 requests since it appeared) and did
  not spike, the cause is the client. If they spiked too, it is called server side. With nothing to compare, the
  cause is "unclear", and the advice says pinning the previous version tests whether the update is the cause.
- **Day alert.** The server is blamed only when the spike shows in at least two versions active that day. With only
  one active version the cause is "unclear". If exactly one of several versions spiked, the client is blamed, and
  the day alert is dropped when that version's own alert already says the same.

## Advice

- Client version: pin the previous version and turn auto-update off. Checked 2026-11-26 on
  https://code.claude.com/docs/en/setup: the native installer takes a version
  (`curl -fsSL https://claude.ai/install.sh | bash -s 2.1.89`, on Windows
  `& ([scriptblock]::Create((irm https://claude.ai/install.ps1))) 2.1.89`), and `"DISABLE_AUTOUPDATER": "1"` in
  the `env` block of `settings.json` stops the background update check (`DISABLE_UPDATES` also blocks manual
  updates; `autoUpdatesChannel: "stable"` follows a release about a week old instead).
- Server side: drop effort one level until it is fixed.

## Reporting it

`trove feedback --regression` drafts a GitHub issue on Claude Code's repository with the numbers only: version or
day, the value and baseline, the change, the sample, the label and the likely cause, plus OS, architecture and
Trove's version. No prompts, code, file paths, tool output, session ids or keys, and none of the advice. It shows
the text first and opens nothing unless you say yes; `--no-open` prints the link only. The Claude Code
troubleshooting page (https://code.claude.com/docs/en/troubleshooting, "Get more help", checked 2026-11-26) points
to that repository and to `/feedback` inside Claude Code.

## When it runs

At every session end, when the saved result is older than 30 minutes: it first reads the newest Claude Code
history (files written in the last 3 days, incremental, capped at 0.7 s), then recomputes. That read only happens
where Trove already reads history: history on, and history already scanned once by `trove report` or
`trove logs scan`. `trove report` recomputes every time. The result is saved as `regress.json` (0600) in the data
folder; the status line only reads that file and shows the strongest alert while the result is under 36 hours
old. So a regression shows within one session end of it reaching the minimum sample.
