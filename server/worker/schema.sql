CREATE TABLE IF NOT EXISTS submissions (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  received_day TEXT NOT NULL,          -- UTC day, YYYY-MM-DD; no time of day
  install_id   TEXT NOT NULL,          -- random, not linked to a person
  payload      TEXT NOT NULL,          -- the validated JSON, as sent
  UNIQUE (install_id, received_day)
);
CREATE INDEX IF NOT EXISTS submissions_day ON submissions (received_day);

-- The weekly limit report (docs/decisions/limit-report.md): aggregates only, never a single install's values.
-- approved = 0 means it waits for a maintainer and is not served.
CREATE TABLE IF NOT EXISTS limit_reports (
  week     TEXT PRIMARY KEY,           -- ISO week, YYYY-Www
  report   TEXT NOT NULL,              -- the JSON from aggregate.js
  approved INTEGER NOT NULL DEFAULT 0
);
