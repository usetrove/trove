# Fan-out serialisation

Status: **verified, and not recommended to build now.** Nothing is built. This document records what was checked on
2026-11-14, what it found, and what would change the answer.

## The idea (Probe0's version)

When an agent starts several sub-agents at once and they share a prompt prefix, each one misses the cache and each pays
to write the same prefix. Hold all but the first for a moment, until the first has written the entry, and the others
read it instead. Probe0's briefing says it holds siblings for up to 3 seconds. It works only for API-key traffic,
because only the gateway can hold a request (CLAUDE.md rules 3 and 10).

## What was verified

Read from official pages on 2026-11-14 through a page-fetching agent. No page shows a last-updated date. Passages are
marked raw (verbatim page text) or summary (the fetch tool's own summary, not verbatim).

**The mechanism is real (raw).** The prompt-caching page
(https://platform.claude.com/docs/en/build-with-claude/prompt-caching) says: "For concurrent requests, note that a cache
entry only becomes available after the first response starts. If you need cache hits for parallel requests, wait for
the first response before sending subsequent requests." It also says the prefix is cached "once the response starts",
that a cache write is billed at 1.25x the base input price for a 5-minute entry and a read at a tenth or less, and that
caches are isolated by workspace (organization-level on Bedrock and Google). So siblings that start together do not
see each other's entry, and the right moment to release them is **when the first response starts**, not after a fixed
delay.

**Not found in raw text:** whether simultaneous requests with the same prefix each write, and how a request that
is still in flight is billed. The page does not say. The last part of the page (from about character 139,919) was not
read.

**Claude Code already does a version of this (raw).** The Claude Code prompt-caching page
(https://code.claude.com/docs/en/prompt-caching) says: "in a workflow fan-out of same-prefix agents, Claude Code holds
all but the first for up to 5 seconds by default, so their first requests can read the prefix that the first agent
cached." So for workflow fan-outs the client already holds siblings, for longer than Probe0's 3 seconds.

**What it does not cover (raw).** The same page says a subagent "starts its own conversation with its own system
prompt and tool set", its first request "doesn't read the parent's cache, because the two prefixes differ", and a fork
"inherits the parent's system prompt, tools, and conversation history exactly, so its first request reads the parent's
cache". It describes sharing for forks, resumed subagents and workflow fan-outs only. Nothing there says parallel
subagents of the same type, or teammates, share an entry. Sessions run in parallel in the same directory "build
matching prefixes and can read each other's cache".

**Concurrency (mixed).** Background subagents "run concurrently" and a subagent Claude spawns starts in the background
by default (raw). The default limit of 20 at once and `CLAUDE_CODE_MAX_CONCURRENT_SUBAGENTS` are summary only. Agent
teams are suggested at 3 to 5 teammates and use many more tokens (raw).

## What the data showed (one developer's own history)

Method: from Claude Code's own history (token counts, times and model names only), take subagent requests that read
nothing from the cache and wrote at least 1,000 tokens (a subagent's first request, or a rewrite). Group those that
follow one another within 5 seconds in the same session. For each later member of a group, take the smaller of its
write and the first member's write as the most it could have read instead, at list prices (5-minute write price minus
read price, per model).

- Fan-outs do occur: a small number of groups of 2 or 3 subagent cold-starts in a few seconds, across about a quarter of
  the sessions that used subagents.
- The members of a group wrote almost the same number of tokens (for example 19,0xx each, or 4,3xx each), which is what
  siblings of one subagent type sharing a prefix would do. So the premise holds in the data.
- **The money is tiny.** Counting every later sibling as fully shareable, which is an upper bound, the saving over about
  six weeks was well under one percent of that developer's total spend at list prices. Subagent requests in total were a
  few percent of it.
- Each held sibling would have saved a few cents and waited for the first response to start, typically one to several
  seconds. That trades developer time for cents.

Limits: one history, one machine. The history's timestamps may be when a message was logged and not when its request
started, so "within 5 seconds" is approximate. Whether the prefixes were really identical cannot be seen, which is why
the figure is an upper bound. People who run agent teams or large fan-outs would see more.

## Conclusion

- The provider mechanism is verified, and Probe0's idea is sound in principle.
- Claude Code already holds same-prefix siblings in workflow fan-outs, so the largest known case is covered by the
  client.
- On real data the remaining saving is very small and costs every held request some latency.
- So **do not build it now.** Not even the shadow version: it would measure something the history has already
  answered, for this user.

## What would change that

- Evidence from a heavier user (many parallel subagents or agent teams) that fan-outs are a visible share of spend. The
  shadow version would be cheap to build then: it needs the cacheable prefix hash (tools, system and messages up to
  the last cache marker, which the analyzer already compares) and the moment the first sibling's response starts.
- Claude Code dropping or shortening its own hold.
- Raw text from the unread part of the provider page, or a controlled test, showing that simultaneous requests each
  pay for the full write, so the saving per sibling is larger than assumed.

## If it is ever built

- Hold only a request whose cacheable prefix equals one that is in flight and has not started its response, and
  release it when that response starts, with a short cap (a few seconds). Never a fixed delay.
- Fail open: on any doubt, no match or a timeout, the request goes straight through. A held request that waits for
  nothing is a cost with no benefit.
- Shadow first (rule 6): count the requests that would have been held, the time they would have waited, and the
  write tokens they paid that a read would have covered. The saving is an estimate.
- It changes only when a request leaves, never its bytes, so it is cache-safe; but it adds latency, so it needs the
  user's opt-in and a visible cost in the report.
- Like in-flight dedup and the spend cap it is a gateway behavior with no effect on subscription users.
