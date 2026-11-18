package optimize

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/technique"
)

func steerEngine(level string, extras map[string]string) *Engine {
	cfg := config.Default()
	cfg.Optimizers[OptimizerVerbositySteer] = config.StateOn
	cfg.Optimize.BudgetMS = 1000
	if level != "" {
		cfg.Optimize.Verbosity = level
	}
	o := Options{Config: cfg, Getenv: func(string) string { return "" }}
	if extras != nil {
		o.ExtraStates = func() map[string]string { return extras }
	}
	return New(o)
}

var steerSystems = map[string]any{
	"string": "You are a coding agent.",
	"array with the agent's cache marker": []blk{
		{"type": "text", "text": "You are a coding agent."},
		{"type": "text", "text": "Project rules.", "cache_control": map[string]any{"type": "ephemeral"}},
	},
	"absent": nil,
}

// Done when (6.4): the prefix-byte-identical checker passes. Every byte the agent sent is kept,
// in order, with the note inserted at one point at the end of the system prompt; and over a
// 30-turn session each request is the previous one's prefix plus the new messages.
func TestVerbositySteerKeepsThePrefixByteIdentical(t *testing.T) {
	for name, sys := range steerSystems {
		t.Run(name, func(t *testing.T) {
			e := steerEngine("", nil)
			var prev []byte
			for turn := 1; turn <= 30; turn++ {
				body := req{system: sys, tools: []string{"bash"}, msgs: sessionTurns(turn)}.body()
				res := e.Run(context.Background(), input(body))
				if len(res.Failures) != 0 || !res.Changed {
					t.Fatalf("turn %d: changed=%v failures=%+v", turn, res.Changed, res.Failures)
				}
				// Only an insertion: the output minus one contiguous run equals the input.
				i := 0
				for i < len(body) && body[i] == res.Body[i] {
					i++
				}
				added := len(res.Body) - len(body)
				if added <= 0 || !bytes.Equal(res.Body[i+added:], body[i:]) {
					t.Fatalf("turn %d: the request was changed beyond one insertion at byte %d", turn, i)
				}
				if !bytes.Contains(res.Body[i:i+added], []byte("Keep replies short")) {
					t.Fatalf("turn %d: the insertion is not the note: %s", turn, res.Body[i:i+added])
				}
				if prev != nil && !bytes.HasPrefix(res.Body, prefixOf(t, prev)) {
					t.Fatalf("turn %d: transformed prefix differs from turn %d", turn, turn-1)
				}
				prev = res.Body
			}
		})
	}
}

// The note is the last thing in the system prompt, the body stays valid JSON, and the agent's own
// cache marker still sits on the same block.
func TestVerbositySteerAppendsTheNoteLast(t *testing.T) {
	body := req{system: steerSystems["array with the agent's cache marker"], msgs: sessionTurns(1)}.body()
	res := steerEngine(config.VerbosityTerse, nil).Run(context.Background(), input(body))
	var got struct {
		System []map[string]any `json:"system"`
	}
	if err := json.Unmarshal(res.Body, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.System) != 3 || got.System[2]["text"] != VerbosityNote(config.VerbosityTerse) || got.System[2]["cache_control"] != nil {
		t.Fatalf("want the agent's two blocks then the terse note with no marker, got %+v", got.System)
	}
	if got.System[1]["cache_control"] == nil {
		t.Error("the agent's cache marker moved")
	}
	if res.Applied[0].OverheadTokens <= 0 || res.Applied[0].OverheadTokens > (VerbositySteer{}).OverheadTokens() {
		t.Errorf("overhead %d must be charged and within the declared maximum %d", res.Applied[0].OverheadTokens, VerbositySteer{}.OverheadTokens())
	}
}

func TestVerbositySteerIsIdempotent(t *testing.T) {
	for name, sys := range steerSystems {
		t.Run(name, func(t *testing.T) {
			r, err := ParseRequest(req{system: sys, msgs: sessionTurns(2)}.body())
			if err != nil {
				t.Fatal(err)
			}
			once, _ := VerbositySteer{}.Transform(context.Background(), r)
			if err := once.Ensure(); err != nil {
				t.Fatal(err)
			}
			twice, rep := VerbositySteer{}.Transform(context.Background(), once)
			if !bytes.Equal(once.Raw, twice.Raw) || rep.Err != "" {
				t.Fatalf("a second pass changed the request:\n%s\n%s", once.Raw, twice.Raw)
			}
		})
	}
}

// The level is pinned with the session: a config change reaches only new sessions.
func TestVerbosityLevelIsPinnedPerSession(t *testing.T) {
	e := steerEngine(config.VerbosityConcise, nil)
	body := req{system: "sys", msgs: sessionTurns(1)}.body()
	first := e.Run(context.Background(), input(body))
	e.cfg.Optimize.Verbosity = config.VerbosityTerse
	again := e.Run(context.Background(), input(body))
	if !bytes.Equal(first.Body, again.Body) {
		t.Fatal("the note changed mid-session")
	}
	in := input(body)
	in.SessionKey = "s2"
	if fresh := e.Run(context.Background(), in); !bytes.Contains(fresh.Body, []byte("Reply tersely")) {
		t.Fatal("a new session did not pick up the new level")
	}
}

// One output-style rule set at a time (6.3): a session that starts with a third-party brevity rule set
// on in the agent's config never gets the note as well.
func TestVerbositySteerStandsAsideForAnExternalRuleSet(t *testing.T) {
	body := req{system: "sys", msgs: sessionTurns(1)}.body()
	for _, tool := range []string{"caveman", "ponytail", "terse-rules"} {
		res := steerEngine("", map[string]string{"ext_" + tool: config.StateOn}).Run(context.Background(), input(body))
		if res.Changed {
			t.Errorf("%s is on, but the note was added too", tool)
		}
	}
	if res := steerEngine("", map[string]string{"ext_rtk": config.StateOn}).Run(context.Background(), input(body)); !res.Changed {
		t.Error("a tool outside the brevity slot must not switch the note off")
	}
}

func TestVerbositySteerOverheadMatchesTheRegistry(t *testing.T) {
	o, ok := technique.OverheadOf(OptimizerVerbositySteer)
	if !ok || int(o.PerSession) != (VerbositySteer{}).OverheadTokens() {
		t.Fatalf("registry declares %+v, the optimizer %d tokens", o, VerbositySteer{}.OverheadTokens())
	}
	x, _ := technique.Get(OptimizerVerbositySteer)
	if x.Tier != technique.T2 || x.Auto {
		t.Errorf("verbosity_steer must be T2 and not Autopilot-eligible from shadow evidence: %+v", x)
	}
	if !strings.Contains(strings.Join(technique.OverlappingBuiltins("caveman"), ","), OptimizerVerbositySteer) {
		t.Error("enabling an external brevity rule set must be refused while verbosity_steer is on")
	}
}
