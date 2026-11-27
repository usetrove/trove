package hooks

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/config"
)

func codeSource(lines int) string {
	var b strings.Builder
	for i := 0; i < lines; i++ {
		fmt.Fprintf(&b, "func f%d() int { return %d }\n", i, i)
	}
	return b.String()
}

func jsonText(s string) string {
	j, _ := json.Marshal(s)
	return strings.Trim(string(j), `"`)
}

type preToolReply struct {
	H struct {
		Event   string `json:"hookEventName"`
		Dec     string `json:"permissionDecision"`
		Reason  string `json:"permissionDecisionReason"`
		Context string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

func TestCodeWriteDeniesASmallRewriteOnceAndCountsTheRetry(t *testing.T) {
	noCallClock(t)
	r := newRig(t, nil)
	r.snapshot(Snapshot{States: map[string]string{config.OptimizerHookWriteToEdit: "on"}})
	r.start()
	old := codeSource(400)
	path := r.file("big.go", old)
	content := strings.Replace(old, "return 7 }", "return 70 }", 1)
	write := func() string {
		out, code := r.run(EventCodeWrite, r.input("claude_pre_write.json", "{{FILE}}", path, "{{CONTENT}}", jsonText(content)))
		if code != 0 {
			t.Fatalf("exit %d", code)
		}
		return out
	}
	var v preToolReply
	if err := json.Unmarshal([]byte(write()), &v); err != nil || v.H.Event != "PreToolUse" || v.H.Dec != "deny" || !strings.Contains(v.H.Reason, "Use Edit for targeted changes") {
		t.Fatalf("first Write: %+v err=%v", v, err)
	}
	if out := write(); out != "" {
		t.Fatalf("the same Write sent again must go through: %q", out)
	}
	if got := strings.Join(kinds(r.events()), ","); got != "blocked,overridden" {
		t.Fatalf("events = %s", got)
	}
}

func TestCodeWriteShadowChangesNothing(t *testing.T) {
	noCallClock(t)
	r := newRig(t, map[string]string{config.OptimizerHookWriteToEdit: "shadow", config.OptimizerHookEditBatch: "shadow"})
	r.start()
	old := codeSource(400)
	path := r.file("big.go", old)
	if out, _ := r.run(EventCodeWrite, r.input("claude_pre_write.json", "{{FILE}}", path, "{{CONTENT}}", jsonText(old+"x\n"))); out != "" {
		t.Fatalf("shadow must not answer: %q", out)
	}
	for i := 0; i < 3; i++ {
		if out, _ := r.run(EventCodeWrite, r.input("claude_pre_edit.json", "{{FILE}}", path)); out != "" {
			t.Fatalf("shadow must not answer: %q", out)
		}
	}
	if got := strings.Join(kinds(r.events()), ","); got != "would_block,would_advise" {
		t.Fatalf("events = %s", got)
	}
}

func TestCodeWriteNotesTheThirdEditOfATurn(t *testing.T) {
	noCallClock(t)
	r := newRig(t, nil)
	r.snapshot(Snapshot{States: map[string]string{config.OptimizerHookEditBatch: "on"}})
	r.start()
	path := r.file("a.go", codeSource(5))
	var outs []string
	for i := 0; i < 4; i++ {
		out, _ := r.run(EventCodeWrite, r.input("claude_pre_edit.json", "{{FILE}}", path))
		outs = append(outs, out)
	}
	var v preToolReply
	if outs[0] != "" || outs[1] != "" || outs[3] != "" {
		t.Fatalf("only the third Edit gets the note: %q", outs)
	}
	if err := json.Unmarshal([]byte(outs[2]), &v); err != nil || v.H.Dec != "" || !strings.Contains(v.H.Context, "single sed command") {
		t.Fatalf("third Edit: %+v err=%v", v, err)
	}
	// A new prompt starts the count again.
	for i := 0; i < 2; i++ {
		if out, _ := r.run(EventCodeWrite, r.input("claude_pre_edit.json", "{{FILE}}", path, "{{PROMPT}}", "p2")); out != "" {
			t.Fatalf("edit %d of a new turn: %q", i+1, out)
		}
	}
}

func TestCodeWriteIsInertWhenOff(t *testing.T) {
	noCallClock(t)
	r := newRig(t, nil)
	r.start()
	old := codeSource(400)
	path := r.file("big.go", old)
	if out, _ := r.run(EventCodeWrite, r.input("claude_pre_write.json", "{{FILE}}", path, "{{CONTENT}}", jsonText(old+"x\n"))); out != "" {
		t.Fatalf("off must not answer: %q", out)
	}
	if ev := r.events(); len(ev) != 0 {
		t.Fatalf("off must record nothing: %v", ev)
	}
}
