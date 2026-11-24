-- Routine agents (docs/tasks.md): the effort level written into a task file, and one row per subagent run seen
-- by the SubagentStart and SubagentStop hooks, so tokens can be attributed to an agent type.
-- agent_type is Trove's own trove-* names and Claude Code's built-in names as they are; any other name (the
-- user's own agents) is stored as 'user'. status is read from the first line of the subagent's reply in memory;
-- the reply itself is never stored.
ALTER TABLE task_installs ADD COLUMN effort TEXT NOT NULL DEFAULT '';

CREATE TABLE subagent_runs (
    session_key TEXT    NOT NULL,                  -- hash of the session id
    agent_key   TEXT    NOT NULL,                  -- hash of the subagent id, the same hash as history_requests.agent_key
    agent_type  TEXT    NOT NULL,                  -- trove-*, a built-in name, 'user' or 'unknown'
    started_at  INTEGER NOT NULL DEFAULT 0,        -- ms; 0 when only the stop was seen
    stopped_at  INTEGER NOT NULL DEFAULT 0,        -- ms; 0 while running or when the stop was not seen
    status      TEXT    NOT NULL DEFAULT 'unknown' CHECK (status IN ('done', 'failed', 'unsure', 'unknown')),
    PRIMARY KEY (session_key, agent_key)
);
CREATE INDEX subagent_runs_type ON subagent_runs (agent_type, started_at);
