-- Claude Code's plan-usage meter, as it passes it to the status line command on stdin
-- (code.claude.com/docs/en/statusline, verified 2026-11-23). One row per change for a session:
-- the status line writes a row only when a value differs from the session's previous row.
-- Numbers, a hashed session key, a model id and a version string only (PRINCIPLES.md rule 7).
-- NULL = Claude Code did not send that value.
CREATE TABLE limit_samples (
    id                  INTEGER PRIMARY KEY,
    ts                  INTEGER NOT NULL,  -- unix ms
    session_key         TEXT    NOT NULL,  -- hashed session id, as the gateway hashes it
    five_hour_pct       REAL,              -- rate_limits.five_hour.used_percentage, 0-100
    five_hour_resets_at INTEGER,           -- unix ms (Claude Code sends epoch seconds)
    seven_day_pct       REAL,              -- rate_limits.seven_day.used_percentage, 0-100
    seven_day_resets_at INTEGER,           -- unix ms
    cache_warm          INTEGER,           -- prompt_cache.warm: 1 or 0
    cache_ttl           INTEGER,           -- prompt_cache.ttl in seconds: 300 or 3600
    model               TEXT,              -- model.id, checked against a strict pattern
    cc_version          TEXT               -- Claude Code version, checked against a strict pattern
);
CREATE INDEX limit_samples_session ON limit_samples(session_key, ts);
