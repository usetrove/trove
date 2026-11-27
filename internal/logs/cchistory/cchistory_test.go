package cchistory

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strconv"
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

// A Bash call's command is kept in memory for `trove fix`, never in what the event marshals to.
func TestBashCommandInMemoryOnly(t *testing.T) {
	line := strings.Replace(asst("r1", "t1"), `"input":{}`, `"input":{"command":"python3 scripts/SECRET_gen.py --all"}`, 1)
	read := strings.Replace(asst("r2", "t2"), `"name":"Bash","input":{}`, `"name":"Read","input":{"command":"not bash"}`, 1)
	huge := strings.Replace(asst("r3", "t3"), `"input":{}`, `"input":{"command":"`+strings.Repeat("x", logs.MaxCommandBytes+1)+`"}`, 1)
	p := write(t, "x.jsonl", line+"\n"+read+"\n"+huge+"\n")
	evs, _, _ := parseFile(t, p, logs.Checkpoint{}, old)
	if len(evs) != 3 {
		t.Fatalf("got %d events", len(evs))
	}
	if got := evs[0].Tools[0].Command; got != "python3 scripts/SECRET_gen.py --all" {
		t.Errorf("Bash command = %q", got)
	}
	if evs[1].Tools[0].Command != "" || evs[2].Tools[0].Command != "" {
		t.Errorf("non-Bash or oversized command kept: %q / %d bytes", evs[1].Tools[0].Command, len(evs[2].Tools[0].Command))
	}
	if b, _ := json.Marshal(evs); strings.Contains(string(b), "SECRET") {
		t.Errorf("command text marshalled: %s", b)
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

// line builds an assistant line for request req with the given usage JSON and extra top-level fields.
func usageLine(req, extra, usage, content string) string {
	return `{"type":"assistant","sessionId":"s","requestId":"` + req + `","timestamp":"2026-10-20T10:00:00.000Z","version":"2.1.331"` + extra +
		`,"message":{"id":"m","model":"claude-sonnet-5-5","content":` + content + `,"usage":` + usage + `}}`
}

// A request written over several lines whose usage grows keeps the last line's figures, not the first.
func TestMergeTakesFinalUsage(t *testing.T) {
	u := func(out, think, write5, write1, read int) string {
		return `{"input_tokens":3,"output_tokens":` + itoa(out) + `,"cache_read_input_tokens":` + itoa(read) +
			`,"cache_creation_input_tokens":` + itoa(write5+write1) +
			`,"cache_creation":{"ephemeral_5m_input_tokens":` + itoa(write5) + `,"ephemeral_1h_input_tokens":` + itoa(write1) + `}` +
			`,"output_tokens_details":{"thinking_tokens":` + itoa(think) + `}}`
	}
	content := `[{"type":"text","text":"x"}]`
	body := usageLine("r1", "", u(1, 0, 0, 0, 0), `[{"type":"thinking","thinking":"x"}]`) + "\n" +
		usageLine("r1", "", u(40, 30, 0, 100, 900), content) + "\n" +
		usageLine("r1", "", u(120, 90, 20, 100, 900), `[{"type":"tool_use","id":"t1","name":"Read","input":{"a":1}}]`) + "\n"
	evs, _, _ := parseFile(t, write(t, "x.jsonl", body), logs.Checkpoint{}, old)
	if len(evs) != 1 {
		t.Fatalf("want 1 merged request, got %d", len(evs))
	}
	e := evs[0]
	if e.Output != 120 || e.Thinking == nil || *e.Thinking != 90 || e.CacheWrite5m != 20 || e.CacheWrite1h != 100 || e.CacheRead != 900 || e.Input != 3 {
		t.Errorf("want final usage, got %+v (thinking %v)", e, e.Thinking)
	}
	if len(e.Tools) != 1 || e.Tools[0].InputBytes != int64(len(`{"a":1}`)) {
		t.Errorf("tool: %+v", e.Tools)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestThinkingUnknownVersusZero(t *testing.T) {
	base := `{"input_tokens":1,"output_tokens":9`
	cases := []struct {
		name  string
		usage string
		want  *int64
	}{
		{"absent", base + `}`, nil},
		{"null", base + `,"output_tokens_details":null}`, nil},
		{"no field inside", base + `,"output_tokens_details":{}}`, nil},
		{"wrong type", base + `,"output_tokens_details":{"thinking_tokens":"7"}}`, nil},
		{"negative", base + `,"output_tokens_details":{"thinking_tokens":-1}}`, nil},
		{"zero", base + `,"output_tokens_details":{"thinking_tokens":0}}`, ptr(0)},
		{"some", base + `,"output_tokens_details":{"thinking_tokens":7}}`, ptr(7)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			evs, _, _ := parseFile(t, write(t, "x.jsonl", usageLine("r", "", c.usage, `[]`)+"\n"), logs.Checkpoint{}, old)
			if len(evs) != 1 {
				t.Fatalf("a bad details field must not drop the request: %d events", len(evs))
			}
			got := evs[0].Thinking
			if (got == nil) != (c.want == nil) || (got != nil && *got != *c.want) {
				t.Errorf("thinking = %v, want %v", deref(got), deref(c.want))
			}
			if evs[0].Output != 9 {
				t.Errorf("thinking must not change output: %d", evs[0].Output)
			}
		})
	}
}

func ptr(n int64) *int64 { return &n }

func deref(p *int64) any {
	if p == nil {
		return "unknown"
	}
	return *p
}

// Context strings only get through when they match a strict pattern, and a bad one never drops the request.
func TestContextFieldsAreChecked(t *testing.T) {
	usage := `{"input_tokens":1,"output_tokens":1}`
	cases := []struct {
		name, extra, version                     string
		wantVer, wantEntry, wantEff, wantTurnEff string
	}{
		{"observed values", `,"entrypoint":"sdk-cli","effort":"medium","perTurnEffort":"max"`, "2.1.331", "2.1.331", "sdk-cli", "medium", "max"},
		{"absent", ``, "2.1.331", "2.1.331", "", "", ""},
		{"unknown effort", `,"effort":"turbo","perTurnEffort":"HIGH"`, "2.1.331", "2.1.331", "", "", ""},
		{"wrong types", `,"entrypoint":7,"effort":{"level":"high"},"perTurnEffort":["low"]`, "2.1.331", "2.1.331", "", "", ""},
		{"free text", `,"entrypoint":"/home/alice/SECRET path"`, "2.1.331 SECRET", "", "", "", ""},
		{"too long", `,"entrypoint":"` + strings.Repeat("a", 33) + `"`, "2.1.331", "2.1.331", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := strings.Replace(usageLine("r", c.extra, usage, `[]`), `"2.1.331"`, strconv.Quote(c.version), 1)
			evs, _, _ := parseFile(t, write(t, "x.jsonl", l+"\n"), logs.Checkpoint{}, old)
			if len(evs) != 1 {
				t.Fatalf("got %d events", len(evs))
			}
			e := evs[0]
			if e.AgentVersion != c.wantVer || e.Entrypoint != c.wantEntry || e.Effort != c.wantEff || e.TurnEffort != c.wantTurnEff {
				t.Errorf("got version %q entrypoint %q effort %q turn %q", e.AgentVersion, e.Entrypoint, e.Effort, e.TurnEffort)
			}
		})
	}
}

// Tool input is measured, never kept.
func TestToolInputSizeOnly(t *testing.T) {
	in := `{"file_path":"/home/alice/SECRET-INPUT.go","limit":5}`
	l := usageLine("r", "", `{"input_tokens":1,"output_tokens":1}`,
		`[{"type":"tool_use","id":"t1","name":"Read","input":`+in+`},{"type":"tool_use","id":"t2","name":"Bash"}]`)
	evs, _, _ := parseFile(t, write(t, "x.jsonl", l+"\n"), logs.Checkpoint{}, old)
	if len(evs) != 1 || len(evs[0].Tools) != 2 {
		t.Fatalf("got %+v", evs)
	}
	if got := evs[0].Tools[0].InputBytes; got != int64(len(in)) {
		t.Errorf("input bytes = %d, want %d", got, len(in))
	}
	if got := evs[0].Tools[1].InputBytes; got != 0 {
		t.Errorf("missing input should measure 0, got %d", got)
	}
	if b, _ := json.Marshal(evs); strings.Contains(string(b), "SECRET") || strings.Contains(string(b), "alice") {
		t.Errorf("input text in event: %s", b)
	}
}

func userLine(extra, content string) string {
	return `{"type":"user","sessionId":"s","timestamp":"2026-10-20T10:00:00.000Z"` + extra + `,"message":{"role":"user","content":` + content + `}}`
}

const boundary = `{"type":"system","subtype":"compact_boundary","sessionId":"s","compactMetadata":{"trigger":"manual"}}`

// Each request records what came right before it, and a compaction boundary marks only the next request.
func TestPhaseAndCompaction(t *testing.T) {
	u := `{"input_tokens":1,"output_tokens":1}`
	body := strings.Join([]string{
		usageLine("r1", "", u, `[]`), // start of file
		userLine("", `"SECRET prompt"`),
		usageLine("r2", "", u, `[{"type":"tool_use","id":"t1","name":"Read","input":{}}]`),
		userLine("", `[{"type":"tool_result","tool_use_id":"t1","content":"x"}]`),
		userLine(`,"isMeta":true`, `"SECRET meta note"`), // written by Claude Code: changes nothing
		usageLine("r3", "", u, `[]`),
		boundary,
		userLine(`,"isCompactSummary":true`, `"SECRET summary"`),
		usageLine("r4", "", u, `[]`),
		userLine("", `[{"type":"text","text":"SECRET typed"}]`),
		usageLine("r5", "", u, `[]`),
		usageLine("r6", "", u, `[]`), // a second request with nothing in between keeps the phase
	}, "\n") + "\n"
	evs, end, _ := parseFile(t, write(t, "x.jsonl", body), logs.Checkpoint{}, old)
	want := []struct {
		phase     string
		compacted bool
	}{
		{logs.PhaseOther, false}, {logs.PhasePrompt, false}, {logs.PhaseToolResult, false},
		{logs.PhaseCompact, true}, {logs.PhasePrompt, false}, {logs.PhasePrompt, false},
	}
	if len(evs) != len(want) {
		t.Fatalf("got %d events", len(evs))
	}
	for i, w := range want {
		if evs[i].Phase != w.phase || evs[i].Compacted != w.compacted {
			t.Errorf("request %d: phase %q compacted %v, want %q %v", i+1, evs[i].Phase, evs[i].Compacted, w.phase, w.compacted)
		}
	}
	if end.Phase != logs.PhasePrompt || end.Compacted {
		t.Errorf("checkpoint carry: %+v", end)
	}
}

// The phase and a pending compaction survive a checkpoint, both at the end of a pass and when a pass
// rewinds to a held request.
func TestPhaseCarriesAcrossPasses(t *testing.T) {
	u := `{"input_tokens":1,"output_tokens":1}`
	first := usageLine("r1", "", u, `[{"type":"tool_use","id":"t1","name":"Read","input":{}}]`) + "\n" +
		userLine("", `[{"type":"tool_result","tool_use_id":"t1","content":"x"}]`) + "\n" + boundary + "\n"
	p := write(t, "x.jsonl", first)
	_, cp, _ := parseFile(t, p, logs.Checkpoint{}, old)
	if cp.Phase != logs.PhaseToolResult || !cp.Compacted {
		t.Fatalf("carry after first pass: %+v", cp)
	}
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString(usageLine("r2", "", u, `[{"type":"tool_use","id":"t2","name":"Read","input":{}}]`) + "\n")
	_ = f.Close()
	// The file is active and r2's call has no result: the pass holds r2 and rewinds to it.
	evs, cp2, _ := parseFile(t, p, cp, time.Now)
	if len(evs) != 0 || cp2.Offset != cp.Offset || cp2.Phase != logs.PhaseToolResult || !cp2.Compacted {
		t.Fatalf("held pass: %d events, %+v", len(evs), cp2)
	}
	evs, _, _ = parseFile(t, p, cp2, old)
	if len(evs) != 1 || evs[0].Phase != logs.PhaseToolResult || !evs[0].Compacted {
		t.Fatalf("r2 after the checkpoint: %+v", evs)
	}
}

func TestAgentKeyAndTextBytes(t *testing.T) {
	u := `{"input_tokens":1,"output_tokens":1}`
	body := usageLine("r1", `,"agentId":"a1b2SECRET"`, u, `[{"type":"text","text":"héllo"}]`) + "\n" +
		usageLine("r1", `,"agentId":"a1b2SECRET"`, u, `[{"type":"text","text":"abc"},{"type":"text","text":7}]`) + "\n" +
		usageLine("r2", "", u, `[]`) + "\n"
	evs, _, _ := parseFile(t, write(t, "x.jsonl", body), logs.Checkpoint{}, old)
	if len(evs) != 2 {
		t.Fatalf("got %d events", len(evs))
	}
	if evs[0].AgentKey == "" || strings.Contains(evs[0].AgentKey, "SECRET") || evs[1].AgentKey != "" {
		t.Errorf("agent keys: %q %q", evs[0].AgentKey, evs[1].AgentKey)
	}
	if evs[0].TextBytes != int64(len("héllo")+len("abc")) {
		t.Errorf("text bytes = %d", evs[0].TextBytes)
	}
}
