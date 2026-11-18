package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/gateway/session"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/tasks"
)

// subModelProject is a project with one subagent whose model line Trove wrote and one the user configured, and a
// transcript whose main thread ran on Sonnet (the newest line on a subagent's sidechain must be ignored).
func subModelProject(t *testing.T) (cwd, transcript string) {
	t.Helper()
	cwd = t.TempDir()
	agents := filepath.Join(cwd, ".claude", "agents")
	if err := os.MkdirAll(agents, 0o755); err != nil {
		t.Fatal(err)
	}
	fixed, why := tasks.SetModel([]byte("---\nname: lister\ndescription: d\n---\nbody\n"), "haiku")
	if fixed == nil {
		t.Fatal(why)
	}
	for name, body := range map[string][]byte{
		"lister.md": fixed,
		"mine.md":   []byte("---\nname: mine\nmodel: haiku\n---\nbody\n"),
	} {
		if err := os.WriteFile(filepath.Join(agents, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	transcript = filepath.Join(cwd, "t.jsonl")
	lines := `{"type":"user","message":{"content":"go"}}` + "\n" +
		`{"type":"assistant","isSidechain":false,"message":{"model":"claude-sonnet-5-5","content":[]}}` + "\n" +
		`{"type":"assistant","isSidechain":true,"message":{"model":"claude-haiku-5-5","content":[]}}` + "\n"
	if err := os.WriteFile(transcript, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	return cwd, transcript
}

func agentCall(id, cwd, transcript, agent, extra string) string {
	enc := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	return fmt.Sprintf(`{"session_id":%q,"hook_event_name":"PreToolUse","tool_name":"Agent","cwd":%s,"transcript_path":%s,`+
		`"tool_input":{"subagent_type":%q,"prompt":"x"%s}}`, id, enc(cwd), enc(transcript), agent, extra)
}

func armIDs(feature string) (offID, onID string) {
	for i := 0; offID == "" || onID == ""; i++ {
		id := fmt.Sprintf("sm%d", i)
		if measure.Arm(session.ClaudeCodeKey(id), feature) == config.StateOn {
			onID = id
		} else {
			offID = id
		}
	}
	return offID, onID
}

// In the off arm a subagent whose model Trove set runs on the main conversation's model again; everything else goes
// through untouched.
func TestSubagentModelControlArm(t *testing.T) {
	r := newRig(t, nil)
	r.snapshot(Snapshot{States: map[string]string{}, TaskTrial: []string{tasks.FeatureSubagentModel}})
	cwd, tr := subModelProject(t)
	offID, onID := armIDs(tasks.FeatureSubagentModel)
	r.startAs(offID)
	r.startAs(onID)

	out, code := r.run(EventSubagent, agentCall(offID, cwd, tr, "lister", ""))
	if code != 0 || !strings.Contains(out, `"updatedInput"`) || !strings.Contains(out, `"model":"sonnet"`) || !strings.Contains(out, `"prompt":"x"`) {
		t.Fatalf("off arm must set the main model's alias and keep the other fields, got %q (code %d)", out, code)
	}
	if strings.Contains(out, "permissionDecision") {
		t.Error("the hook must never approve a call")
	}
	for name, in := range map[string]string{
		"on arm":                agentCall(onID, cwd, tr, "lister", ""),
		"user's own model line": agentCall(offID, cwd, tr, "mine", ""),
		"call names a model":    agentCall(offID, cwd, tr, "lister", `,"model":"opus"`),
		"unknown subagent":      agentCall(offID, cwd, tr, "nobody", ""),
		"no transcript":         agentCall(offID, cwd, filepath.Join(cwd, "missing.jsonl"), "lister", ""),
		"session never started": agentCall("never-started", cwd, tr, "lister", ""),
	} {
		if out, _ := r.run(EventSubagent, in); out != "" {
			t.Errorf("%s: must go through untouched, got %q", name, out)
		}
	}
}

func TestSubagentModelRolledBackGoesThrough(t *testing.T) {
	r := newRig(t, nil)
	r.snapshot(Snapshot{States: map[string]string{}, TaskTrial: []string{tasks.FeatureSubagentModel}})
	cwd, tr := subModelProject(t)
	offID, _ := armIDs(tasks.FeatureSubagentModel)
	r.startAs(offID)
	r.snapshot(Snapshot{States: map[string]string{}, TaskTrial: []string{tasks.FeatureSubagentModel}, RolledBack: []string{tasks.FeatureSubagentModel}})
	if out, _ := r.run(EventSubagent, agentCall(offID, cwd, tr, "lister", "")); out != "" {
		t.Errorf("a rolled-back feature must not rewrite, got %q", out)
	}
}

func TestModelAlias(t *testing.T) {
	for in, want := range map[string]string{"claude-sonnet-5-5": "sonnet", "claude-haiku-5-5": "haiku", "claude-opus-5": "opus",
		"claude-fable-5-2": "fable", "gpt-x": "", "": ""} {
		got, ok := tasks.ModelAlias(in)
		if got != want || ok != (want != "") {
			t.Errorf("ModelAlias(%q) = %q, %v", in, got, ok)
		}
	}
}
