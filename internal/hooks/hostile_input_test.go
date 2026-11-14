package hooks

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A hook's input comes from the agent, which may be steered by a hostile repository or prompt. A session id or a
// file path made of path tricks must never make a hook write outside Trove's own data folder, and must never reach
// disk as text: only hashes are stored.
func TestHostileSessionIDsAndPathsStayInsideTheDataFolder(t *testing.T) {
	base := t.TempDir()
	r := &rig{t: t, dir: filepath.Join(base, "data", ".trove"), files: filepath.Join(base, "files"), env: func(string) string { return "" }}
	r.now = newRig(t, nil).now
	if err := os.MkdirAll(r.files, 0o755); err != nil {
		t.Fatal(err)
	}
	r.snapshot(Snapshot{States: map[string]string{rwLimit: "on", "hook_repeat_read": "on"}})
	r.allow(Capability{Tool: "Read", InputRewrite: CapYes})

	ids := []string{
		"../../../../evil", `..\..\..\evil`, "/etc/passwd", "a/b/c", "x\x00y", strings.Repeat("A", 5000),
		"..", ".", "con", "nul", "sess/../../escape", "%2e%2e%2f%2e%2e%2fescape", "ses sion; rm -rf ~",
	}
	secretPath := filepath.Join(r.files, "SECRET-PROJECT-payroll.go")
	if err := os.WriteFile(secretPath, []byte(strings.Repeat("package x // a line of code\n", 3000)), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		in := func(event, tool string, extra map[string]any) string {
			m := map[string]any{"session_id": id, "prompt_id": "p", "hook_event_name": event, "tool_name": tool,
				"tool_input": map[string]any{"file_path": secretPath}, "cwd": "/home/alice/secret-project", "source": "startup"}
			for k, v := range extra {
				m[k] = v
			}
			b, _ := json.Marshal(m)
			return string(b)
		}
		r.run(EventReset, in("SessionStart", "", nil))
		r.run(EventPreRead, in("PreToolUse", "Read", nil))
		r.run(EventPostRead, in("PostToolUse", "Read", map[string]any{"tool_response": map[string]any{"file": map[string]any{"content": "x"}}}))
		r.run(EventPostRun, in("PostToolUse", runTool, nil))
		r.run(EventSessionEnd, in("SessionEnd", "", nil))
	}

	// Nothing was written anywhere under the base folder except Trove's data folder and the project file we made.
	err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		inData := strings.HasPrefix(p, r.dir+string(filepath.Separator))
		if !inData && p != secretPath {
			t.Errorf("a hook wrote outside the data folder: %s", p)
		}
		if inData {
			b, _ := os.ReadFile(p)
			for _, leak := range []string{"SECRET-PROJECT", "payroll", "alice", "etc/passwd", "rm -rf"} {
				if strings.Contains(string(b), leak) {
					t.Errorf("%s holds %q: only hashes may be stored", p, leak)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Every spool line is the right shape: a hashed key, never the raw id.
	for _, e := range r.events() {
		if !strings.HasPrefix(e.Key, "c-") || len(e.Key) > 64 {
			t.Errorf("session key %q is not a hash", e.Key)
		}
	}
}
