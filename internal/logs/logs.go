package logs

import "context"

// Source names, stored in history tables.
const (
	SourceClaudeCode = "claude_code"
	SourceCodex      = "codex"
	SourceGeminiCLI  = "gemini_cli"
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
	// Parser state carried to the next pass, so a request read after a checkpoint still knows what
	// came before it.
	Phase     string // phase the next request gets unless a later line changes it
	Compacted bool   // a compaction boundary was seen and no request has followed it yet
}

// Phases: what came right before a request in the agent's log.
const (
	PhasePrompt     = "prompt"      // a user prompt (or a subagent's task)
	PhaseToolResult = "tool_result" // a tool result: the request continues a tool loop
	PhaseCompact    = "compact"     // a compaction summary
	PhaseOther      = "other"       // nothing recognizable, e.g. the start of a file
)

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

	// Thinking is the part of Output the model spent on reasoning. It is already inside Output and must
	// never be added to it. nil means the log did not say (unknown), which is not the same as 0.
	Thinking *int64

	// Per-request context, checked against strict patterns so no free text gets through. Empty means
	// the log did not carry a recognizable value.
	AgentVersion string // agent version that wrote the line, e.g. "2.1.331"
	Entrypoint   string // how the agent was started, e.g. "cli" or "sdk-cli" (observed, not documented)
	Effort       string // effort setting for the request: low | medium | high | xhigh | max
	TurnEffort   string // effort for this turn, which can differ from Effort (observed, not documented)

	// Where the request sits in its agent's conversation (Claude Code only).
	AgentKey  string // hashed subagent id; "" for the main conversation
	Phase     string // one of the Phase constants; "" when the parser does not tell
	Compacted bool   // a compaction boundary came between the agent's previous request and this one
	TextBytes int64  // byte size of the response's visible text blocks; the text is never kept

	Tools []ToolCall
}

// ToolCall is one tool use and, when its result was seen, the result's size.
type ToolCall struct {
	Name        string
	InputBytes  int64  // byte size of the call's input JSON as logged; the input itself is never kept
	ResultBytes int64  // 0 when no result was seen
	ResultHash  string // hash of the result, for duplicate detection; never the text
	Error       bool
	// Command is a Bash call's command text (Claude Code only, at most MaxCommandBytes). It exists so
	// `trove fix` can find commands the agent keeps retyping; it is held in memory for one run and
	// never stored: ingest writes tool names, sizes and counts only.
	// It is never marshalled.
	Command string `json:"-"`
}

// MaxCommandBytes bounds the command text a parser keeps per Bash call.
const MaxCommandBytes = 16 << 10

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
