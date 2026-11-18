package optimize

import (
	"context"
	"strings"
	"testing"
)

func effortBody(model, effort string, lastIsTool bool) []byte {
	last := `{"role":"user","content":[{"type":"text","text":"go on"}]}`
	if lastIsTool {
		last = `{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}`
	}
	oc := ""
	if effort != "" {
		oc = `,"output_config":{"effort":"` + effort + `"}`
	}
	return []byte(`{"model":"` + model + `","max_tokens":100` + oc + `,"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"fix it"}]},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"bash","input":{}}]},` + last + `]}`)
}

func TestEffortToolTurns(t *testing.T) {
	for _, tc := range []struct {
		name, model, effort string
		tool                bool
		want                string // the effort after; "" means the body must be unchanged
	}{
		{"tool turn high is lowered", "claude-sonnet-5-5", "high", true, "low"},
		{"tool turn xhigh is lowered", "claude-haiku-5-5", "xhigh", true, "low"},
		{"tool turn medium is lowered", "claude-sonnet-5-5", "medium", true, "low"},
		{"user turn is never touched", "claude-sonnet-5-5", "high", false, ""},
		{"already low", "claude-sonnet-5-5", "low", true, ""},
		{"no effort field is never added", "claude-sonnet-5-5", "", true, ""},
		{"unmeasured model is left alone", "claude-opus-5-5", "high", true, ""},
		{"unknown level is left alone", "claude-sonnet-5-5", "turbo", true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := effortBody(tc.model, tc.effort, tc.tool)
			r, err := ParseRequest(body)
			if err != nil {
				t.Fatal(err)
			}
			if r.Effort != tc.effort {
				t.Fatalf("parsed effort %q, want %q", r.Effort, tc.effort)
			}
			out, rep := EffortToolTurns{}.Transform(context.Background(), r)
			if rep.Err != "" {
				t.Fatal(rep.Err)
			}
			if tc.want == "" {
				if string(out.Raw) != string(body) {
					t.Fatalf("body changed:\n%s", out.Raw)
				}
				return
			}
			want := strings.Replace(string(body), `"effort":"`+tc.effort+`"`, `"effort":"`+tc.want+`"`, 1)
			if string(out.Raw) != want {
				t.Fatalf("got  %s\nwant %s", out.Raw, want)
			}
			// deterministic: the same history always gives the same bytes (rule 5)
			again, _ := EffortToolTurns{}.Transform(context.Background(), mustParse(t, body))
			if string(again.Raw) != string(out.Raw) {
				t.Fatal("not deterministic")
			}
		})
	}
}

func TestEffortToolTurnsSeesTheRoutedModel(t *testing.T) {
	// model_router runs first; a request routed to a measured model is lowered for that model.
	body := effortBody("claude-haiku-5-5", "high", true)
	out, _ := EffortToolTurns{}.Transform(context.Background(), mustParse(t, body))
	if !strings.Contains(string(out.Raw), `"effort":"low"`) {
		t.Fatalf("not lowered: %s", out.Raw)
	}
}

func TestEffortToolTurnsDeclaresItsCost(t *testing.T) {
	e := EffortToolTurns{}
	if e.OverheadTokens() != 0 || e.Tier() != TierBehavior || e.Name() != OptimizerEffortToolTurns {
		t.Fatal("declarations changed")
	}
	if len(e.Points()) != 1 || e.Points()[0] != PointRequest {
		t.Fatal("effort_tool_turns acts on the request only")
	}
}

func mustParse(t *testing.T, b []byte) *Request {
	t.Helper()
	r, err := ParseRequest(b)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
