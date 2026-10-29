// Package claudecode reads Claude Code session history.
//
// Format (checked 2026-10-29 against the Claude Code docs and a local install
// of v2.1.302; see docs/logs.md):
//
//   - <config dir>/projects/<project>/<sessionId>.jsonl, plus subagent files in
//     subdirectories. The config dir is ~/.claude, or CLAUDE_CONFIG_DIR.
//   - One JSON object per line with a "type" field. Only "assistant" and "user"
//     lines are used; every other type is counted and ignored.
//   - An API response is written as several "assistant" lines, one per content
//     block, each repeating the same usage. Lines are merged by requestId so
//     tokens are counted once.
//   - Tool results arrive on later "user" lines and are matched by tool_use_id.
//
// Only counts, sizes, hashes and tool names are kept. Text is read in memory to
// measure and hash it, then dropped.
package claudecode

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/usetrove/trove/internal/gateway/session"
	"github.com/usetrove/trove/internal/logs"
)

// ParserVersion changes when the parser's output for the same input changes.
const ParserVersion = 1

const (
	maxLine    = 16 << 20 // lines longer than this are skipped and counted
	holdWindow = 10 * time.Minute
)

// Source implements logs.Source for Claude Code.
type Source struct {
	// Getenv and Home locate the default directory. Zero values use the process.
	Getenv func(string) string
	Home   func() (string, error)
	// Now is used to decide whether a file is still being written. Zero uses time.Now.
	Now func() time.Time
}

var _ logs.Source = (*Source)(nil)

// ConfigDir is Claude Code's config directory: CLAUDE_CONFIG_DIR, else ~/.claude.
func (s *Source) ConfigDir() string {
	getenv := s.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	if d := getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	home := s.Home
	if home == nil {
		home = os.UserHomeDir
	}
	h, err := home()
	if err != nil || h == "" {
		return ""
	}
	return filepath.Join(h, ".claude")
}

// Name returns the source name.
func (s *Source) Name() string { return logs.SourceClaudeCode }

func (s *Source) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Source) defaultRoots() []string {
	getenv := s.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	if d := getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return []string{filepath.Join(d, "projects")}
	}
	home := s.Home
	if home == nil {
		home = os.UserHomeDir
	}
	h, err := home()
	if err != nil || h == "" {
		return nil
	}
	return []string{filepath.Join(h, ".claude", "projects")}
}

// Discover lists *.jsonl files under the projects directories. Missing
// directories are not an error.
func (s *Source) Discover(ctx context.Context, roots []string) ([]logs.File, error) {
	if len(roots) == 0 {
		roots = s.defaultRoots()
	}
	var out []logs.File
	for _, root := range roots {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				if p == root {
					return fs.SkipAll // root missing or unreadable
				}
				return nil // skip what we cannot read, keep going
			}
			if d.IsDir() {
				if d.Name() == "tool-results" {
					return fs.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(d.Name(), ".jsonl") {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			out = append(out, logs.File{
				Source: logs.SourceClaudeCode,
				Path:   p,
				Key:    logs.Hash("file", p),
				Size:   info.Size(),
				MTime:  info.ModTime().UnixMilli(),
			})
			return nil
		})
		if err != nil && !errors.Is(err, fs.SkipAll) {
			return out, err
		}
	}
	return out, nil
}

// line is the part of a history line the parser reads. Unknown fields are ignored.
type line struct {
	Type        string `json:"type"`
	SessionID   string `json:"sessionId"`
	Timestamp   string `json:"timestamp"`
	Version     string `json:"version"`
	RequestID   string `json:"requestId"`
	IsSidechain bool   `json:"isSidechain"`
	Message     *struct {
		ID      string          `json:"id"`
		Model   string          `json:"model"`
		Usage   *usage          `json:"usage"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type usage struct {
	Input         int64  `json:"input_tokens"`
	Output        int64  `json:"output_tokens"`
	CacheRead     int64  `json:"cache_read_input_tokens"`
	CacheCreation int64  `json:"cache_creation_input_tokens"`
	ServiceTier   string `json:"service_tier"`
	Speed         string `json:"speed"`
	Split         *struct {
		Eph5m int64 `json:"ephemeral_5m_input_tokens"`
		Eph1h int64 `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
}

type block struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"`
}

// pending is an event being assembled, with where it started in the file so a
// pass can rewind to it.
type pending struct {
	ev          logs.Event
	startOffset int64
	startLines  int64
	startStats  logs.Stats
	open        int // tool calls still waiting for a result
}

type pass struct {
	events []*pending
	byReq  map[string]*pending
	byTool map[string]*toolRef
}

type toolRef struct {
	p   *pending
	idx int
}

// Parse reads f from cp. It stops at the last complete line. If the file is
// still being written and its newest requests have tool calls with no result
// yet, the checkpoint is held at the start of those requests so the next pass
// sees them whole.
func (s *Source) Parse(ctx context.Context, f logs.File, cp logs.Checkpoint, emit func(logs.Event)) (logs.Checkpoint, logs.Stats, error) {
	var st logs.Stats
	file, err := os.Open(f.Path)
	if err != nil {
		return cp, st, err
	}
	defer func() { _ = file.Close() }()
	if cp.Offset > 0 {
		info, err := file.Stat()
		if err != nil {
			return cp, st, err
		}
		if info.Size() < cp.Offset { // file was replaced or truncated: start over
			cp = logs.Checkpoint{}
		}
	}
	if _, err := file.Seek(cp.Offset, io.SeekStart); err != nil {
		return cp, st, err
	}

	ps := &pass{byReq: map[string]*pending{}, byTool: map[string]*toolRef{}}
	r := bufio.NewReaderSize(file, 1<<20)
	offset, lines := cp.Offset, cp.Lines
	turns := cp.Turns

	for {
		if ctx.Err() != nil {
			return cp, st, ctx.Err()
		}
		data, complete, tooLong, err := logs.ReadLine(r, maxLine)
		if err != nil && err != io.EOF {
			return cp, st, err
		}
		atEOF := err == io.EOF
		if !complete {
			// Last line has no newline. Accept it only if it is whole JSON.
			if len(data) == 0 || tooLong || !json.Valid(data) {
				break
			}
		}
		consumed := int64(len(data))
		if complete {
			consumed++ // the newline
		}
		lineStart, lineNo := offset, lines
		offset += consumed
		lines++
		st.Lines++
		switch {
		case tooLong:
			st.Skipped++
		default:
			s.handle(ps, data, lineStart, lineNo, &st, &turns)
		}
		if atEOF {
			break
		}
	}

	// Decide what to emit and where to stop.
	cut := len(ps.events)
	if s.now().Sub(time.UnixMilli(f.MTime)) < holdWindow {
		for i, p := range ps.events {
			if p.open > 0 {
				cut = i
				break
			}
		}
	}
	end := logs.Checkpoint{Offset: offset, Lines: lines, Turns: turns}
	if cut < len(ps.events) {
		p := ps.events[cut]
		end = logs.Checkpoint{Offset: p.startOffset, Lines: p.startLines, Turns: p.ev.Turn - 1}
		keep := st.FormatVersion
		st = p.startStats
		if st.FormatVersion == "" {
			st.FormatVersion = keep
		}
	}
	for _, p := range ps.events[:cut] {
		emit(p.ev)
	}
	return end, st, nil
}

func (s *Source) handle(ps *pass, data []byte, start, lineNo int64, st *logs.Stats, turns *int) {
	const prefix = `{"type":"`
	if bytes.HasPrefix(data, []byte(prefix)) {
		rest := data[len(prefix):]
		if !bytes.HasPrefix(rest, []byte(`assistant"`)) && !bytes.HasPrefix(rest, []byte(`user"`)) {
			st.Unknown++
			return
		}
	}
	var l line
	if err := json.Unmarshal(data, &l); err != nil {
		st.Skipped++
		return
	}
	if l.Version != "" {
		st.FormatVersion = l.Version
		if !strings.HasPrefix(l.Version, "2.") {
			st.UnknownFormat = true
		}
	}
	switch l.Type {
	case "assistant":
		s.assistant(ps, &l, start, lineNo, st, turns)
	case "user":
		s.user(ps, &l)
	default:
		st.Unknown++
	}
}

func (s *Source) assistant(ps *pass, l *line, start, lineNo int64, st *logs.Stats, turns *int) {
	m := l.Message
	if m == nil || m.Usage == nil || l.SessionID == "" {
		st.Skipped++
		return
	}
	if strings.HasPrefix(m.Model, "<") { // "<synthetic>": a local message, not an API call
		st.Unknown++
		return
	}
	id := l.RequestID
	if id == "" {
		id = m.ID
	}
	if id == "" {
		st.Skipped++
		return
	}
	p, ok := ps.byReq[id]
	if !ok {
		ts, err := time.Parse(time.RFC3339Nano, l.Timestamp)
		if err != nil {
			st.Skipped++
			return
		}
		*turns++
		u := m.Usage
		ev := logs.Event{
			Source:      logs.SourceClaudeCode,
			SessionKey:  session.ClaudeCodeKey(l.SessionID),
			RequestKey:  logs.Hash(logs.SourceClaudeCode, id),
			Turn:        *turns,
			TS:          ts.UnixMilli(),
			Model:       m.Model,
			Sidechain:   l.IsSidechain,
			CacheRead:   u.CacheRead,
			Output:      u.Output,
			Input:       u.Input,
			ServiceTier: u.ServiceTier,
		}
		if u.Speed != "" && u.Speed != "standard" {
			ev.ServiceTier = u.Speed // e.g. "fast": priced as its own tier, or left unpriced
		}
		if u.Split != nil {
			ev.CacheWrite5m, ev.CacheWrite1h = u.Split.Eph5m, u.Split.Eph1h
		}
		if rest := u.CacheCreation - ev.CacheWrite5m - ev.CacheWrite1h; rest > 0 {
			ev.CacheWriteNA = rest // total with no (or a partial) TTL split
		}
		snap := *st
		snap.Lines-- // the line that starts this request is re-read if the pass rewinds
		p = &pending{ev: ev, startOffset: start, startLines: lineNo, startStats: snap}
		ps.byReq[id] = p
		ps.events = append(ps.events, p)
	}
	// Merge tool_use blocks; the same call can appear on only one line, but be safe.
	var blocks []block
	if len(m.Content) > 0 && m.Content[0] == '[' && json.Unmarshal(m.Content, &blocks) == nil {
		for _, b := range blocks {
			if b.Type != "tool_use" || b.Name == "" {
				continue
			}
			if b.ID != "" {
				if _, dup := ps.byTool[b.ID]; dup {
					continue
				}
				ps.byTool[b.ID] = &toolRef{p: p, idx: len(p.ev.Tools)}
				p.open++
			}
			p.ev.Tools = append(p.ev.Tools, logs.ToolCall{Name: b.Name})
		}
	}
}

func (s *Source) user(ps *pass, l *line) {
	if l.Message == nil || len(l.Message.Content) == 0 || l.Message.Content[0] != '[' {
		return
	}
	var blocks []block
	if json.Unmarshal(l.Message.Content, &blocks) != nil {
		return
	}
	for _, b := range blocks {
		if b.Type != "tool_result" {
			continue
		}
		ref, ok := ps.byTool[b.ToolUseID]
		if !ok {
			continue // result for a call in an earlier pass; already emitted
		}
		size, sum := measure(b.Content)
		tc := &ref.p.ev.Tools[ref.idx]
		tc.ResultBytes, tc.ResultHash, tc.Error = size, sum, b.IsError
		ref.p.open--
		delete(ps.byTool, b.ToolUseID)
	}
}

// measure returns the byte size of a tool result's text and a hash of it. The
// text itself is not kept.
func measure(raw json.RawMessage) (int64, string) {
	if len(raw) == 0 {
		return 0, ""
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			sum := sha256.Sum256([]byte(s))
			return int64(len(s)), hex.EncodeToString(sum[:])[:16]
		}
	}
	sum := sha256.Sum256(raw)
	return int64(len(raw)), hex.EncodeToString(sum[:])[:16]
}
