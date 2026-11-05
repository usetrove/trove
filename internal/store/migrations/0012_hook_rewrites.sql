-- Hook rewrites (internal/hooks). Metadata only, as in 0008.
-- hook_events.kind gains two values for the optimizers that rewrite a tool call
-- or its output:
--   rewritten      the call's input or the tool's output was changed
--   would_rewrite  shadow mode: it would have been changed, nothing was
--   reissued       the agent ran a quieted command again exactly as first written
--   fetched_back   the agent called trove_fetch_full for output a hook trimmed
-- SQLite cannot change a CHECK constraint in place, so the table is rebuilt.
CREATE TABLE hook_events_new (
    id          INTEGER PRIMARY KEY,
    ts          INTEGER NOT NULL,
    session_key TEXT    NOT NULL,
    hook        TEXT    NOT NULL,
    kind        TEXT    NOT NULL CHECK (kind IN ('blocked', 'would_block', 'advised', 'would_advise', 'reread_anyway', 'rewritten', 'would_rewrite', 'reissued', 'fetched_back')),
    bytes       INTEGER NOT NULL DEFAULT 0,
    tokens      INTEGER NOT NULL DEFAULT 0
);
INSERT INTO hook_events_new (id, ts, session_key, hook, kind, bytes, tokens)
    SELECT id, ts, session_key, hook, kind, bytes, tokens FROM hook_events;
DROP TABLE hook_events;
ALTER TABLE hook_events_new RENAME TO hook_events;
CREATE INDEX hook_events_session ON hook_events (session_key);
CREATE INDEX hook_events_hook_ts ON hook_events (hook, ts);
