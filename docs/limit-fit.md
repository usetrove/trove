# Plan limits: how your tokens move the meter

Written 2026-11-23. Code: `internal/measure/limitfit`.

Claude Code shows subscribers two meters: the share of the 5-hour limit and of the weekly limit used so
far. Anthropic does not say how input, output, cache reads and cache writes count toward them. Trove
learns it for **your** account, from those meters and the tokens your own Claude Code used. It then
shows costs and savings as a share of the limits.

**This is an estimate for one account, not an official weighting.** Trove never calls it anything
else, and every figure is labeled "estimated" with a 90% interval.

## Where it shows up

- **`trove report`**: the section "Plan limits (your estimate, not official weights)", and the
  headline's waste and savings as a share of each limit. `--json`: `plan_limits`.
- **Status line**: the savings element gains a share of the 5-hour limit, for example
  `saved ~$47 (~2% of 5h)`.
- **Autopilot**: each planner item's estimated net saving per shadow session, as a share of the
  limits (`est_limit_pct_per_session` in `autopilot.json`). This is display only. Nothing Autopilot decides
  reads it.

Nothing appears until there is enough data (see [Gates](#gates)).

## What it reads

| Input | From | Notes |
|---|---|---|
| Meter readings | `limit_samples`, written by `trove status-line --format claude` | Claude Code passes `rate_limits` to the status line command. See [report.md](report.md#plan-usage-meter). Needs the status line set up. |
| Tokens | `history_requests` for Claude Code | Input, output, cache read, 5-minute and 1-hour cache writes, model, hashed session key, list price |

Only the last 8 weeks are used. Nothing new is stored in the database. The fit is saved as `limitfit.json` in the
data folder (numbers only, permissions 0600), so the status line and Autopilot never query or fit.
It is recomputed by `trove report` and at session end, but only when a newer meter reading arrived
or the saved fit is a day old.

## Method

1. **Intervals.** Readings from every session on this machine form one timeline, because the meter is
   account-wide. Two consecutive readings with the same reset time (within 60 seconds) make an
   interval.
   - Intervals are merged until the meter moved at least **3 points** (5-hour meter) or **1 point**
     (weekly meter). This keeps the meter's rounding and the few seconds between a response and the
     reading small next to the movement.
   - The tokens of every Claude Code request inside an interval are summed by type and model family.
2. **Clean intervals.** An interval is left out, and counted by reason, when:
   - it is longer than 2 hours (5-hour meter) or 12 hours (weekly): too much can happen unseen
   - history has not been read up to its end (with 10 minutes' margin for requests still being written)
   - it contains a request from a session that never sent the meter. That may be an API-key job, which
     does not move the meter, so the interval is dropped rather than guessed.
   - it contains a cache write the log gave no 5m/1h split for
   - the meter went down, or the window reset before the meter moved enough

   An interval where the meter moved but this machine used no tokens is kept. It counts toward
   outside use (step 4).
3. **Fit.** The meter's movement in an interval is taken to be Σ weight × tokens, plus outside use
   that is never negative.
   - The weights are fitted by non-negative least squares (Lawson-Hanson, pure Go).
   - One set of weights per token type is shared by all models. Each model family gets a multiplier
     relative to the family with the most tokens, so "Opus drains faster" shows up as a multiplier
     rather than a second set of weights.
   - Outside use only ever adds movement, so the fit is **trimmed**: intervals whose movement is
     well above what their tokens explain are set aside and the fit is redone. The cut is the
     median leftover + 2.5 × a robust spread, in up to 5 rounds, and at most 20% of intervals.
4. **Outside use.** (total movement − what this machine's tokens explain) ÷ total movement, over
   every clean interval including the trimmed ones. Noise cancels in that sum; outside use does not.
   It is reported on its own line. It covers claude.ai, Cowork, other devices, and calls Claude Code
   makes but does not log, such as compaction summaries.
5. **Intervals for each number.** The fit is redone on 400 resamples of whole reset windows (whole
   days for the weekly meter), because intervals in one window are not independent. The 5th and
   95th percentiles give a 90% interval. The resampling is seeded from the data, so the same data
   always gives the same numbers.
6. **Input = 1.** Weights are shown as % of the limit per million tokens, which always means
   something. They are also shown relative to input only when the input weight's interval is within
   ±50% of its value. In Claude Code most of what the model reads is cache reads, so uncached input
   is a small share and its weight is often not pinned down. The report says so instead of showing
   ratios to a number that is mostly noise.
7. **The weekly meter** moves about 20 times slower. Until it has enough data for its own weights, it
   uses the 5-hour meter's weights with a scale of its own (labeled in the report).

## Gates

| Tier | Needs | What is shown |
|---|---|---|
| None | Fewer clean intervals than below | Nothing in %. The report says how many more intervals and windows are needed, and about how many hours of use that is |
| API prices | ≥ 10 clean intervals over ≥ 2 reset windows | One scale: % of the limit per dollar of API-equivalent usage, with API list prices as the relative weights. Labeled **"estimated from API prices"** |
| Your meter | ≥ 40 clean intervals over ≥ 4 five-hour windows (2 weekly windows for the weekly meter) | Weights per token type, labeled **"estimated from your meter"** |
| A family's own multiplier | That family is ≥ 20% of an interval's tokens in ≥ 15 intervals | Otherwise it is counted like the reference family, and the report says so |

Converting a token change uses the weights per type when there are some. Otherwise it uses the
API-price scale, which needs the change's dollar value. With neither, nothing is shown. Dollar
figures (the status line's savings, the report's waste and savings) always use the API-price scale.

## Regime changes

The way the meter counts may change, for example when plans change. Every 7 days, counting back from
the newest data, Trove fits the API-price scale on that week alone. A change is flagged when two
neighbouring weeks' intervals do not overlap **and** the scale moved by at least 25%. The 25% is
there because the weekly scale shifts a little with that week's mix of models and token types. From
the newest change on, only data since then is used, and the report says from when.

## Assumptions

- The meter counts tokens linearly: twice the tokens, twice the movement, within one regime.
- Readings taken a few seconds after a response include that response. Merging intervals keeps the
  error from this small.
- Requests from sessions that sent the meter are on the subscription; requests from sessions that
  never did may not be.
- The 5-hour and weekly meters weigh token types the same way, up to a scale (until the weekly meter
  has enough data of its own).

## What was verified (2026-11-23)

- <https://code.claude.com/docs/en/statusline>: `rate_limits.five_hour.used_percentage` and
  `seven_day.used_percentage` are the "Percentage of the 5-hour or 7-day rate limit consumed, from 0
  to 100". `resets_at` is in Unix epoch seconds. The example shows one decimal (23.5); the precision
  is not stated. They are only for Pro, Max and Ultra subscribers, after the first response. The
  only other window is the gateway-only `spend_limit`. There is no per-model weekly window.
- support.claude.com, "Use Claude Code with your Pro, Max, or Ultra plan": "These limits are shared
  across Claude and Claude Code". IDE usage counts too. "Max and Ultra plans also have a separate
  weekly limit for Fable", which the status line does not show. Verified Ultra accounts have no
  5-hour limit.
- <https://code.claude.com/docs/en/costs>: cache reads "still draw usage". Local `/usage` excludes
  "usage from other devices or claude.ai".
- support.claude.com, "Usage limit best practices": model choice and effort level affect usage.
- No page gives weights per token type, or says when the 5-hour window starts. "Rolling" is
  stated only for Team and Enterprise seats.

## Limits

- **Not official.** These are one account's estimates and can be wrong. The intervals say how wrong
  they might be, given the assumptions above.
- The Fable weekly limit on Max and Ultra is not in the status line, so it cannot be learned.
- Heavy use elsewhere (claude.ai, another computer) at the same time as this machine raises the
  outside share. Trimming removes the worst of it, but some may still be counted in the weights.
- Background API-key jobs that run alongside a subscription session cost those intervals. They are
  dropped, not guessed, so such a setup needs more time to reach the gates.
- Effort level is not a separate factor. Its effect shows up through output and thinking tokens.
- Ultra accounts without a 5-hour limit get only the weekly meter.
- The fit needs the status line. Without it, Trove sees no meter and shows nothing.
