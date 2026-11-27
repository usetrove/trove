-- What the user did after the cold-resume guard (hook_cold_resume, internal/hooks/coldresume.go) stopped a prompt.
-- hook_events.kind gains two values, both counts and nothing else:
--   overridden  the user sent a prompt again after the guard blocked one, so the session went on and paid the rebuild
--   cleared     the session ended with /clear after the guard blocked a prompt and before any prompt went through
-- SQLite cannot change a CHECK constraint in place, so the table is rebuilt, as in 0012 and 0018.
CREATE TABLE hook_events_new (
    id          INTEGER PRIMARY KEY,
    ts          INTEGER NOT NULL,
    session_key TEXT    NOT NULL,
    hook        TEXT    NOT NULL,
    kind        TEXT    NOT NULL CHECK (kind IN ('blocked', 'would_block', 'advised', 'would_advise', 'reread_anyway', 'rewritten', 'would_rewrite', 'reissued', 'fetched_back', 'extra_read', 'adopted', 'overridden', 'cleared')),
    bytes       INTEGER NOT NULL DEFAULT 0,
    tokens      INTEGER NOT NULL DEFAULT 0
);
INSERT INTO hook_events_new (id, ts, session_key, hook, kind, bytes, tokens)
    SELECT id, ts, session_key, hook, kind, bytes, tokens FROM hook_events;
DROP TABLE hook_events;
ALTER TABLE hook_events_new RENAME TO hook_events;
CREATE INDEX hook_events_session ON hook_events (session_key);
CREATE INDEX hook_events_hook_ts ON hook_events (hook, ts);
