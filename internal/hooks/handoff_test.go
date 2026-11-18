package hooks

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/external"
	"github.com/usetrove/trove/internal/gateway/session"
	"github.com/usetrove/trove/internal/measure"
)

// TestHandoffStandIn is not a test: it is the stand-in third-party tool the hand-off tests run (the usual Go
// helper-process pattern). It echoes its stdin after a fixed prefix, writes a line to stderr, and exits 2 when the
// input asks it to block, so every channel the hand-off must pass through is exercised.
func TestHandoffStandIn(t *testing.T) {
	if os.Getenv("TROVE_HANDOFF_STANDIN") != "1" {
		return
	}
	in, _ := io.ReadAll(os.Stdin)
	fmt.Fprint(os.Stdout, "TOOL:"+string(in))
	fmt.Fprint(os.Stderr, "tool says hi")
	if bytes.Contains(in, []byte("please-block")) {
		os.Exit(2)
	}
	os.Exit(0)
}

func standIn(t *testing.T) {
	t.Helper()
	old := toolCommandFn
	t.Cleanup(func() { toolCommandFn = old })
	t.Setenv("TROVE_HANDOFF_STANDIN", "1")
	toolCommandFn = func(tool string) []string {
		if tool != "ztk" {
			return nil
		}
		return []string{os.Args[0], "-test.run=^TestHandoffStandIn$"}
	}
}

func (r *rig) handoff(tool, stdin string) (out, errOut string, code int) {
	r.t.Helper()
	var o, e bytes.Buffer
	code = Handoff(Env{Args: []string{EventHandoff, tool, "--data-dir", r.dir}, Stdin: strings.NewReader(stdin), Stdout: &o,
		Getenv: r.env}, &e)
	return o.String(), e.String(), code
}

func bashInput(id, cmd string) string {
	return fmt.Sprintf(`{"session_id":%q,"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":%q}}`, id, cmd)
}

func handoffIDs() (offID, onID string) {
	for i := 0; offID == "" || onID == ""; i++ {
		id := fmt.Sprintf("ho%d", i)
		if measure.Arm(session.ClaudeCodeKey(id), external.Prefix+"ztk") == config.StateOn {
			onID = id
		} else {
			offID = id
		}
	}
	return offID, onID
}

// In the "on" arm the tool gets the agent's hook input byte for byte, and its stdout, stderr and exit code reach the
// agent unchanged; in the "off" arm nothing runs and the call goes ahead.
func TestHandoffIsTransparentInTheOnArmAndSilentInTheOffArm(t *testing.T) {
	standIn(t)
	r := newRig(t, nil)
	r.snapshot(Snapshot{States: map[string]string{}, HandoffTrial: []string{external.Prefix + "ztk"}})
	offID, onID := handoffIDs()
	r.startAs(offID)
	r.startAs(onID)

	in := bashInput(onID, "go test ./...")
	out, errOut, code := r.handoff("ztk", in)
	if out != "TOOL:"+in || errOut != "tool says hi" || code != 0 {
		t.Fatalf("on arm must pass everything through: out=%q err=%q code=%d", out, errOut, code)
	}
	if _, _, code := r.handoff("ztk", bashInput(onID, "please-block")); code != 2 {
		t.Errorf("the tool's exit code (2 blocks the call) must reach the agent, got %d", code)
	}
	if out, errOut, code := r.handoff("ztk", bashInput(offID, "please-block")); out != "" || errOut != "" || code != 0 {
		t.Errorf("off arm must do nothing: out=%q err=%q code=%d", out, errOut, code)
	}
	if got := len(r.armEvents()); got != 2 {
		t.Errorf("both sessions must record an arm at SessionStart, got %d", got)
	}
}

func TestHandoffFallsBackToTheToolWhenTheArmIsUnknown(t *testing.T) {
	standIn(t)
	r := newRig(t, nil)
	r.snapshot(Snapshot{States: map[string]string{}, HandoffTrial: []string{external.Prefix + "ztk"}})
	// A session that began before the test has no pin: it keeps the tool, as before.
	if out, _, code := r.handoff("ztk", bashInput("never-started", "ls")); !strings.HasPrefix(out, "TOOL:") || code != 0 {
		t.Errorf("an unpinned session keeps the tool: out=%q code=%d", out, code)
	}
	if out, _, code := r.handoff("nosuchtool", bashInput("x", "ls")); out != "" || code != 0 {
		t.Errorf("an unknown tool does nothing: out=%q code=%d", out, code)
	}
}

func TestHandoffToolsListsOnlyEnabledHookTools(t *testing.T) {
	got := HandoffTools(map[string]string{"ext_ztk": config.StateOn, "ext_rtk": config.StateOff, "ext_caveman": config.StateOn})
	if strings.Join(got, ",") != "ext_ztk" {
		t.Errorf("got %v; only enabled tools that act as a hook are split", got)
	}
}
