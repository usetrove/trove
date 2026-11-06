// Package cxhistory reads Codex CLI session history.
//
// UNVERIFIED ON REAL INSTALLS. Codex CLI was not installed where this was
// written, and OpenAI's documentation does not describe the session files.
// What is verified (2026-10-29, openai/codex codex-rs/protocol/src/protocol.rs):
//
//   - TokenUsage fields: input_tokens, cached_input_tokens, cache_write_input_tokens,
//     output_tokens, reasoning_output_tokens, total_tokens; TokenUsageInfo has
//     total_token_usage, last_token_usage, model_context_window; the event is
//     an EventMsg of type "token_count" with an optional info.
//   - SessionMeta: id, timestamp, cwd, cli_version. TurnContextItem: model.
//   - Cached tokens are a subset of input_tokens (the source computes
//     non-cached input as input_tokens - cached_input_tokens).
//
// What comes only from community write-ups and is not confirmed in the source:
// the files are ~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl, each line is
// {"timestamp", "type", "payload"} with type session_meta, turn_context,
// event_msg or response_item, and tool calls are response_item payloads of type
// function_call (name, call_id) and function_call_output (call_id, output).
// Run `trove logs scan` on a real install and check docs/logs-verification.md
// before relying on these numbers. CODEX_HOME relocates the directory (documented).
package cxhistory

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

	"github.com/usetrove/trove/internal/logs"
)

// ParserVersion changes when the parser's output for the same input changes.
const ParserVersion = 1

const (
	maxLine    = 16 << 20
	holdWindow = 10 * time.Minute
)

// Source implements logs.Source for Codex CLI.
type Source struct {
	Getenv func(string) string
	Home   func() (string, error)
	Now    func() time.Time
}

var _ logs.Source = (*Source)(nil)

// Name returns the source name.
func (s *Source) Name() string { return logs.SourceCodex }

func (s *Source) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Source) root() string {
	getenv := s.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	if d := getenv("CODEX_HOME"); d != "" {
		return filepath.Join(d, "sessions")
	}
	home := s.Home
	if home == nil {
		home = os.UserHomeDir
	}
	h, err := home()
	if err != nil || h == "" {
		return ""
	}
	return filepath.Join(h, ".codex", "sessions")
}

// Discover lists rollout-*.jsonl files. Missing directories are not an error.
func (s *Source) Discover(ctx context.Context, roots []string) ([]logs.File, error) {
	if len(roots) == 0 {
		if r := s.root(); r != "" {
			roots = []string{r}
		}
	}
	var out []logs.File
	for _, root := range roots {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				if p == root {
					return fs.SkipAll
				}
				return nil
			}
			if d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			out = append(out, logs.File{Source: logs.SourceCodex, Path: p, Key: logs.Hash("file", p), Size: info.Size(), MTime: info.ModTime().UnixMilli()})
			return nil
		})
		if err != nil && !errors.Is(err, fs.SkipAll) {
			return out, err
		}
	}
	return out, nil
}

type line struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type payload struct {
	Type       string          `json:"type"`
	ID         string          `json:"id"`
	CLIVersion string          `json:"cli_version"`
	Model      string          `json:"model"`
	Name       string          `json:"name"`
	CallID     string          `json:"call_id"`
	Output     json.RawMessage `json:"output"`
	Info       *struct {
		Total tokenUsage `json:"total_token_usage"`
		Last  tokenUsage `json:"last_token_usage"`
	} `json:"info"`
}

type tokenUsage struct {
	Input      int64 `json:"input_tokens"`
	Cached     int64 `json:"cached_input_tokens"`
	CacheWrite int64 `json:"cache_write_input_tokens"`
	Output     int64 `json:"output_tokens"`
	Total      int64 `json:"total_tokens"`
}

type pending struct {
	ev          logs.Event
	startOffset int64
	startLines  int64
	startStats  logs.Stats
	open        int
}

type toolRef struct {
	p   *pending
	idx int
}

// Parse reads f from cp. Each token_count event with new usage becomes one
// request, priced from last_token_usage. Tool calls seen since the previous
// request belong to this one. Like the Claude Code parser, it holds back the
// newest requests of an active file until their tool results have arrived.
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
		if info.Size() < cp.Offset {
			cp = logs.Checkpoint{}
		}
	}
	if _, err := file.Seek(cp.Offset, io.SeekStart); err != nil {
		return cp, st, err
	}

	var events []*pending
	byCall := map[string]*toolRef{}
	var calls []logs.ToolCall // calls seen since the last request
	var callIDs []string
	var sessionID, model string
	var prevTotal int64 = -1
	if cp.Offset > 0 {
		var v string
		sessionID, model, v, prevTotal = prelude(f.Path, cp.Offset)
		st.FormatVersion = v
	}
	r := bufio.NewReaderSize(file, 1<<20)
	offset, lines, turns := cp.Offset, cp.Lines, cp.Turns
	segStart, segLines := offset, lines // where the calls in `calls` began

	for {
		if ctx.Err() != nil {
			return cp, st, ctx.Err()
		}
		data, complete, tooLong, err := logs.ReadLine(r, maxLine)
		if err != nil && err != io.EOF {
			return cp, st, err
		}
		atEOF := err == io.EOF
		if !complete && (len(data) == 0 || tooLong || !json.Valid(data)) {
			break
		}
		consumed := int64(len(data))
		if complete {
			consumed++
		}
		lineStart, lineNo := offset, lines
		offset += consumed
		lines++
		st.Lines++
		if tooLong {
			st.Skipped++
		} else {
			var l line
			var p payload
			if json.Unmarshal(data, &l) != nil || (len(l.Payload) > 0 && json.Unmarshal(l.Payload, &p) != nil) {
				st.Skipped++
			} else {
				switch l.Type {
				case "session_meta":
					if p.ID != "" {
						sessionID = p.ID
					}
					if p.CLIVersion != "" {
						st.FormatVersion = p.CLIVersion
					}
				case "turn_context":
					if p.Model != "" {
						model = p.Model
					}
				case "response_item":
					switch p.Type {
					case "function_call", "local_shell_call", "custom_tool_call":
						if len(calls) == 0 {
							segStart, segLines = lineStart, lineNo
						}
						name := p.Name
						if name == "" {
							name = p.Type
						}
						calls = append(calls, logs.ToolCall{Name: name})
						callIDs = append(callIDs, p.CallID)
					case "function_call_output", "custom_tool_call_output":
						if ref, ok := byCall[p.CallID]; ok {
							size, sum := measure(p.Output)
							tc := &ref.p.ev.Tools[ref.idx]
							tc.ResultBytes, tc.ResultHash = size, sum
							ref.p.open--
							delete(byCall, p.CallID)
						}
					default:
						st.Unknown++
					}
				case "event_msg":
					if p.Type != "token_count" {
						st.Unknown++
						break
					}
					if p.Info == nil || p.Info.Total.Total == prevTotal || p.Info.Last.Total <= 0 {
						break // no usage yet, or a duplicate update
					}
					prevTotal = p.Info.Total.Total
					ts, terr := time.Parse(time.RFC3339Nano, l.Timestamp)
					if terr != nil || sessionID == "" {
						st.Skipped++
						break
					}
					turns++
					u := p.Info.Last
					ev := logs.Event{
						Source: logs.SourceCodex, SessionKey: "o-" + logs.Hash(logs.SourceCodex, sessionID),
						RequestKey: logs.Hash(logs.SourceCodex, sessionID, p.Info.Total.keyString()),
						Turn:       turns, TS: ts.UnixMilli(), Model: model,
						Input:     max(0, u.Input-u.Cached-u.CacheWrite),
						CacheRead: u.Cached, CacheWriteNA: u.CacheWrite, Output: u.Output,
						Tools: calls,
					}
					pe := &pending{ev: ev, startOffset: lineStart, startLines: lineNo, startStats: st}
					if len(calls) > 0 {
						pe.startOffset, pe.startLines = segStart, segLines
					}
					pe.startStats.Lines = pe.startLines - cp.Lines // lines before this request in this pass
					for i, id := range callIDs {
						if id != "" {
							byCall[id] = &toolRef{p: pe, idx: i}
							pe.open++
						}
					}
					events = append(events, pe)
					calls, callIDs = nil, nil
				default:
					st.Unknown++
				}
			}
		}
		if atEOF {
			break
		}
	}

	cut := len(events)
	if s.now().Sub(time.UnixMilli(f.MTime)) < holdWindow {
		for i, p := range events {
			if p.open > 0 {
				cut = i
				break
			}
		}
	}
	end := logs.Checkpoint{Offset: offset, Lines: lines, Turns: turns}
	if cut < len(events) {
		p := events[cut]
		end = logs.Checkpoint{Offset: p.startOffset, Lines: p.startLines, Turns: p.ev.Turn - 1}
		keep := st.FormatVersion
		st = p.startStats
		if st.FormatVersion == "" {
			st.FormatVersion = keep
		}
	}
	for _, p := range events[:cut] {
		emit(p.ev)
	}
	return end, st, nil
}

func (u tokenUsage) keyString() string {
	b, _ := json.Marshal(u)
	return string(b)
}

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
	sum := sha256.Sum256(bytes.TrimSpace(raw))
	return int64(len(raw)), hex.EncodeToString(sum[:])[:16]
}

// prelude recovers the session id, model and last token total from the part of
// the file before the checkpoint, which a resumed pass does not re-read. Only
// the few lines that carry them are decoded.
func prelude(path string, upTo int64) (sessionID, model, version string, prevTotal int64) {
	prevTotal = -1
	f, err := os.Open(path) //nolint:gosec // a history file found by Discover
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	r := bufio.NewReaderSize(io.LimitReader(f, upTo), 1<<20)
	for {
		data, complete, tooLong, err := logs.ReadLine(r, maxLine)
		if !tooLong && complete {
			switch {
			case bytes.Contains(data, []byte(`"session_meta"`)), bytes.Contains(data, []byte(`"turn_context"`)), bytes.Contains(data, []byte(`"token_count"`)):
				var l line
				var p payload
				if json.Unmarshal(data, &l) == nil && json.Unmarshal(l.Payload, &p) == nil {
					switch l.Type {
					case "session_meta":
						if p.ID != "" {
							sessionID = p.ID
						}
						if p.CLIVersion != "" {
							version = p.CLIVersion
						}
					case "turn_context":
						if p.Model != "" {
							model = p.Model
						}
					case "event_msg":
						if p.Type == "token_count" && p.Info != nil && p.Info.Last.Total > 0 {
							prevTotal = p.Info.Total.Total
						}
					}
				}
			}
		}
		if err != nil {
			return
		}
	}
}
