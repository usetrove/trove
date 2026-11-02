CREATE TABLE IF NOT EXISTS submissions (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  received_day TEXT NOT NULL,          -- UTC day, YYYY-MM-DD; no time of day
  install_id   TEXT NOT NULL,          -- random, not linked to a person
  payload      TEXT NOT NULL,          -- the validated JSON, as sent
  UNIQUE (install_id, received_day)
);
CREATE INDEX IF NOT EXISTS submissions_day ON submissions (received_day);
