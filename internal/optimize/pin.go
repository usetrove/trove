package optimize

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/usetrove/trove/internal/config"
)

// DefaultBytesPerToken is the ratio used to turn removed bytes into an
// estimated token count (it matches analyze's default calibration).
const DefaultBytesPerToken = 3.5

// Pin is the optimizer configuration frozen for one session. Config edits made
// after a session starts only affect later sessions (CLAUDE.md rule 5), and a
// gateway restart reloads the pin so output does not change mid-session.
type Pin struct {
	SessionKey    string            `json:"session_key"`
	States        map[string]string `json:"states"`
	CustomAgents  []string          `json:"custom_agents"`
	BytesPerToken float64           `json:"bytes_per_token"`
	PinnedAt      time.Time         `json:"pinned_at"`
}

// PinStore persists pins across gateway restarts. Implementations must be safe
// for concurrent use.
type PinStore interface {
	Load(ctx context.Context, sessionKey string) (Pin, bool, error)
	Save(ctx context.Context, p Pin) error
}

const maxPins = 4096

// pinBook is an in-memory pin cache in front of an optional PinStore.
type pinBook struct {
	mu    sync.Mutex
	pins  map[string]Pin
	store PinStore
	// recoverable is true when trimmed output can be stored and fetched back, so
	// the output trimmer may be "on". It is fixed when the engine is built.
	recoverable bool
}

func newPinBook(store PinStore) *pinBook {
	return &pinBook{pins: map[string]Pin{}, store: store}
}

// get returns the session's pin, creating it from cfg on the session's first
// request. A store failure never blocks a request: the in-memory pin is used.
func (b *pinBook) get(ctx context.Context, key string, cfg config.Config, bpt float64, now time.Time, gate StateGate) (Pin, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if p, ok := b.pins[key]; ok {
		return p, nil
	}
	var storeErr error
	if b.store != nil {
		p, ok, err := b.store.Load(ctx, key)
		if err != nil {
			storeErr = err
		} else if ok {
			b.put(p)
			return p, nil
		}
	}
	p := newPinFor(key, cfg, bpt, now, gate, b.recoverable)
	b.put(p)
	if b.store != nil && storeErr == nil {
		storeErr = b.store.Save(ctx, p)
	}
	return p, storeErr
}

func (b *pinBook) put(p Pin) {
	if len(b.pins) >= maxPins {
		type kv struct {
			k string
			t time.Time
		}
		all := make([]kv, 0, len(b.pins))
		for k, v := range b.pins {
			all = append(all, kv{k, v.PinnedAt})
		}
		sort.Slice(all, func(i, j int) bool { return all[i].t.Before(all[j].t) })
		for _, e := range all[:len(all)/4] {
			delete(b.pins, e.k)
		}
	}
	b.pins[p.SessionKey] = p
}

// StateGate may lower an optimizer's state when a session starts (baseline
// period, guardrail rollback, interleaving). It is called once per optimizer
// per new session and its answer is pinned with the session. It must be quick
// and must not block; the engine only ever lowers states with the result
// (on -> shadow/off, shadow -> off), never raises them.
type StateGate func(sessionKey, optimizer, configured string, now time.Time) string

func newPin(key string, cfg config.Config, bpt float64, now time.Time, gate StateGate) Pin {
	return newPinFor(key, cfg, bpt, now, gate, false)
}

func newPinFor(key string, cfg config.Config, bpt float64, now time.Time, gate StateGate, recoverable bool) Pin {
	states := make(map[string]string, len(PipelineNames))
	for _, n := range PipelineNames {
		s := effectiveStateFor(n, cfg.OptimizerState(n), recoverable)
		if gate != nil {
			s = lowerOf(s, effectiveStateFor(n, gate(key, n, s, now), recoverable))
		}
		states[n] = s
	}
	return Pin{
		SessionKey:    key,
		States:        states,
		CustomAgents:  append([]string{}, cfg.Optimize.CustomAgents...),
		BytesPerToken: bpt,
		PinnedAt:      now.UTC(),
	}
}

func rank(s string) int {
	switch s {
	case config.StateOn:
		return 2
	case config.StateShadow:
		return 1
	}
	return 0
}

// lowerOf returns the lower of two states, so a gate can never turn anything on.
func lowerOf(a, b string) string {
	if rank(b) < rank(a) {
		return b
	}
	return a
}

// EffectiveState is the state an optimizer really runs in for a configured
// state when trimmed output cannot be recovered: the same clamp the engine
// applies when it pins a session.
func EffectiveState(name, state string) string { return effectiveState(name, state) }

// EffectiveStateFor is EffectiveState when recoverable says whether trimmed
// output can be fetched back (the MCP server is registered and the recovery
// store works).
func EffectiveStateFor(name, state string, recoverable bool) string {
	return effectiveStateFor(name, state, recoverable)
}

func effectiveState(name, state string) string { return effectiveStateFor(name, state, false) }

// effectiveStateFor clamps a configured state to what is allowed. The output
// trimmer removes text from tool results, so it may be on only when the agent
// can fetch the full output back (trove_fetch_full); otherwise it stays in
// shadow, which only estimates.
func effectiveStateFor(name, state string, recoverable bool) string {
	if name == config.OptimizerOutputTrimmer && state == config.StateOn && !recoverable {
		return config.StateShadow
	}
	switch state {
	case config.StateShadow, config.StateOn:
		return state
	}
	return config.StateOff
}

// customFor reports whether the agent label was marked custom in the pin.
// Claude Code and Codex are never custom, whatever the config says.
func (p Pin) customFor(agent string) bool {
	if agent == "claude_code" || agent == "codex" {
		return false
	}
	for _, a := range p.CustomAgents {
		if a == agent {
			return true
		}
	}
	return false
}
