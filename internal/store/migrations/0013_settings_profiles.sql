-- Settings profiles applied by `trove settings` (see docs/settings.md). Metadata
-- only: the task type, the harness, the NAMES of the settings changed and the
-- harness version. Never a path, a value or any file content. The before/after
-- split for measurement is the applied_at of the matching row in fixes.
CREATE TABLE settings_profiles (
    change_id       TEXT PRIMARY KEY,               -- same id as fixes.change_id
    fix_id          TEXT NOT NULL,
    task            TEXT NOT NULL,                  -- quick-fix, feature, refactor, exploration
    harness         TEXT NOT NULL,                  -- claude_code or codex
    keys            TEXT NOT NULL DEFAULT '[]',     -- JSON array of setting names
    harness_version TEXT NOT NULL DEFAULT '',
    catalog_version INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX settings_profiles_fix ON settings_profiles (fix_id);
