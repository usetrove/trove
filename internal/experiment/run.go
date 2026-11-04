package experiment

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/store"
)

// Rules recorded in guardrail_events for rollbacks decided here.
const (
	RuleExperimentCost    = "experiment_cost"
	RuleExperimentOutcome = "experiment_outcome"
)

const reasonMarker = "your A/B comparison"

// Optimizers lists the optimizers that have A/B assignments.
func Optimizers(ctx context.Context, db *store.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT optimizer FROM experiment_assignments ORDER BY optimizer`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// Evaluate analyses every optimizer that has been in an experiment. It writes
// nothing; the same stored data always gives the same results. An optimizer
// that an earlier pass rolled back is shown as rolled back with that reason.
func Evaluate(ctx context.Context, db *store.DB, x config.Experiment, priors Priors) ([]Result, error) {
	names, err := Optimizers(ctx, db)
	if err != nil {
		return nil, err
	}
	rolled, err := measure.Overrides(ctx, db)
	if err != nil {
		return nil, err
	}
	out := make([]Result, 0, len(names))
	for _, n := range names {
		r, err := Analyze(ctx, db, n, x, priors)
		if err != nil {
			return nil, err
		}
		if why, back := rolled[n]; back && strings.Contains(why, reasonMarker) {
			r.Status, r.Reason = RolledBack, why
		}
		out = append(out, r)
	}
	return out, nil
}

// Step is the periodic pass: analyse, store the results, and roll back any
// optimizer whose own sessions show a measured cost rise or worse outcomes.
// Rolling back only lowers new sessions to shadow; running sessions keep their
// pinned state (CLAUDE.md rule 5). Failures lower, never raise, so Step cannot
// make an agent stop working. It returns the results and the rollbacks it made.
func Step(ctx context.Context, db *store.DB, x config.Experiment, priors Priors, now time.Time) ([]Result, []measure.Event, error) {
	if !x.Enabled {
		return nil, nil, nil
	}
	names, err := Optimizers(ctx, db)
	if err != nil {
		return nil, nil, err
	}
	rolled, err := measure.Overrides(ctx, db)
	if err != nil {
		return nil, nil, err
	}
	var results []Result
	var events []measure.Event
	for _, n := range names {
		r, err := Analyze(ctx, db, n, x, priors)
		if err != nil {
			return results, events, err
		}
		if err := db.Experiments().Put(ctx, now, r.rows()); err != nil {
			return results, events, err
		}
		if why, back := rolled[n]; back {
			if strings.Contains(why, reasonMarker) {
				r.Status, r.Reason = RolledBack, why
			}
			results = append(results, r)
			continue
		}
		if r.Status == CostsMore || r.Status == WorseOutcomes {
			ev := rollbackEvent(r, x, now)
			if err := measure.RecordRollback(ctx, db, ev); err != nil {
				return append(results, r), events, err
			}
			r.Status, r.Reason = RolledBack, ev.Reason
			events = append(events, ev)
		}
		results = append(results, r)
	}
	return results, events, nil
}

func rollbackEvent(r Result, x config.Experiment, now time.Time) measure.Event {
	ev := measure.Event{At: now.UTC(), Optimizer: r.Optimizer, Reference: ArmOff, SampleOn: r.NOn, SampleRef: r.NOff}
	if r.Status == CostsMore {
		ev.Rule, ev.Observed, ev.Threshold = RuleExperimentCost, r.Cost.Value, float64(x.CostIncreasePercent)/100
	} else {
		ev.Rule, ev.Threshold = RuleExperimentOutcome, float64(x.OutcomeWorsenPercent)/100
		for _, o := range r.Outcomes {
			if o.Low > 0 {
				ev.Observed = o.Value
				break
			}
		}
	}
	ev.Reason = fmt.Sprintf("Trove set %s back to shadow because in %s %s. Its shadow estimate cannot see this, because shadow mode cannot see extra turns, retries or re-reads. This is a local, statistical signal; review it with `trove report`, and re-enable it deliberately if you disagree.",
		r.Optimizer, reasonMarker, r.Reason)
	return ev
}

// rows turns a Result into the stored metadata rows (numbers only).
func (r Result) rows() []store.ExperimentRow {
	var out []store.ExperimentRow
	add := func(m MetricResult) {
		out = append(out, store.ExperimentRow{Optimizer: r.Optimizer, Metric: m.Metric, Value: m.Value, Low: m.Low, High: m.High,
			Confidence: m.Confidence, NOn: m.NOn, NOff: m.NOff, Label: string(m.Label), Status: string(r.Status), InputsHash: r.InputsHash})
	}
	if r.Cost != nil {
		add(*r.Cost)
	}
	for _, o := range r.Outcomes {
		add(o)
	}
	return out // empty while there is not enough data: nothing measured, nothing stored
}
