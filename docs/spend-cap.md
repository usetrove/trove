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

## Active mode: warn, then stop (plan 8.1)

`spend_cap = "on"` adds two things to shadow mode. Shadow mode itself is unchanged.

- **Warning.** Once per period, when spending reaches `warn_at_percent` of the cap (default 80), and once when it
  reaches the cap, the gateway logs a warning and, if `notifications.desktop` is on, shows it as a desktop
  notification. The figures are estimated. The status line already shows *near cap* and *over cap*.
- **Stopping.** Past the cap, a request is refused only when all of these hold. Otherwise it goes through and is
  marked, as in shadow mode.
  1. The agent that sent it passed the refused-request probe below. Until an agent passes, the cap only warns for it.
  2. The ledger figure was read no more than 10 minutes ago (`spendStaleAfter`). An unread or stale figure never refuses.
  3. The request arrived in the period that figure belongs to.

A refused request never reaches the provider. The agent gets a plain-English message in the provider's own error
shape: what was spent, the cap, and how to continue (`trove budget set <amount>`, or `spend_cap = "shadow"`).

- Anthropic routes answer `402` with type `billing_error`. The errors page documents 402 billing_error, while 429
  is for rate limits, which the SDKs retry (platform.claude.com/docs/en/api/errors, read 2026-11-18).
- OpenAI routes answer `429` with type `insufficient_quota` and code `trove_spend_cap_reached`. OpenAI documents its
  own spend limits as 429 and says retrying billing, spend or quota errors won't help
  (developers.openai.com/api/docs/guides/error-codes, read 2026-11-18). The `{"error": {...}}` body shape was not on
  that page; the probe checks the agent's reaction to it.

Each refusal is recorded as one metadata row with error class `spend_cap_refused`: no tokens and no cost. Requests
already in flight finish, so the cap stops new requests; it does not guarantee a ceiling.

### The refused-request probe (manual, needs a person)

Neither Claude Code's nor Codex CLI's documentation fully says which errors they retry. Claude Code's env-vars page
lists `CLAUDE_CODE_MAX_RETRIES`, but the part read on 2026-11-18 did not list the retried status codes. So each
agent is checked by running it once against a fake provider.

1. Run `trove budget probe` in a terminal. It listens on `127.0.0.1:8799` (`--port` to change it), forwards nothing,
   and answers every request with exactly the refusal above.
2. In another terminal, run the agent against it with a placeholder key. No real key is needed and nothing is billed:
   `ANTHROPIC_BASE_URL=http://127.0.0.1:8799/anthropic ANTHROPIC_API_KEY=trove-probe-placeholder claude -p "say hi"`.
   For Codex CLI, set its custom base URL the way `trove setup` does, using `http://127.0.0.1:8799/openai/v1`.
3. Watch what the agent prints. When it has been quiet for 30 seconds (`--quiet`), the probe asks whether the agent
   showed Trove's message and stopped.
4. The verdict is `stops` only if the agent tried exactly once and you answered yes. `retries` and `not_shown` mean
   the cap keeps only warning for that agent. Results go to `spendcap-probe.json` (see PRIVACY.md). A newer result
   replaces the older one, so probe again after an agent upgrade.
5. Restart the gateway. `trove optimize status` names the agents that passed.

Status: built and tested against recorded requests. Evidence from a real agent, 2026-11-18: Claude Code 2.1.327,
pointed at a loopback server answering every request with the Anthropic-shape refusal above, sent exactly one
request, printed `API Error: 402 Trove spend cap reached: about $10.00 of your $10.00 budget is spent this month
(estimated at list prices). Trove did not send this request to the provider. To continue, ...` and exited with
code 1 after 5 seconds. That is the `stops` behaviour. The verdict file is still written only by a person at a
terminal running `trove budget probe`, so plan item 8.1 waits on that one step.

## What was not built, and why

Items 1, 2, 3, 4 and 7 below are answered by the active mode above; they are kept as the record of why it is built
this way.

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
