# model_router: step down to a cheaper model on tool turns

`model_router` is a gateway optimizer (technique C8, tier T3 routing, off by default). For API-key
traffic only, it sends tool turns to a cheaper model named in `optimize.route_to`. It then keeps the
session on that model for at least k turns before switching back.

## Why k turns
Each model has its own cache. Trove assumes a model cannot read another model's cache. The
caching docs do not say this either way, and assuming it only makes Trove more cautious. So every
switch writes the whole prompt into the new model's cache. The switch repays itself only once
cheaper cache reads have covered that write:

    k >= (W_to - R_to) / (R_from - R_to)

W is the 5-minute cache-write price and R the cache-read price, per million tokens, both from
Trove's pricing table at the moment the session was pinned. With the documented multipliers
(write 1.25x input, read 0.1x) this is 11.5·r/(1−r), where r is the price ratio: about 6 turns at
one third of the price and about 17 at 60%. Savings on output and new input are left out, which
can only make k larger.

Verified 2026-11-17 on platform.claude.com/docs/en/build-with-claude/prompt-caching: 5-minute
write 1.25x, 1-hour write 2x, read 0.1x (0.05x on Opus 5.5 and Sonnet 5.5, 0.025x on Fable 5.2).

## Rules
- The decision is a pure function of the request history and the session's pin (`route_to` and
  the prices at pin time). The same history always goes to the same model.
- A tool turn is a user message made only of tool results. A typed turn returns the session to
  the original model only after k turns.
- No routing when either model is unpriced, when the target is not cheaper, or when the target is
  the request's own model.
- In the OpenCode plugin it is "on" only for API-key logins. Copilot and unknown logins get shadow.
- It removes no tokens, so it never claims tokens removed. Its cost is the cache write.

## Shadow
With `[optimizers] model_router = "shadow"` and `optimize.route_to = "<model>"`, each request's
`optimizations_applied` carries a `route` object: from, to, the model the request would go to,
`price_ratio`, `sticky_turns`, `switch`, `prompt_tokens_estimated` and
`cache_write_usd_estimated` (the predicted cost of the switch), all labeled estimated.

## Manual steps (blocked: needs live API-key sessions)
1. Set `optimize.route_to` to a cheaper same-provider model, and set model_router to `shadow`.
2. Run at least 20 real API-key sessions through `trove gateway`, then read the `route` estimates.
3. Turn on the A/B (`[experiment] enabled = true`) and set model_router to `on`. The T3 guardrails
   (test_fail_rate, turn inflation, spend) roll it back if they trip.
4. Record the verdict from `trove report` and set item 7.2 to done in the plan index.
