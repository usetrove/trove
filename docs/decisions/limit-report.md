# Decision: limit report (new fields in `trove contribute`)

Status: APPROVED 2026-11-26 by the maintainer, with the recommended answer to every question below. Built in
`internal/contribute` (limits.go), `internal/cli` (contribute_limits.go) and `server/worker` (aggregate.js, page.js).
PRINCIPLES.md is unchanged: the proposed rule 1(b) wording is for the maintainer to apply.

## What the feature does

Users who opt in add a small "limits" section to the weekly `trove contribute` upload. It holds what their own
plan-limit estimate ([limit-fit.md](../limit-fit.md), `internal/measure/limitfit`) learned about how each kind of
token counts toward the limits, and what the regression watch ([regress.md](../regress.md), `internal/regress`)
flagged per Claude Code version. The receiver publishes only aggregates, on a static weekly page: median weight
ratios with ranges, the unexplained share, and which Claude Code versions drained the limit faster.

Item numbers: the brief's "1.4" is the plan-limit fit (docs/limit-fit.md, weights normalised to input = 1) and its
"3.5" is the regression watch (docs/regress.md; plan 1.8 in trove-plan/docs/plan/INDEX.md, whose own 1.4 is a
different item). Confirmed by the maintainer 2026-11-26.

## Why it needs a sign-off

1. Rule 1(b) lets the upload send "exactly the text `trove contribute` shows". That covers any field technically,
   but users agreed to a specific list (PRIVACY.md, "The summary holds"). This adds fields they did not agree to.
2. PRIVACY.md promises the summary has no model names or exact dates. Version strings and per-version flags are new
   kinds of data, and they say something about how a user's own usage behaved.
3. The page publishes claims about another company's product ("version X drains faster"). Rule 9 applies to them
   with extra weight: a wrong public number is worse than a wrong local one.

## Fields (proposed)

All new fields sit in one optional object, `limits`. `schema_version` goes from 1 to 2. Users who have not opted in
to the limits section send exactly what they send today, plus the new version number.

```json
"limits": {
  "plan": "max",
  "claude_code_version": "2.1.90",
  "five_hour": {
    "ratios": {
      "output":         {"ratio": 5,   "width": 1.2},
      "cache_read":     {"ratio": 0.1, "width": 0.1},
      "cache_write_5m": {"ratio": 1.3, "width": 0.4},
      "cache_write_1h": {"ratio": 2,   "width": 0.9}
    },
    "unexplained_pct": 12
  },
  "seven_day": { "unexplained_pct": 15 },
  "version_checks": [
    {"version": "2.1.90", "metric": "limit_pct_per_1k_output_equivalent", "status": "alert", "cause": "client"},
    {"version": "2.1.89", "metric": "limit_pct_per_1k_output_equivalent", "status": "normal"}
  ]
}
```

| Field | Source | Rule |
|---|---|---|
| `plan` | **Declared by the user** when they opt in. Fixed list: `pro`, `max`, `ultra`, `team`, `enterprise`, `unspecified`. | Trove has no other source. Claude Code's status line input has no plan field (checked below). Trove never reads credential files to find it (rules 3, 10). |
| `claude_code_version` | The version with the most requests in the period, from local history. | Must match `^\d{1,3}\.\d{1,3}\.\d{1,4}$`, else omitted. |
| `five_hour.ratios` | `MeterFit.Ratios` (each token type's weight divided by the input weight, so input = 1 and is not sent). | Sent only when the meter's tier is `fit` and `input_determined` is true. Otherwise the whole `ratios` object is omitted. |
| `ratio` | `Range.Point` | Rounded to one decimal below 10, to a whole number at 10 or above. |
| `width` | `Range.Hi - Range.Lo` (the 90% bootstrap interval). | Same rounding. The interval's ends are not sent. |
| `unexplained_pct` | `MeterFit.Outside.Point` (drain the tokens on this machine cannot explain: claude.ai, other machines, hidden calls). | Whole percent, 0 to 100. Sent only when the meter's tier is `fit`: at the API-price tier the share rests on prices, not this user's meter. |
| `seven_day.ratios` | As for `five_hour`. | Omitted when the weekly fit borrows the 5-hour profile (`borrows_5h_profile`), since its ratios are then a copy, not evidence. |
| `version_checks` | `regress.Result.Checks` with `kind = version`. | At most the 5 newest versions. Only status `alert` or `normal`. `cause` only on alerts. `metric` from the fixed list of 4 in `regress.Metrics`. |

Every value is an estimate, and the page labels it "estimated" (rule 6).

### Never sent

- No prompt text, code, tool output, file paths, repo names or hashes of them.
- No session, prompt or request ids, and no ids besides the existing random install id.
- No timestamps. No reset times, regime-change times, interval times or day-keyed regression checks (the watch's
  `kind = day` checks carry a date, so all of them are dropped). The receiver keeps only the UTC day of arrival, as
  it does today.
- No absolute weights (% of the limit per million tokens), no % per API dollar, no token counts, interval counts,
  request counts, session counts or bootstrap replicates. Together these would show how much someone uses Claude
  Code. Ratios do not.
- No model names or family multipliers. The ratios do not depend on the family, because a family multiplier scales
  every type equally.
- No regression-watch values, baselines, MADs, sample sizes, the model and effort stratum, sentences or advice.
- Nothing from the status line's `workspace`, `pr`, `worktree`, `cost` or `prompt_cache` fields.

## Consent

The limits section has its own opt-in. Turning on the summary today does not turn it on.

```
trove contribute limits enable --plan max
```

shows this, then the exact payload, then asks:

> Share your plan-limit estimate too?
>
> This adds a "limits" section to the weekly summary. It says how much output, cache reads and cache writes
> seemed to count toward your plan limits compared with input, how sure that estimate is, what share of your
> limit use Trove could not explain, the plan you named, your main Claude Code version, and whether recent
> versions used your limit faster than earlier ones.
>
> It never includes your prompts, code, file paths, session ids, model names, token counts or any time finer
> than the day it arrives.
>
> The project publishes only medians and ranges over at least 20 installs on a weekly page. Your numbers are
> never shown on their own. Once a weekly page is published, its figures can't be withdrawn, but
> `trove contribute delete` removes your rows so later pages leave you out.
>
> Share it? [y/N]

The answer is recorded as `limits: true` and `plan` in `contribute.json`. Without them the section is never built.

## Preview and withdrawal

- `trove contribute --preview` prints the exact JSON bytes the next upload would send, including the install id
  when upload is on, and sends nothing. It is the same function the upload calls, and a test checks that the bytes
  match.
- `trove contribute` (as today) shows the same thing, adding a plain-English line for each limits field.
- `trove contribute limits disable` drops the section from the next upload and onward.
- `trove contribute delete` (unchanged) deletes every row under the install id, including the limits section.

## Receiver and page

- The receiver validates the `limits` object against the same fixed list (validate.js stays in step with
  payload.go). It rejects unknown keys, any value out of range and any version string that does not match.
- Rows are stored as today: validated JSON, UTC day, install id, deleted after 365 days.
- A weekly job computes aggregates only: one row per install per ISO week (the latest), then per cell:
  - **Weights**, by plan and meter: the median ratio per token type, the interquartile range across installs (not
    min/max, which would show one install's value), and the median interval width.
  - **Unexplained share**, by plan and meter: the median and the interquartile range.
  - **Version drain flags**, per Claude Code version: the share of installs on that version whose check was
    `alert` with cause `client`.
- **Minimum group size: 20 distinct installs per cell.** A cell under 20 is folded into `all plans`. If it is still
  under 20, it is not shown, and neither is its count.
- A version is flagged on the page only if it has at least 20 installs with a check, at least 5 of them alerting
  with cause `client`, and an alerting share at least twice the median share for the 5 versions before it.
  Otherwise it is listed as "no signal" or "too few installs".
- A week is held for a maintainer (stored, not served) during the first 4 approved weeks, whenever a published
  median moves by more than 50% against the last approved week, and whenever any version is flagged. Approve it
  with the command in server/worker/README.md.
- The receiver's body limit goes from 8 KB to 16 KB: the largest payload the client can build is about 11 KB
  (`TestLargestPayloadFitsTheReceiver`). Deploy the receiver before releasing a client that sends schema 2.
- The page is static HTML (no scripts, cookies, outside resources or analytics; served with `default-src 'none'`),
  built from the aggregates and nothing else. The receiver serves the latest approved week at `GET /v1/limit-report`
  and its JSON at `/v1/limit-report.json`. It states: "estimated from N opted-in Trove
  installs; self-selected; not official weightings; your own account may differ". The Trove binary never fetches
  it: that would be a new outbound call, which rule 1 does not allow.
- The page and feature are named "limit report". No provider name in any product name (rule 13).

## Verified (rule 8), 2026-11-26

- https://code.claude.com/docs/en/statusline: the stdin JSON has a top-level `version` ("Claude Code version") and
  `rate_limits.five_hour` / `rate_limits.seven_day` (`used_percentage`, `resets_at`). It has **no plan or
  subscription-type field**. `rate_limits` appears only for claude.ai Pro, Max and Ultra subscribers (or behind a
  gateway with a spend limit).
- https://support.claude.com/en/articles/11145838-using-claude-code-with-your-pro-or-max-plan: individual plans are
  named Pro, Max and Ultra. It does not name Max 5x / 20x tiers. Team and Enterprise are covered by a separate
  article (11845131), which I did not read, so whether Team and Enterprise seats get the same meters is unverified.

## Proposed rule text (for the maintainer to apply, or not)

Rule 1(b) would read, with the new words in bold:

> (b) the anonymous aggregate summary upload, which is off by default and happens only after the user runs
> `trove contribute enable`, sends exactly the text `trove contribute` shows, **carries only the fields listed in
> PRIVACY.md, each approved in a decision under docs/decisions/ (the limits section additionally needs its own
> `trove contribute limits enable`),** and is never in the gateway request path,

PRIVACY.md's "The summary holds" paragraph and the receiver README would gain the limits fields and the publishing
rules above.

## Questions for the approver, with recommended answers

1. Is the rounding fine-grained enough? With one decimal, a cache-read ratio near 0.1 can only be sent as 0.0, 0.1
   or 0.2, which is too coarse to tell 0.08 from 0.12.
   Recommended: two significant figures for ratios below 1 (0.0834 is sent as 0.083), and the brief's rule
   (one decimal below 10, whole numbers above) otherwise. **Approved.**
2. Should `plan` split Max into tiers (Max 5x, Max 20x)?
   Recommended: not yet. The official article I read names only Pro, Max and Ultra. Add tiers once a source names
   them. **Approved.**
3. Should the limits section carry the install id, so it can be deleted, or go without one?
   Recommended: carry it. Without it `trove contribute delete` could not remove the section, and the page
   publishes only aggregates over at least 20 installs anyway. **Approved.**
4. Minimum group size of 20, and the flag rule (at least 5 alerting installs, at least twice the earlier median).
   Recommended: as written. The numbers are judgment calls and only bound the risk. **Approved.**
5. Where is the page hosted, and who signs off on each week's page before it goes up?
   Recommended: a static site in the project's own hosting, built by the weekly job and reviewed by a maintainer
   for the first four weeks. After that it publishes on its own unless a cell changes by more than a set amount. **Approved.**

## Sign-off

- Approved by: project owner, 2026-11-26
