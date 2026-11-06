package codex

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

func future() time.Time { return time.Now().Add(24 * time.Hour) }

func parse(t *testing.T, path string, cp logs.Checkpoint) ([]logs.Event, logs.Checkpoint, logs.Stats) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	s := &Source{Now: future}
	var evs []logs.Event
	end, st, err := s.Parse(context.Background(), logs.File{Path: path, MTime: info.ModTime().UnixMilli()}, cp, func(e logs.Event) { evs = append(evs, e) })
	if err != nil {
		t.Fatal(err)
	}
	return evs, end, st
}

func TestParseGolden(t *testing.T) {
	evs, end, st := parse(t, "testdata/rollout_basic.jsonl", logs.Checkpoint{})
	got, _ := json.MarshalIndent(map[string]any{"events": evs, "stats": st, "end": end}, "", "  ")
	got = append(got, '\n')
	golden := "testdata/rollout_basic.golden.json"
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

func TestUsageIsSplitAndDeduped(t *testing.T) {
	evs, _, st := parse(t, "testdata/rollout_basic.jsonl", logs.Checkpoint{})
	if len(evs) != 2 {
		t.Fatalf("want 2 requests (duplicate and null-info updates ignored), got %d", len(evs))
	}
	// OpenAI input includes cached tokens: 1000 input, 800 cached => 200 uncached.
	if e := evs[0]; e.Input != 200 || e.CacheRead != 800 || e.Output != 50 || e.Model != "gpt-5" {
		t.Errorf("first request: %+v", e)
	}
	if e := evs[1]; e.Input != 300 || e.CacheRead != 1000 || e.Output != 70 {
		t.Errorf("second request uses last_token_usage, not the running total: %+v", e)
	}
	if len(evs[0].Tools) != 1 || evs[0].Tools[0].Name != "shell" || evs[0].Tools[0].ResultBytes == 0 {
		t.Errorf("tool call: %+v", evs[0].Tools)
	}
	if st.FormatVersion != "0.99.0" || st.Skipped != 1 {
		t.Errorf("stats: %+v", st)
	}
}

func TestNeverCarriesText(t *testing.T) {
	evs, _, _ := parse(t, "testdata/rollout_basic.jsonl", logs.Checkpoint{})
	b, _ := json.Marshal(evs)
	for _, s := range []string{"SECRET", "alice", "main.go", "codex-sess-1"} {
		if strings.Contains(string(b), s) {
			t.Errorf("event contains %q: %s", s, b)
		}
	}
}

func TestMalformedAndIncremental(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "rollout-x.jsonl")
	first := `{"timestamp":"2026-10-20T10:00:00Z","type":"session_meta","payload":{"id":"s","cli_version":"1"}}` + "\n" +
		`{"timestamp":"2026-10-20T10:00:01Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":10},"last_token_usage":{"input_tokens":10,"cached_input_tokens":0,"output_tokens":1,"total_tokens":11}}}}` + "\n"
	if err := os.WriteFile(p, []byte("\x00\x01garbage\n"+first), 0o644); err != nil {
		t.Fatal(err)
	}
	evs, cp, st := parse(t, p, logs.Checkpoint{})
	if len(evs) != 1 || st.Skipped != 1 {
		t.Fatalf("events=%d stats=%+v", len(evs), st)
	}
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString(`{"timestamp":"2026-10-20T10:00:09Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":30},"last_token_usage":{"input_tokens":20,"output_tokens":2,"total_tokens":22}}}}` + "\n" + `{"timestamp":"2026-`)
	_ = f.Close()
	evs, _, _ = parse(t, p, cp)
	if len(evs) != 1 || evs[0].Turn != 2 {
		t.Fatalf("append pass should see only the new request: %+v", evs)
	}
}

func TestDiscoverUsesCodexHome(t *testing.T) {
	root := t.TempDir()
	d := filepath.Join(root, "sessions", "2026", "10", "20")
	_ = os.MkdirAll(d, 0o755)
	_ = os.WriteFile(filepath.Join(d, "rollout-a.jsonl"), []byte("{}\n"), 0o644)
	s := &Source{Getenv: func(k string) string {
		if k == "CODEX_HOME" {
			return root
		}
		return ""
	}}
	files, err := s.Discover(context.Background(), nil)
	if err != nil || len(files) != 1 {
		t.Fatalf("%v %v", files, err)
	}
	if files, err := (&Source{Home: func() (string, error) { return filepath.Join(root, "nohome"), nil }, Getenv: func(string) string { return "" }}).Discover(context.Background(), nil); err != nil || len(files) != 0 {
		t.Errorf("missing dir: %v %v", files, err)
	}
}
