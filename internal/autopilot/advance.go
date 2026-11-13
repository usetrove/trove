package autopilot

import (
	"context"
	"fmt"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/store"
)

// Enable records the user's consent and starts every eligible optimizer in
// shadow. It changes no request: shadow forwards the original and only estimates.
// An optimizer the user set in config.toml keeps the user's value.
func Enable(dir string, p Policy, now time.Time) (config.AutopilotFile, error) {
	f, err := config.LoadAutopilot(dir)
	if err != nil {
		f = config.AutopilotFile{Entries: map[string]config.AutopilotEntry{}}
	}
	f.Enabled, f.Paused, f.Policy, f.ConsentedAt = true, false, config.AutopilotPolicy, now.UnixMilli()
	for _, name := range p.Eligible {
		if _, ok := f.Entries[name]; !ok {
			f.Entries[name] = config.AutopilotEntry{State: config.AutopilotShadow, SinceMS: now.UnixMilli(), Reason: "collecting shadow estimates"}
		}
	}
	return f, config.SaveAutopilot(dir, f)
}

// Disable withdraws consent and forgets Autopilot's states. New sessions go back
// to what config.toml says. Sessions already running keep their pinned states.
func Disable(dir string) error {
	return config.SaveAutopilot(dir, config.AutopilotFile{Entries: map[string]config.AutopilotEntry{}})
}

// SetPaused pauses or resumes Autopilot without losing its states.
func SetPaused(dir string, paused bool) (config.AutopilotFile, error) {
	f, err := config.LoadAutopilot(dir)
	if err != nil {
		return f, err
	}
	f.Paused = paused
	return f, config.SaveAutopilot(dir, f)
}

// Advance evaluates every eligible optimizer once and saves any change. Call it
// at a session boundary (a session ending, or the gateway starting): the new
// states only reach sessions that start afterwards, because the engine pins a
// session's states on its first request. A failure leaves the saved states as
// they were, so an error never turns anything on.
func Advance(ctx context.Context, db *store.DB, dir string, cfg config.Config, p Policy, now time.Time) (config.AutopilotFile, bool, error) {
	f, err := config.LoadAutopilot(dir)
	if err != nil {
		return f, false, err
	}
	if !f.Active() {
		return f, false, nil
	}
	base, rolled, err := measure.LoadStatus(ctx, db, cfg.Measure, now)
	if err != nil {
		return f, false, fmt.Errorf("cannot read the baseline status: %v", err)
	}
	w := measure.Window{From: now.AddDate(0, 0, -p.LookbackDays), To: now}
	rows, err := measure.ShadowEstimates(ctx, db, w, cfg)
	if err != nil {
		return f, false, fmt.Errorf("cannot read shadow estimates: %v", err)
	}
	byName := map[string]measure.ShadowRow{}
	for _, r := range rows {
		byName[r.Optimizer] = r
	}
	changed := false
	for _, name := range p.Eligible {
		if _, mine := cfg.Optimizers[name]; mine {
			// The user's own setting wins; Autopilot stays out of the way.
			if _, had := f.Entries[name]; had {
				delete(f.Entries, name)
				changed = true
			}
			continue
		}
		cur := f.Entries[name]
		_, back := rolled[name]
		row := byName[name]
		ev := Evidence{
			BaselineDone:   base.Phase == measure.PhaseActive,
			ShadowSessions: row.Sessions,
			NetTokens:      int64(row.Tokens.Value) - p.OverheadTokens[name]*int64(row.Sessions),
			RolledBack:     back,
		}
		d := p.Decide(cur, ev, now)
		if d.ClearRollback {
			if err := measure.ClearOverride(ctx, db, name, now); err != nil {
				return f, false, fmt.Errorf("cannot lift the rollback of %s: %v", name, err)
			}
		}
		if d.Entry != cur {
			f.Entries[name] = d.Entry
			changed = true
		}
	}
	f.LastEvalMS = now.UnixMilli()
	if changed {
		if err := config.SaveAutopilot(dir, f); err != nil {
			return f, false, fmt.Errorf("cannot save Autopilot's state: %v", err)
		}
	}
	return f, changed, nil
}
