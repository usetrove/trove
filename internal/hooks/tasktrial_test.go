package hooks

import (
	"fmt"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/gateway/session"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/tasks"
)

func taskSnapshot() Snapshot {
	return Snapshot{States: map[string]string{}, TaskTrial: []string{tasks.FeatureScout, tasks.FeatureCascade}}
}

func subagentInput(id, tool, agent string) string {
	return fmt.Sprintf(`{"session_id":%q,"hook_event_name":"PreToolUse","tool_name":%q,"tool_input":{"subagent_type":%q,"prompt":"x"}}`, id, tool, agent)
}

// Installed scout and cascade sessions are split by a hash of the session key, and each side is recorded.
func TestTaskSessionsAreSplitAndRecorded(t *testing.T) {
	r := newRig(t, nil)
	r.snapshot(taskSnapshot())
	on := 0
	const n = 60
	for i := 0; i < n; i++ {
		key := r.startAs(fmt.Sprintf("task-session-%d", i))
		for _, f := range []string{tasks.FeatureScout, tasks.FeatureCascade} {
			want := measure.Arm(key, f)
			got := loadSession(r.dir, key).Pins[f]
			if (want == config.StateOn) != (got == stateOn) {
				t.Fatalf("%s: arm %q but pinned %q", f, want, got)
			}
		}
		if loadSession(r.dir, key).Pins[tasks.FeatureScout] == stateOn {
			on++
		}
	}
	if on < 15 || on > 45 {
		t.Errorf("%d of %d on; a random half was intended", on, n)
	}
	if got := len(r.armEvents()); got != n {
		t.Errorf("%d sessions recorded an arm, want %d", got, n)
	}
}

// In the off arm the scout subagent is declined with a plain reason; in the on arm, and for any other subagent,
// the call goes through. Both tool names Claude Code uses for subagents are understood.
func TestOffArmDeclinesTheScoutOnly(t *testing.T) {
	r := newRig(t, nil)
	r.snapshot(taskSnapshot())
	var offID, onID string
	for i := 0; offID == "" || onID == ""; i++ {
		id := fmt.Sprintf("s%d", i)
		if measure.Arm(session.ClaudeCodeKey(id), tasks.FeatureScout) == config.StateOn {
			onID = id
		} else {
			offID = id
		}
	}
	r.startAs(offID)
	r.startAs(onID)
	for _, tool := range []string{"Agent", "Task"} {
		out, code := r.run(EventSubagent, subagentInput(offID, tool, tasks.ScoutName))
		if code != 0 || !strings.Contains(out, `"permissionDecision":"deny"`) || !strings.Contains(out, "do this work directly") {
			t.Errorf("%s off arm: want a plain deny, got %q (code %d)", tool, out, code)
		}
		if out, _ := r.run(EventSubagent, subagentInput(onID, tool, tasks.ScoutName)); out != "" {
			t.Errorf("%s on arm must go through, got %q", tool, out)
		}
		if out, _ := r.run(EventSubagent, subagentInput(offID, tool, "Explore")); out != "" {
			t.Errorf("another subagent must go through, got %q", out)
		}
	}
}

// A session that began before the trial (no pin) and a session whose scout was rolled back are never blocked.
func TestNoPinMeansNoBlock(t *testing.T) {
	r := newRig(t, nil)
	r.snapshot(taskSnapshot())
	if out, _ := r.run(EventSubagent, subagentInput("never-started", "Agent", tasks.ScoutName)); out != "" {
		t.Errorf("an unpinned session must go through, got %q", out)
	}
}
