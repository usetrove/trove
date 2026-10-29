-- Config fixes applied by `trove fix` (see docs/fixer.md). Metadata only: the
-- file is identified by a hash of its path, never the path or its contents.
-- The split between "before" and "after" sessions for measurement is applied_at.
CREATE TABLE fixes (
    change_id   TEXT PRIMARY KEY,                 -- id in changes.json
    fix_id      TEXT NOT NULL,
    kind        TEXT NOT NULL,                    -- mcp_prune, context_slim, cache_breaker_fix, model_default
    agent       TEXT NOT NULL DEFAULT '',
    path_hash   TEXT NOT NULL DEFAULT '',
    applied_at  INTEGER NOT NULL,                 -- unix ms
    reverted_at INTEGER,                          -- unix ms; NULL while the fix is in place
    est_tokens  INTEGER NOT NULL DEFAULT 0,       -- estimated prompt tokens saved per session
    est_usd     REAL,                             -- estimated dollars per session; NULL when unpriced
    label       TEXT NOT NULL DEFAULT 'estimated' CHECK (label IN ('measured', 'estimated'))
);
CREATE INDEX fixes_applied ON fixes (applied_at);
