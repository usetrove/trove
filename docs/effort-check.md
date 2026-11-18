# Effort cache-break check (plan item 7.4)

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

## Manual steps (blocked: needs live API-key sessions)
1. Run API-key sessions through `trove gateway` with an agent or SDK script whose effort changes
   between turns, plus sessions where it does not. Use each model you care about.
2. Run `trove optimize effort-check`.
3. Only for a model whose verdict is keeps: build the tool-turn effort optimizer and run its
   session A/B. Then set item 7.4 to done.
