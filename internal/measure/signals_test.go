package measure

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/optimize"
)

const note = optimize.CollapsedPrefix + `message 2, tool call t1; it is not repeated here.]`

func TestExtractCountsSignals(t *testing.T) {
	body := `{"model":"m","messages":[
	 {"role":"user","content":"please fix SECRET_TOKEN_abc in /home/me/app.go"},
	 {"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"path":"/home/me/app.go","limit":10}}]},
	 {"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"file text"}]},
	 {"role":"assistant","content":[{"type":"tool_use","id":"t2","name":"Read","input":{"limit":10,"path":"/home/me/app.go"}}]},
	 {"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","content":"` + note + `"}]},
	 {"role":"assistant","content":[{"type":"tool_use","id":"t3","name":"Read","input":{"path":"/home/me/app.go","limit":10}},
	                                 {"type":"tool_use","id":"t4","name":"Bash","input":{"cmd":"go test"}}]},
	 {"role":"user","content":[{"type":"tool_result","tool_use_id":"t3","content":[{"type":"text","text":"file text"}]},
	                            {"type":"tool_result","tool_use_id":"t4","is_error":true,"content":"FAIL"}]}
	]}`
	got, ok := Extract([]byte(body))
	if !ok {
		t.Fatal("body not read")
	}
	want := Signals{
		Messages: 7, ToolCalls: 4,
		DistinctCalls: 2, // Read(app.go,10) three times, whatever the key order; Bash once
		ToolErrors:    1,
		Collapsed:     1, // t2's result
		Refetches:     1, // t3 repeats the call whose result was collapsed (t2); t2 itself is not counted
	}
	if got != want {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
	if got.Repeats() != 2 {
		t.Errorf("repeats = %d, want 2", got.Repeats())
	}
}

func TestExtractStoresOnlyCountsNeverContent(t *testing.T) {
	body := `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"x","name":"ReadSecretFile","input":{"path":"/home/me/.ssh/id_rsa"}}]},
	{"role":"user","content":[{"type":"tool_result","tool_use_id":"x","is_error":true,"content":"api key sk-ant-SECRET"}]}]}`
	s, _ := Extract([]byte(body))
	out := string(s.JSON())
	for _, leak := range []string{"ReadSecretFile", "id_rsa", "SECRET", "sk-ant", "/home"} {
		if strings.Contains(out, leak) {
			t.Errorf("signals JSON leaks %q: %s", leak, out)
		}
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatal(err)
	}
	for k, v := range m {
		if _, isNum := v.(float64); !isNum {
			t.Errorf("signal %q is %T, want a number", k, v)
		}
	}
}

func TestExtractRejectsUnreadableBodies(t *testing.T) {
	for _, b := range []string{"", "not json", `{"model":"m"}`, `{"messages":`} {
		if _, ok := Extract([]byte(b)); ok {
			t.Errorf("Extract(%q) should not be ok", b)
		}
	}
}
