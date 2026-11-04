package external

import (
	"context"
	"strings"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/store"
)

// ActiveStates returns the real state of each installed tool, keyed by
// optimizer name, for pinning at session start. A read failure yields an
// empty map: sessions are then simply not labelled, never mislabelled.
func ActiveStates(ctx context.Context, s Store) map[string]string {
	recs, err := s.List(ctx)
	if err != nil {
		return nil
	}
	out := make(map[string]string, len(recs))
	for _, r := range recs {
		st := config.StateOff
		if r.State == "on" {
			st = config.StateOn
		}
		out[Prefix+r.Name] = st
	}
	return out
}

// RolledBack is one tool Guard switched off.
type RolledBack struct {
	Event measure.Event
	Name  string
	Err   error // non-nil if the config changes could not be undone
}

// Guard switches off every enabled tool whose sessions cost more than sessions
// without it (a tool whose overhead exceeds its savings on this user's work).
// The comparison is the real bill per session, so tool definitions, injected
// rules and extra turns are all counted; nothing is charged twice. Running
// sessions keep their pinned state (rule 5). The tool stays installed.
func (e Env) Guard(ctx context.Context, db *store.DB, m config.Measure) ([]RolledBack, error) {
	recs, err := e.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, r := range recs {
		if r.State == "on" {
			names = append(names, Prefix+r.Name)
		}
	}
	if len(names) == 0 {
		return nil, nil
	}
	events, err := measure.EvaluateNetCost(ctx, db, m, e.now(), names)
	var out []RolledBack
	for _, ev := range events {
		rb := RolledBack{Event: ev, Name: strings.TrimPrefix(ev.Optimizer, Prefix)}
		_, rb.Err = e.Disable(ctx, rb.Name)
		out = append(out, rb)
	}
	return out, err
}

// GuardAll runs Guard with the built-in manifests. It is what the gateway's
// periodic check and the hooks' bookkeeping call. It needs no agent config
// folder: undoing a tool's changes uses the paths recorded when it was enabled.
func GuardAll(ctx context.Context, db *store.DB, dataDir string, m config.Measure, now time.Time) ([]RolledBack, error) {
	ms, err := Builtin()
	if err != nil {
		return nil, err
	}
	env := Env{DataDir: dataDir, Runner: ExecRunner{}, Store: db.ExternalTools(), Manifests: ms, Now: func() time.Time { return now }}
	return env.Guard(ctx, db, m)
}
