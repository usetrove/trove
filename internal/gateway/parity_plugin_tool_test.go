package gateway

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/adapter/plugin"
	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/hooks"
	"github.com/usetrove/trove/internal/optimize"
)

// Parity for the optimizers that act only on a tool call before it runs (the
// gateway cannot reach them): a plugin's tool_input must give the same decision
// and the same rewritten input as the hooks adapter does for the same call. Both
// adapters hand the engine a neutral ToolCall; this proves the plugin protocol
// adds nothing in between. The rows come from the capability matrix, so a new
// tool-only optimizer fails here until it has a fixture.

// ledger is a session's read memory for the hooks side of the comparison.
type ledger struct {
	mu    sync.Mutex
	reads map[string]optimize.ReadEntry
}

func (l *ledger) Get(k string) (optimize.ReadEntry, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.reads[k]
	return e, ok
}
func (l *ledger) Put(k string, e optimize.ReadEntry) { l.mu.Lock(); l.reads[k] = e; l.mu.Unlock() }
func (l *ledger) Delete(k string)                    { l.mu.Lock(); delete(l.reads, k); l.mu.Unlock() }
func (l *ledger) Prompts() int                       { return 1 }
func (l *ledger) Save() error                        { return nil }

type toolFixture struct {
	tool  string
	input string // the tool's arguments, in the engine's (Claude Code's) argument names
	// prior is run as a successful call first, so the optimizer has something to remember.
	prior bool
}

func toolInputFixtures(t *testing.T) map[string]toolFixture {
	t.Helper()
	dir := t.TempDir()
	big := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(big, []byte(strings.Repeat("a line of a large file\n", 4000)), 0o644); err != nil {
		t.Fatal(err)
	}
	small := filepath.Join(dir, "small.txt")
	if err := os.WriteFile(small, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	enc := func(v any) string { b, _ := json.Marshal(v); return string(b) }
	return map[string]toolFixture{
		"hook_read_limit":     {"Read", enc(map[string]any{"file_path": big}), false},
		"hook_repeat_read":    {"Read", enc(map[string]any{"file_path": small}), true},
		"hook_quiet_commands": {"Bash", enc(map[string]any{"command": "npm install"}), false},
		"hook_large_output":   {"Bash", enc(map[string]any{"command": "go test ./..."}), false},
	}
}

func TestPluginParityWithHooksOnToolInput(t *testing.T) {
	fixtures := toolInputFixtures(t)
	ran := 0
	for _, row := range optimize.Matrix() {
		if !row.Reaches(optimize.AdapterHooks) || !row.Reaches(optimize.AdapterPlugin) || row.Reaches(optimize.AdapterGateway) {
			continue
		}
		row := row
		t.Run(row.Optimizer, func(t *testing.T) {
			fx, ok := fixtures[row.Optimizer]
			if !ok {
				t.Fatalf("%s runs on the hooks and plugin adapters but has no tool-input fixture; add one to toolInputFixtures", row.Optimizer)
			}
			call := optimize.ToolCall{Name: fx.tool, Input: json.RawMessage(fx.input)}

			// Hooks side: the hook JSON becomes a ToolCall through the hooks adapter.
			var in hooks.Input
			raw := `{"session_id":"s","hook_event_name":"PreToolUse","tool_name":"` + fx.tool + `","tool_input":` + fx.input + `}`
			if err := json.Unmarshal([]byte(raw), &in); err != nil {
				t.Fatal(err)
			}
			led := &ledger{reads: map[string]optimize.ReadEntry{}}
			eng := parityEngine(t, row.Optimizer)
			hookSess := func() optimize.Session {
				return optimize.Session{Key: "hook", Agent: "claude_code", TroveRun: true, Ledger: led, Now: time.Now(),
					State: func(n string) string {
						if n == row.Optimizer {
							return config.StateOn
						}
						return config.StateOff
					}}
			}
			// The engine used by parityEngine has the request pipeline only for gateway rows;
			// tool-level optimizers live in the engine's tool pipeline, which every engine has.
			if fx.prior {
				eng.TransformToolOutput(context.Background(), hooks.ToolCallOf(in), optimize.ToolOutput{Parts: []optimize.Part{{Text: "x"}}}, hookSess())
			}
			wantCall, wantReps := eng.TransformToolInput(context.Background(), hooks.ToolCallOf(in), hookSess())

			// Plugin side, over the protocol.
			srv := plugin.New(parityEngine(t, row.Optimizer))
			sess := plugin.Session{Key: "parity", Harness: "opencode", Login: plugin.LoginAPIKey, TroveRun: true, Prompts: 1,
				States: map[string]string{row.Optimizer: config.StateOn}}
			if fx.prior {
				pluginReply(t, srv, plugin.Message{Method: "tool_output", Session: sess, Call: &call,
					Output: &optimize.ToolOutput{Parts: []optimize.Part{{Text: "x"}}}})
			}
			rep := pluginReply(t, srv, plugin.Message{Method: "tool_input", Session: sess, Call: &call})

			if string(rep.Call.Input) != string(wantCall.Input) || rep.Call.Name != wantCall.Name {
				t.Errorf("rewritten call differs:\nplugin: %s\nhooks:  %s", rep.Call.Input, wantCall.Input)
			}
			got, want := decisionOf(rep.Reports, row.Optimizer), decisionOf(wantReps, row.Optimizer)
			if want == nil {
				t.Fatalf("the fixture did not make %s act (hooks reports: %+v)", row.Optimizer, wantReps)
			}
			if got == nil || *got != *want {
				t.Errorf("decisions differ:\nplugin: %+v\nhooks:  %+v", got, want)
			}
		})
		ran++
	}
	if ran == 0 {
		t.Fatal("no tool-only optimizer is reached by both the hooks and plugin adapters; the parity test is vacuous")
	}
}

// decisionOf returns the optimizer's decision, or a synthetic one for a rewrite
// that carries none, so the two sides are compared on kind, bytes and tokens.
func decisionOf(reps []optimize.Report, name string) *optimize.Decision {
	for _, r := range reps {
		if r.Name != name {
			continue
		}
		if r.Decision != nil {
			d := *r.Decision
			return &d
		}
		return &optimize.Decision{Kind: "none", Bytes: int64(r.BytesRemoved), Tokens: r.TokensRemoved}
	}
	return nil
}
