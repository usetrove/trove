package hooks

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/optimize"
)

// This file is the whole of the hooks adapter's contact with the engine: build
// a neutral ToolCall from the hook's JSON, hand the session's storage over, and
// turn the engine's decision into what the agent expects. No optimization
// decision is made here.

// engine returns the engine hooks call, built once per hook process. Hooks
// resolve states themselves (pins, rollbacks, the kill switch), so the engine's
// own config only supplies the per-call time budget.
func (h *handler) engine() *optimize.Engine {
	if h.eng == nil {
		h.eng = optimize.New(optimize.Options{Config: config.Default(), Getenv: h.getenv, Now: h.now, KillFile: optimize.KillFile(h.dir)})
	}
	return h.eng
}

// sess describes the hook's session to the engine.
func (h *handler) sess(key string, st *sessionState) optimize.Session {
	return optimize.Session{
		Key: key, Agent: h.agent, Now: h.now(), TroveRun: h.snap.TroveRun,
		Ledger: &ledgerView{st: st, dir: h.dir, key: key},
		State:  func(name string) string { return h.snap.State(name, st.Pins[name], h.dir, h.getenv) },
	}
}

// toolCall is the hook's tool call in the engine's neutral form.
func toolCall(in Input) optimize.ToolCall {
	b, _ := json.Marshal(map[string]any{
		"file_path": in.ToolInput.FilePath, "offset": in.ToolInput.Offset, "limit": in.ToolInput.Limit,
		"pages": in.ToolInput.Pages, "command": in.ToolInput.Command,
	})
	return optimize.ToolCall{Name: in.ToolName, Input: b}
}

// ToolOutputOf reads the tool output out of a PostToolUse hook input as text
// parts. The shape of tool_response differs by tool; this takes the string
// fields that carry output (stdout, stderr, content, or a bare string) and
// ignores the rest. Best effort: an unknown shape yields no parts.
func ToolOutputOf(in Input) optimize.ToolOutput {
	var out optimize.ToolOutput
	if len(in.ToolResponse) == 0 {
		return out
	}
	var s string
	if json.Unmarshal(in.ToolResponse, &s) == nil {
		out.Parts = append(out.Parts, optimize.Part{Text: s})
		return out
	}
	var m struct {
		Stdout  string `json:"stdout"`
		Stderr  string `json:"stderr"`
		Content string `json:"content"`
		File    struct {
			Content string `json:"content"`
		} `json:"file"`
	}
	if json.Unmarshal(in.ToolResponse, &m) != nil {
		return out
	}
	for _, t := range []string{m.Stdout, m.Stderr, m.Content, m.File.Content} {
		if t != "" {
			out.Parts = append(out.Parts, optimize.Part{Text: t})
		}
	}
	return out
}

// TransformOutput runs the engine's tool-output optimizers on a PostToolUse
// hook input and returns the result and the reports. It is how the parity test
// drives this adapter. state names the optimizers to run (others stay off).
func TransformOutput(ctx context.Context, eng *optimize.Engine, in Input, state func(name string) string) (optimize.ToolOutput, []optimize.Report) {
	return eng.TransformToolOutput(ctx, toolCall(in), ToolOutputOf(in), optimize.Session{Key: "hook", Agent: "claude_code", State: state})
}

// failure reports the first optimizer that did not complete, as an error for
// the hook's log. The call itself always goes through.
func failure(reps []optimize.Report) error {
	for _, r := range reps {
		if r.Err != "" {
			return fmt.Errorf("%s did not complete (%s); the call was let through", r.Name, r.Err)
		}
	}
	return nil
}

// render counts the engine's decisions and turns the one that needs the agent's
// attention into hook output. Counting comes first: a decision that cannot be
// counted is not acted on.
func (h *handler) render(key string, reps []optimize.Report) ([]byte, error) {
	if err := failure(reps); err != nil {
		return nil, err
	}
	var out []byte
	for _, r := range reps {
		d := r.Decision
		if d == nil {
			continue
		}
		if err := h.emit(key, r.Name, d.Kind, d.Bytes, d.Tokens); err != nil {
			return nil, err
		}
		switch d.Kind {
		case optimize.KindBlocked:
			out = denyJSON(d.Message)
		case optimize.KindAdvised:
			out = contextJSON(d.Message)
		}
	}
	return out, nil
}

// ledgerView gives the engine the session journal as a ReadLedger.
type ledgerView struct {
	st       *sessionState
	dir, key string
}

func (l *ledgerView) Get(k string) (optimize.ReadEntry, bool) {
	e := l.st.Reads[k]
	if e == nil {
		return optimize.ReadEntry{}, false
	}
	return *e, true
}

func (l *ledgerView) Put(k string, e optimize.ReadEntry) {
	l.st.do(op{T: "read", K: k, E: &e})
}

func (l *ledgerView) Delete(k string) { l.st.do(op{T: "del", K: k}) }

func (l *ledgerView) Prompts() int { return l.st.PromptN }

func (l *ledgerView) Save() error { return l.st.save(l.dir, l.key) }
