package hooks

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/external"
	"github.com/usetrove/trove/internal/gateway/session"
	"github.com/usetrove/trove/internal/measure"
)

const cavemanTrial = "ext_caveman"

// ruleStart runs SessionStart for a session. These tests check what the hook says, not how fast it says it, so
// the safety-net deadline is lifted: under a loaded test run it would let the call through with no reply.
func ruleStart(r *rig, id, source string, extra ...string) string {
	r.t.Helper()
	old := callDeadline
	callDeadline = time.Minute
	defer func() { callDeadline = old }()
	out, code := r.run(EventReset, r.input("claude_session_start.json", "{{SESSION}}", id, "{{SOURCE}}", source), extra...)
	if code != 0 {
		r.t.Fatalf("SessionStart exited %d", code)
	}
	return out
}

func sessionContext(t *testing.T, out string) string {
	t.Helper()
	if out == "" {
		return ""
	}
	var v struct {
		H struct {
			Event   string `json:"hookEventName"`
			Context string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil || v.H.Event != "SessionStart" {
		t.Fatalf("not a SessionStart reply: %q (%v)", out, err)
	}
	return v.H.Context
}

// Option (a) of plan 6.6: in a rule-set trial each new session gets an arm from a hash of its key, the arm is
// recorded, and only the "on" arm gets the rules, at startup and again after a resume, /clear or compaction.
func TestRuleTrialAddsTheRulesToTheOnArmOnly(t *testing.T) {
	r := newRig(t, nil)
	r.snapshot(Snapshot{States: map[string]string{}, RuleTrial: []string{cavemanTrial}})
	rules, _ := external.BuiltinRules("caveman")
	on := 0
	const n = 60
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("rule-session-%d", i)
		key := session.ClaudeCodeKey(id)
		got := sessionContext(t, ruleStart(r, id, "startup"))
		if measure.Arm(key, cavemanTrial) == config.StateOn {
			on++
			if got != rules {
				t.Fatalf("on arm: want exactly the caveman rules, got %.80q", got)
			}
			for _, src := range []string{"resume", "clear", "compact"} {
				if again := sessionContext(t, ruleStart(r, id, src)); again != rules {
					t.Fatalf("session %s on arm after %s: the rules must be added again, got %.80q", id, src, again)
				}
			}
		} else if got != "" {
			t.Fatalf("off arm must get nothing, got %.80q", got)
		}
	}
	if on < 15 || on > 45 {
		t.Errorf("%d of %d on; a random half was intended", on, n)
	}
	if got := len(r.armEvents()); got != n {
		t.Errorf("%d sessions recorded an arm, want %d", got, n)
	}
}

// The arm is pinned: a session that started in the on arm keeps the rules after the trial ends, and one that
// starts after it ends gets none.
func TestRuleTrialArmIsPinnedPerSession(t *testing.T) {
	r := newRig(t, nil)
	r.snapshot(Snapshot{States: map[string]string{}, RuleTrial: []string{cavemanTrial}})
	var onID string
	for i := 0; onID == ""; i++ {
		if id := fmt.Sprintf("p%d", i); measure.Arm(session.ClaudeCodeKey(id), cavemanTrial) == config.StateOn {
			onID = id
		}
	}
	ruleStart(r, onID, "startup")
	r.snapshot(Snapshot{States: map[string]string{}})
	if sessionContext(t, ruleStart(r, onID, "compact")) == "" {
		t.Error("a running on-arm session lost its rules when the trial ended")
	}
	if out := ruleStart(r, onID+"-new", "startup"); out != "" {
		t.Errorf("a session started after the trial got %q", out)
	}
}

// Other agents' SessionStart replies are not translated to carry context, so they are never split.
func TestRuleTrialIsClaudeCodeOnly(t *testing.T) {
	r := newRig(t, nil)
	r.snapshot(Snapshot{States: map[string]string{}, RuleTrial: []string{cavemanTrial}})
	for i := 0; i < 20; i++ {
		start := fmt.Sprintf(`{"session_id":"g%d","hook_event_name":"SessionStart","source":"startup"}`, i)
		if out, _ := r.run(EventReset, start, "--agent", AgentGemini); out != "" {
			t.Fatalf("gemini session got %q", out)
		}
	}
	if got := len(r.armEvents()); got != 0 {
		t.Errorf("%d gemini sessions were assigned an arm", got)
	}
}

// SessionStart context over 10,000 characters is replaced by a file path and a preview (code.claude.com/docs/en/
// hooks, checked 2026-11-17), so every rule set Trove can trial must fit.
func TestRuleSetsFitTheContextLimit(t *testing.T) {
	for _, n := range []string{"caveman", "ponytail", "terse-rules"} {
		text, ok := external.BuiltinRules(n)
		if !ok || len([]rune(text)) >= 10000 || strings.TrimSpace(text) == "" {
			t.Errorf("%s: %d characters (ok=%v)", n, len([]rune(text)), ok)
		}
	}
}
