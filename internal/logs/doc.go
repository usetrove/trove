// Package logs reads the session history that coding agents already keep on
// disk and turns it into normalized, metadata-only events.
//
// It is read-only: it never writes to an agent's directory and never opens
// credential files. Events carry counts, sizes, hashes, model names and
// timestamps. They have no field that could hold prompt text, code, file paths
// or tool output, so none of it can reach the database.
//
// Sources live in sub-packages (claudecode, codex) and implement Source.
package logs
