package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/optimize"
	"github.com/usetrove/trove/internal/recovery"
)

// This file is the whole of the hooks adapter's contact with the engine: build
// a neutral ToolCall from the hook's JSON, hand the session's storage over, and
// turn the engine's decision into what the agent expects. No optimization
// decision is made here.

// engineMargin is what the engine's budget leaves of callDeadline for starting
// up, reading the journal and printing.
const engineMargin = 30 * time.Millisecond

// engine returns the engine hooks call, built once per hook process. Hooks
// resolve states themselves (pins, rollbacks, the kill switch), so the engine's
// own config only supplies the per-call time budget. That budget is the hook's
// deadline less a margin, not the gateway's 25 ms: these optimizers read files
// and the journal, which the hook deadline has always allowed time for, and a
// budget shorter than that would silently skip work on a slow disk.
func (h *handler) engine() *optimize.Engine {
	if h.eng == nil {
		cfg := config.Default()
		budget := h.budget
		if budget <= 0 {
			budget = callDeadline
		}
		cfg.Optimize.BudgetMS = int((budget - engineMargin) / time.Millisecond)
		opts := optimize.Options{Config: cfg, Getenv: h.getenv, Now: h.now, KillFile: optimize.KillFile(h.dir)}
		if h.recover {
			// Without a recovery store the engine keeps the trimmer in shadow, so
			// a store that cannot be opened is not an error: nothing is trimmed.
			if rec := h.openRecovery(); rec != nil {
				opts.Recovery = rec
			}
		}
		h.eng = optimize.New(opts)
	}
	return h.eng
}

// openRecovery opens the store that holds the full text of trimmed output. The
// size cap and lifetime come from the snapshot (config [recovery]).
func (h *handler) openRecovery() optimize.RecoveryStore {
	d := config.Default().Recovery
	if h.snap.RecoveryMaxMB > 0 {
		d.MaxMB = h.snap.RecoveryMaxMB
	}
	if h.snap.RecoveryTTLDays > 0 {
		d.TTLDays = h.snap.RecoveryTTLDays
	}
	st, err := recovery.Open(filepath.Join(h.dir, "recovery"), recovery.Options{
		MaxBytes: int64(d.MaxMB) << 20, TTL: time.Duration(d.TTLDays) * 24 * time.Hour})
	if err != nil {
		logf(h.dir, "recovery", "the recovery store could not be opened, so output is not trimmed: %v", err)
		return nil
	}
	return st
}

// capabilities loads the probe result once per process.
func (h *handler) capabilities() Capabilities {
	if h.caps == nil {
		c := LoadCapabilities(h.dir)
		h.caps = &c
	}
	return *h.caps
}

// rewriteKind says which kind of rewrite an optimizer does, or "" if it does not
// rewrite anything the agent could ignore.
func rewriteKind(name string) string {
	switch name {
	case config.OptimizerHookReadLimit, config.OptimizerHookQuietCommands:
		return RewriteInput
	case config.OptimizerNoiseNormalizer, config.OptimizerOutputTrimmer:
		return RewriteOutput
	}
	return ""
}

// stateFor is the state an optimizer acts in for one tool call: the pinned state,
// lowered to shadow when the agent version has not been seen to honour that kind
// of rewrite for this tool (CLAUDE.md rule 4: doing less is always safe), and
// switched off where the optimizer does not belong. Both are adapter-level
// support facts; whether and how to optimize is still the engine's call.
func (h *handler) stateFor(name, pinned, tool string) string {
	st := h.snap.State(name, pinned, h.dir, h.getenv)
	if name == config.OptimizerOutputTrimmer && tool != "Bash" {
		return stateOff // it is made for command output, not search results or file text
	}
	if st != stateOn {
		return st
	}
	if kind := rewriteKind(name); kind != "" && !h.capabilities().Supports(h.agent, tool, kind) {
		return stateShadow
	}
	return st
}

// sess describes the hook's session to the engine.
func (h *handler) sess(key string, st *sessionState, tool string) optimize.Session {
	return optimize.Session{
		Key: key, Agent: h.agent, Now: h.now(), TroveRun: h.snap.TroveRun,
		Ledger: &ledgerView{st: st, dir: h.dir, key: key},
		State:  func(name string) string { return h.stateFor(name, st.Pins[name], tool) },
	}
}

// toolCall is the hook's tool call in the engine's neutral form.
func toolCall(in Input) optimize.ToolCall {
	if len(in.ToolInput.Raw) > 0 {
		return optimize.ToolCall{Name: in.ToolName, Input: in.ToolInput.Raw}
	}
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
func (h *handler) render(key string, cur optimize.ToolCall, reps []optimize.Report) ([]byte, error) {
	if err := failure(reps); err != nil {
		return nil, err
	}
	denied := false
	for _, r := range reps {
		denied = denied || (r.Decision != nil && r.Decision.IsDeny())
	}
	var out []byte
	var notes []string
	rewritten := false
	for _, r := range reps {
		d := r.Decision
		if d == nil {
			continue
		}
		if denied && (d.Kind == optimize.KindRewritten || d.Kind == optimize.KindWouldRewrite) {
			continue // the call is not running, so nothing was rewritten
		}
		if err := h.emit(key, r.Name, d.Kind, d.Bytes, d.Tokens); err != nil {
			return nil, err
		}
		switch d.Kind {
		case optimize.KindBlocked:
			out = denyJSON(d.Message)
		case optimize.KindAdvised:
			notes = append(notes, d.Message)
		case optimize.KindRewritten:
			rewritten = true
			if d.Message != "" {
				notes = append(notes, d.Message)
			}
		}
	}
	switch {
	case denied:
		return out, nil
	case rewritten:
		return updatedInputJSON(cur.Input, joinNotes(notes)), nil
	case len(notes) > 0:
		return contextJSON(joinNotes(notes)), nil
	}
	return nil, nil
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
