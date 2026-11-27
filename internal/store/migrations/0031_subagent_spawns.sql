-- Sibling stagger, shadow only (docs/sibling-stagger.md): one row per Agent (formerly Task) call seen by the
-- PreToolUse hook, so the analysis can find same-type subagents spawned together and check, from the history,
-- whether their first requests wrote or read the shared prefix. Nothing here changes what the agent does.
-- agent_type is labelled like subagent_runs.agent_type; type_key tells two of the user's own agent types apart
-- without storing their names.
CREATE TABLE subagent_spawns (
    session_key TEXT    NOT NULL,              -- hash of the session id
    tool_key    TEXT    NOT NULL,              -- hash of the call's tool_use_id
    ts          INTEGER NOT NULL,              -- ms, when the PreToolUse hook ran
    agent_type  TEXT    NOT NULL,              -- trove-*, a built-in name, 'user' or 'unknown'
    type_key    TEXT    NOT NULL DEFAULT '',   -- hash of the raw subagent_type
    PRIMARY KEY (session_key, tool_key)
);
CREATE INDEX subagent_spawns_ts ON subagent_spawns (session_key, ts);
