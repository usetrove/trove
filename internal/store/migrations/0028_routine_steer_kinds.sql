-- What hook_routine_steer (internal/hooks/routinesteer.go) records. hook_events.kind gains four values, all counts:
--   steered      a routine prompt got the steering text (tokens = the declared overhead, estimated)
--   would_steer  a routine prompt that would have got it: shadow mode, the trial's "off" arm, or before the live check
--   reprompted   the prompt right after a routine one read as a correction of the reply ("no", "try again")
--   turn_end     the prompt right after a routine one: the routine turn ended here (its window closes)
-- Only timestamps, the hashed session key and these words are stored; never the prompt.
-- SQLite cannot change a CHECK constraint in place, so the table is rebuilt, as in 0012, 0018 and 0027.
CREATE TABLE hook_events_new (
    id          INTEGER PRIMARY KEY,
    ts          INTEGER NOT NULL,
    session_key TEXT    NOT NULL,
    hook        TEXT    NOT NULL,
    kind        TEXT    NOT NULL CHECK (kind IN ('blocked', 'would_block', 'advised', 'would_advise', 'reread_anyway', 'rewritten', 'would_rewrite', 'reissued', 'fetched_back', 'extra_read', 'adopted', 'overridden', 'cleared', 'steered', 'would_steer', 'reprompted', 'turn_end')),
    bytes       INTEGER NOT NULL DEFAULT 0,
    tokens      INTEGER NOT NULL DEFAULT 0
);
INSERT INTO hook_events_new (id, ts, session_key, hook, kind, bytes, tokens)
    SELECT id, ts, session_key, hook, kind, bytes, tokens FROM hook_events;
DROP TABLE hook_events;
ALTER TABLE hook_events_new RENAME TO hook_events;
CREATE INDEX hook_events_session ON hook_events (session_key);
CREATE INDEX hook_events_hook_ts ON hook_events (hook, ts);
