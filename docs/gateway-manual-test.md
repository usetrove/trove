# Gateway manual test (real Claude Code, your own API key)

Last verified working steps: 2026-10-29 (curl part). The Claude Code part below has not been completed yet.
Cost: a few cents at most on Haiku. Everything here is reversible with `trove undo`.

Use a throwaway data directory so your real `~/.trove` is untouched. These examples use `C:\tmp\trove-test`.

## Before you start
- You need an Anthropic API key in `ANTHROPIC_API_KEY` in the terminal you will start `claude` from.
  Do not paste the key anywhere; set it with `$env:ANTHROPIC_API_KEY = Read-Host "key"` if needed.
- If you are on a Pro/Max subscription, Claude Code keeps using that login until it approves the API key
  (see step 5). The gateway refuses subscription traffic with a 401; that is intended.
- Make sure nothing else is using port 8787 (`Get-NetTCPConnection -LocalPort 8787 -State Listen`).
- Long pasted commands can get line breaks inserted by some terminals. Keep commands short.

## Part A: curl through the gateway (no Claude Code)
1. Window A: `trove gateway start --data-dir C:\tmp\trove-test`
2. Window B: `powershell -ExecutionPolicy Bypass -File C:\tmp\step2.ps1`
   (sends one normal and one streamed Haiku request; needs `C:\tmp\step2.ps1`, `req.json`,
   `req-stream.json`; recreate from the repo notes if they are gone).
3. Expect HTTP 200 for both. The recorded rows must equal the response `usage` exactly.

## Part B: Claude Code
1. Window A: gateway running as above.
2. Apply the config change (answers `y` at the prompt; backs up your file):
   `trove setup --data-dir C:\tmp\trove-test`
   It sets `env.ANTHROPIC_BASE_URL` in `~/.claude/settings.json` to `http://127.0.0.1:8787/anthropic`.
   If the file already holds Anthropic's default URL, it is replaced and remembered for undo.
3. Start `claude` from the terminal that has `ANTHROPIC_API_KEY`.
4. If it asks to use the API key from `ANTHROPIC_API_KEY`, answer yes.
5. If it does not ask (or you declined before): `/config` -> turn on **Use custom API key**, restart `claude`.
6. `/status` should show `Anthropic base URL: http://127.0.0.1:8787/anthropic` and an API key line, and
   NOT a claude.ai login as the active method.
7. `/model` -> Haiku. Send `say hi`, then one prompt that uses a tool (for example `list the files here`).
8. Look at the gateway rows (see "Reading the rows") and check them.

## What to check
- Agent label is `claude_code` with a version; session key is the same across the turns.
- Token counts match what Claude Code used; cache fields become non-zero on later turns.
- `cost_usd` is filled for every request, including ones that wrote to the prompt cache (priced from the
  `cache_creation` 5-minute / 1-hour split). A blank `cost_usd` means the split was missing or inconsistent.
- `trove gateway status` request count goes up; refused count stays 0 once the API key is active.

## If you get 401 "API-key authentication only"
The gateway logs a reason per refusal in Window A (no credentials in it):
- `bearer_only` / `oauth_beta`: Claude Code is using the claude.ai login. Approve the API key (steps 4-5).
- `no_credential`: no key reached Claude Code; start it from the terminal that has `ANTHROPIC_API_KEY`.
- `mixed_credentials`: two different credentials were sent (check `apiKeyHelper` / `ANTHROPIC_AUTH_TOKEN`).
Press Esc or Ctrl-C to stop Claude Code's retries.

## Reading the rows
Run `trove report --data-dir C:\tmp\trove-test` for the summary. To inspect raw rows, query the database
read-only (`sqlite3 C:\tmp\trove-test\trove.db`). The file holds metadata only, no prompt text.

## Turn it off
1. `trove undo --data-dir C:\tmp\trove-test` (restores `~/.claude/settings.json`).
2. In Claude Code, `/config` -> turn **Use custom API key** off if you want your Pro login back.
3. Ctrl-C the gateway in Window A. `Remove-Item Env:ANTHROPIC_API_KEY` in terminals you set it in.

## Not covered here
- Codex CLI (needs an OpenAI API key; `trove setup` configures it only if Codex is installed).
- `trove gateway start --background` (macOS/Linux only; untested on Windows).

## Part C: request analysis (estimates, findings, shadow savings)
Not yet run against real traffic; the automated tests use synthetic sessions only. Design and limits: `docs/analyze.md`.

1. Do Part B with a few turns of real work. Use tools, re-read a file, run a noisy command (a build or test run) and keep
   going for 6+ turns so the session-level detectors can fire. A longer session is more useful than a short one.
2. Read the three analysis columns of the newest rows (read-only):
   `sqlite3 C:\tmp\trove-test\trove.db "select id, segment_token_estimates, findings, shadow_savings from requests order by id desc limit 5"`
   (or the same `go run` helper as above; any SQLite viewer works).
3. Check:
   - `segment_token_estimates.categories` add up to the request's input tokens (uncached + cache write + cache read),
     and `calibrated` is `true`.
   - `findings` entries all say `"label":"estimated"`. Look for `cache_miss` with a `reason`, `duplicate_tool_result`,
     `noisy_output`, `context_file_weight` (if you have a CLAUDE.md), `unused_tool_schemas` (after 5 requests).
   - A `cache_miss` with `volatile_content` shows only a category (`date`, `path`, `id`) and a position, never the text.
   - `shadow_savings` lists optimizer names with tokens and usd. Nothing was changed; this is what they would have saved.
4. Privacy spot check: search the database for a distinctive phrase you typed or a file path from your project.
   `findstr /m "your-phrase" C:\tmp\trove-test\trove.db*` must find nothing.
5. Tell us about anything that looks wrong: a miss reason that does not match what happened, a detector that fires
   on clean output, or categories that look far off (for example context files that are not context files).

## Part D: cloud backends and OpenAI-compatible servers (not yet run)
These routes are tested only against fake servers. Each step needs your own credentials and costs real (small) money.
Use a throwaway data directory and set `TROVE_DATA_DIR` for every command.

1. **Refusals first (free).** With the gateway running, send a request with `anthropic-beta: oauth-2025-04-20` to
   `/anthropic/v1/messages`, to `/vertex/...`, `/foundry/v1/messages`, `/bedrock/model/x/invoke` and
   `/compat/<name>/chat/completions`. Each must return 401 with "API-key authentication only" and "Trove plugin".
2. **Vertex.** Set `upstreams.vertex`, start Claude Code with `CLAUDE_CODE_USE_VERTEX=1` and
   `ANTHROPIC_VERTEX_BASE_URL=http://127.0.0.1:8787/vertex`. Check rows have `provider = vertex`, real token counts,
   and an empty `cost_usd`. Check streaming still streams.
3. **Foundry.** Same with `upstreams.foundry = https://<resource>.services.ai.azure.com/anthropic`,
   `CLAUDE_CODE_USE_FOUNDRY=1`, `ANTHROPIC_FOUNDRY_BASE_URL=http://127.0.0.1:8787/foundry`.
4. **Bedrock with an API key.** `AWS_BEARER_TOKEN_BEDROCK` set, `upstreams.bedrock` to your regional runtime origin,
   `ANTHROPIC_BEDROCK_BASE_URL=http://127.0.0.1:8787/bedrock`. Streamed rows must show output tokens. If they show
   `stream_incomplete`, the event stream framing assumption in `usage/eventstream.go` is wrong: save a redacted
   capture and tell us.
5. **Bedrock with AWS credentials (SigV4).** Expect a 401 with the SigV4 explanation. If you want to settle the open
   question in `docs/compliance.md`, also send the same request straight to AWS with its Host rewritten and note
   whether AWS rejects it.
6. **Compatible server.** Add a `[[upstreams.compat]]` entry (see `docs/gateway.md`), point Codex CLI or `curl`
   at `/compat/<name>/...`, send a streamed request with `"stream_options":{"include_usage":true}`, then run
   `trove report`. Check latency, time to first byte, tokens per second, and, if configured, the custom-rate cost
   and the GPU-hour estimate against a hand calculation.
