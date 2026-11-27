# Effort cache-break check

Lowering effort on tool turns could save output and thinking tokens. But the provider says that
changing `output_config.effort` between requests "can invalidate" cached message blocks, and on some
models the system and tool caches too. It does not say which models. (Verified 2026-11-17 on
platform.claude.com/docs/en/build-with-claude/effort and /prompt-caching. The effort page also
describes a per-message effort that preserves the cache on some models; Trove does not use it yet.)
So Trove measures this per model before anything changes effort.

`trove optimize effort-check [--days N] [--json]` reads recorded gateway requests. The gateway now
stores each request's effort level (`requests.effort`, one of low, medium, high, xhigh or max, else
empty). The check takes consecutive successful turns of a session, on the same model and less than
4 minutes apart, where the earlier turn had at least 1,024 cached tokens. A turn "kept" the cache
when it read back at least 90% of what the earlier turn read or wrote. Turns where effort changed
are compared with turns where it did not:

- breaks: losses on changed turns are 50 points or more above the control
- keeps: losses on changed turns are at most 10 points above the control
- unclear: anything in between
- insufficient: fewer than 5 changed or 5 control pairs

The counts come from provider-reported usage, so they are labeled measured.

## Result, 2026-11-18 (live API-key sessions)

Fifty-six requests went through a local `trove gateway`, sent by a script that calls the Messages API directly
(not an agent). Per model there were four 7-turn sessions on an ~8.6k-token cached system prompt: two switched
`output_config.effort` between `high` and `low` every turn, and two kept `high`. Each session's system prompt began
with its own nonce, so no session could read another's cache.

| Model | Changed pairs (lost) | Control pairs (lost) | Verdict |
|---|---:|---:|---|
| claude-sonnet-5-5 | 10 (0) | 10 (0) | keeps |
| claude-haiku-5-5 | 10 (0) | 10 (0) | keeps |

Every turn after an effort switch read back the whole earlier prefix. The counts are provider-reported usage, so
they are measured. They cover these two models only, and short turns. Rerun the check before relying on it for
another model.

## The tool-turn effort optimizer (`effort_tool_turns`)

Built after the keeps verdicts above. It is off by default and is a gateway optimizer for API-key traffic only. In
the OpenCode plugin it is shadow-only for any login that is not an API key.

- It acts only on a tool turn, a request whose newest user message holds only tool results. A turn where a person
  is waiting for a reply is never touched.
- It sets `output_config.effort` to `low` only when the request already asks for a higher level. It never adds the
  field and never raises a level.
- It acts only on models listed in `EffortCacheKeeps` (internal/optimize/effort.go), which are the models measured
  above. Every other model is forwarded unchanged.
- It runs right after `model_router`, so a routed request is judged for the model that will serve it.
- Overhead: none (it adds no text). Technique C6, class effort, so T3 in the plan's tiers. It is judged by session
  A/B on cost, test failures and turns, never assumed to save anything.

What is left is the session A/B. That is a benchmark condition (`alone:effort_tool_turns` in bench/), registered in
the components study for a later run.

### Why it does not use per-message effort (checked 2026-11-24)

Per-message effort (beta `mid-conversation-output-config-2026-07-01`,
platform.claude.com/docs/en/build-with-claude/effort-per-message) changes the level without touching the top-level
value, so the cached prefix still matches on every supported model. It cannot serve this optimizer, though: a user
turn that holds only tool results "doesn't count as new input", so a change placed there takes effect only at the
next prompt. `effort_tool_turns` therefore stays on the top-level path for the models measured above. The per-prompt
variant, which per-message effort can do, is `effort_routine_prompts` (B28, see [gateway.md](gateway.md#effort-and-thinking-controls)).
