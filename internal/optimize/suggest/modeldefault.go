package suggest

import (
	"context"
	"fmt"

	"github.com/usetrove/trove/internal/experiment"
)

// KindModelDefault is a cheaper default model for an agent whose frontier-model work is
// mostly small tasks (plan item 7.5). It is the per-model roll-up of model_choice's
// per-turn findings and replaces that line when it fires.
const KindModelDefault = "model_default"

// Thresholds, the same as the model_default fix (internal/fixer/modeldefault.go), so the
// suggestion and the fix never disagree about when it is worth raising.
const (
	modelDefaultMinSmall = 20
	modelDefaultMinShare = 0.4
)

// PriorModelDefault is the benchmark prior a study registers for this suggestion
// (`recommends: {kind: optimizer, name: model_default}` in a bench study). Its cost change
// is a session A/B of the cheaper default against the frontier one.
const PriorModelDefault = "model_default"

// priorsFn returns the benchmark priors; tests replace it.
var priorsFn = experiment.DefaultPriors

type modelSpend struct {
	requests int
	usd      float64
}

// spendByModel is the measured spend per model in the window for interactive agents,
// from the gateway's own records.
func spendByModel(ctx context.Context, db Querier, from, to int64) (map[string]modelSpend, error) {
	rows, err := db.QueryContext(ctx, `SELECT model, COUNT(*), COALESCE(SUM(cost_usd), 0) FROM requests
		WHERE ts >= ? AND ts < ? AND agent IN (`+interactiveAgents+`) AND status_code BETWEEN 200 AND 299 GROUP BY model`, from, to)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]modelSpend{}
	for rows.Next() {
		var m string
		var s modelSpend
		if err := rows.Scan(&m, &s.requests, &s.usd); err != nil {
			return nil, err
		}
		out[m] = s
	}
	return out, rows.Err()
}

// modelDefaults turns model_choice's per-turn findings into a default-model suggestion
// where small tasks dominate a model's work. The money figure is always estimated. Its
// evidence is "benchmark" when a pre-registered study's prior supports the switch, and
// the saving then is the model's measured spend times the benchmark's cost change;
// otherwise it is the analyzer's per-turn estimate, labeled "estimated". The choices
// it replaces are removed from in.
func modelDefaults(ctx context.Context, db Querier, in []Suggestion, from, to int64) ([]Suggestion, []Suggestion, error) {
	spend, err := spendByModel(ctx, db, from, to)
	if err != nil {
		return nil, in, err
	}
	prior, hasPrior := priorsFn().Optimizers[PriorModelDefault]
	supported := false
	if hasPrior {
		supported, _ = prior.Supports()
	}
	var out, keep []Suggestion
	for _, c := range in {
		if c.Kind != KindModelChoice {
			keep = append(keep, c)
			continue
		}
		model, alt := c.from, c.alt
		total := spend[model]
		if c.Requests < modelDefaultMinSmall || total.requests == 0 || float64(c.Requests)/float64(total.requests) < modelDefaultMinShare {
			keep = append(keep, c)
			continue
		}
		share := float64(c.Requests) / float64(total.requests)
		s := Suggestion{
			Kind:  KindModelDefault,
			Title: fmt.Sprintf("Make %s the default instead of %s", alt, model),
			Detail: fmt.Sprintf("%.0f%% of %s's %d requests were small tasks (a small prompt and a small reply), and it cost $%.2f in this period (measured spend). "+
				"`trove fix` can set the default model, with a diff and an undo. Trove cannot check that %s handles your tasks as well, so try it first.",
				share*100, model, total.requests, total.usd, alt),
			Confidence: "low", Label: "estimated", Requests: total.requests,
		}
		if supported {
			usd := total.usd * -prior.CostChange
			s.EstSavingsUSD = &usd
			s.Confidence = "medium"
			s.EvidenceLabel = "benchmark"
			s.Detail += fmt.Sprintf(" A pre-registered benchmark (%s) measured a %.0f%% lower cost per session with the cheaper default, without lowering task success; the figure applies that to your measured spend.",
				prior.Source, -prior.CostChange*100)
		} else {
			s.EstSavingsUSD = c.EstSavingsUSD
			s.Scenario = "No benchmark result supports this switch yet, so the figure is the per-turn estimate for the small tasks only."
		}
		out = append(out, s)
	}
	return out, keep, nil
}
