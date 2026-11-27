# Sibling stagger (hook version)

Status: **shadow only. No active mode is proposed**, because the data shows no clear saving. Analysis dated
2026-11-24.

## The question

When an agent starts several subagents of one type at once (for example three Explore agents in one message), each
sibling's first request writes the same prefix (that agent type's system prompt and tools) to the cache. None of them
can read another's entry, because an entry only becomes readable once the first response starts. If a PreToolUse hook
on the Agent tool held the later siblings for a few seconds, would they read one shared entry instead of each writing
their own, and is that worth the wait?

A gateway could do this only for API-key traffic. A hook also works for subscription users, because it only delays a
documented hook's reply; it reads and changes no traffic.

## What was verified

- **Provider (raw text, read 2026-11-14):** the prompt-caching page
  (https://platform.claude.com/docs/en/build-with-claude/prompt-caching) says "a cache entry only becomes available
  after the first response starts. If you need cache hits for parallel requests, wait for the first response before
  sending subsequent requests."
- **Claude Code (raw text, read 2026-11-24 from https://code.claude.com/docs/en/prompt-caching):** "in a workflow
  fan-out of same-prefix agents, Claude Code holds all but the first for up to 5 seconds by default". Also: "A subagent
  starts its own conversation with its own system prompt and tool set". The page says nothing about parallel subagents
  of one type sharing an entry, and nothing about where a subagent's cache breakpoints are.
- **Hooks (raw text, read 2026-11-24 from https://code.claude.com/docs/en/hooks.md):** a command hook's default
  `timeout` is 600 seconds. On PreToolUse "a timed-out `command` ... hook doesn't block the tool call", so a held call
  fails open. PreToolUse input includes `tool_use_id`. **Not stated:** whether the PreToolUse hooks of parallel tool
  calls run at the same time or one after another. That decides whether a sleeping hook on sibling 2 also delays
  sibling 3. About 74k characters at the end of hooks.md were not read.

## What was built (shadow only)

- The existing PreToolUse hook on `Agent|Task` (`pre_tool_subagent`, which Trove installs with a 5-second timeout) now
  also records each call: session hash, hashed `tool_use_id`, time, the agent type label (Trove's and built-in names as
  they are, the user's own agent types as `user`), and a hash of the raw type. These go to a new table,
  `subagent_spawns`. The prompt and description are never stored. The hook still prints nothing and holds nothing.
- `trove tasks stagger [--json]` reads what was recorded. It groups same-type spawns in one session that come within
  2 s of each other. Each spawn is matched to its SubagentStart run (`subagent_runs`), and each run to its first request
  in the history. For each later sibling it reports, as measured, whether that request read or wrote the prefix. Then
  it reports what a hold capped at 3 s would have done: how many siblings the first sibling's reply would have reached
  within the cap, the wait, and the saving. The saving is labeled estimated and is an upper bound: the smaller of the
  two first writes, priced as a read instead of a write, at list prices.

## What the data showed (one developer's own Claude Code history)

The same rules were applied to the Claude Code transcripts on this machine, reading token counts, times and agent
types only. The history runs from 2026-10-25 to 2026-11-25 (UTC): 128 subagent runs in 57 sessions, and $614.83 of
spend at list prices, of which $17.84 was subagents.

- **The waste is real, and it is measured.** 24 sibling groups had 33 later siblings (types: Explore, web-fetch,
  general-purpose). **None of the 33 read the cache on its first request; all of them wrote it**, for 386,913 write
  tokens in total. Siblings start 50 to 200 ms apart, rarely up to 1.8 s.
- **Holding would work.** When a later run of the same type happened to start after an earlier run's first reply,
  it read the shared part: web-fetch runs read 2,830 tokens of about 4,400 (6 of 7 such pairs). So the shared prefix
  carries a cache breakpoint. For Explore no such pair exists in this history, so its shared share is not measured.
- **The money is tiny, at any reasonable cap.** The first sibling's first reply is logged 0.6 to 28 s after a later
  sibling starts. That is an upper bound on the wait, because the entry is readable from when the response starts.

| Hold cap | Later siblings reached (of 33) | Median wait | Saving, upper bound (estimated) | Share of spend |
|---|---|---|---|---|
| 3 s | 11 | 2.2 s | $0.10 | 0.02% |
| 5 s | 26 | 3.3 s | $0.42 | 0.07% |
| 10 s | 32 | 3.5 s | $0.70 | 0.11% |

Most of it is Explore on Sonnet (about 19,000 write tokens per sibling, at $2.50 per million to write against $0.10
to read). web-fetch runs on Haiku, where the whole difference is $0.115 per million.

Limits: one history and one machine. Transcript times are when a line was logged, not when its request started. The
upper bound counts each sibling's whole first write as shareable, but only the system prompt and tools are shared, not
the task prompt.

## Conclusion

No clear saving. Over a month, a 3 s hold would have saved at most about ten cents and made 11 subagents wait about
two seconds each. Even a 10 s hold stays under a dollar. So **an active mode is not proposed.** The recorder stays in
shadow so the question can be asked again on heavier histories.

## What would change that

- `trove tasks stagger` on a user who runs large fan-outs of one type (many Explore or custom agents at once on an
  expensive model) shows a saving that is a visible share of their spend.
- Claude Code changes how it starts parallel subagents, or drops its own hold for workflow fan-outs.

If that happens, these must be settled before any active mode is designed:

- Whether parallel PreToolUse hooks run at the same time (not stated in the docs). This can be measured from the
  recorded spawn times: if sibling hooks never overlap, they run one after another.
- A wait that ends when the first sibling's response starts, not a fixed sleep. A hook can't see that moment directly.
- A cap of a few seconds, well under the hook's 5 s timeout, so the hook always fails open.
