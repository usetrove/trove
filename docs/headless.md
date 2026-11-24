# Headless jobs on your plan's API credits (`trove headless`)

Added 2026-11-24. Max, Ultra and Team plans include monthly API credits. Jobs that run `claude -p` or the Agent SDK
with an API key from your linked Console organisation use those credits instead of your plan's limits. This covers
CI, cron and scripts. `trove headless` shows how to run those jobs through Trove's gateway. Your interactive sessions
are left alone.

## What was verified, and where

All read on 2026-11-24. No page showed a last-updated date.

| Fact | Source |
|---|---|
| Max gets $100 a month and Ultra $200. Team gets $20 per Standard seat and $100 per Premium seat, pooled and capped at $500. Free, Pro and Enterprise plans aren't eligible. You claim the credits after seven days on the plan, for one Console organisation linked on claude.ai. They don't roll over. | support.claude.com/en/articles/17154008 |
| "API credits cover `claude -p` and the Claude Agent SDK when you run them yourself with an API key from your linked Claude Console organization." Signed in with the plan instead, those runs "still draw from your plan's usage limits". | same |
| They don't cover interactive Claude Code (terminal, IDE, desktop or web) or extra usage. "Runs started by the Claude Code GitHub Action, an IDE extension or the Claude desktop app count as Claude Code usage, so API credits don't cover them, even with `-p`." | same |
| When they run out: purchased credits or auto-reload carry on. "If the organization has no other credits, API requests stop until your next monthly credits arrive. Usage is never charged to your Claude plan." Organisations invoiced through sales are billed as usual. | same |
| In `-p` mode, `ANTHROPIC_API_KEY` "is always used when present", ahead of a subscription login. | code.claude.com/docs/en/iam |
| `--bare` "never reads OAuth credentials or the system keychain". It also skips hooks, skills, plugins, MCP servers, auto memory and CLAUDE.md. It is "the recommended mode for scripted and SDK calls". | code.claude.com/docs/en/headless |
| Install: `curl -fsSL https://claude.ai/install.sh \| bash`. | code.claude.com/docs/en/setup |
| `ANTHROPIC_BASE_URL` routes requests through a gateway. A gateway credential (here the API key) replaces the subscription login for those requests. | code.claude.com/docs/en/env-vars, /llm-gateway |

## The trade-off

`trove headless` prints the trade-off in two lines:
- Those jobs stop using your plan's limits.
- They stop when the credits run out, unless your organisation has bought credits or turned on auto-reload.
- `--bare` skips your hooks, plugins, MCP servers and CLAUDE.md. Pass `--append-system-prompt-file` or `--settings`
  if a job needs them.

## Steps

1. Link a Console organisation to your plan on claude.ai (Settings > Billing) and claim the credits.
2. Create an API key in that organisation and keep it in your secret store.
3. On this machine (cron, scripts):
   ```sh
   trove gateway start --background
   trove headless env >> ~/.profile   # or paste it into the job's script
   ```
   Give the job the key as `HEADLESS_API_KEY`, then run `headless_job "..." --allowedTools "Read"`.
   `ANTHROPIC_BASE_URL` and `ANTHROPIC_API_KEY` are set for that one command only. Your interactive sessions never
   see them.
4. In CI: `trove headless ci` prints a GitHub Actions workflow. Store the key as the repository secret
   `HEADLESS_API_KEY`. The workflow:
   - installs Trove and Claude Code;
   - starts the gateway on the runner and waits for `/trove/health`;
   - runs `claude --bare -p` in a plain shell step.

   It deliberately does not use the Claude Code GitHub Action, whose runs the credits don't cover.

`--port` changes the gateway port in both snippets (default `gateway.port`). `--json` returns the trade-off, the
base URL, the snippets and the sources.

## What Trove does and does not do

- It only prints text. It never reads, stores or moves an API key, an OAuth token or a login (PRINCIPLES.md rules 2,
  3 and 10). The snippets read the key from a variable you fill.
- It never changes Claude Code's settings or your shell profile.
- The gateway still rejects anything that is not API-key traffic. `--bare` ensures the job never sends your login.
- Gateway optimizers that are proven for API-key traffic (for example `model_router`) apply to these jobs under
  their usual policy and pins. Nothing new is switched on for them.

## How much of your week they use

`trove report` adds a "Headless and SDK runs" section when the last 7 days have any. It shows:
- sessions and requests (measured);
- their cost at API prices (estimated);
- their share of Claude Code's use in that time (estimated);
- with a plan-limit fit, their share of your weekly limit (estimated from API prices).

Which sessions are headless comes from the `entrypoint` Claude Code writes to its history. That field is **observed,
not documented**: `cli` (interactive) and `sdk-cli` (print mode) were seen, and every `sdk-` value counts as
headless. Runs that already use an API key are not on your plan's limits, so the share is an upper bound.
