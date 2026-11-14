# What Trove stores, what it sends, and for how long

This page lists every field Trove keeps on your computer, every field it sends if you opt in, and every
field a team edition would hold (not built yet). It is checked against the code: a test
(`internal/store/datadoc_test.go`) fails if a table or column in the database is missing from this page,
or listed here and not in the database, or has no stated retention.

Last checked against the code and schema version 17 on 2026-11-06.

## The rules behind it

- **Local first.** Everything is in one folder, `~/.trove` (or `TROVE_DATA_DIR`), readable only by you
  (folder 0700, files 0600). On Windows the folder inherits your profile's permissions.
- **Metadata only.** Counts, sizes, hashes, model names, timings and outcome labels. Never prompt text,
  code, file contents, file paths, repo names, ticket titles, commands, tool output, API keys or any
  login or session token. Names that identify you or your project are stored only as salted or plain
  hashes. `internal/privacy/leak_test.go` runs real flows with canary strings for each of these and
  fails if any appears in the database, the data folder, or anything Trove prints for sharing.
- **Nothing is sent by default.** The only data that can leave your computer is the summary described in
  [Sent only if you opt in](#sent-only-if-you-opt-in). Forwarding your own requests to the provider you
  configured is not Trove sending data; Trove does not keep or change your key.
- **Retention.** `retention_days` (default **90 days**, 1 to 3650, in `config.toml` or
  `TROVE_RETENTION_DAYS`) applies to every table marked "deleted after retention_days" below. It runs at
  most once a day, from whichever Trove process runs first: the gateway, a hook at the end of a session, or
  `trove report`. It does not need the gateway. Tables marked "kept" hold current settings or state, not
  history.
- **Your controls.** `trove data export` copies it all to a folder; `trove data delete` removes the usage
  data; `trove uninstall --purge-data` removes everything Trove created. All work offline.

## Files in the data folder

| File or folder | What it holds | Retention |
|---|---|---|
| `trove.db` (+ `-wal`, `-shm`) | The database described below | Per table, below |
| `config.toml` | Your Trove settings (retention, budgets, optimizer states, the sharing endpoint) | Kept until you delete it |
| `changes.json`, `fixqueue.json` | Records of the edits Trove made to your agents' own files, so `trove undo` can reverse them | Kept until undone or uninstalled |
| `backups/` | Full copies of agent config files from before Trove edited them. **Can contain keys you put in those files**, so they are never exported and never sent | Kept until undone or uninstalled |
| `mcp_installs.json`, `mcp_trust.json`, plugin state file | Where Trove's MCP server and plugin were registered, and which commands you trusted | Kept until uninstalled |
| `hooks/` | Per-session hook journals (content hashes and sizes of files read, never the files or their names), feature lines, the counter spool (capped at 4 MB), `config.json` (optimizer states), `capabilities.json` | Journals are removed at session end and swept after a week. `config.json` and `capabilities.json` are setup state and are kept by `trove data delete`, since deleting them would quietly return the hooks to their defaults; the rest is removed by it |
| `hooks.log`, `hooks.log.1` | Redacted hook errors, capped at 256 KB | Rotated at the cap |
| `recovery/` | Full raw output of commands you ran through `trove_run`, kept so a trimmed result can be fetched back. **Can contain anything a command printed.** Opt-in; local; size-capped | 7 days, 200 MB cap, then deleted |
| `contribute.json` | Whether sharing is on, your random install id, the date of the last upload | Kept until you disable and delete it; removed by `trove data delete` and `--purge-data` |
| `autopilot.json` | Whether you agreed to Autopilot, the policy name, and for each optimizer Autopilot manages: its state (off, shadow, candidate, on, rolled_back), the time it entered that state, a short plain-English reason and a try count; the last 50 state changes; the planner's latest advice per optimizer (verdict, shadow session count, estimated net tokens, a reason); and the time of the last desktop notice. No prompts, paths or credentials | Kept until you run `trove autopilot off`; removed by `--purge-data`. Written only by `trove autopilot`, `trove setup` (with your yes) and Autopilot's own evaluation |
| `gateway.json` | Process id and loopback address of a running gateway | Removed when the gateway stops |
| `gateway.log` | Gateway service output on macOS | Kept until deleted |

Trove also writes files you ask for: share cards (`trove share`) and `trove contribute --export`, to the
place you choose.

## The local database

Timestamps are Unix milliseconds in UTC unless a column says otherwise. "Hash" means a one-way hash:
Trove cannot recover the original, and uses it only to tell things apart.

### `requests`

One row per request that passed through the gateway (API-key users only).

| Column | Meaning |
|---|---|
| `id` | Row number |
| `ts` | When the request was made |
| `session_key` | Hash of the agent's session id; groups requests into a session |
| `agent` | Which agent sent it (a label such as claude-code or codex) |
| `agent_version` | The agent's version from its user-agent header |
| `provider` | Which provider API it went to |
| `model` | Model name the provider reported |
| `endpoint` | API route only, no query string |
| `streamed` | 1 if the reply was streamed |
| `status_code` | HTTP status of the reply |
| `latency_ms` | Total time to answer |
| `ttft_ms` | Time to the first streamed token, empty when not streamed |
| `input_uncached_tokens` | Input tokens not served from cache, as reported by the provider |
| `cache_write_tokens` | Tokens written to the provider's cache |
| `cache_read_tokens` | Tokens read from the provider's cache |
| `output_tokens` | Output tokens |
| `reasoning_tokens` | Reasoning tokens, when the provider reports them |
| `cost_usd` | List-price cost of the reported tokens (estimated); empty when the model has no price |
| `pricing_version` | Which price table was used |
| `segment_token_estimates` | JSON: estimated token counts per part of the prompt (system, tools, history, ...), numbers only |
| `findings` | JSON: waste patterns detected, as names and counts |
| `optimizations_applied` | JSON: which optimizers changed the request and by how much, as names and numbers |
| `shadow_savings` | JSON: what shadow-mode optimizers estimate they would have saved |
| `error_class` | A short fixed label for a failed request, such as `http_429`, `client_disconnected` or `usage_unavailable`; empty when it succeeded |
| `usage_raw` | The provider's usage block exactly as reported: numbers and a tier label, never text |
| `service_tier` | The service tier the provider says it used |
| `tokens_removed` | Estimated tokens an optimizer removed |
| `cost_diff_usd` | Estimated dollars those tokens were worth; empty if it could not be priced exactly |
| `signals` | JSON: outcome signals as integer counts (retries, repeated fetches), never names or output |
| `kind` | The label you chose for the session with the `X-Trove-Kind` header (for example `automated`), `unknown` if you set none |
| `upstream` | Which configured upstream served it |

**Retention:** deleted after `retention_days`.

### `sessions`

One row per agent session seen by the gateway.

| Column | Meaning |
|---|---|
| `session_key` | Hash of the session id |
| `agent` | Which agent |
| `started_at` | First request time |
| `ended_at` | Last request time, empty while running |
| `request_count` | Number of requests |
| `total_input_uncached_tokens` | Sum over the session |
| `total_cache_write_tokens` | Sum over the session |
| `total_cache_read_tokens` | Sum over the session |
| `total_output_tokens` | Sum over the session |
| `total_cost_usd` | Sum of estimated list-price cost |
| `outcome_signals` | JSON: integer counts of outcome signals |

**Retention:** deleted after `retention_days`.

### `session_pins`

The optimizer settings fixed for a session when it started (so nothing changes mid-session).

| Column | Meaning |
|---|---|
| `session_key` | Hash of the session id |
| `states` | JSON: each optimizer's state (off, shadow or on) for this session |
| `custom_agents` | JSON: agent labels treated as custom agents |
| `bytes_per_token` | The bytes-per-token ratio used to estimate tokens |
| `pinned_at` | When the settings were fixed |

**Retention:** deleted after `retention_days`.

### `optimizations`

| Column | Meaning |
|---|---|
| `name` | Optimizer name |
| `tier` | Its safety tier |
| `state` | off, shadow or on |
| `changed_at` | When the state last changed |
| `reason` | Why, in plain English written by Trove (for example a guardrail rollback) |

**Retention:** kept: the current state of each optimizer, one row each.

### `measurements`

| Column | Meaning |
|---|---|
| `id` | Row number |
| `optimizer` | Which optimizer |
| `window` | The time window measured |
| `metric` | What was measured |
| `value` | The number |
| `label` | measured or estimated |
| `sample_size` | How many sessions it rests on |

**Retention:** kept: the latest computed windows, replaced as they are recomputed.

### `budgets`

| Column | Meaning |
|---|---|
| `id` | Row number |
| `period` | daily, weekly or monthly |
| `cap_usd` | Your own spending cap |
| `starts_at` | When the cap period starts |

**Retention:** kept: your own budget settings.

### `settings`

| Column | Meaning |
|---|---|
| `key` | Setting name (the install time, the random salt used to hash repo names, the last retention run) |
| `value` | Its value |

**Retention:** kept: install settings. The salt never leaves the computer.

### `guardrail_events`

Written when an optimizer's safety check trips and Trove rolls it back.

| Column | Meaning |
|---|---|
| `id` | Row number |
| `ts` | When it tripped |
| `optimizer` | Which optimizer |
| `rule` | Which rule (for example retries or refetch) |
| `observed` | The measured value that tripped it |
| `threshold` | The limit |
| `sample_on` | Sessions the optimizer acted on |
| `sample_ref` | Comparison sessions |
| `reference` | What it was compared with |
| `reason` | Plain-English explanation written by Trove |
| `cleared_at` | When you lifted the rollback, empty if not |

**Retention:** deleted after `retention_days`.

### `usage_daily`

| Column | Meaning |
|---|---|
| `day` | Local calendar date, YYYY-MM-DD |
| `requests` | Requests that day |
| `unpriced` | Requests with no known price |
| `spend_usd` | Estimated list-price spend |
| `saved_usd` | Estimated saving from optimizers that were on |

**Retention:** deleted after `retention_days` (by date).

### `receipts`

One row per finished session, shown by `trove receipts` and the status line.

| Column | Meaning |
|---|---|
| `session_key` | Hash of the session id |
| `agent` | Which agent |
| `ended_at` | Time of the last request |
| `model` | Model used for most calls |
| `models` | How many different models the session used |
| `calls` | Number of calls |
| `cost_usd` | Estimated list-price cost |
| `unpriced` | Calls left out of the cost |
| `cache_hit_rate` | Cache reads over input tokens, measured |
| `saved_usd` | Estimated saving |

**Retention:** deleted after `retention_days`.

### `history_files`

Checkpoints for reading your agents' own session logs (read-only), so each file is read once.

| Column | Meaning |
|---|---|
| `file_key` | Hash of the log file's path; the path itself is not stored |
| `source` | Which agent's log (claude_code or codex) |
| `size` | File size when last read |
| `mtime` | File modification time |
| `offset` | Byte position after the last complete line read |
| `parser_version` | Version of Trove's reader |
| `format_version` | The agent version seen in the file |
| `lines` | Lines read |
| `turns` | Turns found |
| `skipped_lines` | Lines that could not be parsed (counted, not stored) |
| `updated_at` | When this checkpoint was written |

**Retention:** kept: a checkpoint of a path hash and offsets, so that history already purged is not read again. Removed by `trove data delete`.

### `history_requests`

One row per provider request found in an agent's session log (estimates, since there is no gateway).

| Column | Meaning |
|---|---|
| `request_key` | Hash of the source and the provider's request id; makes re-reads idempotent |
| `source` | claude_code or codex |
| `session_key` | Hash of the session id |
| `ts` | Request time |
| `model` | Model name |
| `input_tokens` | Uncached input tokens |
| `cache_write_5m` | Cache-write tokens with a 5-minute lifetime |
| `cache_write_1h` | Cache-write tokens with a 1-hour lifetime |
| `cache_write_other` | Cache-write tokens with no lifetime split |
| `cache_read` | Cache-read tokens |
| `output_tokens` | Output tokens |
| `cost_usd` | Estimated list-price cost |
| `pricing_status` | Whether a price was found |
| `plan` | api_key, subscription or unknown, inferred from the log |
| `sidechain` | 1 for a sub-agent request |
| `tool_names` | JSON: names of tools called (for example Read, Bash), no inputs |
| `tool_result_bytes` | Total size of tool results |
| `errors` | Count of errors |
| `findings` | JSON: waste patterns as names and counts |
| `shadow_savings` | JSON: estimated savings as numbers |

**Retention:** deleted after `retention_days`.

### `fixes`

Config changes Trove made at your request (`trove fix`, `trove settings`, `trove tasks`).

| Column | Meaning |
|---|---|
| `change_id` | Id of the matching record in `changes.json` |
| `fix_id` | Which fix |
| `kind` | The kind of fix |
| `agent` | Which agent's config it changed |
| `path_hash` | Hash of the file path; the path is not stored |
| `applied_at` | When it was applied |
| `reverted_at` | When it was undone, empty while in place |
| `est_tokens` | Estimated prompt tokens saved per session |
| `est_usd` | Estimated dollars per session, empty if unpriced |
| `label` | measured or estimated |

**Retention:** deleted after `retention_days` once reverted. A fix still in place is kept: it is state, not history.

### `settings_profiles`

| Column | Meaning |
|---|---|
| `change_id` | Same id as `fixes.change_id` |
| `fix_id` | Which fix |
| `task` | Task type (quick-fix, feature, refactor, exploration) |
| `harness` | Which agent |
| `keys` | JSON: the names of the settings changed, not their values |
| `harness_version` | The agent's version |
| `catalog_version` | Version of Trove's settings catalog |

**Retention:** deleted after `retention_days` together with its reverted fix; kept while the fix is in place.

### `task_installs`

| Column | Meaning |
|---|---|
| `change_id` | Same id as `fixes.change_id` |
| `fix_id` | Which fix |
| `feature` | task_scout or task_cascade |
| `role` | Role of the file written (scout, worker, escalate, cascade, snippet) |
| `scout_model` | Model alias chosen for the scout |
| `worker_model` | Model alias chosen for the worker |
| `escalation_model` | Model alias chosen for escalation |
| `overhead_request` | Estimated tokens it adds to every request |
| `overhead_use` | Estimated tokens it adds each time it is used |

**Retention:** deleted after `retention_days` together with its reverted fix; kept while the fix is in place.

### `external_tools`

Third-party token tools you approved Trove to install.

| Column | Meaning |
|---|---|
| `name` | Tool name |
| `version` | Pinned version |
| `license` | Its license |
| `state` | on or off |
| `installed_at` | When installed |
| `license_ack_at` | When you confirmed the license notice |
| `changes` | JSON: ids of the changes written for it, so removing it can undo them |

**Retention:** kept while the tool is installed.

### `experiment_assignments`

Which arm (on or off) a session was randomly given in a session-level A/B test.

| Column | Meaning |
|---|---|
| `session_key` | Hash of the session id |
| `optimizer` | Which optimizer |
| `arm` | on or off |
| `stratum` | A hashed label naming the repo (by hash, never a path) and the task type |
| `seq` | Position within the stratum, for reproducible blocks |
| `assigned_at` | When assigned |

**Retention:** deleted after `retention_days`.

### `experiment_results`

| Column | Meaning |
|---|---|
| `id` | Row number |
| `optimizer` | Which optimizer |
| `computed_at` | When the analysis ran |
| `metric` | cost_per_session, turns, tool_errors or abandoned |
| `value` | Relative change, 0.05 = 5% higher with the optimizer on |
| `low` | Lower end of the 95% interval |
| `high` | Upper end of the 95% interval |
| `confidence` | high, medium or low |
| `n_on` | Sessions with the optimizer on |
| `n_off` | Sessions with it off |
| `label` | measured or estimated |
| `status` | Outcome of the check |
| `inputs_hash` | Hash of the inputs, so a re-run can be compared |

**Retention:** deleted after `retention_days`.

### `hook_events`

One row per decision made by Trove's agent hooks.

| Column | Meaning |
|---|---|
| `id` | Row number |
| `ts` | When |
| `session_key` | Hash of the session id |
| `hook` | Which optimizer (for example hook_repeat_read) |
| `kind` | What happened: blocked, would_block, advised, would_advise, reread_anyway, rewritten, would_rewrite, reissued or fetched_back |
| `bytes` | Size of the file or output involved |
| `tokens` | Estimated tokens kept out of the context, 0 when unknown |

**Retention:** deleted after `retention_days`.

### `adapter_events`

Decisions and usage reports from harness plugins (such as the OpenCode plugin).

| Column | Meaning |
|---|---|
| `id` | Row number |
| `ts` | When |
| `session_key` | Hash of the session id |
| `harness` | Which tool the plugin runs in |
| `login` | api_key, copilot or subscription_or_unknown, classified from non-secret fields; Trove never sees a credential |
| `provider` | Provider name |
| `model` | Model name |
| `kind` | The decision kinds of hook_events, plus usage and outcome |
| `optimizer` | Optimizer name, empty for usage and outcome rows |
| `state` | shadow or on, empty otherwise |
| `bytes` | Size involved |
| `tokens` | Estimated tokens kept out of the context |
| `overhead_tokens` | Tokens the optimizer itself added to the context |
| `in_tokens` | Input tokens the harness reported (usage rows), else empty |
| `out_tokens` | Output tokens the harness reported |
| `cache_read_tokens` | Cache-read tokens the harness reported |
| `cache_write_tokens` | Cache-write tokens the harness reported |
| `cost_usd` | Cost the harness reported |
| `detail` | A short fixed label for outcome rows (tool_error, retry, abort, session_end) |

**Retention:** deleted after `retention_days`.

### `repo_features`

What kind of repo you work in, as a few fixed labels.

| Column | Meaning |
|---|---|
| `repo_key` | Salted, truncated hash of the working folder; the name is never stored |
| `computed_at` | When computed |
| `size_bucket` | S, M, L or XL |
| `lang_mix` | Up to three `language:percent` pairs, for example go:70,python:30 |
| `test_framework` | A name from a fixed list, or unknown |

**Retention:** deleted after `retention_days` (it is recomputed when needed).

### `session_features`

| Column | Meaning |
|---|---|
| `session_key` | Hash of the session id |
| `repo_key` | Salted hash of the working folder |
| `recorded_at` | When the session ended |
| `files_read` | Count of files read |
| `files_edited` | Count of files edited |
| `repeat_reads` | Files read again unchanged |
| `scout_calls` | Scout sub-agent calls |
| `compactions` | Context compactions |
| `retries` | Repeated identical tool calls |
| `tool_results` | Number of tool results |
| `large_results` | Results over 6,000 bytes |
| `avg_dup_ratio` | Average share of repeated content in results |
| `error_share` | Share of results that were errors |
| `stack_share` | Share of results that were stack traces |

**Retention:** deleted after `retention_days`.

### `repo_tuning`

| Column | Meaning |
|---|---|
| `repo_key` | Salted hash of the working folder |
| `optimizer` | Which optimizer |
| `param` | Name of the tunable parameter (for example keep_tail) |
| `value` | The number |
| `state` | suggested, applied or reverted |
| `tuned_at` | When |
| `source` | Where the value came from (local) |
| `before_rate` | Share of trimmed outputs fetched back before it was applied |
| `n_before` | How many outputs that rests on |

**Retention:** kept: the tuning in force for a repo, until `trove tune reset` or `trove data delete`.

### `schema_migrations`

| Column | Meaning |
|---|---|
| `version` | Schema version number |
| `applied_at` | When it was applied |

**Retention:** kept: the schema history.

## Sent only if you opt in

`trove contribute enable` shows the exact text and the address first and asks. Until you run it, nothing
is sent: `trove contribute` only shows or saves the summary. If you enable it, Trove sends the summary at
most once every 7 days (when you run `trove report`), to the address shown, with no cookies or
credentials, a 4-second timeout and no redirects. A missing or damaged consent record means off.

The summary is exactly these fields (`internal/contribute/payload.go`; a golden file and a test fail if
one is added without review):

| Field | Meaning |
|---|---|
| `schema_version` | Format version |
| `trove_version` | The Trove version |
| `os`, `arch` | Operating system and CPU type |
| `period_days` | Length of the period summarised (not the dates) |
| `requests_bucket` | A coarse range: 0, 1-19, 20-99, 100-999 or 1000+ |
| `agents` | Which agents were used, from a fixed list: claude-code, codex, other |
| `waste_percent` | Whole-number percent of tokens detected as waste, with a measured/estimated label |
| `optimizers[]` | Per optimizer: name, whether it was effective, whether it was rolled back, and the measured percent fewer tokens when known |
| `guardrail_trips` | How many times a safety check rolled something back |
| `install_id` | A random 128-bit identifier with no link to you or your computer; it lets the receiver drop repeats and delete your rows on request |

No prompts, code, file paths, hashes, model names, dollar amounts or exact token counts. **Retention at the receiver** (per `server/worker/README.md`): it keeps one row per install id per day, keeps no IP address
(the Worker discards it), applies a daily cap, and deletes rows after 365 days. You can ask it to delete
yours with `trove contribute delete`, which shows the exact request (`DELETE <endpoint>/<install id>`), asks, sends it, and
then switches sharing off and removes your local id. It has no body, credentials or cookies and follows no redirects.
It is the only thing that ever sends that request: nothing runs it automatically, and `trove data delete` only reminds
you to run it first (CLAUDE.md rule 1, part c).

## Never stored or sent

Prompt text and model replies; code and file contents; file names, folder names and repo names (hashes
and salted hashes aside); ticket, issue or branch titles; shell commands and their output (except the
opt-in `recovery/` store, which stays on your computer); API keys, bearer tokens, cookies and any
subscription, OAuth or session credential (rule 3 and 10: the gateway rejects them before reading the
request); your name, email or IP address.

## Team edition (not built)

There is no team or organisation feature in Trove today and nothing in this section is stored or sent.
It describes the design any future team edition has to meet, so the questions can be reviewed first.
**Employee-monitoring law varies and this is for lawyer review, not legal advice.**

| Proposed field | Purpose | Proposed retention |
|---|---|---|
| Pseudonymous member id (random, per install, no name or email) | Count distinct users in a group | Admin-set, at most 365 days |
| Day (not time of day) | Trends | Same |
| Requests bucket, tokens and estimated cost per day | Spend by group | Same |
| Model names and optimizer names with measured/estimated savings | See which settings help | Same |
| Group label chosen by the admin | Aggregation unit | Same |

Rules the design must keep:

- **Aggregated by default.** The admin view shows groups, not individuals. A group is shown only when it
  has at least a minimum number of members (proposed: 5), so a "group" cannot be one person.
- **No content, ever.** The same never-stored list as above. No per-person prompts, paths, repo names,
  ticket titles or session logs.
- **Individual views are opt-in by the individual.** A person sees their own numbers locally; an admin
  never sees one person's numbers unless that person chooses to share them.
- **Notice and consent.** Every member sees what is shared before it is shared. Regional notes, for a lawyer to
  check, are in [privacy-law.md](privacy-law.md): employee monitoring is regulated differently in Canada,
  the EU and UK, and individual US states (notice, consent, proportionality, works-council or union
  involvement).
- **New outbound call.** Sending anything to a team server needs a new rule in CLAUDE.md first (rule 1).

## Known gaps

- The Windows data folder has no Trove-set permissions; it relies on your profile's.
- `history_files` keeps a path hash and offsets until `trove data delete`, by design (see its retention).
- `trove contribute delete` asks the receiver to delete your rows but cannot check that it did; the receiver's own
  documentation is the only statement of what it keeps.
