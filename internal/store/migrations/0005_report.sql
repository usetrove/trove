-- Report support. Metadata only: totals, counts and rates, never names or text.

-- usage_daily is a small per-day summary the gateway keeps current in the same
-- transaction as each request. trove status-line reads it instead of scanning
-- requests. Days are local calendar dates (YYYY-MM-DD).
--   spend_usd  provider-reported usage priced at list prices (estimated); lower
--              bound when unpriced > 0
--   saved_usd  estimated savings from optimizers that were on for the request
CREATE TABLE usage_daily (
    day        TEXT    PRIMARY KEY,
    requests   INTEGER NOT NULL DEFAULT 0,
    unpriced   INTEGER NOT NULL DEFAULT 0,
    spend_usd  REAL    NOT NULL DEFAULT 0,
    saved_usd  REAL    NOT NULL DEFAULT 0
);

INSERT INTO usage_daily (day, requests, unpriced, spend_usd, saved_usd)
SELECT strftime('%Y-%m-%d', ts / 1000, 'unixepoch', 'localtime'),
       COUNT(*),
       SUM(CASE WHEN cost_usd IS NULL THEN 1 ELSE 0 END),
       COALESCE(SUM(cost_usd), 0),
       COALESCE(SUM(CASE WHEN tokens_removed > 0 AND optimizations_applied IS NOT NULL THEN cost_diff_usd END), 0)
FROM requests
WHERE status_code > 0 AND status_code < 400
GROUP BY 1;

-- One receipt per finished session, written once when the session has been idle
-- past the session gap.
CREATE TABLE receipts (
    session_key    TEXT    PRIMARY KEY,
    agent          TEXT    NOT NULL DEFAULT '',
    ended_at       INTEGER NOT NULL,          -- unix ms of the last request
    model          TEXT    NOT NULL DEFAULT '', -- model used for most calls
    models         INTEGER NOT NULL DEFAULT 1,  -- how many different models the session used
    calls          INTEGER NOT NULL,
    cost_usd       REAL    NOT NULL,          -- estimated, list price
    unpriced       INTEGER NOT NULL DEFAULT 0, -- calls left out of cost_usd
    cache_hit_rate REAL,                      -- cache reads / input tokens (measured); NULL when no input
    saved_usd      REAL    NOT NULL DEFAULT 0  -- estimated
);
CREATE INDEX receipts_ended ON receipts(ended_at);
