package stress

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// canary is planted in message text and tool output. It must never appear in anything the replay returns, and
// the logs themselves must be left byte for byte as they were.
const canary = "TROVE_CANARY_REPLAY_7f3a9c"

// writeFixtureLog writes one synthetic Claude Code session log: user prompts, streamed assistant responses and
// a long tool result of test-runner-like lines, which is what the trimmer acts on.
func writeFixtureLog(t *testing.T, dir, name string, turns int) string {
	t.Helper()
	var b bytes.Buffer
	enc := func(v any) {
		line, _ := json.Marshal(v)
		b.Write(line)
		b.WriteByte('\n')
	}
	enc(map[string]any{"type": "user", "message": map[string]any{"role": "user",
		"content": "keep the tests green " + canary}})
	for i := 0; i < turns; i++ {
		rid := "req_" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		enc(map[string]any{"type": "assistant", "requestId": rid, "message": map[string]any{"role": "assistant",
			"model": "claude-sonnet-5-5", "content": []any{map[string]any{"type": "tool_use", "id": "toolu_" + rid,
				"name": "Bash", "input": map[string]any{"command": "go test ./..."}}}}})
		var out strings.Builder
		for l := 0; l < 200; l++ {
			out.WriteString("ok   pkg" + string(rune('a'+l%9)) + "  0.0" + string(rune('0'+l%9)) + "s " + canary + "\n")
		}
		enc(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "toolu_" + rid, "content": out.String()}}}})
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReplayOutputIsAggregatesOnly(t *testing.T) {
	logs := t.TempDir()
	writeFixtureLog(t, logs, "session-one.jsonl", 6)
	writeFixtureLog(t, logs, "session-two.jsonl", 4)
	if err := os.WriteFile(filepath.Join(logs, "broken.jsonl"), []byte("{not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	before := snapshotFiles(t, logs)
	sum, err := replayLogs(logs, 8)
	if err != nil {
		t.Fatal(err)
	}
	after := snapshotFiles(t, logs)
	if !equalSnapshots(before, after) {
		t.Fatal("replay changed a log file")
	}

	b, err := json.Marshal(sum)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), canary) {
		t.Fatal("replay output contains message or tool text")
	}
	if strings.Contains(string(b), logs) || strings.Contains(string(b), "session-one") {
		t.Fatal("replay output contains a path or file name")
	}
	if sum.LogFiles != 3 || sum.Sessions != 2 || sum.Turns != 10 {
		t.Fatalf("counts: files=%d sessions=%d turns=%d, want 3, 2, 10", sum.LogFiles, sum.Sessions, sum.Turns)
	}
	// A line that is not JSON is skipped, not fatal: the broken file is read, yields no turns, and is not an
	// I/O failure. Unreadable means the file could not be opened or streamed.
	if sum.FilesUnreadable != 0 {
		t.Fatalf("unreadable files = %d, want 0; a malformed line is skipped, not an I/O failure", sum.FilesUnreadable)
	}
	if sum.Label != "estimated" {
		t.Fatalf("label = %q, want estimated", sum.Label)
	}
	if len(sum.Optimizers) != len(replayTargets) {
		t.Fatalf("optimizers = %d, want %d", len(sum.Optimizers), len(replayTargets))
	}
}

// The replay must not leave anything in the scratch folder either: it has no reason to, and a stray file
// would be a leak path.
func TestReplayWritesNothingToScratch(t *testing.T) {
	scratch, err := scratchDir()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	logs := t.TempDir()
	writeFixtureLog(t, logs, "s.jsonl", 3)
	if _, err := replayLogs(logs, 4); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(scratch)
	if len(entries) != 0 {
		t.Fatalf("scratch folder has %d entries after replay", len(entries))
	}
}

func TestReplayRejectsAFileAndAMissingFolder(t *testing.T) {
	logs := t.TempDir()
	p := writeFixtureLog(t, logs, "one.jsonl", 1)
	if _, err := replayLogs(p, 2); err == nil {
		t.Fatal("a single file was accepted as a folder")
	}
	if _, err := replayLogs(filepath.Join(logs, "nope"), 2); err == nil {
		t.Fatal("a missing folder was accepted")
	}
}

type fileSnap struct {
	size int64
	data string
}

func snapshotFiles(t *testing.T, dir string) map[string]fileSnap {
	t.Helper()
	out := map[string]fileSnap{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		info, _ := e.Info()
		b, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		out[e.Name()] = fileSnap{size: info.Size(), data: string(b)}
	}
	return out
}

func equalSnapshots(a, b map[string]fileSnap) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
