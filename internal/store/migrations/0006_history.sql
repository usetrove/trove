-- Agent session history (internal/logs). Metadata only: counts, sizes, hashes,
-- model names and timestamps. Never prompt text, code, paths or tool output.
-- Kept apart from requests so gateway figures stay measured and history
-- figures stay labelled as estimated.

-- One row per history file that has been read. file_key is a hash of the path,
-- so the path itself is never stored. offset is the byte position after the
-- last complete line that was read; the next scan starts there.
CREATE TABLE history_files (
    file_key       TEXT    PRIMARY KEY,
    source         TEXT    NOT NULL,           -- claude_code | codex
    size           INTEGER NOT NULL,
    mtime          INTEGER NOT NULL,           -- unix ms
    offset         INTEGER NOT NULL,
    parser_version INTEGER NOT NULL,
    format_version TEXT    NOT NULL DEFAULT '', -- agent version seen in the file
    lines          INTEGER NOT NULL DEFAULT 0,
    turns          INTEGER NOT NULL DEFAULT 0,
    skipped_lines  INTEGER NOT NULL DEFAULT 0,  -- unparseable, counted not stored
    updated_at     INTEGER NOT NULL
);

-- One row per provider request found in a history file.
--   session_key   same hash the gateway uses for Claude Code, so overlap with
--                 gateway data can be found without raw ids
--   request_key   hash of source + provider request id; makes re-scans idempotent
--   cost_usd      list-price cost (estimated); NULL when unpriced
--   plan          api_key | subscription | unknown
--   tool_names / findings / shadow_savings are small JSON documents of names,
--   counts and token numbers.
CREATE TABLE history_requests (
    request_key       TEXT    PRIMARY KEY,
    source            TEXT    NOT NULL,
    session_key       TEXT    NOT NULL,
    ts                INTEGER NOT NULL,         -- unix ms
    model             TEXT    NOT NULL DEFAULT '',
    input_tokens      INTEGER NOT NULL DEFAULT 0, -- uncached input
    cache_write_5m    INTEGER NOT NULL DEFAULT 0,
    cache_write_1h    INTEGER NOT NULL DEFAULT 0,
    cache_write_other INTEGER NOT NULL DEFAULT 0, -- writes with no TTL split
    cache_read        INTEGER NOT NULL DEFAULT 0,
    output_tokens     INTEGER NOT NULL DEFAULT 0,
    cost_usd          REAL,
    pricing_status    TEXT    NOT NULL DEFAULT '',
    plan              TEXT    NOT NULL DEFAULT 'unknown',
    sidechain         INTEGER NOT NULL DEFAULT 0,
    tool_names        TEXT,
    tool_result_bytes INTEGER NOT NULL DEFAULT 0,
    errors            INTEGER NOT NULL DEFAULT 0,
    findings          TEXT,
    shadow_savings    TEXT
);

CREATE INDEX history_requests_ts      ON history_requests (ts);
CREATE INDEX history_requests_session ON history_requests (session_key);
