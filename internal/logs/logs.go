package logs

import "context"

// Source names, stored in history tables.
const (
	SourceClaudeCode = "claude_code"
	SourceCodex      = "codex"
)

// File is one history file found on disk. Path is used to open the file and is
// never stored; Key is a hash of it.
type File struct {
	Source string
	Path   string
	Key    string
	Size   int64
	MTime  int64 // unix ms
}

// Checkpoint says where the last scan of a file stopped.
type Checkpoint struct {
	Offset int64 // byte position after the last complete line read
	Lines  int64
	Turns  int // requests emitted so far, so Turn keeps counting across passes
}

// Stats counts what a parse did, including what it could not read.
type Stats struct {
	Lines         int64  // complete lines read in this pass
	Skipped       int64  // lines that were corrupted, oversized or not JSON
	Unknown       int64  // lines of a type the parser does not use (not an error)
	FormatVersion string // agent version seen last in the file
	UnknownFormat bool   // version outside the range the parser was written for
}

// Event is one provider request, normalized across agents. It has no text
// fields on purpose.
type Event struct {
	Source     string
	SessionKey string // hashed; same scheme as the gateway for Claude Code
	RequestKey string // hashed provider request id; stable across re-scans
	Turn       int    // 1-based request number within the session file
	TS         int64  // unix ms
	Model      string
	Sidechain  bool // subagent request

	// Usage buckets are disjoint, matching internal/pricing.Usage.
	Input        int64
	CacheWrite5m int64
	CacheWrite1h int64
	CacheWriteNA int64 // cache writes the log gave no TTL split for
	CacheRead    int64
	Output       int64
	ServiceTier  string

	Tools []ToolCall
}

// ToolCall is one tool use and, when its result was seen, the result's size.
type ToolCall struct {
	Name        string
	ResultBytes int64  // 0 when no result was seen
	ResultHash  string // hash of the result, for duplicate detection; never the text
	Error       bool
}

// Source finds and parses one agent's history.
type Source interface {
	Name() string
	// Discover lists history files under the agent's default location, or
	// under roots when given. It never fails on a missing directory.
	Discover(ctx context.Context, roots []string) ([]File, error)
	// Parse reads f from cp and calls emit for each event. It returns the new
	// checkpoint; a partial last line is left for the next pass.
	Parse(ctx context.Context, f File, cp Checkpoint, emit func(Event)) (Checkpoint, Stats, error)
}
