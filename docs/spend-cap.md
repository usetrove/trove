# Spend cap

Status: **shadow mode is built. Refusing requests is not.** Setting `spend_cap = "on"` runs as shadow today.

## The idea

You set a budget (`trove budget set 10 --period daily`). A hard cap would stop new requests once the period's spending
reaches it, so a runaway agent loop cannot burn the day's money unnoticed. It is for API-key traffic only: the gateway
is the only place a request can be stopped, and the gateway never handles subscription logins (CLAUDE.md rules 3 and 10).

## What exists today (shadow)

It finds the requests a hard cap would have refused and says what they cost. Nothing is refused, delayed or changed:
every request still goes to the provider and every client gets its answer.

- **Switch:** `[optimizers] spend_cap = "shadow"` in `config.toml` (off by default). It needs a budget cap
  (`[budget] cap_usd`, set with `trove budget set`); without one it does nothing, and `trove optimize status` says so.
  The kill switch (`trove optimize off`) turns it off.
- **Which budget:** the one you already have. Its period (daily, weekly or monthly) and cap, with the same period
  boundaries the report and the status line use (local time, weeks from Monday). A test keeps the two definitions equal.
- **What "spent" means:** the figure the report shows: the list-price cost of successful requests in the ledger for the
  period (`measure.SpentUSD`). Requests that could not be priced are not in it, so spending can be understated, and
  the real bill can differ from list prices.
- **How the check works.** The ledger is read when the gateway starts, when the period rolls over, and every five
  minutes after that, so the figure survives a restart and cannot drift. Between reads the gateway adds the cost of
  each request it records. The check on a request is a comparison of two numbers in memory: it never waits for the
  database, and until the ledger has been read once nothing is marked. A failed read is retried after 30 seconds, not
  on every request.
- **What is recorded.** On the row of a request that arrived when spending had already reached the cap, one more entry
  in the existing `shadow_savings` column: `spend_cap` with that request's billed tokens and its whole cost. Nothing
  else is stored.
- **Where it shows.** `trove report` (Optimizers, as an estimate with a note, never offered as something to switch on)
  and `trove optimize status`. A default report does not mention it.
- **It cannot fail a request.** Any problem inside it means the request goes on as if it were off.

What the number is: an **estimate** of spending after the cap was reached. A person who hit a real cap would probably
have raised it or stopped, so it is an upper bound on what the cap would have saved, not a forecast.

## What was not built, and why

1. **What to refuse with.** The refusal has to be a response the agent will show, not retry in a loop. A 429 invites
   a retry with backoff, which is the opposite of what a cap is for. Which status and error type make Claude Code and
   Codex CLI stop and print our message is **not verified** and must be read from their documentation (and tried in a
   real session) before anything is built (CLAUDE.md rule 8).
2. **The message.** Plain English: what happened, what it cost, and how to continue (raise the cap with
   `trove budget set`, or resume for the period).
3. **How to continue.** A cap with no way past it is a trap. It needs an obvious, deliberate override.
4. **Never refuse when unsure.** If the ledger cannot be read, or the figure is stale, the request goes through
   (rule 4). Trove must never be the reason an agent stops because of a database problem.
5. **It stops new requests; it does not guarantee a ceiling.** Parallel agents mean requests already in flight finish
   and are billed, and the ledger lags a little. The wording must say "stops new requests", not "never exceeds".
6. **It cannot act per session.** A cap has to act at once, so it cannot wait for a session boundary the way optimizers
   do (rule 5). It would be opt-in with consent text saying so, and it changes which requests are served, not the
   bytes of any.
7. **Warning at the warn level.** `warn_at_percent` exists in the budget. The status line already shows pace; whether
   to add an in-band warning is open.
8. **Automated work.** `X-Trove-Kind: automated` separates scripted runs. Whether a cap should apply to them
   differently, and per-run caps, are later questions.
9. **Measuring it once on (rule 6).** Refusals can be counted, but the money not spent is a counterfactual, so that
   figure stays "estimated". Outcome checks (retries, abandoned sessions) compared before and after would show whether
   a cap is causing trouble.

## Exit criteria: build refusing only when all of these hold

- How Claude Code and Codex CLI treat the chosen refusal is read from their documentation and tried in a real session,
  and the result is written down with its source and date.
- Shadow data from a real API-key install shows the cap is reached sometimes and the estimate looks plausible, or
  users ask for it.
- The consent wording, the override and the message are decided.
- Tests exist for each item above: refusing only on a confirmed ledger figure, fail open on a failed or stale read,
  streams and requests already in flight untouched, rollover at the period boundary, a restart mid-period, and a
  canary showing no credential or prompt text reaches a row or a log.

## What to do now

```
trove budget set 10 --period daily      # or any amount and period you use
# in ~/.trove/config.toml:
#   [optimizers]
#   spend_cap = "shadow"
```

Restart the gateway, work as usual, and read `trove report` after a few days. If requests are marked, the figure is
what a hard cap would have stopped. If none are, you never reached the cap.
