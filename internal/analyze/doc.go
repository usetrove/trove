// Package analyze inspects each request the gateway forwarded and records what
// it learned as metadata: where the tokens went (estimated), why the prompt
// cache missed, which kinds of waste were present, and what each optimizer
// would have saved (shadow mode). It never modifies a request.
//
// Everything here runs after the response has been forwarded. Prompt text,
// code, file paths and tool output are parsed in memory and dropped; only
// counts, sizes, hashes, tool names, model names and reason codes leave this
// package. The one place that keeps more than hashes is per-session state in
// memory: line hashes (never text) of system-level segments, used to tell a
// changed date from a changed instruction.
//
// All numbers produced here are estimates and are labeled as such.
package analyze
