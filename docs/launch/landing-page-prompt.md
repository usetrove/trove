# Landing page redesign prompt

Written 2026-11-16, results updated 2026-11-18 with the `launch-3` study. Paste everything below the line into the tool that builds troverelay.com.
Every number in it is copied from README.md "Measured results" and `bench/published/workflows/workflows.md`
as of v0.4.0 (2026-11-14), and from `bench/published/launch-3/report.md` (2026-11-18). If those change, change this file first.

---

Redesign the Trove website at troverelay.com as a multi-page site. Trove is a free, open-source, single-binary tool that finds the tokens an AI coding agent wastes, applies only proven, cache-safe fixes, and measures whether they helped. It runs on the developer's machine and sends nothing anywhere by default.

The current site is a single long page with a "request early access" form and an illustrative "verify" story. Replace all of that. Trove v0.4.0 is public and installable, and the story is now: install, run `trove`, say yes once to Autopilot, read the measured results.

## 1. Design direction

Model the layout, density and typographic system on probe0.xyz, but use Trove's own palette and typefaces. Specifically take from probe0:

- A sticky 56px top bar, page background at 85% opacity with backdrop blur, 1px hairline underneath. Wordmark on the left, three or four 13px text links, one filled pill button on the right.
- Content column of max 1080px with 24px side padding. Section gaps of 80 to 96px.
- An enormous two-line hero headline set at `min(10.4vw, 8.2rem)`, line-height 0.88, letter-spacing −0.05em, weight 700. Line one in ink, line two in the faint ink colour. Below it a hairline rule, then a two-column row: 18px sub-copy (max 40ch) on the left, the primary pill button and a copyable install command on the right, with a 12.5px footnote in `--ink-soft` under them.
- A row of pill tabs that drive a single large product "stage" below them. Clicking a tab swaps the stage's content; a 2px progress bar in the active tab auto-advances the tour every few seconds and pauses on hover.
- The stage is a large rounded container (24px radius, 1px ring at 8% ink) that holds a product mockup.
- Short, declarative copy. Plenty of air. Tabular numerals everywhere a figure appears.
- Entrance motion only: rows fade up 28px over 0.42s with `cubic-bezier(.16,1,.3,1)`, staggered 60ms per row. Respect `prefers-reduced-motion`. No scroll-jacking, no parallax.
- A closing section with one large 2.5rem line and the primary button again.
- A quiet footer: link row, then one line in `--ink-soft` with the copyright and a non-affiliation note.

Do not copy probe0's copy, its macOS desktop mockup, or its black-and-grey palette.

## 2. Design tokens

Page surfaces are light "paper". Terminal and report mockups are dark panels inside the paper page. Gold is the only brand accent and is used sparingly. Green, amber and red are status colours and appear only inside mockups to show a state (on, shadow, rolled back); never use them for branding, buttons or headings.

Paper (page):
- `--paper`: #fbfaf7 (page background)
- `--paper-2`: #efede8 (sunken areas, code chips, table header rows)
- `--paper-raised`: #ffffff (cards, the pill tab bar)
- `--stroke`: #dcd9d2 (hairlines, rings, table rules)
- `--ink`: #2d2e2a (headings, body, primary button fill)
- `--ink-soft`: #6b6c68 (sub-copy, nav links, footnotes, footer text; passes 4.5:1 on paper)
- `--ink-faint`: #8f8f8a (large text only: hero line two and the big closing line, where it passes 3:1 on paper; never for text under 24px)
- `--gold`: #B8860B (wordmark mark, link underlines on hover, the active tab's progress bar, small highlight marks)
- `--gold-text`: #9A6A00 (gold used as text on paper, for contrast)

Dark (mockups only):
- `--bg`: #0E1217
- `--panel`: #171D25
- `--rule`: #2A323D
- `--text`: #F3F5F7
- `--muted`: #9AA6B5
- `--gold-dark`: #E0B04A (highlight boxes and callouts inside terminals)
- `--green`: #46D39A, `--amber`: #F2A83B, `--red`: #F0716E (status only)

Type:
- Display (hero, section headings, big figures): Figtree 700/800.
- Body and UI: Bricolage Grotesque 500, 700 for emphasis. Fallback `system-ui, sans-serif`.
- Mono (terminals, commands, figures in tables): JetBrains Mono 400/700. Fallback `ui-monospace, monospace`.
- Load from Google Fonts or self-host; subset latin.

Buttons: primary is a pill, ink fill, paper text, 48px tall, 15px text, hover opacity 0.85. Secondary is a pill with a 1px stroke and ink text. Code chips are `--paper-2` with a 6px radius and mono 13px.

Terminal mockup: dark panel, 14px radius inside the stage, a title bar with three 12px dots in `--rule` (not the macOS traffic-light colours), JetBrains Mono 13.5px with line-height 1.55, prompt lines prefixed with `$`, muted output in `--muted`, and gold highlight boxes with a small label for the one thing the viewer should look at.

## 3. Site map

Build these as separate pages with shared nav and footer. Keep the existing legal pages (privacy, terms, cookies, licenses, delete) and link to them from the footer.

- `/` Home
- `/how-it-works` Autopilot and the two ways Trove can run
- `/benchmarks` Measured results, in full
- `/install` Install and first run
- `/data` What Trove stores and what it never does
- `/faq`

Nav links: How it works, Benchmarks, Data, FAQ. Primary nav button: "Install". Wordmark: "trove" with a small gold mark. Docs and source link to https://github.com/usetrove/trove.

## 4. Page content

Use this copy as written. You may tighten sentences but you may not add numbers, change numbers, drop the "measured" or "estimated" labels, or add a claim about how much Trove saves. Every figure below carries its label; keep the label next to the figure on the page.

### 4.1 Home

Hero, line one: "Find the tokens your agent wastes."
Hero, line two (faint): "Prove the fix didn't hurt."

Sub-copy: "Trove reads the history your coding agent already keeps, shows where the tokens went, and turns on only the fixes that prove themselves on your own sessions. Open source. Runs on your machine."

Primary button: "Install" (links to /install). Beside it a copyable command chip: `brew install usetrove/trove/trove`.

Footnote: "Works with Claude Code today. Sends nothing by default. Measured on 30 long Claude Code sessions: 25% lower estimated cost with model routing on (API keys only). The plugin alone has not shown a saving yet; see the benchmarks."

Pill tabs and stage (each tab swaps the terminal in the stage; use these exact terminal contents):

1. "Where it goes": a terminal running `trove` that prints:
   ```
   Trove report: this month (since Oct 1)

   Headline
     Spend        ~$73.50 (estimated at list prices, not an invoice); 1.6M tokens (measured)
     Waste found  at least ~$5.85, 8% of spend (estimated; overlapping findings counted once)
     Savings      none yet; no optimizer has been on (estimated)

   Biggest leaks (estimated, overlapping, not additive)
     Repeated tool results   ~$4.50   60K tokens
     Noisy tool output       ~$3.00   90K tokens
   ```
   Gold highlight on "estimated" with the label "every number is labeled". Caption under the stage: "Run `trove`. See where they leak. These are example figures from the report's test fixture, not a real user."

2. "Say yes once": a terminal running `trove autopilot on` showing the consent text and the `[y/N]` prompt answered `y`, then "Autopilot is on. It starts in shadow mode and changes nothing yet." Caption: "One question. Nothing changes until there is evidence."

3. "Shadow first": `trove autopilot` status listing `duplicate_result_collapser  shadow  (collecting shadow estimates)` and `noise_normalizer  shadow  (collecting shadow estimates)`, amber dot beside "shadow". Caption: "Each fix runs on a copy and records what it would have saved, after paying for its own overhead."

4. "Promoted by evidence": the same status with `noise_normalizer  on  (promoted from shadow evidence, estimated)` and a green dot, plus a one-line note "Changes reach only sessions that start afterwards, never one in progress." Caption: "Turned on only from shadow evidence, only between sessions, never over a setting you made."

5. "Rolled back by itself": status showing `noise_normalizer  shadow  (guardrail: net saving went negative)` with a red dot on the guardrail line. Caption: "If the net saving goes negative, outcomes get worse or errors rise, it drops back to shadow and tells you. `trove undo` reverses everything."

Below the stage, a section headed "What the benchmarks say" with three stat tiles and one line each. Figures in Figtree 800 at 3.5rem, label in a small mono chip beside each:

- "25%" chip "estimated". "Lower cost per session on 30 long Claude Code sessions on Sonnet with model routing on (98% interval 21% to 29%), with no drop in tasks passed (measured). Pre-registered. API-key traffic only."
- "19.3%" chip "estimated". "Median on 3 scripted output-heavy sessions (range 2.5% to 50.7%). A ceiling for that kind of work, not a typical day."
- "698 of 698" chip "measured". "Failure and error lines still visible to the model after trimming. 400 of 400 requests kept their cached prefix byte-identical."

Under the tiles, in `--ink-soft`: "The 25% is model routing, an opt-in option for API keys; it never runs on a subscription. The plugin alone and the safe optimizers have not yet shown a saving on real tasks. The 19.3% comes from scripted sessions replayed through the real engine, no API calls. Read all of it on the benchmarks page." Link: "See the benchmarks" to /benchmarks.

Then a four-step row headed "Nothing changes until it's proven":
- 01 Watch. "Trove reads the session history your agent already keeps. It changes nothing."
- 02 Shadow. "Each fix runs on a copy of the request and records what it would have saved, minus its own overhead."
- 03 Promote. "A fix turns on only from that evidence, only when a new session starts, never over a setting you made."
- 04 Measure. "Once on, it is measured against your own sessions. If guardrails trip, it rolls back and tells you."

Closing section, one large line: "Open source. Runs on your machine. `trove uninstall` puts everything back." Primary button "Install", secondary button "Source on GitHub".

### 4.2 How it works

Heading: "Autopilot, and the two ways Trove runs."

Section "Two channels":
- "On a subscription (Claude Free, Pro, Max, Team, Enterprise): the Claude Code plugin. Hooks, MCP tools and config fixes. No proxy, no credentials touched, nothing rerouted."
- "On API keys: an optional local gateway on 127.0.0.1. Your agent points at it with its documented custom-endpoint setting. Keys pass through in memory and are never stored. If Trove hits an error, the original request is forwarded unchanged."
- A boxed note: "Trove never handles subscription or OAuth logins. Gateway mode is for API-key traffic only and rejects anything else with a plain explanation."

Section "The lifecycle": a horizontal diagram of five states, shadow → eligible → on → measured → (rolled back to shadow if guardrails trip), drawn with hairlines and small status dots. Under it, the six consent bullets exactly as `trove autopilot on` prints them:
- It starts in shadow mode: nothing is changed until there is enough evidence.
- Only lossless optimizers that leave your cached prompt alone are eligible.
- Changes reach only sessions that start afterwards, never one in progress.
- Anything you set yourself in config.toml is left exactly as you set it.
- A guardrail turns an optimizer back to shadow if it stops paying off.
- It never touches your login or your traffic beyond what you already set up.
- Nothing leaves your machine.

Section "Cache safe by construction": "Every transformation is a pure, deterministic function of the request. The same history always produces byte-identical output, so cached prefixes stay valid. Optimizers switch only at session boundaries. Each one declares its own token overhead and is charged for it."

Section "Honest numbers": "Every figure Trove shows is labeled measured (counted from usage the provider reported) or estimated (list prices or an assumption). Dollar figures are estimates, not invoices. Savings are judged against your actual sessions with and without an optimizer, never against a hypothetical baseline."

Note at the bottom: "Autopilot is a first version. Its promotion thresholds are placeholders not yet validated by a benchmark, and riskier changes need a separate second yes (`trove autopilot trials on`)."

### 4.3 Benchmarks

This page is the measured-results section of the README, laid out properly. Open with a boxed bottom line in `--paper-2`:

"Bottom line. In a pre-registered study of 30 long Claude Code sessions on Sonnet (launch-3, 2026-11-18), Trove's full setup with model routing on cut the estimated cost per session by 25% (98% interval 21% to 29%), with no drop in tasks passed (measured). The saving comes from model routing, an opt-in option for API-key traffic that sends tool turns to a cheaper model; it never runs on a subscription. The Claude Code plugin alone and the safe optimizers have not yet shown a saving on real tasks: on the tasks studied they had almost nothing to remove. Trove makes no savings promise beyond what a study shows."

Then "Decision rule": "Trove recommends an optimizer for automatic use only if its cost reduction is at least 5% and the whole 95% interval lies at or below −5%, with the success rate not falling by more than 5 points and at least 30 pairs. Otherwise the verdict is NEED MORE DATA or DON'T SHIP. A result is never rounded up."

Section 1, "Long Claude Code sessions (launch-3)". Lead: "Does the Trove plugin cut cost, and does the plugin plus the gateway with model routing cut it further? 30 long-session Python exercises, Claude Code 2.1.319, claude-sonnet-5-5 routing to claude-haiku-5-5, API-key auth, every arm launched the same way. Pre-registered; costs are estimated (list price on the usage the gateway recorded), pass or fail is measured." Table:

| Compared with no Trove | Total cost change, estimated (98% interval) | Tasks passed (measured) | Verdict |
|---|---|---|---|
| Plugin + gateway + model routing | −25.3% (−29.2% to −21.4%) | 30 of 30 vs 30 of 30 | SHIP |
| Plugin alone | +3.5% (−0.6% to +7.7%) | 30 of 30 vs 30 of 30 | DON'T SHIP: even the best case saves less than 5% |

Under it: "The saving is model routing: the safe optimizers removed no tokens in this study, and input tokens rose because a model switch rewrites the cache, which the cheaper model more than paid for. Routing and brevity steering were both on; both are off by default and need an explicit opt-in, and routing never runs on a subscription. The plugin's hooks acted in 2 of 30 runs, so these tasks gave them little to do. Every arm passed every task, so the tasks were easy. Routed sessions took a median 9.4 s longer (measured)." Link: "Full report" to bench/published/launch-3/report.md on GitHub.

Section 2, "Real tasks, two pre-registered studies". Lead: "Does the gateway with its safe optimizers on cost less than the same gateway with every optimizer off? Costs are estimated (list price on the token counts the agent reports). Pass or fail is measured." Table:

| | Aider polyglot, 30 tasks | SWE-bench Verified, 35 tasks |
|---|---|---|
| Study | gateway-cost-2 | swebench-gateway-2 |
| Model, agent | claude-haiku-5-5, Claude Code 2.1.318 | claude-sonnet-5-5, Claude Code 2.1.319 |
| Runs | 180 (90 pairs) | 140 (35 pairs per comparison) |
| Median paired cost change, all_safe vs passthrough (estimated) | −3.0% (95% interval −9.5% to +3.7%) | +4.4% (95% interval −7.0% to +13.8%) |
| Success (measured) | 90 of 90 under both | 28 of 35 vs 29 of 35 |
| Mean cost per run (estimated) | about $0.0026 | about $0.02 to $0.03 |
| Verdict | NEED MORE DATA | NEED MORE DATA |

Under it: "The two studies point in opposite directions, which shows how wide the noise is. In the SWE-bench study the two single optimizers were also within noise: noise_normalizer 0.0% (−11.0% to +20.0%) and duplicate_result_collapser +1.9% (−11.6% to +26.0%). The gateway's own records say the optimizers had almost nothing to remove: the median SWE-bench session was 5 turns, and all_safe removed about 0.2% of input tokens (estimated). A saving of that size cannot move a bill."

Section 3, "Scripted everyday sessions, replayed through the real engine". Lead: "Short benchmark tasks cannot show a long working day, so `trove bench workflows` replays hand-scripted Claude Code sessions through the real optimizer engine, offline: no API call, no spend. How often a file is re-read, how noisy a shell is and how long a session runs are assumptions, so these figures say what the optimizers do on these patterns. They are not a forecast of anyone's bill." Model claude-sonnet-5-5, list prices as of 2026-11-08.

Tier table:

| Kind of session | Median saving (range) | Label |
|---|---:|---|
| Everyday: fix a failing test, rename across a codebase, debug with logs, explore a repo, a ~90-turn feature session (7 workflows) | 3.7% (0.0% to 8.6%) | estimated |
| Output-heavy: a verbose test loop, a failing container build, incident logs (3 workflows) | 19.3% (2.5% to 50.7%) | estimated |
| A custom agent or script that sends no cache markers (the 7 everyday workflows again) | 72.0% (51.6% to 79.8%) | estimated |

Three reading notes under it:
- "Everyday. Claude Code already caches its own prompt, so the optimizers can only trim what it sends. Even an oracle that deleted every tool output would save between 6.5% and 56.9% depending on the session."
- "Output-heavy. Chosen because large, repetitive tool output dominates them. A ceiling for that kind of work, never a typical day, never averaged with the everyday row. One workflow does most of the lifting (a monorepo test loop at 50.7%); incident logs save only 2.5%."
- "No caching. This saving is Anthropic's prompt caching, which Trove switches on for agents that do not use it. It is zero for an agent that already caches and does not apply to Claude Code."

Then a horizontal bar chart, one bar per workflow, grouped by tier, bars in `--ink` with the custom-agent tier in `--ink-faint`, value labels in mono, and the full per-workflow table beneath it:

| Workflow | Tier | Turns | Cost off (est.) | Cost on (est.) | Saving (est.) | Key lines kept | Prefix kept |
|---|---|---:|---:|---:|---:|---:|---:|
| quick-question | everyday | 3 | $0.0635 | $0.0635 | 0.0% | 0/0 | 3/3 |
| fix-failing-test | everyday | 12 | $0.1418 | $0.1418 | 0.0% | 10/10 | 12/12 |
| debug-with-logs | everyday | 12 | $0.2730 | $0.2628 | 3.7% | 70/70 | 12/12 |
| dependency-upgrade | everyday | 10 | $0.1594 | $0.1513 | 5.1% | 10/10 | 10/10 |
| rename-across-codebase | everyday | 28 | $0.5567 | $0.5566 | 0.0% | 8/8 | 28/28 |
| explore-and-review | everyday | 20 | $0.4750 | $0.4416 | 7.0% | 0/0 | 20/20 |
| long-feature-session | everyday | 94 | $3.5912 | $3.2806 | 8.6% | 80/80 | 94/94 |
| ci-failure-triage | heavy | 15 | $0.4420 | $0.2181 | 50.7% | 4/4 | 15/15 |
| container-build-debug | heavy | 14 | $0.2675 | $0.2159 | 19.3% | 66/66 | 14/14 |
| incident-log-forensics | heavy | 13 | $0.4405 | $0.4295 | 2.5% | 276/276 | 13/13 |
| quick-question (no caching) | custom agent | 3 | $0.1234 | $0.0597 | 51.6% | 0/0 | 3/3 |
| fix-failing-test (no caching) | custom agent | 12 | $0.6819 | $0.1986 | 70.9% | 10/10 | 12/12 |
| debug-with-logs (no caching) | custom agent | 12 | $0.7231 | $0.1517 | 79.0% | 57/57 | 12/12 |
| dependency-upgrade (no caching) | custom agent | 10 | $0.4826 | $0.1577 | 67.3% | 10/10 | 10/10 |
| rename-across-codebase (no caching) | custom agent | 28 | $2.2186 | $0.4814 | 78.3% | 8/8 | 28/28 |
| explore-and-review (no caching) | custom agent | 20 | $1.4643 | $0.4097 | 72.0% | 0/0 | 20/20 |
| long-feature-session (no caching) | custom agent | 94 | $16.4598 | $3.3321 | 79.8% | 89/89 | 94/94 |

Section 4, "Nothing lost for the saving (measured)": four stat tiles. "698 of 698" failure and error lines still visible to the model. "400 of 400" requests kept the cached prefix byte-identical. "0" broken invariants; repeat runs byte-identical. "2.54 ms" engine time per request at p50 (p99 17.09 ms), measured in-process on one machine, excluding the gateway's forwarding time.

Section 5, "Caveats", as a bulleted list: the one confirmed saving is model routing for API-key traffic, on easy tasks that every arm passed, with one model and one Claude Code version; two models and two Claude Code versions across the real-task studies; results may differ for other models, versions or longer sessions. Costs are list-price estimates, never an invoice. Token counts in the scripted runs use the engine's bytes-per-token ratio, not provider counts. The heavy tier was written to be waste-rich. The SWE-bench study ran on a separate machine and its result files are not yet in the repository.

Section 6, "Reproduce": two code blocks.
```
python -m trovebench fetch polyglot -n 30 --seed 20261108
python -m trovebench run --study studies/gateway-cost-2.yaml --tasks <that folder> --batch <folder> --yes
```
```
bash bench/scripts/95_workflows.sh
```
Link to the methodology (docs/benchmark-methodology.md on GitHub) and the published workflow reports (bench/published/workflows/).

### 4.4 Install

Heading: "Install in one line."

Three tabs, macOS/Linux (Homebrew), macOS/Linux (script), Windows (PowerShell, experimental), each with a copyable command:
```
brew install usetrove/trove/trove
```
```
curl -fsSL https://github.com/usetrove/trove/raw/main/install.sh | sh
```
```
irm https://github.com/usetrove/trove/raw/main/install.ps1 | iex
```
Under the tabs: "Every path checks the download against the release's SHA-256 checksums before it runs anything. No account, no sign-up."

Then "First run" as three numbered steps: 1. Add the Claude Code plugin (link to the getting-started doc on GitHub). 2. Run `trove` to see where your tokens went. 3. Run `trove autopilot on` if you want fixes to prove themselves in the background.

Then "Remove it": "`trove uninstall` restores your settings exactly. Your data lives in `~/.trove` and nowhere else."

Note: "Latest release v0.4.0, 2026-11-14. macOS and Linux first; Windows is experimental."

### 4.5 Data

Heading: "What Trove stores. What it never does."

Two columns of short cards:

Stored, locally in ~/.trove:
- Token counts, sizes, hashes, model names, timings and outcome signals.
- Which kind of sign-in a session used, never the credential.

Never:
- Prompt text, code, file paths or tool output are never written to the database.
- API keys pass through in memory and are never logged, stored or printed.
- No telemetry, no analytics, no network calls except forwarding your own API requests to the provider you configured.
- Subscription and OAuth logins are never handled, intercepted or rerouted.

Then "The one opt-in": "`trove contribute enable` turns on an anonymous aggregate summary. It is off by default, shows you the exact text before anything is sent, carries a random install id, and `trove contribute delete` asks the receiver to delete everything under that id. The receiver's code is in the repository." Link to PRIVACY.md and SECURITY.md on GitHub.

### 4.6 FAQ

- Does it change my agent's behavior? Only if an optimizer is on. Everything starts in shadow mode, which changes nothing, and Autopilot promotes a fix only from shadow evidence and only between sessions.
- Will it save me money? With an API key and model routing on, a pre-registered study of 30 long Claude Code sessions on Sonnet measured 25% lower estimated cost per session with no drop in tasks passed. On a subscription, or with only the plugin and the safe optimizers, a saving is not proven yet. Run `trove` and see your own number.
- Does it work with a subscription (Pro, Max, Team)? Yes, through the Claude Code plugin. It never touches your login.
- Does it work with API keys? Yes, through the plugin or the optional local gateway.
- Does Trove send my data anywhere? No. Nothing leaves your machine unless you run `trove contribute enable`.
- Does it resell tokens or hold my account? No. You bring your own key or subscription.
- Does it use an AI to rewrite my context? No. Every transformation is a deterministic rule, and the same history always produces byte-identical output.
- What if a fix hurts? Guardrails roll it back to shadow and tell you. `trove undo` reverses everything Trove changed.
- Which agents? Claude Code today. Codex CLI and OpenCode have adapters that are less tested. See the supported-agents page on GitHub.

## 5. Footer

Links: How it works, Benchmarks, Install, Data, FAQ, GitHub, Privacy, Terms, Cookies, Licenses, Delete your data.
Faint line: "© 2026 Trove. Open source under the repository's license. Not affiliated with Anthropic or OpenAI."

## 6. Rules for whoever builds this

- Use only the numbers in this prompt, with their labels, next to each other. Never invent a figure, never say "up to", never put a percentage in the hero.
- Product, feature and page names never contain "Claude", "Anthropic", "Codex" or "OpenAI". Plain text such as "works with Claude Code" is fine.
- Every command shown must be copyable with one click and must match this prompt character for character.
- No third-party scripts, fonts from a tracker, or analytics beyond what the existing cookie policy already covers.
- Lighthouse accessibility 100: real headings in order, 4.5:1 contrast on all text under 24px (`--ink-faint` is reserved for large display text), focus rings, reduced-motion support, skip link.
- Mobile: hero drops to `min(14vw, 4.5rem)`, the stage scrolls horizontally rather than shrinking the terminal text below 12px, tables become scrollable with the first column pinned.
- Keep the existing legal pages and their URLs.
