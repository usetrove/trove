-- Provider usage exactly as reported (numbers only, never prompt text) and the
-- service tier the provider says it used.
ALTER TABLE requests ADD COLUMN usage_raw TEXT;
ALTER TABLE requests ADD COLUMN service_tier TEXT NOT NULL DEFAULT '';
