-- Session-level A/B experiments (internal/experiment).
-- Metadata only: names, arms, hashes, counts, numbers. No prompt text, paths or output.
--   arm:     'on' or 'off' (off = the optimizer ran in shadow for that session).
--   stratum: a hash label such as 'repo:ab12cd34|task:fix'; never a path.
-- The first assignment for a session and optimizer wins, so an arm can never change mid-session.
CREATE TABLE experiment_assignments (
    session_key TEXT    NOT NULL,
    optimizer   TEXT    NOT NULL,
    arm         TEXT    NOT NULL CHECK (arm IN ('on', 'off')),
    stratum     TEXT    NOT NULL DEFAULT '',
    seq         INTEGER NOT NULL DEFAULT 0,  -- position within the stratum, for reproducible blocks
    assigned_at INTEGER NOT NULL,
    PRIMARY KEY (session_key, optimizer)
);
CREATE INDEX experiment_assignments_opt ON experiment_assignments(optimizer, stratum);

-- One row per optimizer per analysis run. inputs_hash lets a re-run be compared.
CREATE TABLE experiment_results (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    optimizer   TEXT    NOT NULL,
    computed_at INTEGER NOT NULL,
    metric      TEXT    NOT NULL,            -- cost_per_session, turns, tool_errors, abandoned
    value       REAL    NOT NULL,            -- relative change, 0.05 = 5% higher with the optimizer on
    low         REAL    NOT NULL,
    high        REAL    NOT NULL,
    confidence  TEXT    NOT NULL,            -- 95% interval label: high, medium, low
    n_on        INTEGER NOT NULL,
    n_off       INTEGER NOT NULL,
    label       TEXT    NOT NULL CHECK (label IN ('measured', 'estimated')),
    status      TEXT    NOT NULL,
    inputs_hash TEXT    NOT NULL
);
CREATE INDEX experiment_results_opt ON experiment_results(optimizer, computed_at);
