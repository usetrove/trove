-- Per-repo optimizer tuning (internal/tuning, internal/recommend).
-- Metadata only: a hashed repo key, an optimizer name, a parameter name and a number.
--   repo_key:    salted truncated hash of the working directory (see repo_features); never a path.
--   param:       the name of a tunable parameter, for example 'keep_tail'.
--   state:       'suggested' (shown, not in effect), 'applied' (new sessions in the repo pin it)
--                or 'reverted' (applied, then undone because it did not help).
--   before_rate: the share of trimmed outputs the agent fetched back before the value was
--                applied, and n_before how many outputs that rests on. A later review compares against it.
-- Values stay on this device. They are shared only if the user opts in to `trove contribute`.
CREATE TABLE repo_tuning (
    repo_key    TEXT    NOT NULL,
    optimizer   TEXT    NOT NULL,
    param       TEXT    NOT NULL,
    value       INTEGER NOT NULL,
    state       TEXT    NOT NULL CHECK (state IN ('suggested', 'applied', 'reverted')),
    tuned_at    INTEGER NOT NULL,
    source      TEXT    NOT NULL DEFAULT 'local',
    before_rate REAL    NOT NULL DEFAULT 0,
    n_before    INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (repo_key, optimizer, param)
);
