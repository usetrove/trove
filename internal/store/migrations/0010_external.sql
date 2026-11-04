-- Third-party token tools Trove has installed with the user's approval (internal/external).
-- Metadata only: names, pinned versions, timestamps. No prompt text, paths or command output.
--   state:          'off' or 'on' (external tools have no shadow mode).
--   license_ack_at: when the user confirmed the license notice (unix ms).
--   changes:        JSON array of setup.Change IDs written for this tool, so remove can undo them.
CREATE TABLE external_tools (
    name           TEXT PRIMARY KEY,
    version        TEXT NOT NULL,
    license        TEXT NOT NULL,
    state          TEXT NOT NULL DEFAULT 'off',
    installed_at   INTEGER NOT NULL,
    license_ack_at INTEGER NOT NULL,
    changes        TEXT NOT NULL DEFAULT '[]'
);
