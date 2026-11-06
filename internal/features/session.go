package features

import "hash/fnv"

// SessionFeatures are the per-session features. Numbers only.
type SessionFeatures struct {
	FilesRead    int     `json:"files_read"`   // distinct files read
	FilesEdited  int     `json:"files_edited"` // distinct files edited
	RepeatReads  int     `json:"repeat_reads"` // reads of a file already read this session
	ScoutCalls   int     `json:"scout_calls"`  // calls delegated to the scout
	Compactions  int     `json:"compactions"`
	Retries      int     `json:"retries"` // a failed call repeated unchanged
	ToolResults  int     `json:"tool_results"`
	AvgDupRatio  float64 `json:"avg_dup_ratio"`
	ErrorShare   float64 `json:"error_share"`   // tool results with an error / tool results
	StackShare   float64 `json:"stack_share"`   // tool results with a stack trace / tool results
	LargeResults int     `json:"large_results"` // tool results over 6,000 bytes
}

// Session accumulates one session's features as events arrive. Paths and call
// inputs are given as hashes and live only in memory, in this struct; Snapshot
// returns numbers only.
type Session struct {
	read, edited map[uint64]struct{}
	f            SessionFeatures
	dupSum       float64
	errs, stacks int
	lastCall     uint64
	lastFailed   bool
}

// NewSession returns an empty accumulator.
func NewSession() *Session {
	return &Session{read: map[uint64]struct{}{}, edited: map[uint64]struct{}{}}
}

// HashPath reduces a path (or any input) to a number for in-memory identity.
func HashPath(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}

// Read records a file read.
func (s *Session) Read(path uint64) {
	if _, ok := s.read[path]; ok {
		s.f.RepeatReads++
	}
	s.read[path] = struct{}{}
}

// Edit records a file edit.
func (s *Session) Edit(path uint64) { s.edited[path] = struct{}{} }

// Scout records a call delegated to the scout.
func (s *Session) Scout() { s.f.ScoutCalls++ }

// Compaction records a compaction.
func (s *Session) Compaction() { s.f.Compactions++ }

// Call records a tool call and whether it failed. A call identical to the
// previous one, made after that one failed, counts as a retry.
func (s *Session) Call(input uint64, failed bool) {
	if s.lastFailed && input == s.lastCall {
		s.f.Retries++
	}
	s.lastCall, s.lastFailed = input, failed
}

// Add folds one tool result's features in.
func (s *Session) Add(r Result) {
	s.f.ToolResults++
	s.dupSum += r.DupLineRatio
	if r.HasError {
		s.errs++
	}
	if r.HasStackTrace {
		s.stacks++
	}
	if r.SizeBytes > 6000 {
		s.f.LargeResults++
	}
}

// Snapshot returns the features so far.
func (s *Session) Snapshot() SessionFeatures {
	f := s.f
	f.FilesRead, f.FilesEdited = len(s.read), len(s.edited)
	if f.ToolResults > 0 {
		n := float64(f.ToolResults)
		f.AvgDupRatio = round2(s.dupSum / n)
		f.ErrorShare = round2(float64(s.errs) / n)
		f.StackShare = round2(float64(s.stacks) / n)
	}
	return f
}
