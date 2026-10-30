package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
)

const up = "https://api.anthropic.com"

var t0 = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

func write(t *testing.T, path, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const claudeOriginal = `{
  "model": "opus",
  "env": {"FOO": "bar"},
  "hooks": {"PreToolUse": [{"matcher": "Bash"}]}
}
`

const codexOriginal = `# my codex config
model = "gpt-5"
approval_policy = "on-request"

[profiles.fast]
model = "gpt-5-mini"
`

func TestClaudeCodeApplyAndExactUndo(t *testing.T) {
	data, claude := t.TempDir(), t.TempDir()
	path := filepath.Join(claude, "settings.json")
	write(t, path, claudeOriginal)
	e, err := PlanClaudeCode(claude, 8787, up)
	if err != nil {
		t.Fatal(err)
	}
	if read(t, path) != claudeOriginal {
		t.Fatal("planning must not write anything")
	}
	if _, err := Apply(data, []*Edit{e}, t0); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Model string            `json:"model"`
		Env   map[string]string `json:"env"`
		Hooks json.RawMessage   `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(read(t, path)), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Env["ANTHROPIC_BASE_URL"] != "http://127.0.0.1:8787/anthropic" || doc.Env["FOO"] != "bar" || doc.Model != "opus" || len(doc.Hooks) == 0 {
		t.Errorf("merge lost or missed keys: %+v", doc)
	}
	if strings.Contains(read(t, path), "API_KEY") {
		t.Error("Trove must never write credentials")
	}
	// Idempotent.
	if again, _ := PlanClaudeCode(claude, 8787, up); !again.NoOp {
		t.Error("a second plan should be a no-op")
	}
	res, err := Undo(data)
	if err != nil || len(res) != 1 || res[0].Action != "restored" {
		t.Fatalf("undo: %v %+v", err, res)
	}
	if read(t, path) != claudeOriginal {
		t.Errorf("undo must restore the original byte for byte:\n%s", read(t, path))
	}
	if cs, _ := LoadChanges(data); len(cs) != 0 {
		t.Error("manifest should be empty after undo")
	}
}

func TestClaudeCodeFileCreatedByTrovelIsRemoved(t *testing.T) {
	data, claude := t.TempDir(), filepath.Join(t.TempDir(), ".claude")
	e, err := PlanClaudeCode(claude, 9000, up)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(data, []*Edit{e}, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := Undo(data); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(claude, "settings.json")); !os.IsNotExist(err) {
		t.Error("a file Trove created should be removed on undo")
	}
}

func TestClaudeCodeEditedSinceOnlyOurKeyIsRemoved(t *testing.T) {
	data, claude := t.TempDir(), t.TempDir()
	path := filepath.Join(claude, "settings.json")
	write(t, path, claudeOriginal)
	e, _ := PlanClaudeCode(claude, 8787, up)
	if _, err := Apply(data, []*Edit{e}, t0); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	_ = json.Unmarshal([]byte(read(t, path)), &doc)
	doc["theme"] = "dark" // the user edits after setup
	b, _ := json.Marshal(doc)
	write(t, path, string(b))
	res, err := Undo(data)
	if err != nil || res[0].Action != "removed_our_lines" {
		t.Fatalf("%v %+v", err, res)
	}
	got := read(t, path)
	if strings.Contains(got, "ANTHROPIC_BASE_URL") || !strings.Contains(got, `"theme": "dark"`) || !strings.Contains(got, `"FOO": "bar"`) {
		t.Errorf("surgical undo wrong:\n%s", got)
	}
}

func TestClaudeCodeRefusesToOverrideExistingBaseURLOrBrokenJSON(t *testing.T) {
	claude := t.TempDir()
	path := filepath.Join(claude, "settings.json")
	write(t, path, `{"env":{"ANTHROPIC_BASE_URL":"https://corp.example"}}`)
	if _, err := PlanClaudeCode(claude, 8787, up); err == nil || !strings.Contains(err.Error(), "will not overwrite") {
		t.Errorf("got %v", err)
	}
	write(t, path, `{broken`)
	if _, err := PlanClaudeCode(claude, 8787, up); err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Errorf("got %v", err)
	}
	if read(t, path) != `{broken` {
		t.Error("a broken file must be left alone")
	}
}

func TestCodexApplyKeepsCommentsAndUndoIsExact(t *testing.T) {
	data, codex := t.TempDir(), t.TempDir()
	path := filepath.Join(codex, "config.toml")
	write(t, path, codexOriginal)
	e, err := PlanCodex(codex, 8787)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(data, []*Edit{e}, t0); err != nil {
		t.Fatal(err)
	}
	got := read(t, path)
	if !strings.Contains(got, "# my codex config") || !strings.Contains(got, "[profiles.fast]") {
		t.Errorf("the user's content must survive:\n%s", got)
	}
	var parsed struct {
		ModelProvider  string `toml:"model_provider"`
		Model          string `toml:"model"`
		ModelProviders map[string]struct {
			BaseURL    string `toml:"base_url"`
			EnvKey     string `toml:"env_key"`
			WireAPI    string `toml:"wire_api"`
			WebSockets bool   `toml:"supports_websockets"`
		} `toml:"model_providers"`
	}
	if err := toml.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("result is not valid TOML: %v\n%s", err, got)
	}
	p := parsed.ModelProviders["trove"]
	if parsed.ModelProvider != "trove" || parsed.Model != "gpt-5" || p.BaseURL != "http://127.0.0.1:8787/openai/v1" ||
		p.EnvKey != "OPENAI_API_KEY" || p.WireAPI != "responses" || p.WebSockets {
		t.Errorf("parsed: %+v", parsed)
	}
	if again, _ := PlanCodex(codex, 8787); !again.NoOp {
		t.Error("second plan should be a no-op")
	}
	if _, err := Undo(data); err != nil {
		t.Fatal(err)
	}
	if read(t, path) != codexOriginal {
		t.Errorf("undo must restore the original:\n%s", read(t, path))
	}
}

func TestCodexEditedSinceStripsOnlyMarkedBlocks(t *testing.T) {
	data, codex := t.TempDir(), t.TempDir()
	path := filepath.Join(codex, "config.toml")
	write(t, path, codexOriginal)
	e, _ := PlanCodex(codex, 8787)
	if _, err := Apply(data, []*Edit{e}, t0); err != nil {
		t.Fatal(err)
	}
	write(t, path, read(t, path)+"\n[mcp_servers.x]\ncommand = \"x\"\n")
	if _, err := Undo(data); err != nil {
		t.Fatal(err)
	}
	got := read(t, path)
	if strings.Contains(got, "trove") || !strings.Contains(got, "[mcp_servers.x]") || !strings.Contains(got, "# my codex config") {
		t.Errorf("got:\n%s", got)
	}
	var v map[string]any
	if err := toml.Unmarshal([]byte(got), &v); err != nil {
		t.Errorf("still valid TOML expected: %v", err)
	}
}

// A user who deletes the closing marker must not lose everything after the
// opening one when Trove later cleans up.
func TestUndoWithMissingCloseMarkerLeavesTheFileAlone(t *testing.T) {
	data, codex := t.TempDir(), t.TempDir()
	path := filepath.Join(codex, "config.toml")
	write(t, path, codexOriginal)
	e, _ := PlanCodex(codex, 8787)
	if _, err := Apply(data, []*Edit{e}, t0); err != nil {
		t.Fatal(err)
	}
	damaged := strings.ReplaceAll(read(t, path), markerClose+"\n", "") + "\n[mcp_servers.mine]\ncommand = \"keep-me\"\n"
	write(t, path, damaged)
	res, err := Undo(data)
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != damaged {
		t.Errorf("file was modified.\nwant:\n%s\ngot:\n%s", damaged, got)
	}
	if len(res) == 0 || res[0].Action != "left_alone" || !strings.Contains(res[0].Detail, "closing one") {
		t.Errorf("unhelpful result: %+v", res)
	}
}

func TestManifestIsPrivateAndBackupsDoNotOutliveAnUndo(t *testing.T) {
	data, codex := t.TempDir(), t.TempDir()
	path := filepath.Join(codex, "config.toml")
	write(t, path, codexOriginal)
	e, _ := PlanCodex(codex, 8787)
	cs, err := Apply(data, []*Edit{e}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(filepath.Join(data, manifestFile)); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("manifest mode = %v, %v; want 0600", fi.Mode().Perm(), err)
		}
	}
	backup := cs[0].Backup
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("no backup to begin with: %v", err)
	}
	if _, err := Undo(data); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Errorf("backup (a full copy of the user's settings) survived a successful undo: %v", err)
	}
}

func TestBackupIsKeptWhenUndoCouldNotFinish(t *testing.T) {
	data, codex := t.TempDir(), t.TempDir()
	path := filepath.Join(codex, "config.toml")
	write(t, path, codexOriginal)
	e, _ := PlanCodex(codex, 8787)
	cs, err := Apply(data, []*Edit{e}, t0)
	if err != nil {
		t.Fatal(err)
	}
	write(t, path, strings.ReplaceAll(read(t, path), markerClose+"\n", "")) // damaged markers: undo must give up
	if _, err := Undo(data); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cs[0].Backup); err != nil {
		t.Errorf("backup deleted although the file was not restored: %v", err)
	}
}

func TestStripBetweenRefusesUnclosedBlock(t *testing.T) {
	in := "a = 1\n" + mcpMarkerOpen + "\n[mcp_servers.trove]\n\n[mine]\nx = 1\n"
	if out, err := stripBetween([]byte(in), mcpMarkerOpen, mcpMarkerClose); err == nil {
		t.Errorf("stripped an unclosed block:\n%s", out)
	}
}

func TestCodexRefusesExistingProviderAndNewFileWorks(t *testing.T) {
	codex := t.TempDir()
	path := filepath.Join(codex, "config.toml")
	write(t, path, "model_provider = \"azure\"\n")
	if _, err := PlanCodex(codex, 8787); err == nil || !strings.Contains(err.Error(), "will not override") {
		t.Errorf("got %v", err)
	}
	fresh := filepath.Join(t.TempDir(), "newdir")
	e, err := PlanCodex(fresh, 8787)
	if err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	if _, err := Apply(data, []*Edit{e}, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := Undo(data); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(fresh, "config.toml")); !os.IsNotExist(err) {
		t.Error("created file should be removed")
	}
}

func TestUndoReversesEverythingNewestFirst(t *testing.T) {
	data, claude, codex := t.TempDir(), t.TempDir(), t.TempDir()
	write(t, filepath.Join(claude, "settings.json"), claudeOriginal)
	write(t, filepath.Join(codex, "config.toml"), codexOriginal)
	a, _ := PlanClaudeCode(claude, 8787, up)
	b, _ := PlanCodex(codex, 8787)
	if _, err := Apply(data, []*Edit{a, b}, t0); err != nil {
		t.Fatal(err)
	}
	res, err := Undo(data)
	if err != nil || len(res) != 2 || res[0].Agent != Codex {
		t.Fatalf("%v %+v", err, res)
	}
	if read(t, filepath.Join(claude, "settings.json")) != claudeOriginal || read(t, filepath.Join(codex, "config.toml")) != codexOriginal {
		t.Error("both files must be back to their originals")
	}
	if res2, err := Undo(data); err != nil || len(res2) != 0 {
		t.Errorf("a second undo has nothing to do: %v %+v", err, res2)
	}
}

func TestClaudeCodeReplacesAnthropicDefaultAndUndoPutsItBack(t *testing.T) {
	data, claude := t.TempDir(), t.TempDir()
	path := filepath.Join(claude, "settings.json")
	orig := "{\n  \"env\": {\n    \"ANTHROPIC_BASE_URL\": \"https://api.anthropic.com\"\n  }\n}\n"
	write(t, path, orig)
	e, err := PlanClaudeCode(claude, 8787, up)
	if err != nil || e.NoOp {
		t.Fatalf("the default address should be replaceable: %v", err)
	}
	if _, err := Apply(data, []*Edit{e}, t0); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(t, path), "127.0.0.1:8787/anthropic") {
		t.Fatal("not replaced")
	}
	// Exact undo from the backup.
	if _, err := Undo(data); err != nil || read(t, path) != orig {
		t.Fatalf("undo: %v\n%s", err, read(t, path))
	}
	// Edited since: surgical undo restores the previous value instead of deleting the key.
	e, _ = PlanClaudeCode(claude, 8787, up)
	if _, err := Apply(data, []*Edit{e}, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	_ = json.Unmarshal([]byte(read(t, path)), &doc)
	doc["theme"] = "dark"
	b, _ := json.Marshal(doc)
	write(t, path, string(b))
	if _, err := Undo(data); err != nil {
		t.Fatal(err)
	}
	got := read(t, path)
	if !strings.Contains(got, `"ANTHROPIC_BASE_URL": "https://api.anthropic.com"`) || !strings.Contains(got, "dark") {
		t.Errorf("surgical undo should restore the previous value and keep your edit:\n%s", got)
	}
}
