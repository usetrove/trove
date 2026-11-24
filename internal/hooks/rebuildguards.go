package hooks

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
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

// The cold-compact guard (hook_cold_compact, technique B23). A /compact after the cache expired sends the whole
// history to be summarized as uncached input (code.claude.com/docs/en/prompt-caching, read 2026-11-24). The guard
// stops the first manual /compact of a session in that state once, by exiting 2: "For a manual /compact, the
// stderr message is shown to the user" (code.claude.com/docs/en/hooks). The second /compact goes through. Auto
// compaction is never stopped: when it recovers from a context-limit error, blocking it fails the request.

// EventCompactCheck runs on PreCompact (matcher manual).
const EventCompactCheck = "pre_compact_cold_check"

const (
	// pinCompactPending is "<tokens>" after the guard stopped a /compact, until the next /compact or the session ends.
	pinCompactPending = "c:compact_pending"
	suffixCompact     = ".compact"
)

// coldCompactTestHook runs before the cold-compact guard decides; tests use it to make the hook panic or stall.
var coldCompactTestHook func()

// compactCheck returns a block (exit 2 with a message) or nothing.
func (h *handler) compactCheck(in Input, key string) (*blockExit, error) {
	if h.agent != AgentClaude || in.Trigger != "manual" {
		return nil, nil
	}
	if coldCompactTestHook != nil {
		coldCompactTestHook()
	}
	st := loadSession(h.dir, key)
	// A /compact after a stopped one is the user's answer: compact anyway. Record it once and let it through.
	if v := st.Pins[pinCompactPending]; v != "" && v != pinCleared {
		n, _ := strconv.ParseInt(v, 10, 64)
		st.do(op{T: "pin", N: pinCompactPending, V: pinCleared})
		if err := st.save(h.dir, key); err != nil {
			return nil, err
		}
		return nil, h.emit(key, config.OptimizerHookColdCompact, KindOverridden, 0, n)
	}
	state := h.snap.State(config.OptimizerHookColdCompact, st.Pins[config.OptimizerHookColdCompact], h.dir, h.getenv)
	if state == stateOff || guardFired(h.dir, key, suffixCompact) {
		return nil, nil
	}
	s, ok := parseColdSettings(st.Pins[pinColdSettings])
	if !ok {
		return nil, nil
	}
	t, ok := lastMainRequest(in.TranscriptPath)
	if !ok {
		return nil, nil
	}
	r := rebuild{tokens: t.prompt, model: t.model, ttl: h.ttlInEffect(key, t), uncached: true}
	if h.now().Sub(t.at) <= r.ttl {
		return nil, nil // the summarization reads the history from the cache
	}
	h.priceRebuild(&r)
	// A manual /compact is always typed, so the stop reaches a person: "ask" with nothing to gate.
	d := decideCold(state, coldSettings{action: config.ColdResumeAsk, minTokens: s.minTokens, minWeekPct: s.minWeekPct}, caseResume, r, true)
	if d.kind == "" {
		return nil, nil
	}
	if err := markGuardFired(h.dir, key, suffixCompact); err != nil {
		return nil, err
	}
	if err := h.emit(key, config.OptimizerHookColdCompact, d.kind, 0, r.tokens); err != nil {
		return nil, err
	}
	if !d.block {
		return nil, nil
	}
	st.do(op{T: "pin", N: pinCompactPending, V: strconv.FormatInt(r.tokens, 10)})
	if err := st.save(h.dir, key); err != nil {
		return nil, err
	}
	return &blockExit{msg: compactBlockText(r, buildColdNote(in.TranscriptPath, in.Cwd))}, nil
}

func compactBlockText(r rebuild, note string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Trove stopped this /compact once: this session was idle longer than its %s prompt cache lifetime, so compacting now sends the whole history to be summarized as uncached input, ",
		ttlWords(r.ttl)) + coldCostText(r) + ".\n\n")
	b.WriteString("To compact anyway, type /compact again. Trove will not stop it twice.\n")
	if note == "" {
		b.WriteString("To start fresh instead, type /clear. Clearing costs nothing.\n")
	} else {
		b.WriteString("To start fresh instead, type /clear, then paste this note with your next prompt. Clearing costs nothing.\n\n")
		b.WriteString("--- note ---\n" + note + "--- end of note ---\n")
	}
	return b.String()
}
