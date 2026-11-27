# Reports, budget, receipts, status line and share card

Written 2026-10-29. Code: `internal/report` (and `internal/report/share`). Commands: `trove`,
`trove report`, `trove budget`, `trove receipts`, `trove status-line`, `trove share`.

Every number comes from the measurement layer (`internal/measure`) unchanged and carries a label:

- **measured**: counted from provider-reported usage (token counts, cache-hit rate) or from the
  recorded requests.
- **estimated**: derived from list prices, an assumption or a counterfactual. All dollar figures are
  estimated. They are list-price estimates, not invoices. A `~` in front of a dollar amount marks an
  estimate; `+` after one (status line) or "at least" means some calls could not be priced.

`--json` on any command returns the same data. Every figure there is an object with `value`,
`label` and `n`, and a test fails if one has no label.

## trove / trove report

Period: `--today`, `--week` (since Monday), `--month` (since the 1st), `--since YYYY-MM-DD`.
With none given the report uses the budget period if a budget is set, otherwise the month.

Sections, in order:

1. **Headline.** Spend (estimated; tokens measured), waste found, savings, and a "Measured" line.
   - Waste is a lower bound. Findings overlap (the same tokens can be both a duplicate and
     oversized), so each request contributes only its largest finding (`measure.WasteTotalIn`).
     The per-category rows below it are not additive.
   - Savings are what optimizers that were on saved (estimated). The only "measured" savings
     evidence is the on-versus-off prompt-token comparison, and it says "not enough data yet"
     until both sides have enough sessions.
   - A budget line appears here when a cap is set.
2. **Spend by agent and model.** Narrow terminals drop the token column.
   - **Cache rebuilds (Claude Code history).** Cache writes that rebuilt a prefix rather than
     extending it: total count and tokens (measured), then count, tokens and share per cause
     (estimated: the cause is inferred). Also shown: the threshold, session starts (listed, not
     counted) and what "unexplained" can hide. See [analyze.md](analyze.md#history-ledgers-claude-code-added-2026-11-23).
     `--json`: `cache_rebuilds`.
   - **Output by kind (Claude Code history).** Output tokens (measured) split into thinking
     (measured where the log has a count, `~` where estimated), visible text and tool input
     (estimated). Split by phase (after a prompt or continuing after a tool result), model, effort
     and Claude Code version, plus earlier thinking carried in context (estimated upper bound).
     Rows that could not be split show `-`. `--json`: `output_mix`.
   - Both appear only when there is Claude Code history in the period. The output section
     appears only when some of it could be split.
   - **Plan limits (your estimate, not official weights).** How your tokens move Claude Code's
     5-hour and weekly meters, learned from the meter and your own history. It shows weights per
     token type with 90% intervals, model-family multipliers, the share of movement from elsewhere,
     the waste and savings above in % of each limit, and how much more data the next tier needs.
     Everything is estimated. See [limit-fit.md](limit-fit.md). Appears once the status line has
     recorded the meter. `--json`: `plan_limits`.
3. **Biggest leaks.** Up to five, ranked by estimated dollars, each with what to do. For a leak an
   optimizer can remove, the action is the exact `[optimizers]` line to add, once Trove offers it.
   During the baseline nothing is offered, and the report says so.
4. **Optimizers.** State, estimated savings (or what shadow mode would have saved), the measured
   comparison, rollbacks and guardrail events, and baseline progress.
5. **Recommendations.** Suggestions Trove never applies, each with a confidence. Usage-based ones carry an
   estimated saving or a scenario. Ones from the local recommender ([recommend.md](recommend.md)) carry a reason,
   what they rest on (your own data, a benchmark run, or a rule of thumb) and a measured/estimated label, and show
   no dollar figure. The recommender is steered by the folder you run `trove report` in; it is best effort and a
   failure there never fails the report.

`trove report claims` is a separate table: every tool and optimizer that has been in a randomized session test,
with its own claim, the estimated change in the weekly limit, the measured change in tokens and the pass-rate change,
each with a 95% range, plus the sample size and dates. `--markdown` prints it ready to publish. Method:
[benchmark-methodology.md](benchmark-methodology.md#11-the-claims-table).

Empty states: no database yet, or no data in the period, prints what to do next
(`trove setup`, `trove gateway`) and no zeros. Under 20 requests the report says its numbers
are rough.

## trove budget

`trove budget set 1500 --period month` (also `week`, `day`; `set 0` removes the cap) edits only the
`period` and `cap_usd` lines of `config.toml`. `trove budget show` prints pacing:

- percent of the cap used against percent of the period gone (periods start at local midnight, the
  1st of the month, or Monday),
- the date the cap would be reached at the current rate, if that is before the period ends,
- what would change it: the leak with the most dollars in the period.

Status words: *under pace*, *on pace*, *ahead of pace* (also when the projection reaches the cap),
*close to the cap* (at `warn_at_percent`, default 80), *the cap is reached*, and *too early to judge*
in the first 5% of the period. A budget only informs; it never blocks a request.

Subscription usage is not a bill, so a dollar cap does not apply to it. `trove budget set 200 --usage`
(same `--period` choices) writes `usage_usd` instead: a budget in API-equivalent dollars. When the
status line reports API-equivalent value, it adds the share used, for example
`~$170 API-equivalent this month · 85% of $200 usage budget · near usage budget`. The share is
estimated from list prices; the warning starts at `warn_at_percent` and turns to *over usage budget*
at 100%. It is a warning only and nothing is ever stopped. `TROVE_BUDGET_USAGE_USD` overrides it.

## trove receipts

A receipt is one line per finished session: estimated cost, model, calls, cache-hit rate
(measured) and estimated savings. The gateway has no session-end event, so a session ends when its
idle gap (`gateway.idle_minutes`) has passed, the same rule the outcome roll-up uses. The sweep runs in
the gateway's measurement loop (every 5 minutes) and again when you run `trove receipts`. It looks
back 7 days and is safe to repeat. The gateway cannot print into the agent's terminal, so receipts
appear in `trove receipts` and the status line.

Desktop notifications are off by default. Turn them on with `notifications.desktop = true`
(macOS `osascript`, Linux `notify-send`). Only sessions that ended in the last hour notify.

## trove status-line

One line, for example `~$612 / $1,500 · 41% used · 48% of month · on pace · session ~$3.20 · saved ~$47`.
It reads the per-day summary table `usage_daily`, which the gateway updates in the same transaction
as each request, plus one row of `sessions`. It never scans `requests`, opens the database read-only,
and never fails: on any problem it prints a short hint and exits 0.

Formats (`--format`): `claude`, `tmux` (`#[fg=...]` codes), `shell` (plain). Color shows only the pace
word: amber when ahead of pace, red near or over the cap. `NO_COLOR` and `--no-color` turn it off.

Claude Code contract, verified 2026-10-29 at <https://code.claude.com/docs/en/statusline>:
`"statusLine": {"type": "command", "command": "..."}` in `settings.json`. Claude Code runs the command
after each assistant message (debounced 300 ms, an in-flight run is cancelled by a new one), sends
session JSON on stdin including `session_id`, and shows what the command prints (several lines and
ANSI colors are allowed). Trove hashes `session_id` the same way the gateway hashes the
`x-claude-code-session-id` header, so the status line finds its own session's cost.

`trove setup` offers to add it as a separate step after the gateway change: its own consent
(`--yes` alone does not agree to it; `--status-line` does), its own backup, undone by `trove undo`.
An existing `statusLine` is never replaced.

tmux: `set -g status-right '#(trove status-line --format tmux)'`. bash or zsh: add
`$(trove status-line)` to your prompt.

### Plan-usage meter

With `--format claude`, the line ends with Claude Code's own subscription meter when Claude Code sends it,
for example `… · saved ~$47 · 5h 23% · wk 41%`. These are Claude Code's numbers, not Trove's estimates, so
they carry no `~`; in `--json` they are `plan_usage` (`source: "claude_code"`, each window's
`used_percent` labeled measured, and `resets_at`). Trove reads them only from the status line's stdin. It
never calls a usage endpoint or reads a web page for them (PRINCIPLES.md rule 11).

Fields used, verified 2026-11-23 at <https://code.claude.com/docs/en/statusline>:

| Field | What Trove does with it |
|---|---|
| `session_id` | Hashed as above; the key of each sample |
| `rate_limits.five_hour.used_percentage`, `rate_limits.seven_day.used_percentage` | Shown and recorded. 0 to 100 |
| `rate_limits.five_hour.resets_at`, `rate_limits.seven_day.resets_at` | Recorded; a window past its reset time is not shown. Unix epoch seconds |
| `prompt_cache.warm`, `prompt_cache.ttl` | Recorded (`ttl` is `"5m"` or `"1h"`, stored as 300 or 3600 seconds) |
| `model.id`, `version` | Recorded when they match a strict pattern, else dropped |

What the docs say about when they are absent: `rate_limits` appears only for claude.ai Pro, Max and Ultra
subscribers (or behind a Claude apps gateway that sets a spend limit), only after the session's first
API response; each window may be absent on its own, and Claude Code drops a window once its `resets_at`
passes. `prompt_cache` needs Claude Code v2.1.251 or later, covers the main conversation only, and is
absent until its first response. Claude Code runs the command after each assistant message, when a
rate-limit window reaches `resets_at` and when a warm cache reaches `expires_at`. Trove does not read
`rate_limits.spend_limit`, `context_window`, `cost` or any other field; the folders, transcript path and
session name are never decoded.

Rules: every field is optional and checked on its own, so a bad value drops only itself. A percentage
outside 0 to 100 is treated as absent. Percentages are rounded down, so 99.6% shows as 99% and the line
never reports a limit reached before it is (rule 9).

Recording (table `limit_samples`, see docs/data.md): the status line keeps a sample only when a value
changed since the session's previous one. It never writes to the database and never takes its lock. It
checks an empty marker file named by the hashed session key and a hash of the latest values
(`statusline/seen/`, one file check), falls back to the database's latest row on its read-only
connection, and appends one line to `statusline/limits.jsonl`. Session end and `trove report` move the
spool into the table. `--json` says what happened as `limit_sample`: `spooled`, `unchanged`,
`skipped_no_data_dir`, `skipped_spool_full` or `skipped_error`. None of these changes the line or the
exit code (rule 4).

Why a spool and not a direct write: on the development machine (Windows 11), opening, writing and
closing SQLite from the status line cost 25 to 30 ms per changed sample, because closing checkpoints and
re-creates the write-ahead log. Reading back a file written moments earlier cost another 20 ms. The
write path's p95 was 43 to 73 ms. The marker file avoids every read of something just written.

Timing: `TestStatusLineReadsClaudeCodeInputAndIsFast` runs it 50 times over 50,000 recorded requests with
the full documented input, once with the same meter every time and once with a meter that changes on
every run, and requires p95 under 50 ms for both. On the development machine (Windows 11, 2026-11-23,
measured back to back against the previous build) the previous build had a median of 11 to 14 ms and a p95 of
15 to 30 ms. With an unchanged meter the median was 11 to 16 ms and the p95 17 to 25 ms; with a meter that
changes every run, the median was 17 ms and the p95 22 to 25 ms. Under a full parallel `go test ./...` the
p95 of both the old and new paths can pass 100 ms on that machine, so the timing is judged in an
isolated run. Process start-up is on top of that and depends on the operating system. One finding from
that test: cobra's Windows "started from Explorer" check cost about 30 ms on every command, so it is
switched off (`cobra.MousetrapHelpText = ""`).

## trove share

Draws a 1200 x 630 card (the shape X shows uncropped) as SVG and PNG from the report's numbers:
period, spend, waste found, savings and the biggest leak's name, each labeled estimated. It shows the exact
text first and asks before saving to `./trove-share.svg` and `.png` (or `--out`). It never uploads.

The card cannot contain anything identifying: `share.Card` has no field for a repository, file,
prompt, agent or model name, the leak name must come from a fixed list, and a test plants fake repo,
file, prompt and model names in the data and checks that none reach the SVG, the PNG or its text.
The PNG has no metadata chunks.

`--theme dark|light`. Each card paints its own solid background, so it reads on a light or a dark page.
The PNG uses the Go fonts bundled with `golang.org/x/image` (no font files to ship); the SVG asks for a
system sans-serif and leaves 8% of slack for font differences.

## Known gaps

- Session cost in the status line comes from `sessions.total_cost_usd`, which counts unpriced calls as
  zero, so it can be low. The headline spend carries an unpriced count and shows `+`.
- Desktop notifications are not implemented on Windows.
- Plan-usage samples wait in `statusline/limits.jsonl` until a session ends (with Trove's hooks) or
  `trove report` runs, so `limit_samples` lags the status line. With neither, the spool stops growing
  at 1 MB (about 4,000 changes) and later samples are dropped.
- The status line never creates Trove's data folder, so nothing is recorded before Trove has one.
- A model id is stored as Claude Code sends it when it looks like an id (letters, digits and `._:/@+[]-`,
  up to 96 characters), the same rule as `adapter_events`. A custom model named after something private
  would be stored; free text with spaces is dropped.
- Nothing analyses `limit_samples` yet. Turning tokens into "% of the 5-hour limit" is later work.
- `trove optimize status` does not show baseline progress yet; `trove report` does.
- No command lifts a guardrail rollback yet (`measure.ClearOverride` exists).
- The measurement layer's quality claim mentions benchmarks "(Prompt 8)" that do not exist yet; the
  report uses its own wording ("No regression detected locally ... it does not prove output quality").
- `--since` and the week and month starts use the local time zone of the machine running the command.
