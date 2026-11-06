package cchistory

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/logs"
)

var update = flag.Bool("update", false, "rewrite golden files")

// old makes every file look finished, so no tail is held back.
var old = func() time.Time { return time.Now().Add(24 * time.Hour) }

func parseFile(t *testing.T, path string, cp logs.Checkpoint, now func() time.Time) ([]logs.Event, logs.Checkpoint, logs.Stats) {
	t.Helper()
	s := &Source{Now: now}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	f := logs.File{Source: logs.SourceClaudeCode, Path: path, MTime: info.ModTime().UnixMilli()}
	var evs []logs.Event
	end, st, err := s.Parse(context.Background(), f, cp, func(e logs.Event) { evs = append(evs, e) })
	if err != nil {
		t.Fatal(err)
	}
	return evs, end, st
}

func TestParseGolden(t *testing.T) {
	// The file's mtime is "now", but Now is far in the future, so nothing is held.
	evs, end, st := parseFile(t, "testdata/v2_basic.jsonl", logs.Checkpoint{}, func() time.Time { return time.Now().Add(time.Hour) })
	got, _ := json.MarshalIndent(map[string]any{"events": evs, "stats": st, "end": end}, "", "  ")
	got = append(got, '\n')
	golden := "testdata/v2_basic.golden.json"
	if *update {
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ReplaceAll(string(want), "\r\n", "\n") != string(got) {
		t.Errorf("golden mismatch (run with -update to refresh)\n got: %s", got)
	}
}

func TestParseDedupesAndMatchesResults(t *testing.T) {
	evs, _, st := parseFile(t, "testdata/v2_basic.jsonl", logs.Checkpoint{}, old)
	if len(evs) != 2 {
		t.Fatalf("want 2 requests (one split over 2 lines, one synthetic skipped), got %d", len(evs))
	}
	a := evs[0]
	if a.Output != 50 || a.CacheWrite1h != 300 || a.CacheWriteNA != 0 {
		t.Errorf("usage counted wrong: %+v", a)
	}
	if len(a.Tools) != 1 || a.Tools[0].Name != "Read" || a.Tools[0].ResultBytes != int64(len("SECRET-FILE-CONTENT line1\nline2")) || a.Tools[0].ResultHash == "" {
		t.Errorf("tool call wrong: %+v", a.Tools)
	}
	if st.FormatVersion != "2.1.302" || st.UnknownFormat {
		t.Errorf("version: %+v", st)
	}
	if st.Unknown < 3 { // mode, api-request-blob, unknown type, synthetic
		t.Errorf("unknown lines not counted: %+v", st)
	}
}

func TestParseNeverCarriesText(t *testing.T) {
	evs, _, _ := parseFile(t, "testdata/v2_basic.jsonl", logs.Checkpoint{}, old)
	b, _ := json.Marshal(evs)
	for _, s := range []string{"SECRET", "alice", "main.go", "line1"} {
		if strings.Contains(string(b), s) {
			t.Errorf("event contains %q: %s", s, b)
		}
	}
}

func write(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func asst(req, id string) string {
	return `{"type":"assistant","sessionId":"s","requestId":"` + req + `","timestamp":"2026-10-20T10:00:00.000Z","version":"2.1.302","message":{"id":"m","model":"claude-sonnet-5-5","content":[{"type":"tool_use","id":"` + id + `","name":"Bash","input":{}}],"usage":{"input_tokens":1,"output_tokens":2}}}`
}

func TestMalformedInputSurvives(t *testing.T) {
	cases := map[string]string{
		"garbage":        "\x00\x01\x02 not json\n{{{{\n",
		"empty":          "",
		"truncated-tail": asst("r1", "t1") + "\n" + `{"type":"assistant","sessionId":"s","requ`,
		"unknown-ver":    strings.Replace(asst("r1", "t1"), "2.1.302", "9.0.0", 1) + "\n",
		"wrong-types":    `{"type":"assistant","sessionId":5,"message":"x"}` + "\n" + `{"type":"user","message":{"content":42}}` + "\n",
		"no-usage":       `{"type":"assistant","sessionId":"s","requestId":"r","timestamp":"2026-10-20T10:00:00Z","message":{"model":"m"}}` + "\n",
		"bad-timestamp":  strings.Replace(asst("r1", "t1"), "2026-10-20T10:00:00.000Z", "yesterday", 1) + "\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			p := write(t, "x.jsonl", content)
			evs, end, st := parseFile(t, p, logs.Checkpoint{}, old)
			if end.Offset > int64(len(content)) {
				t.Errorf("offset past end: %d", end.Offset)
			}
			if name == "unknown-ver" && (!st.UnknownFormat || len(evs) != 1) {
				t.Errorf("unknown version should parse best-effort and be flagged: %+v %d", st, len(evs))
			}
			if name == "truncated-tail" && (len(evs) != 1 || end.Offset != int64(len(asst("r1", "t1"))+1)) {
				t.Errorf("truncated tail should stop before the partial line: %d events, offset %d", len(evs), end.Offset)
			}
			if (name == "wrong-types" || name == "no-usage" || name == "bad-timestamp") && len(evs) != 0 {
				t.Errorf("want no events, got %d", len(evs))
			}
			if name == "garbage" && st.Skipped == 0 {
				t.Errorf("garbage lines not counted: %+v", st)
			}
		})
	}
}

func TestOversizedLineIsSkippedAndCounted(t *testing.T) {
	big := `{"type":"assistant","x":"` + strings.Repeat("a", maxLine+10) + `"}`
	p := write(t, "x.jsonl", big+"\n"+asst("r1", "t1")+"\n")
	evs, _, st := parseFile(t, p, logs.Checkpoint{}, old)
	if len(evs) != 1 || st.Skipped != 1 {
		t.Errorf("events=%d stats=%+v", len(evs), st)
	}
}

func TestIncrementalResumesAtCheckpoint(t *testing.T) {
	first := asst("r1", "t1") + "\n" + `{"type":"user","sessionId":"s","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}` + "\n"
	p := write(t, "x.jsonl", first)
	evs, cp, _ := parseFile(t, p, logs.Checkpoint{}, old)
	if len(evs) != 1 || cp.Offset != int64(len(first)) || cp.Turns != 1 {
		t.Fatalf("first pass: %d events, %+v", len(evs), cp)
	}
	// Nothing new: nothing emitted.
	if evs, _, _ := parseFile(t, p, cp, old); len(evs) != 0 {
		t.Fatalf("re-read emitted %d events", len(evs))
	}
	// Append one request, leaving a partial line at the end.
	more := asst("r2", "t2") + "\n" + `{"type":"user","sess`
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString(more)
	_ = f.Close()
	evs, cp2, _ := parseFile(t, p, cp, old)
	if len(evs) != 1 || evs[0].Turn != 2 {
		t.Fatalf("second pass: %+v", evs)
	}
	if cp2.Offset != cp.Offset+int64(len(asst("r2", "t2"))+1) {
		t.Errorf("checkpoint should stop before the partial line: %+v", cp2)
	}
}

func TestActiveFileHoldsBackUnansweredToolCalls(t *testing.T) {
	content := asst("r1", "t1") + "\n"
	p := write(t, "x.jsonl", content)
	now := func() time.Time { return time.Now() } // file was just written: active
	evs, cp, st := parseFile(t, p, logs.Checkpoint{}, now)
	if len(evs) != 0 || cp.Offset != 0 || st.Lines != 0 {
		t.Fatalf("should hold the request until its result arrives: %d events, %+v, %+v", len(evs), cp, st)
	}
	// The result arrives; now it is emitted whole.
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString(`{"type":"user","sessionId":"s","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"ok","is_error":true}]}}` + "\n")
	_ = f.Close()
	evs, _, _ = parseFile(t, p, cp, now)
	if len(evs) != 1 || len(evs[0].Tools) != 1 || !evs[0].Tools[0].Error || evs[0].Tools[0].ResultBytes != 2 {
		t.Fatalf("after result: %+v", evs)
	}
	// A finished file with an unanswered call is emitted anyway.
	p2 := write(t, "y.jsonl", content)
	if evs, _, _ := parseFile(t, p2, logs.Checkpoint{}, old); len(evs) != 1 {
		t.Errorf("finished file should flush: %d", len(evs))
	}
}

func TestDiscover(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{"projects/a/s1.jsonl", "projects/a/s1/subagents/agent-x.jsonl", "projects/a/s1/tool-results/big.jsonl", "projects/a/notes.txt"} {
		full := filepath.Join(root, filepath.FromSlash(p))
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		_ = os.WriteFile(full, []byte("{}\n"), 0o644)
	}
	s := &Source{Getenv: func(k string) string {
		if k == "CLAUDE_CONFIG_DIR" {
			return root
		}
		return ""
	}}
	files, err := s.Discover(context.Background(), nil)
	if err != nil || len(files) != 2 {
		t.Fatalf("want main + subagent file, got %d (%v)", len(files), err)
	}
	for _, f := range files {
		if strings.Contains(f.Key, "projects") || len(f.Key) != 24 {
			t.Errorf("key should be a hash: %q", f.Key)
		}
	}
	if files, err := s.Discover(context.Background(), []string{filepath.Join(root, "missing")}); err != nil || len(files) != 0 {
		t.Errorf("missing dir: %v %v", files, err)
	}
}

func FuzzHandleLine(f *testing.F) {
	for _, seed := range []string{asst("r", "t"), `{"type":"user","message":{"content":[{"type":"tool_result"}]}}`, `{`, ``, `{"type":"assistant","message":{"usage":{"input_tokens":"x"}}}`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, line string) {
		p := filepath.Join(t.TempDir(), "f.jsonl")
		if err := os.WriteFile(p, []byte(line+"\n"), 0o644); err != nil {
			t.Skip()
		}
		s := &Source{Now: old}
		_, _, _ = s.Parse(context.Background(), logs.File{Path: p}, logs.Checkpoint{}, func(logs.Event) {})
	})
}
