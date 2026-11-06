# Reports, budget, receipts, status line and share card

Written 2026-10-29. Code: `internal/report` (and `internal/report/share`). Commands: `trove`,
`trove report`, `trove budget`, `trove receipts`, `trove status-line`, `trove share`.
How to check it yourself, step by step: `docs/report-verification.md`.

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

Timing: `TestStatusLineReadsClaudeCodeInputAndIsFast` runs it 50 times over 50,000 recorded requests and
requires p95 under 50 ms. On the development machine (Windows 11) the in-process time was a median of
about 7 ms and a p95 of about 11 ms. Process start-up is on top of that and depends on the operating
system. One finding from that test: cobra's Windows "started from Explorer" check cost about 30 ms on
every command, so it is switched off (`cobra.MousetrapHelpText = ""`).

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
- `trove optimize status` does not show baseline progress yet; `trove report` does.
- No command lifts a guardrail rollback yet (`measure.ClearOverride` exists).
- The measurement layer's quality claim mentions benchmarks "(Prompt 8)" that do not exist yet; the
  report uses its own wording ("No regression detected locally ... it does not prove output quality").
- `--since` and the week and month starts use the local time zone of the machine running the command.
