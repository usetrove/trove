-- Scout and cascade files written by `trove tasks` (see docs/tasks.md). Metadata
-- only: the feature, the role of the file, the model aliases chosen and the
-- estimated overhead. Never a path, a test command or any file content. The
-- before/after split for measurement is the applied_at of the matching row in fixes.
CREATE TABLE task_installs (
    change_id        TEXT PRIMARY KEY,               -- same id as fixes.change_id
    fix_id           TEXT NOT NULL,
    feature          TEXT NOT NULL,                  -- task_scout or task_cascade
    role             TEXT NOT NULL,                  -- scout, worker, escalate, cascade, snippet
    scout_model      TEXT NOT NULL DEFAULT '',
    worker_model     TEXT NOT NULL DEFAULT '',
    escalation_model TEXT NOT NULL DEFAULT '',
    overhead_request INTEGER NOT NULL DEFAULT 0,     -- estimated tokens added to every request
    overhead_use     INTEGER NOT NULL DEFAULT 0      -- estimated tokens added each time it is used
);
CREATE INDEX task_installs_fix ON task_installs (fix_id);
