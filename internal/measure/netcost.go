package measure

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/store"
)

// RuleNetCost is recorded in guardrail_events when a third-party tool's
// sessions cost more than comparable sessions without it.
const RuleNetCost = "net_cost"

// NetCost compares the real bill per session with a tool on and off. Unlike
// Corroborate it counts everything the tool causes: its tool definitions and
// rules (already inside every request's prompt tokens), extra turns, and any
// extra output. That makes it the honest net figure for a tool whose savings
// happen outside Trove's request path. It cannot prove cause: sessions differ.
type NetCost struct {
	OnMeanUSD  float64 `json:"on_cost_per_session_usd"`
	OffMeanUSD float64 `json:"off_cost_per_session_usd"`
	NOn        int     `json:"n_on_sessions"`
	NOff       int     `json:"n_off_sessions"`
	// Costlier is true only when the on sessions cost more and the gap is beyond chance.
	Costlier bool   `json:"costlier"`
	Enough   bool   `json:"enough_data"`
	Label    Label  `json:"label"`
	Text     string `json:"text"`
}

// NetCostFor returns nil when there are no off sessions to compare with.
func NetCostFor(ctx context.Context, db *store.DB, optimizer string, m config.Measure) (*NetCost, error) {
	rows, err := db.QueryContext(ctx, `SELECT p.states, p.pinned_at, s.total_cost_usd
		FROM sessions s JOIN session_pins p ON p.session_key = s.session_key WHERE s.request_count > 0`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	type pt struct {
		on     bool
		pinned int64
		v      float64
	}
	var all []pt
	var firstOn int64
	for rows.Next() {
		var st string
		var pinned int64
		var v float64
		if err := rows.Scan(&st, &pinned, &v); err != nil {
			return nil, err
		}
		var states map[string]string
		if json.Unmarshal([]byte(st), &states) != nil {
			continue
		}
		on := states[optimizer] == config.StateOn
		if on && (firstOn == 0 || pinned < firstOn) {
			firstOn = pinned
		}
		all = append(all, pt{on, pinned, v})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var on, off []float64
	for _, p := range all {
		switch {
		case p.on:
			on = append(on, p.v)
		case firstOn != 0 && p.pinned >= firstOn:
			off = append(off, p.v)
		}
	}
	if len(off) == 0 {
		return nil, nil
	}
	c := &NetCost{Label: Measured, NOn: len(on), NOff: len(off)}
	if len(on) < m.MinSessions || len(off) < m.MinSessions {
		c.Text = TextNotEnough + " to compare the cost of on and off sessions"
		return c, nil
	}
	c.Enough = true
	c.OnMeanUSD, c.OffMeanUSD = mean(on), mean(off)
	se := math.Sqrt(sampleVar(on)/float64(len(on)) + sampleVar(off)/float64(len(off)))
	c.Costlier = c.OnMeanUSD-c.OffMeanUSD > z95*se && c.OnMeanUSD > c.OffMeanUSD
	if c.Costlier {
		c.Text = "sessions with the tool on cost more than sessions with it off, beyond what chance explains; sessions differ, so review before concluding"
	} else {
		c.Text = "no clear cost increase between on and off sessions"
	}
	return c, nil
}

// EvaluateNetCost checks each named third-party tool and records a rollback
// event for one whose sessions cost more than without it. It does not change
// any agent config: the caller (internal/external) switches the tool off.
// Safe to call repeatedly: a tool with an active event is skipped until cleared.
func EvaluateNetCost(ctx context.Context, db *store.DB, m config.Measure, now time.Time, optimizers []string) ([]Event, error) {
	active, err := Overrides(ctx, db)
	if err != nil {
		return nil, err
	}
	var events []Event
	for _, name := range optimizers {
		if _, rolled := active[name]; rolled {
			continue
		}
		c, err := NetCostFor(ctx, db, name, m)
		if err != nil {
			return events, err
		}
		if c == nil || !c.Costlier {
			continue
		}
		ev := Event{At: now.UTC(), Optimizer: name, Rule: RuleNetCost,
			Observed: c.OnMeanUSD - c.OffMeanUSD, Threshold: 0, SampleOn: c.NOn, SampleRef: c.NOff, Reference: RefOffSessions,
			Reason: fmt.Sprintf("Trove switched %s off because sessions with it on cost $%.4f each on average against $%.4f without it (%d on, %d off sessions), so it was costing more than it saved. This is a local, statistical signal; review it with `trove report`, and re-enable it deliberately if you disagree.",
				name, c.OnMeanUSD, c.OffMeanUSD, c.NOn, c.NOff)}
		if err := recordRollback(ctx, db, ev); err != nil {
			return events, err
		}
		events = append(events, ev)
	}
	return events, nil
}
