-- Token detail and context per history request (internal/logs; docs/logs.md "Token detail",
-- verified 2026-11-23). Numbers and short checked identifiers only (PRINCIPLES.md rule 7).
-- NULL = the log did not say. In particular a NULL thinking_tokens is unknown, not zero.
--   thinking_tokens  usage.output_tokens_details.thinking_tokens; already inside output_tokens,
--                    never add the two
--   agent_version    agent version that wrote the request, e.g. 2.1.331
--   entrypoint       how the agent was started, e.g. cli or sdk-cli (observed, not documented)
--   effort           effort setting: low | medium | high | xhigh | max (observed field)
--   turn_effort      effort for this turn (observed field, not documented)
ALTER TABLE history_requests ADD COLUMN thinking_tokens INTEGER;
ALTER TABLE history_requests ADD COLUMN agent_version   TEXT;
ALTER TABLE history_requests ADD COLUMN entrypoint      TEXT;
ALTER TABLE history_requests ADD COLUMN effort          TEXT;
ALTER TABLE history_requests ADD COLUMN turn_effort     TEXT;

-- One row per tool call in a history request: the tool's name and sizes, never its input or output.
-- A side table rather than more JSON in history_requests.tool_names, so size distributions per tool
-- can be queried. ts repeats the request's time so retention can age these rows on their own.
CREATE TABLE history_tool_calls (
    request_key  TEXT    NOT NULL,           -- history_requests.request_key
    idx          INTEGER NOT NULL,           -- 0-based position of the call in the request
    ts           INTEGER NOT NULL,           -- unix ms, the request's time
    tool_name    TEXT    NOT NULL,
    input_bytes  INTEGER NOT NULL DEFAULT 0, -- size of the call's input JSON as logged
    result_bytes INTEGER,                    -- NULL when no result was seen
    error        INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (request_key, idx)
);
CREATE INDEX history_tool_calls_ts ON history_tool_calls (ts);
