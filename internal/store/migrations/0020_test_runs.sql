-- Test-pass signal (plan item 2.1): the exit status of a Bash command that matched one of the
-- project's allowlisted test commands (.trove/commands.toml), written by the post_tool_test_result
-- hook. Metadata only: the command is stored as a 16-character hash, never its text, output or folder.
CREATE TABLE test_runs (
    id          INTEGER PRIMARY KEY,
    ts          INTEGER NOT NULL,          -- unix ms
    session_key TEXT    NOT NULL,          -- hashed session key
    cmd_hash    TEXT    NOT NULL,          -- hash of the allowlisted argument list
    exit_code   INTEGER NOT NULL
);
CREATE INDEX test_runs_session ON test_runs(session_key, ts);
