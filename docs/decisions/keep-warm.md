# Decision: keep-warm pings

Status: APPROVED 2026-11-17. The rule below has been added to PRINCIPLES.md (rules 1(d), 2 and 7).

## What the feature does

When an API-key gateway session goes idle, Trove may send a small request that re-reads the session's cached prompt prefix so the provider cache does not expire before the user's next turn. A warm-up read costs about 0.1x the cached prefix; a cold rewrite costs about 1.25x (5-minute lifetime). So a ping pays off when the chance the user comes back before expiry is better than about 1 in 12.

These multipliers, and whether a cache read refreshes the cache lifetime, are provider details. They must be checked against current official documentation before the feature ships (rule 8), and the check recorded here with source and date.

## Why it needs a sign-off

It goes beyond four existing rules:

1. Rule 1, outbound calls. A ping is a request the user's agent did not make. Rule 1 only allows forwarding the user's own requests.
2. Rule 2, credentials. To send a ping later, the gateway must keep the API key in memory after the user's request has finished. Today keys only pass through.
3. Rule 7, metadata only. The gateway must keep the last request prefix (prompt text, code, tool output) in memory between requests. It is never written to disk, but it outlives the request.
4. Spending. Each ping is billed to the user's key without their agent asking for it.

## Decision (proposed)

Allowed, under all of these conditions:

Scope
- API-key gateway traffic only. Never for subscription or OAuth traffic, never through hooks or the plugin, never for a request the gateway rejected.
- Off by default. Enabled only per user, by an explicit command, and only after a shadow run (estimate only, no pings sent) has shown its estimated net saving to the user, labeled "estimated".
- Switched on or off only at session boundaries (rule 5).

What is held, where, for how long
- The API key and the last request prefix are held in process memory only. Never written to the database, logs, crash output, files or swap-backed caches that Trove controls.
- One held copy per active session. Dropped at the earliest of: the cache lifetime expiring, the ping cap being reached, the session ending, the gateway stopping, or the user disabling the feature.
- Held data is redacted from every error, panic and debug output path, like auth headers today.
- When keep-warm is off, the gateway holds no key after a request ends.
- Go cannot reliably wipe memory (the runtime may copy strings), so Trove does not promise that held data is zeroed, only that it is never persisted, logged or printed and that Trove drops every reference to it.

What a ping is
- Sent only to the same provider endpoint the user's own request went to, with the same key, model and byte-identical prefix. Same headers the agent sent, minus nothing, plus nothing that changes client identity (rule 3).
- Asks for the smallest possible output. Its token usage is recorded as metadata and charged to the optimizer (rule 15).
- Never in the request path of the user's own traffic. A failed ping is logged as metadata and ignored; it never retries in a loop and never blocks or alters a user request (rule 4).

Limits
- 5-minute cache lifetime only. Prefixes using the 1-hour lifetime are never pinged.
- At most N pings per idle period per session: default N = 2 (covers idle gaps up to about 15 minutes), hard ceiling N = 6 (about 30 minutes). Past that, the 1-hour cache lifetime (items 4.1, 4.2) is cheaper than pinging. There is no separate idle-time limit; it follows from N.
- Spend guardrail: pings switch off automatically if their measured cost over a rolling 24 hours exceeds the measured savings they produced over the same window.
- Fixed backstop: at most $1 of ping spend per day, user-configurable downward only; reaching it disables pings until the next day.
- Guardrail: if measured cache reads on the user's next real request do not follow pings at the predicted rate, the optimizer rolls back automatically (rule 6).

Documentation before shipping
- PRIVACY.md: what is held in memory, for how long, and what is sent.
- docs/threat-model.md: a new asset (in-memory key and prefix after the request) and its exposure to A1 (other local processes) and crash dumps.
- `trove contribute` and every other channel unchanged: pings add nothing to them.

## Rule text (now in PRINCIPLES.md)

Add to the list in rule 1, after (c):

> (d) keep-warm pings: when the user has enabled the keep-warm optimizer for API-key gateway traffic, the gateway may resend the session's last cached prefix, byte-identical, to the same provider endpoint with the same key, within the limits and conditions in docs/decisions/keep-warm.md. Pings are never sent for subscription or OAuth traffic, never in the request path, and never after the session ends.

Add to rule 2, at the end:

> The only exception is the keep-warm optimizer, which may hold a key in process memory for the lifetime of one idle session, as described in docs/decisions/keep-warm.md; it is still never logged, stored, printed or persisted.

Add to rule 7, at the end:

> The keep-warm optimizer may also hold one session's last request prefix in process memory between requests, never on disk, as described in docs/decisions/keep-warm.md.

## Questions for the approver, with recommended answers

1. Is holding the key in memory between requests acceptable at all?
   Recommended: yes, with the conditions above. The gateway already holds the key for the length of a streaming request, which can run for minutes; this extends that by minutes, not hours. A process able to read the gateway's memory almost certainly runs as the same user and can already read wherever the key lives (environment, shell profile, agent config).
2. Values for N, M and the daily spend cap.
   Recommended: N = 2 by default, ceiling 6; no separate M; a savings-based guardrail plus a $1/day backstop. The $1 figure is arbitrary and only bounds the worst case.
3. Should pings be allowed for the 1-hour cache lifetime, or only the 5-minute one?
   Recommended: 5-minute only. Long gaps are already covered by the 1-hour lifetime, and pinging it would mean holding the key and prefix for hours. Revisit with shadow data on how often users return after an hour or more.

The cost multipliers behind these answers (about 0.1x per cache read, 1.25x and 2x for 5-minute and 1-hour cache writes) are unverified. If the provider documentation differs, re-check answers 2 and 3.

## Sign-off

- Approved by: project owner
- Date: 2026-11-17
- Answers to the open questions: the recommended answers above, unchanged.
- Provider documentation checked (source, date): platform.claude.com/docs/en/build-with-claude/prompt-caching and platform.claude.com/docs/en/about-claude/pricing, 2026-11-17. Confirmed: a read restarts the 5-minute lifetime, counted from the request's start; 5-minute writes 1.25x, 1-hour writes 2x, reads 0.1x (0.05x on Opus 5.5 and Sonnet 5.5, 0.025x on Fable 5.x, which makes pings cheaper than the answers above assumed); `max_tokens: 0` is the documented pre-warm request and bills no output, and is refused with streaming, extended thinking, structured outputs or a forced tool choice. Live check 2026-11-17 (curl through the gateway, claude-haiku-5-5, 3,723-token cached system prompt, 5-minute lifetime): the first request wrote 3,723 cache tokens; two keep_warm pings at about 4.5 and 9 minutes each read 3,723 tokens with 0 output ($0.0000383 each); the same request sent 10.5 minutes after the first read all 3,723 tokens and wrote none. A request with adaptive thinking was also pinged: the provider accepted it (200) and it read the cache. Not yet checked with a real agent session (Claude Code or a custom agent) or on other models.
