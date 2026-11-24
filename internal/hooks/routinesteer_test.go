package hooks

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/gateway/session"
	"github.com/usetrove/trove/internal/hookcaps"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/optimize"
	"github.com/usetrove/trove/internal/technique"
)

const (
	opus   = "claude-opus-5-5"
	sonnet = "claude-sonnet-5-5"
	haiku  = "claude-haiku-5-5"
	fable  = "claude-fable-5-2"
)

func TestSteerKind(t *testing.T) {
	cases := []struct {
		name        string
		state, arm  string
		killed, ctx bool
		kind        string
		send        bool
	}{
		{"off", stateOff, "", false, true, "", false},
		{"shadow", stateShadow, "", false, true, KindWouldSteer, false},
		{"on, checked", stateOn, "", false, true, KindSteered, true},
		{"on, not checked yet", stateOn, "", false, false, KindWouldSteer, false},
		{"trial on arm, checked", stateOn, stateOn, false, true, KindSteered, true},
		{"trial on arm, not checked", stateOn, stateOn, false, false, KindWouldSteer, false},
		{"trial on arm, rolled back", stateShadow, stateOn, false, true, KindWouldSteer, false},
		{"trial off arm", stateOff, stateOff, false, true, KindWouldSteer, false},
		{"trial off arm, not checked", stateOff, stateOff, false, false, KindWouldSteer, false},
		{"kill switch, on", stateOff, "", true, true, "", false},
		{"kill switch, trial on arm", stateOff, stateOn, true, true, "", false},
		{"kill switch, trial off arm", stateOff, stateOff, true, true, "", false},
		{"unknown state", "bogus", "", false, true, "", false},
	}
	for _, c := range cases {
		kind, send := steerKind(c.state, c.arm, c.killed, c.ctx)
		if kind != c.kind || send != c.send {
			t.Errorf("%s: got (%q, %v), want (%q, %v)", c.name, kind, send, c.kind, c.send)
		}
	}
}

// Only Opus 5.5 is steered: the adaptive-thinking page says the phrase does not reliably reduce thinking on Haiku 5.5
// and Fable 5.2, and Sonnet 5.5 waits for a trial of its own (decided 2026-11-24).
func TestSteerModelOK(t *testing.T) {
	for m, want := range map[string]bool{
		opus: true, opus + "[1m]": true,
		sonnet: false, sonnet + "[1m]": false, haiku: false, fable: false, "claude-fable-5-1": false,
		"": false, "opus": false, "claude-opus-5": false, "claude-opus-5-5-x": false, "CLAUDE-OPUS-5-5": false,
	} {
		if got := steerModelOK(m); got != want {
			t.Errorf("steerModelOK(%q) = %v, want %v", m, got, want)
		}
	}
	for m := range steerModels {
		for _, bad := range []string{"haiku", "fable", "sonnet"} {
			if strings.Contains(m, bad) {
				t.Errorf("%s is on the steer's model list, but the phrase is not documented to work there", m)
			}
		}
	}
}

// TestSteerOverheadIsDeclared keeps the phrase, the wrapper allowance and the registry's declared overhead in step
// (rule 15).
func TestSteerOverheadIsDeclared(t *testing.T) {
	tech, ok := technique.Get(config.OptimizerHookRoutineSteer)
	if !ok || tech.UseOverhead == nil {
		t.Fatal("hook_routine_steer has no declared per-use overhead")
	}
	if *tech.UseOverhead != steerTokens {
		t.Errorf("registry declares %d tokens per steer, the hook records %d", *tech.UseOverhead, steerTokens)
	}
	if need := (len(SteerText) + steerWrapperBytes) * 2; need > steerTokens*7 {
		t.Errorf("the phrase and wrapper (%d bytes) need more than %d tokens at 3.5 bytes per token", len(SteerText)+steerWrapperBytes, steerTokens)
	}
	if tech.Tier != technique.T2 || tech.Prefix || !tech.AddsText {
		t.Errorf("hook_routine_steer must be T2, add text and leave the cached prompt alone: %+v", tech)
	}
}

func TestPromptReply(t *testing.T) {
	block := coldBlockJSON("stopped")
	if got := promptReply(block, SteerText); string(got) != string(block) {
		t.Errorf("a block must win unchanged, got %s", got)
	}
	if got := promptReply(nil, ""); got != nil {
		t.Errorf("nothing to say: got %s", got)
	}
	warn := coldWarnJSON("costly")
	if got := promptReply(warn, ""); string(got) != string(warn) {
		t.Errorf("a warning alone must pass unchanged, got %s", got)
	}
	var v struct {
		SystemMessage string `json:"systemMessage"`
		H             struct {
			Event   string `json:"hookEventName"`
			Context string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(promptReply(warn, SteerText), &v); err != nil {
		t.Fatal(err)
	}
	if v.SystemMessage != "costly" || v.H.Event != "UserPromptSubmit" || v.H.Context != SteerText {
		t.Errorf("warning and steer must both survive: %+v", v)
	}
}

// steerRig drives the real hooks: SessionStart with a model, then prompts.
type steerRig struct {
	*rig
	id string
}

func newSteerRig(t *testing.T, s Snapshot, contextChecked bool) *steerRig {
	t.Helper()
	r := &steerRig{rig: newRig(t, nil), id: sessionID}
	old := postDeadline
	postDeadline = 10 * time.Second // decisions, not speed: see newColdRig
	t.Cleanup(func() { postDeadline = old })
	r.snapshot(s)
	if contextChecked {
		r.capability(hookcaps.Yes)
	}
	return r
}

func steerSnapshot(state string) Snapshot {
	return Snapshot{States: map[string]string{config.OptimizerHookRoutineSteer: state}}
}

func (r *steerRig) capability(ctx string) {
	r.t.Helper()
	c := hookcaps.Capabilities{V: hookcaps.Version, Entries: []hookcaps.Capability{
		{Agent: "claude", Tool: hookcaps.ToolPrompt, Context: ctx, Method: "session", AgentVersion: "2.1.400"}}}
	b, _ := json.Marshal(c)
	if err := os.MkdirAll(filepath.Dir(hookcaps.Path(r.dir)), 0o700); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(hookcaps.Path(r.dir), b, 0o600); err != nil {
		r.t.Fatal(err)
	}
}

// begin runs SessionStart; model "" sends none.
func (r *steerRig) begin(model string) {
	r.t.Helper()
	in := map[string]any{"session_id": r.id, "hook_event_name": "SessionStart", "source": "startup", "cwd": r.files}
	if model != "" {
		in["model"] = model
	}
	b, _ := json.Marshal(in)
	if out, code := r.run(EventReset, string(b)); out != "" || code != 0 {
		r.t.Fatalf("session start: out=%q code=%d", out, code)
	}
}

func (r *steerRig) switchTo(model, source string) {
	r.t.Helper()
	b, _ := json.Marshal(map[string]any{"session_id": r.id, "hook_event_name": "PostModelSwitch", "from_model": "x",
		"to_model": model, "source": source})
	if out, code := r.run(EventPostModelSwitch, string(b)); out != "" || code != 0 {
		r.t.Fatalf("post switch: out=%q code=%d", out, code)
	}
}

// say sends a prompt and returns the additionalContext the hook added, or "".
func (r *steerRig) say(prompt string) string {
	r.t.Helper()
	b, _ := json.Marshal(map[string]any{"session_id": r.id, "prompt_id": "p-" + prompt, "hook_event_name": "UserPromptSubmit",
		"cwd": r.files, "permission_mode": "default", "prompt": prompt})
	out, code := r.run(EventPromptSubmit, string(b))
	if code != 0 {
		r.t.Fatalf("exit %d", code)
	}
	if out == "" {
		return ""
	}
	var v struct {
		H struct {
			Event   string `json:"hookEventName"`
			Context string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		r.t.Fatalf("reply %q: %v", out, err)
	}
	if v.H.Event != "UserPromptSubmit" {
		r.t.Fatalf("reply names event %q", v.H.Event)
	}
	return v.H.Context
}

func (r *steerRig) steerKinds() []string {
	var out []string
	for _, e := range r.events() {
		if e.Hook == config.OptimizerHookRoutineSteer {
			out = append(out, e.Kind)
		}
	}
	return out
}

func TestRoutineSteerSteersRoutinePromptsOnOpus(t *testing.T) {
	r := newSteerRig(t, steerSnapshot(stateOn), true)
	r.begin(opus)
	if got := r.say("yes"); got != SteerText {
		t.Fatalf("a routine prompt on Opus 5.5 got %q", got)
	}
	es := r.events()
	if len(es) != 1 || es[0].Kind != KindSteered || es[0].Tokens != steerTokens || es[0].Hook != config.OptimizerHookRoutineSteer {
		t.Fatalf("events: %+v", es)
	}
	if got := r.say("yes but fix the race first"); got != "" {
		t.Errorf("a prompt with content after the confirmation was steered: %q", got)
	}
	if got := r.say("run the tests"); got != SteerText {
		t.Errorf("run the tests: %q", got)
	}
	if got := r.say("no, that's wrong"); got != "" {
		t.Errorf("a correction was steered: %q", got)
	}
	want := []string{KindSteered, KindTurnEnd, KindSteered, KindTurnEnd, KindReprompted}
	if got := r.steerKinds(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("kinds = %v, want %v", got, want)
	}
	if got := r.say("continue"); got != SteerText {
		t.Errorf("continue: %q", got)
	}
}

// The steer is silent, with nothing recorded, on any model it may not act on, including an unknown one: the prompt
// goes through untouched (rule 4).
func TestRoutineSteerStaysOffOnOtherModels(t *testing.T) {
	for _, m := range []string{sonnet, haiku, fable, "", "claude-opus-5", "opus"} {
		r := newSteerRig(t, steerSnapshot(stateOn), true)
		r.begin(m)
		for _, p := range []string{"yes", "continue", "commit"} {
			if got := r.say(p); got != "" {
				t.Errorf("model %q: %q was steered", m, p)
			}
		}
		if got := r.steerKinds(); len(got) != 0 {
			t.Errorf("model %q: recorded %v", m, got)
		}
	}
}

func TestRoutineSteerFollowsModelSwitches(t *testing.T) {
	r := newSteerRig(t, steerSnapshot(stateOn), true)
	r.begin(opus)
	if r.say("yes") != SteerText {
		t.Fatal("not steered on Opus")
	}
	r.switchTo(haiku, "user")
	if got := r.say("ok"); got != "" {
		t.Errorf("steered after switching to Haiku 5.5: %q", got)
	}
	r.switchTo(opus, "auto") // a switch Claude Code made itself still changes the model
	if got := r.say("ok"); got != SteerText {
		t.Errorf("not steered after switching back to Opus: %q", got)
	}
	r.switchTo("", "user") // a switch with no model: unknown, so off
	if got := r.say("ok"); got != "" {
		t.Errorf("steered with the model unknown: %q", got)
	}
}

// A PostModelSwitch before any SessionStart must not create pins, or the session would look started and its
// optimizer states would never be pinned.
func TestModelSwitchBeforeSessionStartPinsNothing(t *testing.T) {
	r := newSteerRig(t, steerSnapshot(stateOn), true)
	r.switchTo(opus, "user")
	if st := loadSession(r.dir, r.key()); len(st.Pins) != 0 {
		t.Fatalf("pins before SessionStart: %v", st.Pins)
	}
	r.begin(opus)
	if st := loadSession(r.dir, r.key()); st.Pins[config.OptimizerHookRoutineSteer] != stateOn {
		t.Fatalf("states were not pinned at SessionStart: %v", st.Pins)
	}
}

func TestRoutineSteerShadowAndUncheckedOnlyRecord(t *testing.T) {
	cases := []struct {
		name    string
		state   string
		checked bool
		cap     string
	}{
		{"shadow", stateShadow, true, hookcaps.Yes},
		{"on, live check not run", stateOn, false, ""},
		{"on, live check failed", stateOn, true, hookcaps.No},
	}
	for _, c := range cases {
		r := newSteerRig(t, steerSnapshot(c.state), false)
		if c.checked {
			r.capability(c.cap)
		}
		r.begin(opus)
		if got := r.say("yes"); got != "" {
			t.Errorf("%s: steered: %q", c.name, got)
		}
		r.say("try again")
		want := []string{KindWouldSteer, KindTurnEnd, KindReprompted}
		if got := r.steerKinds(); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: kinds = %v, want %v", c.name, got, want)
		}
		for _, e := range r.events() {
			if e.Kind == KindWouldSteer && e.Tokens != 0 {
				t.Errorf("%s: a steer that was not sent was charged %d tokens", c.name, e.Tokens)
			}
		}
	}
}

func TestRoutineSteerOffDoesNothing(t *testing.T) {
	r := newSteerRig(t, steerSnapshot(stateOff), true)
	r.begin(opus)
	for _, p := range []string{"yes", "no", "continue"} {
		if got := r.say(p); got != "" {
			t.Errorf("off steered %q", p)
		}
	}
	if es := r.events(); len(es) != 0 {
		t.Errorf("off recorded %+v", es)
	}
}

func TestRoutineSteerKillSwitch(t *testing.T) {
	r := newSteerRig(t, steerSnapshot(stateOn), true)
	r.begin(opus)
	if err := os.WriteFile(optimize.KillFile(r.dir), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := r.say("yes"); got != "" {
		t.Errorf("steered with the kill switch on: %q", got)
	}
	if got := r.steerKinds(); len(got) != 0 {
		t.Errorf("recorded with the kill switch on: %v", got)
	}
}

// A session pinned before a rollback keeps "on", and State lowers it to shadow at once.
func TestRoutineSteerRollbackLowersAtOnce(t *testing.T) {
	r := newSteerRig(t, steerSnapshot(stateOn), true)
	r.begin(opus)
	r.snapshot(Snapshot{States: map[string]string{config.OptimizerHookRoutineSteer: stateOn}, RolledBack: []string{config.OptimizerHookRoutineSteer}})
	if got := r.say("yes"); got != "" {
		t.Errorf("steered after a rollback: %q", got)
	}
	if got := r.steerKinds(); strings.Join(got, ",") != KindWouldSteer {
		t.Errorf("kinds = %v", got)
	}
}

// A setting changed mid-session waits for the next session (rule 5).
func TestRoutineSteerStateIsPinnedPerSession(t *testing.T) {
	r := newSteerRig(t, steerSnapshot(stateShadow), true)
	r.begin(opus)
	r.snapshot(steerSnapshot(stateOn))
	if got := r.say("yes"); got != "" {
		t.Errorf("a session started in shadow was steered after the config changed: %q", got)
	}
}

// In a trial each session gets an arm by a hash of its key. The "on" arm is steered; the "off" arm only counts its
// routine prompts, so the two can be compared turn for turn.
func TestRoutineSteerTrialArms(t *testing.T) {
	name := config.OptimizerHookRoutineSteer
	var onID, offID string
	for i := 0; i < 200 && (onID == "" || offID == ""); i++ {
		id := "trial-sess-" + string(rune('a'+i%26)) + strings.Repeat("x", i/26)
		if measure.Arm(session.ClaudeCodeKey(id), name) == config.StateOn {
			if onID == "" {
				onID = id
			}
		} else if offID == "" {
			offID = id
		}
	}
	if onID == "" || offID == "" {
		t.Fatal("could not find a session for each arm")
	}
	snap := Snapshot{States: map[string]string{name: stateShadow}, RuleTrial: []string{name}}
	for _, c := range []struct {
		id, arm, steer, kind string
	}{
		{onID, stateOn, SteerText, KindSteered},
		{offID, stateOff, "", KindWouldSteer},
	} {
		r := newSteerRig(t, snap, true)
		r.id = c.id
		r.begin(opus)
		st := loadSession(r.dir, session.ClaudeCodeKey(c.id))
		if st.Pins[pinSteerArm] != c.arm || st.Pins[name] != c.arm {
			t.Fatalf("arm %s: pins %v", c.arm, st.Pins)
		}
		if got := r.say("commit"); got != c.steer {
			t.Errorf("arm %s: got %q", c.arm, got)
		}
		var kinds []string
		for _, e := range r.events() {
			kinds = append(kinds, e.Hook+":"+e.Kind)
		}
		armKind := KindArmOff
		if c.arm == stateOn {
			armKind = KindArmOn
		}
		want := name + ":" + armKind + "," + name + ":" + c.kind
		if strings.Join(kinds, ",") != want {
			t.Errorf("arm %s: events %v, want %s", c.arm, kinds, want)
		}
	}
}

// The prompt never leaves memory: nothing under the data directory holds any of its text (rule 7).
func TestRoutineSteerNeverStoresThePrompt(t *testing.T) {
	r := newSteerRig(t, steerSnapshot(stateOn), true)
	r.begin(opus)
	r.say("yes")
	r.say("no, SECRET-PROMPT-WORDS are wrong")
	r.say("yes")
	r.say("SECRET-PROMPT-WORDS again\nwith a second line")
	err := filepath.WalkDir(r.dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p) //nolint:gosec // test temp dir
		if err != nil {
			return err
		}
		if strings.Contains(string(b), "SECRET-PROMPT") {
			t.Errorf("%s holds prompt text", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The cold-resume guard's block still wins, and the steer adds nothing to a prompt it stops.
func TestRoutineSteerDefersToAColdBlock(t *testing.T) {
	r := newColdRig(t, stateOn, config.ColdResumeAsk, true)
	r.snapshot(Snapshot{States: map[string]string{config.OptimizerHookColdResume: stateOn, config.OptimizerHookRoutineSteer: stateOn},
		ColdResume: ColdResumeSettings{Action: config.ColdResumeAsk, MinTokens: 150000, MinWeekPct: 3}})
	r.rig.now = coldLastRequest.Add(3 * time.Hour)
	in := r.fill("claude_session_start_resume.json", "{{SOURCE}}", "resume", "{{SECONDS}}", "7200",
		"{{TOKENS}}", "200000", "{{EXPIRED}}", "true", "{{USD}}", "1.5")
	// The resume fixture's model is Opus 5.5, so the steer would act if the guard let the prompt through.
	if out, code := r.run(EventReset, in); out != "" || code != 0 {
		t.Fatalf("resume: %q %d", out, code)
	}
	b, _ := json.Marshal(map[string]any{"session_id": sessionID, "prompt_id": "p1", "hook_event_name": "UserPromptSubmit",
		"transcript_path": r.transcript, "cwd": r.files, "prompt": "yes"})
	out, _ := r.run(EventPromptSubmit, string(b))
	if kind, _ := coldReply(t, out); kind != "block" || strings.Contains(out, SteerText) {
		t.Fatalf("want the guard's block alone, got %s", out)
	}
	for _, e := range r.events() {
		if e.Hook == config.OptimizerHookRoutineSteer {
			t.Errorf("the steer recorded %s for a prompt that was stopped", e.Kind)
		}
	}
}
