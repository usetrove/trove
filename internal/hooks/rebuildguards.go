package hooks

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/usetrove/trove/internal/config"
)

// The model-switch guard (hook_model_switch, technique B22). Each model has its own prompt cache, so switching model
// on a large, warm session makes the next request write the whole context to the new model's cache. Claude Code
// already asks the user to confirm such a switch; this guard only adds what it costs, as a systemMessage, which is
// "shown to the user regardless of the decision" (code.claude.com/docs/en/hooks, read 2026-11-24). It never answers
// allow, deny or ask: "ask" is a refusal outside an interactive /model, and a PreModelSwitch hook that times out
// blocks the switch, so the guard decides nothing and runs under the 150 ms safety net.

// Events of the rebuild guards.
const (
	EventModelSwitch     = "pre_model_switch"
	EventPostModelSwitch = "post_model_switch"
)

const (
	// pinSwitchPending is "<tokens>" after the guard warned (or would have), until the switch happens.
	pinSwitchPending = "c:switch_pending"
	suffixSwitch     = ".switch"
	// coldWarnMaxBytes caps a rebuild warning; the registry declares its tokens (coldWarnTokens, 120).
	coldWarnMaxBytes = 420
)

// coldWarnTestHook runs before the model-switch guard decides; tests use it to make the hook panic or stall.
var coldWarnTestHook func()

// modelSwitch is the PreModelSwitch hook. It returns a systemMessage or nothing; never a decision.
func (h *handler) modelSwitch(in Input, key string) ([]byte, error) {
	if h.agent != AgentClaude {
		return nil, nil
	}
	if coldWarnTestHook != nil {
		coldWarnTestHook()
	}
	st := loadSession(h.dir, key)
	state := h.snap.State(config.OptimizerHookModelSwitch, st.Pins[config.OptimizerHookModelSwitch], h.dir, h.getenv)
	if state == stateOff || guardFired(h.dir, key, suffixSwitch) {
		return nil, nil
	}
	s, ok := parseColdSettings(st.Pins[pinColdSettings])
	if !ok || in.PromptCacheWarm == nil || !*in.PromptCacheWarm || in.ContextTokens == nil || *in.ContextTokens <= 0 ||
		in.ToModel == "" || in.FromModel == in.ToModel {
		return nil, nil
	}
	r := rebuild{tokens: *in.ContextTokens, model: in.ToModel, ttl: cacheTTL(in.CacheTTL)}
	if v := in.EstimatedCacheWriteUSD; v != nil && *v > 0 && *v < 1e6 {
		r.usd, r.usdBy = *v, "Claude Code"
	}
	h.priceRebuild(&r)
	// The same size gate and modes as the cold-resume guard's idle case: shadow records, on warns.
	d := decideCold(state, coldSettings{action: config.ColdResumeWarn, minTokens: s.minTokens, minWeekPct: s.minWeekPct}, caseIdle, r, false)
	if d.kind == "" {
		return nil, nil
	}
	if err := markGuardFired(h.dir, key, suffixSwitch); err != nil {
		return nil, err
	}
	if err := h.emit(key, config.OptimizerHookModelSwitch, d.kind, 0, r.tokens); err != nil {
		return nil, err
	}
	st.do(op{T: "pin", N: pinSwitchPending, V: strconv.FormatInt(r.tokens, 10)})
	if err := st.save(h.dir, key); err != nil {
		return nil, err
	}
	if !d.warn {
		return nil, nil
	}
	b, _ := json.Marshal(map[string]any{"systemMessage": switchWarnText(r)})
	return b, nil
}

// postModelSwitch records that a switch the guard warned about (or would have) went ahead. It prints nothing.
// Switches Claude Code made itself ("auto") or restored on resume are not the user's answer.
func (h *handler) postModelSwitch(in Input, key string) error {
	if h.agent != AgentClaude || in.Source == "auto" || in.Source == "resume" {
		return nil
	}
	st := loadSession(h.dir, key)
	v := st.Pins[pinSwitchPending]
	if v == "" || v == pinCleared {
		return nil
	}
	n, _ := strconv.ParseInt(v, 10, 64)
	st.do(op{T: "pin", N: pinSwitchPending, V: pinCleared})
	if err := st.save(h.dir, key); err != nil {
		return err
	}
	return h.emit(key, config.OptimizerHookModelSwitch, KindOverridden, 0, n)
}

// switchWarnText is the warning, capped at coldWarnMaxBytes.
func switchWarnText(r rebuild) string {
	return capBytes("Trove: each model has its own cache, so this switch writes the whole context again, "+coldCostText(r)+".", coldWarnMaxBytes)
}

func cacheTTL(v string) time.Duration {
	switch v {
	case "5m":
		return 5 * time.Minute
	case "1h":
		return time.Hour
	}
	return coldDefaultTTL
}
