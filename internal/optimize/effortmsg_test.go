package optimize

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func effortFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "effortmsg", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func routineTransform(t *testing.T, body []byte) ([]byte, Report) {
	t.Helper()
	r, err := ParseRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	out, rep := EffortRoutinePrompts{}.Transform(context.Background(), r)
	if rep.Err != "" {
		t.Fatal(rep.Err)
	}
	if err := out.Ensure(); err != nil {
		t.Fatalf("output does not parse: %v", err)
	}
	return out.Raw, rep
}

// systemLevels lists the per-message effort changes in a body, in order.
func systemLevels(t *testing.T, body []byte) []string {
	t.Helper()
	var v struct {
		Messages []struct {
			Role         string          `json:"role"`
			Content      json.RawMessage `json:"content"`
			OutputConfig *struct {
				Effort string `json:"effort"`
			} `json:"output_config"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatal(err)
	}
	var out []string
	for i, m := range v.Messages {
		if m.Role == "system" {
			if string(m.Content) != "[]" || m.OutputConfig == nil {
				t.Fatalf("message %d: %+v", i, m)
			}
			out = append(out, v.Messages[i-1].Role+">"+m.OutputConfig.Effort)
		}
	}
	return out
}

func TestEffortRoutinePromptsLowersTheTurnAfterARoutinePromptAndRestoresIt(t *testing.T) {
	out, rep := routineTransform(t, effortFixture(t, "turn4.json"))
	if rep.Name != OptimizerEffortRoutinePrompts || len(rep.Betas) != 1 || rep.Betas[0] != PerMessageEffortBeta {
		t.Fatalf("report %+v", rep)
	}
	// Prompts: real, "go ahead" (routine), real, "continue" (routine). The top-level effort is high.
	if got := strings.Join(systemLevels(t, out), ","); got != "user>low,user>high,user>low" {
		t.Fatalf("changes = %s", got)
	}
	if !bytes.Contains(out, []byte(`"output_config":{"effort":"high"},"system"`)) {
		t.Error("the top-level effort must stay as it was")
	}
}

// Rule 5: the transformed history of one request is a byte prefix of the next request's, so the cached prefix holds
// and every change stays where it was first sent.
func TestEffortRoutinePromptsKeepsEarlierBytes(t *testing.T) {
	prev, _ := routineTransform(t, effortFixture(t, "turn2.json"))
	for _, next := range []string{"turn3.json", "turn4.json"} {
		cur, _ := routineTransform(t, effortFixture(t, next))
		cut := bytes.LastIndex(prev, []byte("]}")) // the end of the earlier messages array
		if !bytes.HasPrefix(cur, prev[:cut]) {
			t.Fatalf("%s does not extend the earlier transformed history", next)
		}
		again, _ := routineTransform(t, effortFixture(t, next))
		if !bytes.Equal(cur, again) {
			t.Fatalf("%s: not deterministic", next)
		}
		prev = cur
	}
}

func TestEffortRoutinePromptsLeavesTheseAlone(t *testing.T) {
	base := string(effortFixture(t, "turn4.json"))
	cases := map[string]string{
		"model without per-message effort": strings.Replace(base, `"claude-opus-5-5"`, `"claude-fable-5"`, 1),
		"already low":                      strings.Replace(base, `"effort":"high"`, `"effort":"low"`, 1),
		"no top-level effort":              strings.Replace(base, `,"output_config":{"effort":"high"}`, "", 1),
		"thinking between_tools":           strings.Replace(base, `"type":"adaptive"`, `"type":"between_tools"`, 1),
		"thinking disabled":                strings.Replace(base, `"type":"adaptive"`, `"type":"disabled"`, 1),
		"a system message already":         strings.Replace(base, `"messages":[`, `"messages":[{"role":"system","content":[],"output_config":{"effort":"medium"}},`, 1),
		"no routine prompt": strings.NewReplacer(`"text":"go ahead"`, `"text":"Rename the helper."`,
			`"text":"continue"`, `"text":"Add a test for it."`).Replace(base),
	}
	for name, body := range cases {
		if base == body {
			t.Fatalf("%s: the case did not change the fixture", name)
		}
		r, err := ParseRequest([]byte(body))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out, rep := EffortRoutinePrompts{}.Transform(context.Background(), r)
		if rep.Name != "" || !bytes.Equal(out.Raw, []byte(body)) {
			t.Errorf("%s: changed the request: %+v", name, rep)
		}
	}
}

func TestPromptTextIgnoresReminders(t *testing.T) {
	cases := []struct {
		content string
		want    string
		ok      bool
	}{
		{`"go ahead"`, "go ahead", true},
		{`[{"type":"text","text":"<system-reminder>x</system-reminder>"},{"type":"text","text":"yes"}]`, "yes", true},
		{`[{"type":"text","text":"yes"},{"type":"text","text":"and fix it"}]`, "", false},
		{`[{"type":"image","source":{}},{"type":"text","text":"yes"}]`, "", false},
	}
	for _, c := range cases {
		body := []byte(`{"messages":[{"role":"user","content":` + c.content + `}]}`)
		r, err := ParseRequest(body)
		if err != nil {
			t.Fatal(err)
		}
		got, ok := promptText(r.Raw, r.Messages[0])
		if got != c.want || ok != c.ok {
			t.Errorf("%s: %q %v", c.content, got, ok)
		}
	}
}

func TestEngineForwardsBetasOnlyForAChangeItSends(t *testing.T) {
	body := effortFixture(t, "turn4.json")
	for _, state := range []string{"on", "shadow"} {
		e := testEngine(t, map[string]string{OptimizerEffortRoutinePrompts: state})
		res := e.Run(context.Background(), input(body))
		if state == "on" && (!res.Changed || len(res.Betas) != 1 || res.Betas[0] != PerMessageEffortBeta) {
			t.Errorf("on: %+v", res)
		}
		if state == "shadow" && (res.Changed || res.Betas != nil) {
			t.Errorf("shadow must send nothing new: %+v", res)
		}
	}
}
