# Derived features (internal/features)

Trove computes a few numbers about your sessions on your own device so that the
recommender ([recommend.md](recommend.md)) can say which optimizations suit a repo.
This page says exactly what is computed, what is stored, and what is never kept.

## The rule

A feature is **a number, a true/false value, or a value from a small fixed list**.
It cannot hold text from your content. Tool output, code, file names, paths and
commands are read in memory, reduced to these values and dropped (CLAUDE.md rules
7 and 12). Nothing here leaves your device; features are not part of
`trove contribute`.

Two tests enforce it, and both run in CI's privacy job:

- `TestNoFeatureCanHoldText` walks every field of every feature type by reflection
  and fails on any field that is not an integer, a float, a boolean or a named
  closed category (file type, size bucket, test framework).
- Sentinel tests plant a marker string in the content, in file names and in
  `package.json`, run the extractors and the hooks, and fail if the marker or the
  repo folder shows up in the database, the journal or any JSON output.

## What is computed

**Per tool result** (`features.ToolResult`)

| Feature | Meaning |
|---|---|
| `size_bytes`, `lines` | size of the output |
| `dup_line_ratio` | share of non-blank lines that repeat an earlier line (0 to 1) |
| `has_error`, `has_stack_trace` | a failure line, or a stack trace, is present |
| `has_tests`, `pass_share` | the output has test-result lines; passing / (passing + failing) |
| `file_type` | go, python, javascript, typescript, rust, java, ruby, c_cpp, markdown, data, shell, other, unknown (from the extension; the path is dropped) |

Extraction looks at no more than 256 KiB and 4,000 lines of one output
(`truncated` says if it stopped early). Lines are cheap-filtered by substring before
any regex runs, and a differential test checks the filter against the plain
regexes on 60,000 generated lines.

**Per session** (`features.SessionFeatures`)

`files_read`, `files_edited` (distinct files), `repeat_reads`, `scout_calls`,
`compactions`, `retries`, `tool_results`, `large_results` (over 6,000 bytes),
`avg_dup_ratio`, `error_share`, `stack_share`.

- *Scout calls* are the session's subagent ("sidechain") requests found in the
  history Trove has read, so they are requests, not calls, and only those already
  ingested at session end.
- *Retries* are a tool call identical to the one before it, made after that one's
  output had an error. Claude Code's `PostToolUse` fires only for successful calls,
  so this is an approximation.

**Per repo, bucketed** (`features.Repo`)

Size bucket (S under 200 files, M under 2,000, L under 20,000, XL beyond or if the
scan hit its limit), up to three languages with whole-percent shares, and the test
framework (go_test, pytest, jest, vitest, mocha, cargo_test, junit, rspec or
unknown) found from marker files in the repo root. The scan skips `.git`,
`node_modules`, `vendor` and similar, stops at 20,000 files or 500 ms, and runs at
most once a week per repo.

## How the hooks collect them

Each hook call appends one short numeric line to
`<data dir>/hooks/sessions/<key>.feat`: hashes and numbers only, capped at 256 KB,
write errors ignored (rule 4). Nothing is read back until `SessionEnd`, which sums
the journal, writes one `session_features` row, refreshes `repo_features` if stale
and deletes the journal.

| Hook | Records |
|---|---|
| `post_tool_read_record` | a read (path hash) |
| `post_tool_edit_record` | an edit (path hash); registered for `Edit`, `Write`, `MultiEdit`, `NotebookEdit` |
| `post_tool_output` | the tool result's features (needs `--rewrite`) |
| `session_reset` on `PreCompact` | a compaction |

The repo is identified by `features.RepoKey`: a truncated SHA-256 of the working
directory under a random per-install salt kept in your `settings` table. The
folder itself is never stored.

## What is stored

Tables `repo_features` and `session_features` (migration 0016), described in
[PRIVACY.md](../PRIVACY.md). `session_features` follows `retention_days`.
`TestSchemaIsMetadataOnly` pins their columns.

## Time budget

Measured on the development machine (AMD Ryzen 5 5500U, Windows, Go benchmarks in
`internal/features`):

| Case | Time |
|---|---|
| a typical command output (about 2 KB) | about 0.12 ms |
| an output at the scan limit (4,000 lines) | about 6 ms |

An earlier version without the substring filter took about 0.56 ms and 69 ms for
the same two cases. The hook's own deadline for `post_tool_output` is 250 ms, and
extraction failures are ignored. I have not measured the whole hook process end to
end; on Windows, antivirus rescans make the per-call file append cost about 17 ms
regardless of size (see docs/hooks-verification.md), so the budget is not enforced
there.

## Token overhead (rule 15)

None. Features inject no text into any request and add no tool definitions.

## Verification log (rule 8)

| What | Source | Checked |
|---|---|---|
| `cwd` is a common input field of every hook event | code.claude.com/docs/en/hooks, "Common input fields" table | 2026-11-05 |
| `SubagentStart`, `SubagentStop`, `PreCompact`, `PostCompact` exist | the same page, event table | 2026-11-05 |

The fetch tool returned the first part of that page as raw text and later parts as
a model summary. The common-fields table was in the raw part. The per-event input
fields of the four events above were not read verbatim, so Trove does not depend
on them: compactions come from `PreCompact` reaching `session_reset` (already in
use), and scout work from history, not from `SubagentStart`.

## Known gaps

- Codex CLI gets only session-level features: it has no read or edit hooks.
- Tool-result features exist only when `post_tool_output` is registered
  (`trove hooks install --rewrite`).
- The edit hook reaches existing installs only after `trove hooks install` is run
  again (the plugin picks it up with its next release).
- The repo key hashes the cleaned path string, so the same repo reached through a
  symlink, or with different letter case, counts as a different repo.
