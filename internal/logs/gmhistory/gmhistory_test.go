package gmhistory

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/logs"
)

// The fixture is written from the record types in the Gemini CLI source
// (packages/core/src/services/chatRecordingTypes.ts, read 2026-11-17), not
// captured from a real install; the text fields are placeholders.

var update = flag.Bool("update", false, "rewrite golden files")

const fixture = "testdata/session-2026-11-17T10-00-abcd1234.jsonl"

func parse(t *testing.T, path string, cp logs.Checkpoint) ([]logs.Event, logs.Checkpoint, logs.Stats) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	var evs []logs.Event
	end, st, err := (&Source{}).Parse(context.Background(), logs.File{Path: path, Size: info.Size()}, cp, func(e logs.Event) { evs = append(evs, e) })
	if err != nil {
		t.Fatal(err)
	}
	return evs, end, st
}

func TestParseGolden(t *testing.T) {
	evs, end, st := parse(t, fixture, logs.Checkpoint{})
	got, _ := json.MarshalIndent(map[string]any{"events": evs, "stats": st, "end": end}, "", "  ")
	got = append(got, '\n')
	golden := "testdata/basic.golden.json"
	if *update {
		_ = os.WriteFile(golden, got, 0o644)
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ReplaceAll(string(want), "\r\n", "\n") != string(got) {
		t.Errorf("golden mismatch (run with -update)\n%s", got)
	}
}

func TestUsageBuckets(t *testing.T) {
	evs, _, st := parse(t, fixture, logs.Checkpoint{})
	if len(evs) != 2 {
		t.Fatalf("want 2 requests (a message without tokens is not one), got %d", len(evs))
	}
	// Gemini's prompt count includes cached tokens: 12000 prompt, 8000 cached => 4000 uncached.
	// Thoughts are billed at the output price, so they are added to output.
	if e := evs[0]; e.Input != 4000 || e.CacheRead != 8000 || e.Output != 420 || e.Model != "gemini-3.1-pro-preview" || e.Source != logs.SourceGeminiCLI {
		t.Errorf("first request: %+v", e)
	}
	// Tool-use prompt tokens are outside the prompt count and are billed as input.
	if e := evs[1]; e.Input != 1050 || e.CacheRead != 12000 || e.Output != 40 {
		t.Errorf("second request: %+v", e)
	}
	if e := evs[0]; len(e.Tools) != 2 || e.Tools[0].Name != "read_file" || e.Tools[0].ResultBytes == 0 || e.Tools[0].ResultHash == "" || !e.Tools[1].Error {
		t.Errorf("tools: %+v", e.Tools)
	}
	if evs[0].SessionKey == "" || evs[0].SessionKey != evs[1].SessionKey || evs[0].RequestKey == evs[1].RequestKey {
		t.Errorf("keys: %+v", evs)
	}
	if st.Skipped != 1 {
		t.Errorf("skipped = %d, want 1 (the non-JSON line)", st.Skipped)
	}
}

func TestNothingTextualIsKept(t *testing.T) {
	evs, _, _ := parse(t, fixture, logs.Checkpoint{})
	b, _ := json.Marshal(evs)
	for _, s := range []string{"redacted", "package a", "/w/a.go", "go test", "abcd1234-0000"} {
		if strings.Contains(string(b), s) {
			t.Errorf("event carries %q: %s", s, b)
		}
	}
}

func TestResumeFromCheckpoint(t *testing.T) {
	all, end, _ := parse(t, fixture, logs.Checkpoint{})
	// Resume right after the first request's line: the session id must still be known.
	b, _ := os.ReadFile(fixture)
	lines := strings.SplitAfter(string(b), "\n")
	off := int64(len(lines[0]) + len(lines[1]) + len(lines[2]))
	rest, end2, _ := parse(t, fixture, logs.Checkpoint{Offset: off, Lines: 3, Turns: 1})
	if len(rest) != 1 || rest[0].RequestKey != all[1].RequestKey || rest[0].SessionKey != all[1].SessionKey || rest[0].Turn != 2 {
		t.Fatalf("resumed: %+v", rest)
	}
	if end2 != end {
		t.Errorf("end %+v != %+v", end2, end)
	}
}

func TestPartialLastLineWaits(t *testing.T) {
	dir := t.TempDir()
	b, _ := os.ReadFile(fixture)
	cut := strings.Index(string(b), `{"id":"m2"`) + 40
	p := filepath.Join(dir, "session-x.jsonl")
	_ = os.WriteFile(p, b[:cut], 0o600)
	evs, end, _ := parse(t, p, logs.Checkpoint{})
	if len(evs) != 0 || end.Offset != int64(strings.Index(string(b), `{"id":"m2"`)) {
		t.Fatalf("evs=%d end=%+v", len(evs), end)
	}
}

func TestDiscoverFindsMainAndSubagentFiles(t *testing.T) {
	home := t.TempDir()
	chats := filepath.Join(home, ".gemini", "tmp", "my-proj", "chats")
	for _, p := range []string{
		filepath.Join(chats, "session-2026-11-17T10-00-abcd1234.jsonl"),
		filepath.Join(chats, "abcd1234", "sub-1.jsonl"),
		filepath.Join(chats, "session-old.json"), // legacy whole-file JSON: not read
		filepath.Join(home, ".gemini", "tmp", "my-proj", "other.jsonl"),
	} {
		_ = os.MkdirAll(filepath.Dir(p), 0o700)
		_ = os.WriteFile(p, []byte("{}\n"), 0o600)
	}
	s := &Source{Home: func() (string, error) { return home, nil }, Getenv: func(string) string { return "" }}
	fs, err := s.Discover(context.Background(), nil)
	if err != nil || len(fs) != 2 {
		t.Fatalf("files = %+v, %v", fs, err)
	}
	s.Getenv = func(k string) string {
		if k == "GEMINI_CLI_HOME" {
			return filepath.Join(home, "elsewhere")
		}
		return ""
	}
	if fs, _ := s.Discover(context.Background(), nil); len(fs) != 0 {
		t.Errorf("GEMINI_CLI_HOME was ignored: %+v", fs)
	}
}
