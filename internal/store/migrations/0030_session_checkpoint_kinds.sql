-- What hook_session_checkpoint (internal/hooks/checkpoint.go) records. hook_events.kind gains seven values:
--   checkpointed      a session note was kept in the recovery store (bytes = its size on disk)
--   restored          a note was added to the context after /compact or /clear (bytes, tokens = what was added, estimated)
--   pointed           the one-line pointer was added instead (bytes, tokens)
--   would_restore     a boundary in shadow or in the off arm of the session A/B (bytes, tokens = what would have been added)
--   reread_unchanged  in the 20 tool calls after a boundary, a Read of a file read before it and unchanged (bytes = the call's position, 1-20)
--   repeat_failure    in that window, a failed Bash command that had failed before the boundary (bytes = position)
--   recovered         the window's first edit (bytes = position)
-- Only timestamps, the hashed session key, these words and the numbers are stored; never the note, a path or a command.
-- SQLite cannot change a CHECK constraint in place, so the table is rebuilt, as in 0012, 0018, 0027 and 0028.
CREATE TABLE hook_events_new (
    id          INTEGER PRIMARY KEY,
    ts          INTEGER NOT NULL,
    session_key TEXT    NOT NULL,
    hook        TEXT    NOT NULL,
    kind        TEXT    NOT NULL CHECK (kind IN ('blocked', 'would_block', 'advised', 'would_advise', 'reread_anyway', 'rewritten', 'would_rewrite', 'reissued', 'fetched_back', 'extra_read', 'adopted', 'overridden', 'cleared', 'steered', 'would_steer', 'reprompted', 'turn_end',
        'checkpointed', 'restored', 'pointed', 'would_restore', 'reread_unchanged', 'repeat_failure', 'recovered')),
    bytes       INTEGER NOT NULL DEFAULT 0,
    tokens      INTEGER NOT NULL DEFAULT 0
);
INSERT INTO hook_events_new (id, ts, session_key, hook, kind, bytes, tokens)
    SELECT id, ts, session_key, hook, kind, bytes, tokens FROM hook_events;
DROP TABLE hook_events;
ALTER TABLE hook_events_new RENAME TO hook_events;
CREATE INDEX hook_events_session ON hook_events (session_key);
CREATE INDEX hook_events_hook_ts ON hook_events (hook, ts);
