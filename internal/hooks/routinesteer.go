package hooks

import (
	"encoding/json"
	"strings"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/hookcaps"
	"github.com/usetrove/trove/internal/routine"
)

// The routine steer (hook_routine_steer, technique B24, T2). On a routine prompt ("yes", "continue", "run the tests",
// "commit") it adds SteerText to the context through UserPromptSubmit additionalContext, so the model spends less
// thinking on a turn that needs little.
//
// What it relies on, read 2026-11-24:
//   - platform.claude.com/docs/en/build-with-claude/adaptive-thinking ("Steering thinking"): on Opus 5.5 adaptive
//     thinking is always on and effort is the control; changing effort between requests can invalidate cache
//     breakpoints, while per-message prompting such as "Answer directly without deliberating." leaves earlier
//     breakpoints intact. "On Claude Haiku 5.5 and Claude Fable 5.2, the discouraging phrase doesn't reliably reduce
//     thinking", and asking Sonnet 5.5 to think less in the system prompt is not reliable either. So only the models
//     in steerModels are steered, and a session whose model is unknown is left alone, silently: the prompt goes
//     through untouched (rule 4).
//   - code.claude.com/docs/en/hooks: additionalContext is "added to Claude's context alongside the submitted prompt";
//     Claude Code wraps it in a system reminder at the point the hook fired and saves it in the transcript, replaying
//     it on resume. UserPromptSubmit input carries the prompt but no model; SessionStart carries model and
//     PostModelSwitch carries to_model, so the session's model is pinned from those.
//
// The documentation does not say outright that the text lands inside the new user turn, after the cached prefix. So
// until `trove hooks verify --prompt-context` records that it does on the installed version (hookcaps
// PromptContext), "on" and the trial's "on" arm only record what they would have done.
//
// The prompt is read in memory by the classifier and dropped. Only fixed kinds, times and the hashed session key go
// to the spool (rule 7).

// SteerText is the phrase the adaptive-thinking page gives for per-message steering towards less thinking.
const SteerText = "Answer directly without deliberating."

// steerTokens is the declared overhead per steered prompt (technique.routineSteerTokens): the phrase plus up to
// steerWrapperBytes of Claude Code's system-reminder wrapper, at 3.5 bytes per token, rounded up.
const (
	steerTokens       = 30
	steerWrapperBytes = 68
)

// Session pins of the steer. Values are fixed words.
const (
	// pinSteerModel is "ok" while the session's model is one the steer may act on, "-" otherwise or when unknown.
	pinSteerModel = "c:steer_model"
	// pinSteerArm is the session's arm in the steer's trial, "on" or "off"; absent outside a trial.
	pinSteerArm = "c:steer_arm"
	// pinSteerOpen is "1" from a routine prompt the steer recorded until the session's next prompt, which closes it.
	pinSteerOpen = "c:steer_open"
)

// Kinds of the steer, as stored in hook_events.kind (migration 0028).
const (
	KindSteered    = "steered"
	KindWouldSteer = "would_steer"
	KindReprompted = "reprompted"
	KindTurnEnd    = "turn_end"
)

// steerModels are the models the phrase is documented to work on. Anything else, including Sonnet 5.5 until a trial
// of its own shows the phrase works there, is never steered.
var steerModels = map[string]bool{"claude-opus-5-5": true}

// steerModelOK reports whether a model id from SessionStart or PostModelSwitch is one the steer may act on. The
// one-million-token context variant is the same model.
func steerModelOK(model string) bool {
	return steerModels[strings.TrimSuffix(model, "[1m]")]
}

// pinSteerModelFor records whether the session's model may be steered. It runs on every SessionStart and
// PostModelSwitch; an event without a model clears it, so a session whose model is unknown stays unsteered.
func pinSteerModelFor(st *sessionState, model string) {
	v := "-"
	if steerModelOK(model) {
		v = "ok"
	}
	if st.Pins[pinSteerModel] != v {
		st.do(op{T: "pin", N: pinSteerModel, V: v})
	}
}

// steerKind is the pure decision for a routine prompt on an allowed model: what to record, and whether to send the
// text. state is the optimizer's state for this session (config, lowered by a rollback or the kill switch), arm the
// session's trial arm ("" outside a trial), killed whether the kill switch is on, and contextSafe whether the live
// check passed.
func steerKind(state, arm string, killed, contextSafe bool) (kind string, send bool) {
	if killed {
		return "", false
	}
	switch arm {
	case stateOff:
		return KindWouldSteer, false // the comparison: counted, never steered
	case stateOn:
		if state == stateOn && contextSafe {
			return KindSteered, true
		}
		return KindWouldSteer, false // rolled back, or the live check has not passed
	}
	switch state {
	case stateOn:
		if contextSafe {
			return KindSteered, true
		}
		return KindWouldSteer, false
	case stateShadow:
		return KindWouldSteer, false
	}
	return "", false
}

// routineSteer runs on UserPromptSubmit after the cold-resume guard let the prompt through. It returns the text to
// add, or "". Any error means no text (rule 4).
func (h *handler) routineSteer(in Input, key string) (string, error) {
	if h.agent != AgentClaude {
		return "", nil
	}
	st := loadSession(h.dir, key)
	label := routine.Classify(in.PromptText)
	var kinds []string
	if st.Pins[pinSteerOpen] == "1" {
		st.do(op{T: "pin", N: pinSteerOpen, V: pinCleared})
		kinds = append(kinds, KindTurnEnd)
		if label.Correction {
			kinds = append(kinds, KindReprompted)
		}
	}
	kind, send := "", false
	if label.Routine && st.Pins[pinSteerModel] == "ok" {
		name := config.OptimizerHookRoutineSteer
		state := h.snap.State(name, st.Pins[name], h.dir, h.getenv)
		killed := h.snap.State(name, stateShadow, h.dir, h.getenv) == stateOff
		contextSafe := LoadCapabilities(h.dir).Supports(AgentClaude, hookcaps.ToolPrompt, hookcaps.PromptContext)
		kind, send = steerKind(state, st.Pins[pinSteerArm], killed, contextSafe)
		if kind != "" {
			st.do(op{T: "pin", N: pinSteerOpen, V: "1"})
		}
	}
	if len(kinds) == 0 && kind == "" {
		return "", nil
	}
	if err := st.save(h.dir, key); err != nil {
		return "", err
	}
	for _, k := range kinds {
		if err := h.emit(key, config.OptimizerHookRoutineSteer, k, 0, 0); err != nil {
			return "", err
		}
	}
	if kind == "" {
		return "", nil
	}
	var tokens int64
	if send {
		tokens = steerTokens
	}
	// Recorded before it is sent: a steer that could not be counted is not sent, so the comparison never misses one.
	if err := h.emit(key, config.OptimizerHookRoutineSteer, kind, 0, tokens); err != nil {
		return "", err
	}
	if !send {
		return "", nil
	}
	return SteerText, nil
}

// promptReply combines the cold-resume guard's reply (a block, a systemMessage or nothing) with the steer's text.
// A block wins and nothing is added: the prompt does not go to the model.
func promptReply(cold []byte, steer string) []byte {
	if steer == "" {
		return cold
	}
	out := map[string]any{}
	if len(cold) > 0 {
		if json.Unmarshal(cold, &out) != nil {
			return cold
		}
		if _, blocked := out["decision"]; blocked {
			return cold
		}
	}
	out["hookSpecificOutput"] = map[string]any{"hookEventName": "UserPromptSubmit", "additionalContext": steer}
	b, err := json.Marshal(out)
	if err != nil {
		return cold
	}
	return b
}
