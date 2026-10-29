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
- `cost_usd` is filled for requests with no cache writes (cache writes stay unpriced until the TTL split
  is implemented).
- `trove gateway status` request count goes up; refused count stays 0 once the API key is active.

## If you get 401 "API-key authentication only"
The gateway logs a reason per refusal in Window A (no credentials in it):
- `bearer_only` / `oauth_beta`: Claude Code is using the claude.ai login. Approve the API key (steps 4-5).
- `no_credential`: no key reached Claude Code; start it from the terminal that has `ANTHROPIC_API_KEY`.
- `mixed_credentials`: two different credentials were sent (check `apiKeyHelper` / `ANTHROPIC_AUTH_TOKEN`).
Press Esc or Ctrl-C to stop Claude Code's retries.

## Reading the rows
No report command exists yet. Query the database read-only, for example with the helper used on
2026-10-29 (a small Go program run from the repo: `go run <path>\main.go C:/tmp/trove-test/trove.db`),
or ask Claude Code/the assistant to read `trove.db` for you. The file holds metadata only, no prompt text.

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
