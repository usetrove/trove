// Package gmhistory reads Gemini CLI session history.
//
// UNVERIFIED ON REAL INSTALLS. Gemini CLI was not installed where this was
// written. What is verified (2026-11-17):
//
//   - geminicli.com/docs/cli/session-management/: sessions are stored per project
//     under ~/.gemini/tmp/<project>/chats/ and include token usage.
//   - The Gemini CLI source (github.com/google-gemini/gemini-cli, main,
//     packages/core/src/services/chatRecordingService.ts and chatRecordingTypes.ts;
//     packages/core/src/config/storage.ts): session files are
//     chats/session-<time>-<id>.jsonl, appended one JSON value per line; subagent
//     sessions are chats/<parent id>/<id>.jsonl. Lines are a metadata record
//     (sessionId, projectHash, startTime, kind "main"|"subagent"), message records
//     ({id, timestamp, type, content, ...}; type "gemini" adds model, tokens and
//     toolCalls [{id, name, args, result, status}]), and update records ($set,
//     $patch, $rewindTo). tokens is {input, output, cached, thoughts, tool, total}
//     taken from promptTokenCount, candidatesTokenCount, cachedContentTokenCount,
//     thoughtsTokenCount, toolUsePromptTokenCount and totalTokenCount. The home
//     folder honours GEMINI_CLI_HOME. Older .json whole-file sessions are not read.
//   - aistudio.google.com/api-reference/generate-content (the Gemini API reference):
//     promptTokenCount "includes the number of tokens in the cached content";
//     totalTokenCount is "prompt + tool-use prompt + thoughts + response candidates",
//     from which Trove infers that tool-use prompt and thought tokens are outside
//     the prompt and candidate counts. Thoughts are billed at the output price
//     (internal/pricing); tool-use prompt tokens are counted as uncached input.
//
// The pages carry a notice that Gemini CLI was replaced by Antigravity CLI on
// 2026-06-18, so the format may stop changing or move; run `trove logs scan` on a
// real install and check the logs verification notes before relying on numbers.
package gmhistory

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
	"time"

	"github.com/usetrove/trove/internal/logs"
)

// ParserVersion changes when the parser's output for the same input changes.
const ParserVersion = 1

const maxLine = 16 << 20

// Source implements logs.Source for Gemini CLI.
type Source struct {
	Getenv func(string) string
	Home   func() (string, error)
}

var _ logs.Source = (*Source)(nil)

// Name returns the source name.
func (s *Source) Name() string { return logs.SourceGeminiCLI }

func (s *Source) root() string {
	getenv := s.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	home := getenv("GEMINI_CLI_HOME")
	if home == "" {
		h := s.Home
		if h == nil {
			h = os.UserHomeDir
		}
		var err error
		if home, err = h(); err != nil || home == "" {
			return ""
		}
	}
	return filepath.Join(home, ".gemini", "tmp")
}

// Discover lists *.jsonl files inside each project's chats folder. Missing
// directories are not an error.
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
			if d.IsDir() || filepath.Ext(p) != ".jsonl" || !underChats(root, p) {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			out = append(out, logs.File{Source: logs.SourceGeminiCLI, Path: p, Key: logs.Hash("file", p), Size: info.Size(), MTime: info.ModTime().UnixMilli()})
			return nil
		})
		if err != nil && !errors.Is(err, fs.SkipAll) {
			return out, err
		}
	}
	return out, nil
}

// underChats reports whether p sits in a "chats" folder below root.
func underChats(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	for d := filepath.Dir(rel); d != "." && d != string(filepath.Separator); d = filepath.Dir(d) {
		if filepath.Base(d) == "chats" {
			return true
		}
	}
	return false
}

type record struct {
	// metadata
	SessionID string `json:"sessionId"`
	Kind      string `json:"kind"`
	// message
	ID        string          `json:"id"`
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Model     string          `json:"model"`
	Tokens    *tokens         `json:"tokens"`
	ToolCalls []toolCall      `json:"toolCalls"`
	Set       json.RawMessage `json:"$set"`
	Patch     json.RawMessage `json:"$patch"`
	Rewind    json.RawMessage `json:"$rewindTo"`
}

type tokens struct {
	Input    int64 `json:"input"`
	Output   int64 `json:"output"`
	Cached   int64 `json:"cached"`
	Thoughts int64 `json:"thoughts"`
	Tool     int64 `json:"tool"`
}

type toolCall struct {
	Name   string          `json:"name"`
	Result json.RawMessage `json:"result"`
	Status string          `json:"status"`
}

// Parse reads f from cp. Each "gemini" message that carries token counts becomes
// one request, keyed by the session and message id, so a message written again
// with the same id replaces its row instead of adding one.
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
	var sessionID string
	var sidechain bool
	if cp.Offset > 0 {
		sessionID, sidechain = prelude(f.Path, cp.Offset)
	}
	if _, err := file.Seek(cp.Offset, io.SeekStart); err != nil {
		return cp, st, err
	}
	r := bufio.NewReaderSize(file, 1<<20)
	end := cp
	emitted := map[string]bool{}
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
			break // a partial last line waits for the next pass
		}
		end.Offset += int64(len(data))
		if complete {
			end.Offset++
		}
		end.Lines++
		st.Lines++
		var rec record
		switch {
		case tooLong:
			st.Skipped++
		case json.Unmarshal(data, &rec) != nil:
			st.Skipped++
		case rec.SessionID != "" && rec.ID == "":
			sessionID, sidechain = rec.SessionID, rec.Kind == "subagent"
		case rec.Type == "gemini" && rec.Tokens != nil:
			ts, terr := time.Parse(time.RFC3339Nano, rec.Timestamp)
			if terr != nil || sessionID == "" || rec.ID == "" || emitted[rec.ID] {
				st.Skipped++
				break
			}
			emitted[rec.ID] = true
			end.Turns++
			t := rec.Tokens
			ev := logs.Event{
				Source: logs.SourceGeminiCLI, SessionKey: "g-" + logs.Hash(logs.SourceGeminiCLI, sessionID),
				RequestKey: logs.Hash(logs.SourceGeminiCLI, sessionID, rec.ID),
				Turn:       end.Turns, TS: ts.UnixMilli(), Model: rec.Model, Sidechain: sidechain,
				Input:     max(0, t.Input-t.Cached) + t.Tool,
				CacheRead: t.Cached, Output: t.Output + t.Thoughts,
			}
			for _, c := range rec.ToolCalls {
				size, sum := measure(c.Result)
				ev.Tools = append(ev.Tools, logs.ToolCall{Name: c.Name, ResultBytes: size, ResultHash: sum, Error: c.Status == "error"})
			}
			emit(ev)
		case rec.Type != "" || len(rec.Set) > 0 || len(rec.Patch) > 0 || len(rec.Rewind) > 0:
			// Other messages and update records carry no usage. A rewound message was
			// still billed, so its row stays.
			st.Unknown++
		default:
			st.Unknown++
		}
		if atEOF {
			break
		}
	}
	return end, st, nil
}

func measure(raw json.RawMessage) (int64, string) {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || string(t) == "null" {
		return 0, ""
	}
	sum := sha256.Sum256(t)
	return int64(len(t)), hex.EncodeToString(sum[:])[:16]
}

// prelude recovers the session id from the part of the file before the
// checkpoint, which a resumed pass does not re-read. Only metadata lines are decoded.
func prelude(path string, upTo int64) (sessionID string, sidechain bool) {
	f, err := os.Open(path) //nolint:gosec // a history file found by Discover
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	r := bufio.NewReaderSize(io.LimitReader(f, upTo), 1<<20)
	for {
		data, complete, tooLong, err := logs.ReadLine(r, maxLine)
		if complete && !tooLong && bytes.Contains(data, []byte(`"sessionId"`)) {
			var rec record
			if json.Unmarshal(data, &rec) == nil && rec.SessionID != "" && rec.ID == "" {
				sessionID, sidechain = rec.SessionID, rec.Kind == "subagent"
			}
		}
		if err != nil {
			return
		}
	}
}
