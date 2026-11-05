-- Content-derived features (internal/features), computed on the device.
-- Metadata only: every column is a number, a hash or a value from a small fixed set.
-- No prompt text, code, file paths, commands or tool output is stored, and none of
-- these columns could hold it: repo_key is a salted truncated hash of the working
-- directory, and lang_mix is a list of fixed language names with whole percentages.
--   size_bucket:    S, M, L or XL.
--   lang_mix:       up to three 'language:percent' pairs, for example 'go:70,python:30'.
--   test_framework: a name from a fixed list, or 'unknown'.
CREATE TABLE repo_features (
    repo_key       TEXT    PRIMARY KEY,
    computed_at    INTEGER NOT NULL,
    size_bucket    TEXT    NOT NULL CHECK (size_bucket IN ('S', 'M', 'L', 'XL')),
    lang_mix       TEXT    NOT NULL DEFAULT '',
    test_framework TEXT    NOT NULL DEFAULT 'unknown'
);

-- One row per finished session, written once at its end. Tool results are folded
-- into these numbers; no per-result row is kept.
CREATE TABLE session_features (
    session_key   TEXT    PRIMARY KEY,
    repo_key      TEXT    NOT NULL DEFAULT '',
    recorded_at   INTEGER NOT NULL,
    files_read    INTEGER NOT NULL DEFAULT 0,
    files_edited  INTEGER NOT NULL DEFAULT 0,
    repeat_reads  INTEGER NOT NULL DEFAULT 0,
    scout_calls   INTEGER NOT NULL DEFAULT 0,
    compactions   INTEGER NOT NULL DEFAULT 0,
    retries       INTEGER NOT NULL DEFAULT 0,
    tool_results  INTEGER NOT NULL DEFAULT 0,
    large_results INTEGER NOT NULL DEFAULT 0,
    avg_dup_ratio REAL    NOT NULL DEFAULT 0,
    error_share   REAL    NOT NULL DEFAULT 0,
    stack_share   REAL    NOT NULL DEFAULT 0
);
CREATE INDEX session_features_repo ON session_features(repo_key, recorded_at);
