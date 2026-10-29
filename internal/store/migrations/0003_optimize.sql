-- Optimizer results per request, and the optimizer settings pinned per session.
-- Metadata only: counts, money and names. tokens_removed and cost_diff_usd are
-- estimates (removed bytes over a bytes-per-token ratio, priced at the mix of
-- rates the request was billed at); the optimizations_applied JSON says so.
ALTER TABLE requests ADD COLUMN tokens_removed INTEGER NOT NULL DEFAULT 0;
ALTER TABLE requests ADD COLUMN cost_diff_usd REAL;           -- NULL when it could not be priced exactly

CREATE TABLE session_pins (
    session_key      TEXT PRIMARY KEY,
    states           TEXT    NOT NULL,                        -- JSON {optimizer: off|shadow|on}
    custom_agents    TEXT    NOT NULL DEFAULT '[]',           -- JSON list of agent labels
    bytes_per_token  REAL    NOT NULL,
    pinned_at        INTEGER NOT NULL
);
CREATE INDEX session_pins_pinned ON session_pins(pinned_at);
