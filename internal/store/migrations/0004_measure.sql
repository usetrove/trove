-- Outcome signals per request and guardrail events. Metadata only: the signals
-- column holds a JSON object of integers (counts), never names, inputs or output.
ALTER TABLE requests ADD COLUMN signals TEXT;

CREATE TABLE guardrail_events (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    ts            INTEGER NOT NULL,
    optimizer     TEXT    NOT NULL,
    rule          TEXT    NOT NULL,       -- retries, refetch
    observed      REAL    NOT NULL,       -- the measured value that tripped the rule
    threshold     REAL    NOT NULL,
    sample_on     INTEGER NOT NULL,       -- sessions the optimizer acted on
    sample_ref    INTEGER NOT NULL,       -- comparison sessions
    reference     TEXT    NOT NULL,       -- baseline or off_sessions
    reason        TEXT    NOT NULL,       -- plain English, shown in the report
    cleared_at    INTEGER                 -- set when the user lifts the override
);
CREATE INDEX guardrail_events_optimizer ON guardrail_events(optimizer);
