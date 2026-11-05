-- Harness plugins (internal/adapter/plugin, plugins/opencode). Metadata only:
-- hashed session keys, counts, sizes, model names and enum labels. Never prompt
-- text, code, file paths, tool output, commands or credentials.
--
-- One row per optimizer decision or harness report. The plugin talks to the
-- engine over `trove engine serve`, which writes these rows.
--   harness  which tool the plugin runs in, e.g. opencode
--   login    api_key | copilot | subscription_or_unknown. The plugin classifies
--            it from non-secret fields; Trove never sees a credential. Only the
--            first two may be changed by an optimizer; the third is shadow-only.
--   kind     the decision kinds of hook_events (blocked, would_block, advised,
--            would_advise, reread_anyway, rewritten, would_rewrite, reissued),
--            plus: usage (tokens and cost the harness reported for one model
--            call, "measured" by the harness) and outcome (an outcome signal)
--   optimizer the optimizer name for decision kinds; empty for usage and outcome
--   state    the state the optimizer ran in (shadow or on); empty otherwise
--   bytes, tokens  size involved and estimated tokens kept out of the context
--   overhead_tokens what the optimizer itself added to the context (rule 15)
--   in_tokens .. cache_write_tokens, cost_usd  usage rows only; NULL when the
--            harness did not report them
--   detail   a short enum for outcome rows (tool_error, retry, abort, session_end)
CREATE TABLE adapter_events (
    id                 INTEGER PRIMARY KEY,
    ts                 INTEGER NOT NULL,
    session_key        TEXT    NOT NULL,
    harness            TEXT    NOT NULL,
    login              TEXT    NOT NULL CHECK (login IN ('api_key', 'copilot', 'subscription_or_unknown')),
    provider           TEXT    NOT NULL DEFAULT '',
    model              TEXT    NOT NULL DEFAULT '',
    kind               TEXT    NOT NULL CHECK (kind IN ('blocked', 'would_block', 'advised', 'would_advise', 'reread_anyway', 'rewritten', 'would_rewrite', 'reissued', 'usage', 'outcome')),
    optimizer          TEXT    NOT NULL DEFAULT '',
    state              TEXT    NOT NULL DEFAULT '',
    bytes              INTEGER NOT NULL DEFAULT 0,
    tokens             INTEGER NOT NULL DEFAULT 0,
    overhead_tokens    INTEGER NOT NULL DEFAULT 0,
    in_tokens          INTEGER,
    out_tokens         INTEGER,
    cache_read_tokens  INTEGER,
    cache_write_tokens INTEGER,
    cost_usd           REAL,
    detail             TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX adapter_events_session ON adapter_events (session_key);
CREATE INDEX adapter_events_harness_ts ON adapter_events (harness, login, ts);
