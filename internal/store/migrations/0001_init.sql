-- Initial schema. Metadata only: never prompt text, code, file paths or tool output.
-- Timestamps are Unix milliseconds (UTC). JSON columns hold small structured metadata.

CREATE TABLE requests (
    id                      INTEGER PRIMARY KEY AUTOINCREMENT,
    ts                      INTEGER NOT NULL,
    session_key             TEXT    NOT NULL DEFAULT '',   -- hash, never a raw identifier
    agent                   TEXT    NOT NULL DEFAULT '',
    agent_version           TEXT    NOT NULL DEFAULT '',
    provider                TEXT    NOT NULL DEFAULT '',
    model                   TEXT    NOT NULL DEFAULT '',
    endpoint                TEXT    NOT NULL DEFAULT '',   -- API route only, no query string
    streamed                INTEGER NOT NULL DEFAULT 0,
    status_code             INTEGER NOT NULL DEFAULT 0,
    latency_ms              INTEGER NOT NULL DEFAULT 0,
    ttft_ms                 INTEGER,                       -- NULL when not streamed
    input_uncached_tokens   INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens      INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens       INTEGER NOT NULL DEFAULT 0,
    output_tokens           INTEGER NOT NULL DEFAULT 0,
    reasoning_tokens        INTEGER NOT NULL DEFAULT 0,
    cost_usd                REAL,                          -- NULL when pricing is unknown
    pricing_version         TEXT    NOT NULL DEFAULT '',
    segment_token_estimates TEXT,                          -- JSON
    findings                TEXT,                          -- JSON
    optimizations_applied   TEXT,                          -- JSON
    shadow_savings          TEXT,                          -- JSON
    error_class             TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX requests_ts ON requests(ts);
CREATE INDEX requests_session ON requests(session_key);

CREATE TABLE sessions (
    session_key                 TEXT PRIMARY KEY,
    agent                       TEXT    NOT NULL DEFAULT '',
    started_at                  INTEGER NOT NULL,
    ended_at                    INTEGER,
    request_count               INTEGER NOT NULL DEFAULT 0,
    total_input_uncached_tokens INTEGER NOT NULL DEFAULT 0,
    total_cache_write_tokens    INTEGER NOT NULL DEFAULT 0,
    total_cache_read_tokens     INTEGER NOT NULL DEFAULT 0,
    total_output_tokens         INTEGER NOT NULL DEFAULT 0,
    total_cost_usd              REAL    NOT NULL DEFAULT 0,
    outcome_signals             TEXT                       -- JSON
);
CREATE INDEX sessions_started ON sessions(started_at);

CREATE TABLE optimizations (
    name       TEXT PRIMARY KEY,
    tier       TEXT    NOT NULL DEFAULT '',
    state      TEXT    NOT NULL DEFAULT 'off' CHECK (state IN ('off', 'shadow', 'on')),
    changed_at INTEGER NOT NULL,
    reason     TEXT    NOT NULL DEFAULT ''
);

CREATE TABLE measurements (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    optimizer   TEXT NOT NULL,
    "window"    TEXT NOT NULL,
    metric      TEXT NOT NULL,
    value       REAL NOT NULL,
    label       TEXT NOT NULL CHECK (label IN ('measured', 'estimated')),
    sample_size INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX measurements_optimizer ON measurements(optimizer);

CREATE TABLE budgets (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    period    TEXT    NOT NULL CHECK (period IN ('daily', 'weekly', 'monthly')),
    cap_usd   REAL    NOT NULL,
    starts_at INTEGER NOT NULL
);

CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
