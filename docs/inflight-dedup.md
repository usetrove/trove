# In-flight dedup

Status: **shadow mode is built. Sharing a call is not.** Setting `inflight_dedup = "on"` runs as shadow today.
The reasons sharing waits, and what must be true before it is built, are below.

## The idea

Parallel agents sometimes send the same request at the same moment: a retry that raced the original, or sibling
subagents given the same prompt. A gateway could make one call to the provider and give every one of them the
answer: one call, one bill. It only works for API-key traffic, because only the gateway can see the requests (CLAUDE.md
rules 3 and 10), so it is not offered to subscription users.

## What exists today (shadow)

It finds the cases and says what sharing would have saved. Nothing is shared and nothing changes: every request still
goes to the provider on its own, and every client gets the provider's own answer.

- **Switch:** `[optimizers] inflight_dedup = "shadow"` in `config.toml` (off by default). `trove optimize status`
  shows it; the kill switch (`trove optimize off`) turns it off.
- **When a request counts as a duplicate.** All of these are equal: the method, path and query; the headers that change
  the answer (`anthropic-version`, `anthropic-beta`, the OpenAI beta and organization headers, content type and
  encoding); the credential; and every byte of the body. And the earlier request has **not yet received the provider's
  response headers**. That is the only window in which a later request could be attached to the same call without
  replaying a stream that has already started.
- **How the credential is compared.** Through an HMAC held in memory for the length of the request, under a key made
  when the gateway starts (`auth.Fingerprinter`). The key itself is never read by anything else, a different key never
  counts as a duplicate, the digest is not written anywhere, and it differs after a restart.
- **How the body is compared.** A fast 64-bit hash narrows the search, then the bytes are compared, so a hash
  collision cannot make two different requests look alike (`TestInflightACollision...`).
- **What is recorded.** On the later request's row, one more entry in the existing `shadow_savings` column:
  `inflight_dedup` with that request's billed tokens (uncached input, cache writes, cache reads and output) and its
  whole cost. Nothing else is stored: no hash, no digest, no prompt text.
- **Where it shows.** `trove report` (Optimizers, as an estimate with a note, and never offered as something to switch
  on) and `trove optimize status`. A report with the setting off and no estimate recorded does not mention it.
- **Cost.** About 55 microseconds per request on a 217 KB body (a benchmark in `inflight_test.go`), two small
  allocations, one short lock, no input or output. The gateway's budget is 5 ms.
- **It cannot fail a request.** Any problem inside it (including a panic) means the request goes on as if it were off.

What the shadow number is, and is not: an **estimate**. It assumes the shared call's answer would have served the
later request as well. It also counts only the later request's bill, because the first call would have been made
either way.

## What was not built, and why

Sharing is not a small addition. Each of these is a real decision, and none has evidence behind it yet.

1. **It changes behavior.** Two requests that would have been sampled separately get one answer. A framework that sends
   the same prompt several times on purpose to get different samples would be silently broken. Most requests carry a
   sampling temperature, so this cannot be filtered out by a flag in the body. It must be opt-in, by agent, with plain
   wording.
2. **Streams must be replayed.** The leader's response is a stream. A follower has to receive it from the first byte,
   which needs a shared buffer in memory with a cap, waiting followers, and per-follower write positions. The rule
   that the gateway never buffers a stream still has to hold for the leader: the leader's bytes go to its own client
   immediately, and the copy for followers is on the side.
3. **The leader's client can leave.** Today a client disconnect cancels the upstream call. With followers attached, the
   call must continue until the last one is gone, which means decoupling the upstream request from the first client's
   context.
4. **Errors.** A 429 or a 5xx given to every waiting client could make a burst fail together where separate calls might
   not. The rule would have to be: on any status of 400 or more, every follower makes its own call (fail open).
5. **Memory.** The shared buffer is bounded by the output size. A join would be allowed only when the request's output
   limit (`max_tokens`) keeps the possible response under a fixed cap.
6. **Bookkeeping.** One upstream bill, several client rows. Followers would record no upstream cost and a marker, so
   spend and receipts stay correct. The saving is the leader's usage, still an estimate.
7. **Session boundaries (rule 5).** Whether a session takes part must be pinned when it starts, like the pipeline's
   optimizers, so it never changes in the middle of one.
8. **Guardrails (rule 6).** It must be measured once on and roll back by itself: the existing outcome checks (errors,
   retries, abandoned sessions) compared between on and off sessions, with `measure.NetGuard`.

## Exit criteria: build sharing only when all of these hold

- Shadow data from at least one real API-key install shows duplicates often enough to matter: a meaningful share of
  requests flagged across several sessions, worth at least a few dollars a month at list prices. If real traffic shows
  almost none, the answer is to not build it.
- It is known what produces the duplicates (a retry, sibling subagents, something else), by agent, from the shadow
  data. This decides whether point 1 is a real risk for those users.
- The wording and the per-agent opt-in for point 1 are decided.
- Tests exist for each item above: stream replay byte for byte, leader disconnect, follower disconnect, an upstream
  error, an over-large output, rapid starts, and a canary showing no credential or prompt text reaches a row or a log.

## What to do now

Run the gateway with `inflight_dedup = "shadow"` for a week of real work, then read `trove report`. The row says what
it would have saved and how many requests were flagged. That number is what decides whether any of the above is worth
building.
