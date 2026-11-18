-- Confounder tags pinned at session start (internal/optimize/confounder.go).
-- Metadata only: a JSON array of fixed tag names such as "brevity_rules", "skill" or
-- "external_tool". Never prompt text. NULL means the session was pinned before tags existed.
ALTER TABLE session_pins ADD COLUMN confounders TEXT;
