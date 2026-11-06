# Privacy

Last checked against the code: 2026-11-06 for the stored-data tables, retention and your controls (the complete, tested field-by-field list is [docs/data.md](docs/data.md)); 2026-10-30 for the rest (the "Sharing a summary" section and its table row: 2026-11-02; the receiver paragraph: 2026-11-02). Each statement below was checked against the source or a test,
named in brackets. If the code changes and this file does not, that is a bug; please report it.

## In one paragraph

Trove runs on your machine and keeps its data in one folder, `~/.trove` (or the folder you choose with
`--data-dir` / `TROVE_DATA_DIR`). It has no account, no update check and no telemetry that runs on its own.
The only network traffic it creates is forwarding your own API requests, unchanged, to the provider you
configured, when you choose to run the gateway, and, only if you turn it on with `trove contribute enable`, a weekly
anonymous summary (see "Sharing a summary" below). Your API keys pass through memory and are not written anywhere.
Your prompts, code, file paths and tool output are not written to the database.

## What leaves your machine

| What | To where | When |
|---|---|---|
| Your agent's API request (including your API key, as your agent sent it) | The upstream in your config: `https://api.anthropic.com` and `https://api.openai.com` by default | Only while the gateway runs and your agent is pointed at it |
| The anonymous summary shown by `trove contribute` (including a random install id) | `https://contribute.troverelay.com/v1/contribute` (the `[contribute] endpoint` in your config; you can change it) | Only after you run `trove contribute enable` and answer yes; at most once every 7 days, when you run `trove report` |

Nothing else. Specifically, Trove does not send crash reports, version checks, or pricing lookups, and sends no
usage data unless you turned on the summary above.
Prices are embedded in the binary.

How this is checked: a test fails if shipped code outside four files can open a connection or build an HTTP
client, or if a telemetry library appears in `go.mod` [`internal/egress` `TestOnlyApprovedCodeCanReachTheNetwork`,
`TestNoUnexpectedNetworkDependencies`]; and a Linux CI test runs the built binary under `strace` through the gateway
and a set of commands and fails on any connection or send to a non-loopback address [`internal/egress`
`TestCommandsMakeNoOutboundConnections`, `TestGatewayOnlyTalksToItsConfiguredUpstream`, CI job `privacy`].

Two things Trove runs locally can have their own network behavior, which are outside Trove: the agent
commands it starts (`claude mcp add`, `claude mcp remove`) and any command you approve in
`.trove/commands.toml` (run by `trove_run`).

## What is never stored

- **API keys, tokens, `Authorization`/`x-api-key`/cookie values.** Keys are compared in memory and forwarded.
  Everything after the credential check receives a copy of the request headers that has only `User-Agent`,
  session id, encoding, content type and API version. [`withoutCredentials`;
  `TestWithoutCredentialsKeepsOnlyWhatLaterCodeNeeds`; `TestNoCredentialOrPromptReachesLogsResponsesOrDisk`
  scans logs, client responses and every byte of every data file]
- **Subscription or OAuth credentials.** Such requests are refused with HTTP 401 and never forwarded.
  Trove never opens `~/.claude.json`, `.credentials.json` or `auth.json`. [`gateway/auth`; `fixer/paths.go`
  denylist; `TestNonAPIKeyAuthIsRejected`]
- **Prompt text, code, tool output or file contents in the database.** [`TestSchemaIsMetadataOnly` lists all
  26 tables and every column, and fails if a table or column is added without review]
- **File paths in the database.** Files are identified by a hash (`history_files.file_key`, `fixes.path_hash`).
  The endpoint column holds the request path on the provider (like `/v1/messages`), not a file path.

## What is stored, and where

Everything is under the data folder: the folder is created `0700`, and the database, config, manifest,
trust file, pid file and recovery files `0600`. If a folder or database that already existed is more open,
Trove tightens it when it opens the store, and `trove doctor` reports it. [`store.Open`;
`TestOpenTightensLoosePermissionsButDoctorPathDoesNot`] (Windows has no meaningful mode bits; Trove sets no
ACLs there.)

### The database (`trove.db`)

All numbers, hashes, names and timings. "Kept" is how long Trove keeps it on its own.

| Table | Holds | Kept |
|---|---|---|
| `requests` | One row per gateway request: time, hashed session key, agent label and version (derived, not the raw User-Agent), provider, model name, route, streamed or not, HTTP status, latency, token counts, cost, findings and shadow-savings counts, optimizer names applied, error class, the provider's usage object (numbers) | `retention_days` (default 90) |
| `sessions` | Per-session totals of the above, hashed key | `retention_days` after the session ends |
| `session_pins` | Which optimizers were on for a session (names and states) | `retention_days` |
| `external_tools` | Third-party tools you approved: name, pinned version, license, on/off, timestamps, IDs of config changes to undo on removal | removed by `trove external remove` |
| `experiment_assignments` | Which arm ("on" or "off") a session was given for an optimizer in the A/B comparison: hashed session key, optimizer name, a hashed stratum label, position, time | `retention_days` |
| `experiment_results` | A/B result numbers per optimizer: metric name, relative change, 95% range, session counts, measured/estimated label, status, a hash of the inputs | `retention_days` |
| `history_requests` | One row per request found in your agent's local session logs: hashed keys, model, token counts, cost, tool **names** with counts, sizes, error counts | `retention_days` |
| `history_files` | Which log files were read: hash of the path, size, modified time, parser offsets, line counts | until `trove data delete` or uninstall (kept on purpose so purged history is not read again) |
| `usage_daily` | Per-day request count and spend | `retention_days` |
| `receipts` | One row per finished session: hashed key, agent, model names, call count, cost, cache-hit rate, savings | `retention_days` |
| `repo_features` | A bucketed description of a repo, computed on your device: a **salted hash** of the working directory (the salt is random and stays in your `settings` table), a size bucket (S, M, L, XL), up to three language names with whole percentages, and the name of the test framework from a fixed list. No path or file name | `retention_days` |
| `session_features` | One row per finished session: hashed key, the repo hash, and counts and ratios derived from the session (files read and edited, repeated reads, scout calls, compactions, retries, tool results, duplicate-line, error and stack-trace shares). Numbers only; nothing from the content itself | `retention_days` |
| `repo_tuning` | Per-repo tuned values for an optimizer: the repo **hash**, optimizer and parameter **names**, a number, whether it is suggested, applied or reverted, and the fetch-back share it was judged against. Stays on your device unless you opt in to `trove contribute` | until `trove tune reset` or uninstall |
| `hook_events` | Counts from hooks: time, hashed session key, hook name, byte and token counts | `retention_days` |
| `adapter_events` | What a plugin for another harness (OpenCode) did, written by `trove engine serve`: time, **hashed** session key, harness name, a login label (`api_key`, `copilot` or `subscription_or_unknown`, which the plugin works out from non-secret fields; Trove never sees a credential), provider and model names (checked against a strict pattern, so free text is dropped), optimizer name and state, byte and token counts, and, for usage rows, the token counts and cost the harness itself reported | `retention_days` |
| `fixes` | Fixes applied: change id, kind, agent, **hash** of the file path, estimated tokens and dollars | `retention_days` after the fix is undone; while it is in place it is current state |
| `settings_profiles` | Settings profiles applied by `trove settings`: change id, task type (for example `quick-fix`), agent, the **names** of the settings changed (never their values), the agent version | `retention_days` after its fix is undone; kept while in place |
| `task_installs` | Scout and cascade files written by `trove tasks`: change id, the feature and role, the model aliases chosen, and estimated token overhead. No path or file content | `retention_days` after its fix is undone; kept while in place |
| `guardrail_events` | When an optimizer was rolled back and why (generated text, numbers) | `retention_days` |
| `optimizations`, `measurements`, `budgets`, `settings`, `schema_migrations` | Optimizer states, latest measured results, your budget, small settings (including the repo-hash salt), migration history | kept: current state, until `trove data delete` or uninstall |

Text that originates outside Trove is limited to: model name (cut to 100 bytes), route (200), error class (64),
tool names from your agent logs, and the agent label. [`TestClientControlledFieldsAreCappedInTheRecord`]
Every table with a time in it follows `retention_days` (default 90), except the state tables marked "kept". The purge runs
at most once a day, from whichever Trove process runs first (the gateway, a hook at the end of a session, or
`trove report`), so it no longer depends on the gateway [`TestEveryTableHasARetentionDecision`,
`TestPurgeDeletesOldRowsFromEveryAgedTable`; closes finding F-8 in [docs/security-findings.md](docs/security-findings.md)].
`trove uninstall --purge-data` deletes all of it.

### Other files in the data folder

| File or folder | Holds | Notes |
|---|---|---|
| `config.toml` | Your settings | |
| `changes.json` | A record of each edit Trove made to an agent file: **the path of that file** (such as `~/.claude/settings.json`, or a project's `.mcp.json`), where its backup is, a hash, and the value Trove wrote | Paths of config files, not of your source files |
| `backups/` | A **full copy of each agent config file as it was before Trove edited it** | May contain anything in that file, including API keys you had put there. Deleted when the change is undone; kept if the undo could not finish. |
| `recovery/` | The **full text** of command output, named by a hash. Written when `trove_run` runs a command you approved, and by the output trimmer if you turn it on | The one place raw tool output is written. Defaults: expires after 7 days, 200 MB cap. May contain secrets that appeared in that output, and is not scoped to a project (finding F-9). [`recovery` tests] |
| `hooks/` | Per-session lists of file **content hashes** and sizes (so a repeated read can be spotted), a spool of counts, optimizer states | No paths, no contents, no raw session ids [`TestNothingSensitiveIsStored`] |
| `hooks.log` (and `.1`) | One-line errors from hooks, with file names removed, redacted, capped at 256 KB | |
| `contribute.json` | Whether you turned the summary upload on, your random install id, and the day of the last upload | Written only by `trove contribute enable/disable/reset`; absent means off |
| `gateway.json` | The running gateway's process id and address | Removed on stop |
| `gateway.log` | Only on macOS, only if you install the background service: the gateway's own log lines | Redacted logger; no headers or bodies. Path is `~/.trove/gateway.log` even if you chose another data folder (finding F-24) |
| `mcp_installs.json` | The commands that undo MCP registrations Trove made | |
| `mcp_trust.json` | Hashes of approved `.trove/commands.toml` files, keyed by a hash of the project path | |
| `fixqueue.json` | Ids and file hashes of fixes waiting for your agent to be idle (for a settings profile, also its task name and whether unproven values were allowed) | |

Trove also **reads**, without changing them, your agents' local session logs (`~/.claude/projects`,
`~/.codex/sessions`) to estimate waste before you route traffic through the gateway. It keeps only the numbers
described in `history_requests` above. Pass `--no-history` or set `history.enabled = false` to stop that.
It reads `~/.claude/settings.json` for two key names (`apiKeyHelper`, `forceLoginMethod`) to tell API-key from
subscription use, and checks whether `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN` and `OPENAI_API_KEY` are set
without reading their values.

## Sharing a summary (optional, off by default)

`trove contribute` prints a small summary you may choose to share to help check Trove's savings claims, and
`trove contribute --export file.json` writes the same text to a file (permissions `0600`). Those two commands send
nothing.

`trove contribute enable` shows the exact text, asks, and only on yes records your consent in `contribute.json`.
From then on `trove report` (default period only, and never `trove gateway` or the agent hooks) sends that same text
to your `[contribute] endpoint` at most once every 7 days, within a 4 second limit, with no cookies or credentials,
following no redirects, and silently trying again later if it fails. The endpoint has a built-in default, but that
sends nothing by itself: without your yes there is no consent record, so no upload. Set `endpoint = ""` to make
sending impossible. `trove contribute disable` stops it at once. [`internal/contribute`
`TestAutoDoesNothingUnlessConsentedAndDue`, `internal/cli` `TestDefaultEndpointStillNeedsConsent`, `TestAutoSendsExactlyTheEncodedPayloadWithNoCredentials`,
`TestSendDoesNotFollowRedirects`; `internal/cli` `TestContributeSendsNothingUntilEnabled`]

The upload also carries a random **install id** (128 bits, made on your machine, not derived from anything about you
or your machine). It lets the receiver ignore repeats and delete your rows. `trove contribute reset` makes a new one.
The receiver ([server/worker](server/worker/README.md)) validates against a fixed list of fields, stores the summary
and the UTC day it arrived (no time, no IP address, no headers), keeps its own request logging switched off, accepts at
most 5,000 summaries a day, and deletes rows after 365 days. It runs on Cloudflare, which carries your connection
and may keep its own operational records of it (such as your IP address) under its own policies; the project cannot
see or delete those. If that matters to you, use `trove contribute --export` and send the file yourself. To have your rows
deleted sooner, send `DELETE <endpoint>/<install id>` (the id is shown by `trove contribute status`).

The summary holds: schema and Trove version, OS and architecture, the length of the period in days, a coarse
request-count bucket (`0`, `1-19`, `20-99`, `100-999`, `1000+`), the agents seen from a fixed list (`claude-code`,
`codex`, `other`), waste as a whole percent labeled "estimated", each optimizer's name, state and whether it was
rolled back, its measured percent drop in prompt tokens when there was enough data, and the number of guardrail
trips. It has no prompts, code, file paths, hashes, model names, dollar amounts, exact token counts, exact dates
or identifiers. [`internal/contribute` `TestBuildIsCoarseAndDeterministic`; `TestPayloadFieldsAreReviewed` fails
if a field is added without review]

## Your control

- `trove uninstall` restores every file Trove edited and removes its registrations; add `--purge-data` to
  delete what Trove created in the data folder (it leaves any file it did not create).
- `trove undo` reverses only the edits.
- `trove data export` copies everything Trove stores about your usage into a new folder (backups of your agent config
  files are never copied; the recovery store only with `--include-recovery`). `trove data delete` removes the usage
  data and keeps what `trove undo` needs. Both work offline and send nothing; if you ever shared a summary, `delete`
  prints the request that asks the receiver to remove it [`TestData*` in `internal/cli`].
- Delete `~/.trove` yourself at any time; nothing outside it depends on it.

## What is not covered here

Your provider's handling of the requests you send (see their terms), and your agent's own logs and telemetry.
Trove does not change either. See [docs/compliance.md](docs/compliance.md).
