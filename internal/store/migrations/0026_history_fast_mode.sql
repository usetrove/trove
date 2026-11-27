-- Whether a history request ran in fast mode (the log's usage.speed or service_tier is "fast").
-- Fast and standard speed keep separate prompt caches (code.claude.com/docs/en/prompt-caching,
-- checked 2026-11-23), so the rebuild ledger needs it. NULL = read before this column existed.
ALTER TABLE history_requests ADD COLUMN fast_mode INTEGER;
