-- Agent hooks. Metadata only: counts, sizes and hashed session keys. Never file
-- names, file contents, commands or prompt text.

-- One row per hook decision, copied in from the hook spool (hooks/events.jsonl)
-- by the session-end hook, `trove report` or the gateway's measure loop.
--   hook    the optimizer name, e.g. hook_repeat_read
--   kind    blocked        the call was denied so the agent reuses what it has
--           would_block    shadow mode: it would have been denied, nothing changed
--           advised        a message was added to the call, nothing was denied
--           would_advise   shadow mode: a message would have been added
--           reread_anyway  the agent retried a blocked read, so the block did not help
--   bytes   size of the file or output involved
--   tokens  estimated tokens the decision kept out of the context (0 when unknown)
CREATE TABLE hook_events (
    id          INTEGER PRIMARY KEY,
    ts          INTEGER NOT NULL,
    session_key TEXT    NOT NULL,
    hook        TEXT    NOT NULL,
    kind        TEXT    NOT NULL CHECK (kind IN ('blocked', 'would_block', 'advised', 'would_advise', 'reread_anyway')),
    bytes       INTEGER NOT NULL DEFAULT 0,
    tokens      INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX hook_events_session ON hook_events (session_key);
CREATE INDEX hook_events_hook_ts ON hook_events (hook, ts);
