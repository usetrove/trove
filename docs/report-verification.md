# Report, budget, receipts, status line and share: verification checklist

Written 2026-10-29. What the code does is in `docs/report.md`. This file is how to check it
yourself: first what the automated tests cover, then what needs a human or real traffic. Tick items
off and note the date. Nothing in sections 3 to 9 has been run by a person yet. Automated tests were
run on Windows 11 only, and the end-to-end smoke test (section 2) was run once on Windows with a fake
upstream.

## 0. Read this first: never run Trove against your real `~/.trove`

`C:\Users\jacly\.trove` is in use by an older Trove product (a ~2 GB `trove.db` with a different
schema). On 2026-10-29 a test run opened it by accident: it switched the file to WAL mode and added an
empty `schema_migrations` table. No other table was changed, but do not repeat it. For every manual step
below, point Trove at a throwaway directory first:

```bash
# bash (Git Bash, macOS, Linux)
export TROVE_DATA_DIR="$HOME/trove-verify"
export TROVE_GATEWAY_PORT=18787
export TROVE_UPSTREAMS_ANTHROPIC=http://127.0.0.1:9911
```

```powershell
# PowerShell
$env:TROVE_DATA_DIR = "$HOME\trove-verify"
$env:TROVE_GATEWAY_PORT = "18787"
$env:TROVE_UPSTREAMS_ANTHROPIC = "http://127.0.0.1:9911"
```

Delete the directory between runs for a clean start. Automated tests are safe: the CLI test harness
sets `TROVE_DATA_DIR` into a temp folder.

- [ ] Optional: check your older Trove tool still works. `~/.trove/trove.db` also has `trove.db-wal`
  and `trove.db-shm` files left by a read-only inspection. They are harmless. The empty
  `schema_migrations` table can be dropped with nothing else using the file:
  `sqlite3 ~/.trove/trove.db "DROP TABLE schema_migrations"`. Only do that if you want it gone.

## 1. Automated tests (run these first)

```bash
go build ./... && go vet ./... && gofmt -l .
go test -count=1 ./...
```

`gofmt -l .` should print nothing. `-count=1` avoids cached results.

| Area | Package and tests | What it proves |
|---|---|---|
| Report numbers and labels | `internal/report`: `TestReportMatchesMeasurementLayer`, `TestEveryFigureIsLabeled`, `TestReportGoldens` | Every figure equals what `internal/measure` computes; every JSON figure has a `measured` or `estimated` label, in all seven scenarios (empty, quiet, small, normal, narrow, baseline, rolled back) |
| Pacing and colors | `pacing_test.go` | Status for 8 cases, projected cap date, amber only when ahead, red only near or over the cap, no color when not asked |
| Rounding | `format_test.go` | `$0.45`, `$612`, `$1,500`, `<$0.01`, `<1%`, `1.6M` |
| Receipts | `internal/measure/receipts_test.go`, `internal/report/receipts_test.go`, `internal/cli/receipts_test.go` | Idle boundary, no duplicates, failed calls ignored, unpriced counted, 7-day horizon |
| Status line | `internal/report/statusline_test.go`, `internal/cli/statusline_test.go` | Summary equals the measurement layer; session found from `session_id`; 50,000 requests, 50 runs, p95 under 50 ms; never exits non-zero |
| Share card | `internal/report/share/share_test.go`, `internal/cli/share_test.go` | Fake repo, file, prompt, agent and model names never reach the SVG, PNG or card text; text fits; both themes; PNG has no metadata; preview and consent |
| Setup | `internal/cli/setup_statusline_test.go`, `internal/setup/statusline_test.go` | Status line is its own consent, never replaces yours, undo restores the file byte for byte |
| Budget config | `internal/config/budget_test.go` | Only two lines of `config.toml` change |
| Daily summary | `internal/store/daily_test.go` | `usage_daily` follows requests in the same transaction |

Golden files live in `internal/report/testdata` and `internal/report/share/testdata`. When you change
wording or layout on purpose:

```bash
go test ./internal/report ./internal/report/share -update
git diff internal/report/testdata internal/report/share/testdata   # read it before accepting
```

- [ ] `go test -count=1 ./...` passes on macOS or Linux too (not yet run there; the status-line
  timing test and the notifier are the likeliest to differ).

## 2. A local gateway with a fake upstream (used by the sections below)

This sends real HTTP through the real gateway to a stand-in provider. No real API key or provider is
used. The key below is fake.

1. Save this as `fake_upstream.py` somewhere outside the repo and run `python fake_upstream.py`:

   ```python
   import json
   from http.server import BaseHTTPRequestHandler, HTTPServer

   class H(BaseHTTPRequestHandler):
       def do_POST(self):
           self.rfile.read(int(self.headers.get("content-length", 0)))
           body = json.dumps({
               "id": "msg_test", "type": "message", "role": "assistant", "model": "claude-sonnet-4-5",
               "content": [{"type": "text", "text": "hi"}], "stop_reason": "end_turn",
               "usage": {"input_tokens": 1000, "output_tokens": 50,
                         "cache_read_input_tokens": 4000, "cache_creation_input_tokens": 0}}).encode()
           self.send_response(200)
           self.send_header("content-type", "application/json")
           self.send_header("content-length", str(len(body)))
           self.end_headers()
           self.wfile.write(body)
       def log_message(self, *a): pass

   HTTPServer(("127.0.0.1", 9911), H).serve_forever()
   ```

2. With the environment from section 0 set, build and start the gateway in another terminal:

   ```bash
   go build -o trove ./cmd/trove      # trove.exe on Windows
   ./trove gateway start
   ```

3. Send six requests (the session header makes them one session):

   ```bash
   for i in 1 2 3 4 5 6; do curl -s -o /dev/null -w "%{http_code} " -X POST \
     http://127.0.0.1:18787/anthropic/v1/messages \
     -H "x-api-key: sk-ant-fake-test-key" -H "anthropic-version: 2023-06-01" \
     -H "content-type: application/json" -H "x-claude-code-session-id: demo-session-1" \
     -d '{"model":"claude-sonnet-4-5","max_tokens":64,"messages":[{"role":"user","content":"hello"}]}'; done
   ```

   - [ ] All six return 200.
4. Run `./trove`. Expected on 2026-10-29: "Only 6 requests so far", spend about `~$0.03`, tokens
   `30K (measured)`, one leak ("Prompt cache misses"), baseline "day 1 of 7 and 1 of 20 sessions".
   - [ ] Matches. (If the date or the price table changed, the dollar amount may differ slightly:
     6 x (1,000 x input price + 4,000 x cache-read price + 50 x output price). Check it by hand.)
5. Stop with `./trove gateway stop` and stop the Python server.

## 3. `trove` and `trove report`

With the data from section 2:

- [ ] `./trove` and `./trove report` print the same thing. Sections appear in this order: Headline,
  Spend by agent and model, Biggest leaks, Optimizers, Recommendations.
- [ ] Every dollar figure has `~` and the word "estimated" nearby. Token counts say "(measured)".
- [ ] `./trove report --today`, `--week`, `--month`, `--since 2026-10-01` each change the period line.
  `--today --week` is refused with "choose one".
- [ ] `./trove report --json | jq .` is valid JSON. Run
  `./trove report --json | jq '[.. | objects | select(has("value")) | .label] | unique'`:
  it prints only `["estimated"]` and `["measured"]`.
- [ ] Do the arithmetic once by hand: spend in the report equals the sum of
  `SELECT SUM(cost_usd) FROM requests` in `$TROVE_DATA_DIR/trove.db`
  (`sqlite3 "$TROVE_DATA_DIR/trove.db"`).
- [ ] Empty state: `TROVE_DATA_DIR=$HOME/trove-empty ./trove` explains `trove setup` and
  `trove gateway`, shows no zeros, and does not create `$HOME/trove-empty`.
- [ ] Narrow terminal: `COLUMNS=40 ./trove` wraps and drops the token column; nothing is cut off
  mid-word. Try the real terminal at about 50 columns too.
- [ ] Color: on a real terminal the report has no color when nothing is wrong. `NO_COLOR=1 ./trove`
  and `./trove --no-color` never print an escape sequence (`./trove | cat -v` shows none).
- [ ] With real data: do the top leaks look plausible? Is "waste found" believable next to spend? It is
  a lower bound taken from analyzer estimates, so a number above 100% of spend means a bad estimate or a bug (nothing in the code caps it, and
  a test fixture with inflated findings did show 2500%).
- [ ] Baseline text: while in the baseline, a leak that an optimizer could fix says nothing is
  offered until the baseline ends. After 7 days AND 20 sessions (see
  `docs/measurement-verification.md` section 2 for how to age the install time), it shows the exact
  `name = "on"` line to add.

## 4. Budget

- [ ] `./trove budget` with no budget says how to set one.
- [ ] `./trove budget set 1500 --period month` prints the new pacing, and `config.toml` now has
  `period = "monthly"` and `cap_usd = 1500.0`. `diff` the file against a copy taken before: only those
  two lines changed, comments intact.
- [ ] `./trove budget set '$1,500' --period week`, `--period day` and `set 0` work; `set lots`,
  `set -5` and `--period year` are refused with a plain message.
- [ ] Pacing sanity: spend ÷ cap is "used"; days passed ÷ days in period is "gone". Check against
  the calendar (weeks start Monday, months on the 1st, days at local midnight).
- [ ] To see each status, set a tiny cap and watch the words change: cap far above spend = "Under pace"
  or "On pace"; cap a little above projected spend = "Ahead of pace" and a projected date; cap at
  80% of spend = "Close to the cap"; cap below spend = "The cap is reached".
- [ ] On a real terminal, "Ahead of pace" is amber and "Close to the cap" or "reached" is red. Nothing
  else is colored.
- [ ] `TROVE_BUDGET_CAP_USD=5 ./trove budget` overrides the file for that run.

## 5. Receipts

A session ends when it has been idle longer than `gateway.idle_minutes` (default 30). To avoid
waiting, run the gateway with `TROVE_GATEWAY_IDLE_MINUTES=1`.

- [ ] Right after the six requests, `./trove receipts` says "No finished sessions yet".
- [ ] Two minutes later it lists one session: agent, model, `6 calls`, cost matching the report,
  cache hits `80% (measured)` (4,000 of 5,000 input tokens), no saving.
- [ ] Running it twice does not add a second receipt (`SELECT COUNT(*) FROM receipts`).
- [ ] `./trove receipts --json` has `cost_usd.label = "estimated"` and `cache_hit_percent.label =
  "measured"`. `--limit 0` is refused.
- [ ] Start a second session (a different `x-claude-code-session-id`), wait for the gateway's
  5-minute sweep, and check the receipt appears **without** running `trove receipts` first.
- [ ] Notifications: set `[notifications] desktop = true` (or `TROVE_NOTIFICATIONS_DESKTOP=true`),
  restart the gateway, finish a session. A desktop notification appears on macOS and Linux and not
  before the session ends. Not implemented on Windows. Default is off: with the setting off, no
  notification ever appears.
- [ ] Receipts older than 7 days are not created for old sessions on a first run.

## 6. Status line

Standalone:

- [ ] `./trove status-line` prints one short line, for example
  `~$0.03 this month` or, with a budget, `~$0.03 / $1.00 · 3% used · 49% of week · under pace`.
- [ ] With Claude Code's input on stdin the session appears:
  `echo '{"session_id":"demo-session-1"}' | ./trove status-line --format claude` ends with
  `· session ~$0.03`. A different or missing id shows no session segment.
- [ ] `--format tmux` uses `#[fg=yellow]` or `#[fg=red]` only for "ahead of pace", "near cap" and
  "over cap". `--format shell` never has color. `NO_COLOR=1` removes it everywhere.
- [ ] Broken config (`TROVE_GATEWAY_PORT=banana ./trove status-line`) prints a hint and exits 0:
  `echo $?` is `0`.
- [ ] No database yet prints `Trove: no usage yet` and creates nothing.
- [ ] `--json` has a `line` and a `status` with labeled figures.

Timing (the budget is under 50 ms):

- [ ] Automated: `go test ./internal/cli -run StatusLineReadsClaudeCodeInputAndIsFast -v` logs the
  median and p95. 2026-10-29 on Windows 11: about 7 ms and 11 ms.
- [ ] Real process, PowerShell:

  ```powershell
  1..20 | ForEach-Object { (Measure-Command { & .\trove.exe status-line | Out-Null }).TotalMilliseconds } |
    Sort-Object | Select-Object -Index 10
  ```

  Compare with a do-nothing program (`hostname.exe`). On the development machine both were about 46
  to 51 ms, so process start-up dominates and Trove adds almost nothing. On macOS or Linux use
  `hyperfine './trove status-line'` and expect well under 50 ms.
- [ ] A status line must stay fast with a big database: copy a real `trove.db` into a throwaway dir
  and time it again.

Inside Claude Code (needs a real Claude Code install):

- [ ] Run `./trove setup` (section 8) and say yes to the status line, or add this to
  `~/.claude/settings.json` yourself:

  ```json
  "statusLine": { "type": "command", "command": "/full/path/to/trove status-line --format claude" }
  ```

- [ ] Start Claude Code, send a message. The line appears under the input after the reply, with the
  session cost moving up as you work, and no flicker or error.
- [ ] After `/clear` (a new session id), the session figure restarts from the new session.
- [ ] If the gateway is not running the line still shows the last known numbers.
- [ ] Re-read <https://code.claude.com/docs/en/statusline> if Claude Code has been updated: the
  contract was verified on 2026-10-29.
- [ ] tmux (optional): `set -g status-right '#(trove status-line --format tmux)'` renders and colors.
- [ ] bash or zsh prompt (optional): `PS1='$(trove status-line) \$ '` does not slow the prompt.

## 7. Share card

- [ ] `./trove share --out ./cards` shows the preview and asks. Answering `n` or pressing Enter
  saves nothing. `y` writes `cards/trove-share.svg` and `.png`. `--yes` skips the question. If the
  files exist, the question says it will replace them.
- [ ] The preview lists exactly: period, spend, waste found, saved, biggest leak. Compare each line
  with `./trove report` for the same period (the card rounds amounts of $10 to $99 to whole dollars).
- [ ] Open the PNG in an image viewer and the SVG in two or three browsers (Chrome, Firefox, Safari
  if you have it). Text is not cut off or overlapping, numbers sit inside their panels, colors match
  across PNG and SVG. `--theme light` and `--theme dark` both look right.
- [ ] Put each card on a white page and on a black page (for example a Markdown preview in a light
  and a dark editor theme). Both stay readable because each paints its own background.
- [ ] Post a test card to a private or draft X post (or any social composer) and check the crop:
  nothing important should be near the edges. Delete the draft. Trove itself never uploads anything.
- [ ] Privacy, by hand: run `./trove share --yes --out ./cards`, then search the files for anything
  you consider private (repo name, a file name, your username, a model name):
  `grep -il -e yourrepo -e yourname cards/*` should print nothing. `exiftool cards/trove-share.png`
  shows no author, path or comment fields.
- [ ] Very large numbers (try a fixture with spend over $100,000) shrink to fit rather than overflow.
- [ ] With no data, `./trove share` says "Nothing to share yet" and writes nothing.
- [ ] `./trove share --json` without `--yes` saves nothing and reports `"saved": false`.

## 8. `trove setup`: closing explanation and the status-line step

Use a throwaway data dir and a throwaway Claude config: `CLAUDE_CONFIG_DIR=$HOME/claude-verify`
(Trove honors it). Put `{"model": "opus"}` in `settings.json` there first and keep a copy.

- [ ] `./trove setup` shows the gateway change, asks, applies on `y`. Then it shows the status-line
  change as a **second** question. Answering `n` to the second leaves `statusLine` out and prints how to
  add it later.
- [ ] The summary ends with "What happens next:" covering: metadata only, never your prompts, code,
  file paths, tool output or API keys; everything stays in the data dir; no network except your own
  requests; observe-only with a 7-day, 20-session baseline; run `trove` to see results. Read it for
  accuracy and tone.
- [ ] `./trove setup --yes` applies the gateway change only. `./trove setup --yes --status-line`
  also adds the status line. Running it again says it is already configured.
- [ ] If `settings.json` already has a `statusLine`, setup leaves it and explains how to call
  `trove status-line --format claude` from your own script.
- [ ] `./trove undo` restores `settings.json` byte for byte (`diff` against your copy). Edit the file
  by hand first and undo again: only Trove's lines are removed.
- [ ] `./trove setup --json --yes` includes `explain`, `status_line` and `status_line_applied`.
- [ ] The saved command uses the absolute path of the binary, quoted if it has spaces, and carries
  `--data-dir` only when you used a non-default data directory. If you move or rebuild the binary,
  re-run setup.

## 9. Decisions to confirm and things not proven

- [ ] **Spend is "estimated".** Token counts are measured; dollars use list prices and can differ from
  your invoice (discounts, unpriced tiers). Confirm you want the `~` shown everywhere, including the
  status line and the card.
- [ ] **Waste is a lower bound.** Per request only the largest finding counts, which understates
  waste when findings do not overlap. Confirm that is the right side to err on. A percentage above 100
  is not blocked in code; decide whether to cap it or hide it.
- [ ] **Pace rules.** "Ahead" means used% is more than 2 points above elapsed%, or the projection
  reaches the cap; "close to the cap" uses `warn_at_percent` (default 80); no verdict in the first 5% of
  the period. Confirm these feel right on a real month.
- [ ] **`--yes` does not agree to the status line.** Only `--status-line` does. Confirm.
- [ ] **Card wording**, including "Made locally. Nothing uploaded." and the amounts rounded to whole
  dollars from $10 up.
- [ ] **Status line shows `~`** where your original example had none. It is the labeling rule; say if
  you want it dropped there.
- [ ] Session cost in the status line counts unpriced calls as zero. Decide whether to track an
  unpriced count on `sessions`.
- [ ] Windows: the cobra "started from Explorer" check is off for all commands
  (`cobra.MousetrapHelpText = ""`). Double-clicking `trove.exe` now just opens and closes a console.
  Fine for a terminal tool; confirm.
- [ ] Not yet built: baseline progress in `trove optimize status`; a command to lift a guardrail
  rollback; Windows notifications.
- [ ] Not yet run: macOS and Linux (build, tests, notifications, `osascript`, `notify-send`), a real
  Claude Code status line, a real X post, a real 30-minute idle gap without the shortened setting.

## 10. Cleanup

```bash
./trove gateway stop
rm -rf "$HOME/trove-verify" "$HOME/trove-empty" "$HOME/claude-verify" ./cards
unset TROVE_DATA_DIR TROVE_GATEWAY_PORT TROVE_UPSTREAMS_ANTHROPIC
```

Stop the Python fake upstream too. Confirm `~/.trove` was never used: its `trove.db` modified time
should not change while you test.
