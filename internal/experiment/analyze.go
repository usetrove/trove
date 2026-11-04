package experiment

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"sort"
	"strings"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/store"
)

// Status says what the A/B data shows for one optimizer.
type Status string

const (
	NotEnoughData Status = "not_enough_data"
	NoClearEffect Status = "no_clear_effect"
	Saves         Status = "saves"
	CostsMore     Status = "costs_more"
	WorseOutcomes Status = "worse_outcomes"
	RolledBack    Status = "rolled_back"
)

// Metric names.
const (
	MetricCost      = "cost_per_session"
	MetricTurns     = "turns"
	MetricToolErrs  = "tool_errors"
	MetricAbandoned = "abandoned"
)

// abandonedPointsLimit is how many percentage points more sessions may end
// abandoned after an action before the optimizer is rolled back (a fraction).
const abandonedPointsLimit = 0.10

// MetricResult is one measured comparison, on minus off.
type MetricResult struct {
	Metric string `json:"metric"`
	// Kind is "relative" (0.05 = 5% higher with the optimizer on) or "points"
	// (0.05 = 5 percentage points more sessions).
	Kind       string        `json:"kind"`
	Value      float64       `json:"value"`
	Low        float64       `json:"low"`  // 95% range
	High       float64       `json:"high"` // 95% range
	Label      measure.Label `json:"label"`
	Confidence string        `json:"confidence"`
	NOn        int           `json:"n_on"`
	NOff       int           `json:"n_off"`
	Strata     int           `json:"strata"`
}

// Result is everything known about one optimizer's A/B comparison.
type Result struct {
	Optimizer string `json:"optimizer"`
	Status    Status `json:"status"`
	NOn       int    `json:"n_on"`
	NOff      int    `json:"n_off"`
	// Excluded counts assigned sessions left out because they were not really
	// run that way (baseline, rollback) or had unpriced requests.
	Excluded int `json:"excluded"`
	Unpriced int `json:"unpriced"`

	Cost     *MetricResult  `json:"cost,omitempty"`
	Outcomes []MetricResult `json:"outcomes,omitempty"`

	// Combined shrinks the cost estimate toward the benchmark prior, if there is
	// one. Rollback never uses it: a prior must not hide a loss on your own data.
	Combined *Combined `json:"combined_with_benchmark,omitempty"`
	PriorSrc string    `json:"benchmark_source,omitempty"`

	// OverheadTokensPerSession is what the optimizer itself adds to a request
	// (declared by the optimizer). It is already inside the measured cost.
	OverheadTokensPerSession float64 `json:"overhead_tokens_per_session"`
	OverheadLabel            string  `json:"overhead_label"`

	Suggest       bool   `json:"suggest_on"`
	SuggestReason string `json:"suggest_reason,omitempty"`
	Reason        string `json:"reason"`
	InputsHash    string `json:"inputs_hash"`
}

// sess is one assigned, finished session.
type sess struct {
	key, arm, stratum string
	cost, turns, errs float64
	abandoned         float64
	overhead          float64
}

// load reads the sessions of one optimizer's experiment. Only sessions that
// really ran the way they were assigned are kept: an "on" session must have been
// pinned on (not held at shadow by the baseline or a rollback), an "off"
// session must not. Sessions with unpriced requests are left out of cost
// rather than guessed. Only assignments after the last time the user cleared a
// rollback count.
func load(ctx context.Context, db *store.DB, optimizer string) (kept []sess, excluded, unpriced int, err error) {
	var since sql.NullInt64
	if err = db.QueryRowContext(ctx, `SELECT MAX(cleared_at) FROM guardrail_events WHERE optimizer = ? AND cleared_at IS NOT NULL`, optimizer).Scan(&since); err != nil {
		return nil, 0, 0, err
	}
	rows, err := db.QueryContext(ctx, `SELECT a.session_key, a.arm, a.stratum, s.total_cost_usd, s.outcome_signals, p.states,
		(SELECT COUNT(*) FROM requests r WHERE r.session_key = a.session_key AND r.cost_usd IS NULL)
		FROM experiment_assignments a
		JOIN sessions s ON s.session_key = a.session_key
		JOIN session_pins p ON p.session_key = a.session_key
		WHERE a.optimizer = ? AND a.assigned_at > ? AND s.outcome_signals IS NOT NULL
		ORDER BY a.session_key`, optimizer, since.Int64)
	if err != nil {
		return nil, 0, 0, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var s sess
		var sig, states string
		var nulls int
		if err = rows.Scan(&s.key, &s.arm, &s.stratum, &s.cost, &sig, &states, &nulls); err != nil {
			return nil, 0, 0, err
		}
		var ss measure.SessionSignals
		var pins map[string]string
		if json.Unmarshal([]byte(sig), &ss) != nil || json.Unmarshal([]byte(states), &pins) != nil {
			excluded++
			continue
		}
		if (s.arm == ArmOn) != (pins[optimizer] == config.StateOn) {
			excluded++
			continue
		}
		if nulls > 0 {
			unpriced++
			continue
		}
		s.turns, s.errs = float64(ss.Turns), float64(ss.ToolErrors)
		if ss.Abandoned {
			s.abandoned = 1
		}
		kept = append(kept, s)
	}
	if err = rows.Err(); err != nil {
		return nil, 0, 0, err
	}
	return kept, excluded, unpriced, nil
}

// overheadPerSession is the mean overhead tokens the optimizer declared per
// "on" session, from the stored per-request records.
func overheadPerSession(ctx context.Context, db *store.DB, optimizer string, onKeys map[string]bool) float64 {
	if len(onKeys) == 0 {
		return 0
	}
	rows, err := db.QueryContext(ctx, `SELECT r.session_key, r.optimizations_applied FROM requests r
		JOIN experiment_assignments a ON a.session_key = r.session_key AND a.optimizer = ? AND a.arm = 'on'
		WHERE r.optimizations_applied IS NOT NULL`, optimizer)
	if err != nil {
		return 0
	}
	defer func() { _ = rows.Close() }()
	var total float64
	for rows.Next() {
		var key, raw string
		if rows.Scan(&key, &raw) != nil || !onKeys[key] {
			continue
		}
		var ap struct {
			Optimizers []struct {
				Name           string `json:"name"`
				State          string `json:"state"`
				OverheadTokens int    `json:"overhead_tokens"`
			} `json:"optimizers"`
		}
		if json.Unmarshal([]byte(raw), &ap) != nil {
			continue
		}
		for _, o := range ap.Optimizers {
			if o.Name == optimizer && o.State == config.StateOn {
				total += float64(o.OverheadTokens)
			}
		}
	}
	return total / float64(len(onKeys))
}

func split(all []sess, pick func(sess) float64) (on, off byStratum, nOn, nOff int) {
	on, off = byStratum{}, byStratum{}
	for _, s := range all {
		if s.arm == ArmOn {
			on[s.stratum] = append(on[s.stratum], pick(s))
			nOn++
		} else {
			off[s.stratum] = append(off[s.stratum], pick(s))
			nOff++
		}
	}
	return
}

func seedFor(base int64, optimizer, metric string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(optimizer + "\x00" + metric))
	return base ^ int64(h.Sum64())
}

func hashInputs(optimizer string, all []sess, x config.Experiment) string {
	lines := make([]string, 0, len(all))
	for _, s := range all {
		lines = append(lines, fmt.Sprintf("%s|%s|%s|%.9f|%g|%g|%g", s.key, s.arm, s.stratum, s.cost, s.turns, s.errs, s.abandoned))
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d|%d|%d\n%s", optimizer, x.Seed, x.MinSessionsPerArm, x.CostIncreasePercent, x.OutcomeWorsenPercent, strings.Join(lines, "\n"))))
	return hex.EncodeToString(sum[:8])
}

// Analyze compares sessions with the optimizer on against sessions with it in
// shadow. It is a pure function of the stored metadata and the config, so the
// same data always gives the same answer (the bootstrap is seeded).
func Analyze(ctx context.Context, db *store.DB, optimizer string, x config.Experiment, priors Priors) (Result, error) {
	all, excluded, unpriced, err := load(ctx, db, optimizer)
	if err != nil {
		return Result{}, err
	}
	res := Result{Optimizer: optimizer, Excluded: excluded, Unpriced: unpriced, OverheadLabel: string(measure.Estimated),
		InputsHash: hashInputs(optimizer, all, x)}
	for _, s := range all {
		if s.arm == ArmOn {
			res.NOn++
		} else {
			res.NOff++
		}
	}
	minPer := x.MinSessionsPerArm
	if res.NOn < minPer || res.NOff < minPer {
		res.Status = NotEnoughData
		res.Reason = fmt.Sprintf("%s: %d sessions with it on and %d with it off so far; each side needs at least %d", measure.TextNotEnough, res.NOn, res.NOff, minPer)
		return res, nil
	}
	// A stratum needs enough sessions on both sides to be used; a small
	// stratum is dropped, never padded.
	minStratum := minPer
	if minStratum > 10 {
		minStratum = 10
	}

	type spec struct {
		metric   string
		relative bool
		pick     func(sess) float64
	}
	specs := []spec{
		{MetricCost, true, func(s sess) float64 { return s.cost }},
		{MetricTurns, true, func(s sess) float64 { return s.turns }},
		{MetricToolErrs, true, func(s sess) float64 { return s.errs }},
		{MetricAbandoned, false, func(s sess) float64 { return s.abandoned }},
	}
	for _, sp := range specs {
		on, off, nOn, nOff := split(all, sp.pick)
		est, lo, hi, used, ok := bootstrap(on, off, minStratum, sp.relative, seedFor(x.Seed, optimizer, sp.metric))
		if !ok {
			continue
		}
		kind := "relative"
		if !sp.relative {
			kind = "points"
		}
		mr := MetricResult{Metric: sp.metric, Kind: kind, Value: est, Low: lo, High: hi, Label: measure.Measured,
			Confidence: confidence(lo, hi, min(nOn, nOff), minPer), NOn: nOn, NOff: nOff, Strata: used}
		if sp.metric == MetricCost {
			c := mr
			res.Cost = &c
		} else {
			res.Outcomes = append(res.Outcomes, mr)
		}
	}
	if res.Cost == nil {
		res.Status = NotEnoughData
		res.Reason = measure.TextNotEnough + ": too few sessions in each group (by repo and task type) to compare like with like"
		return res, nil
	}
	onKeys := map[string]bool{}
	for _, s := range all {
		if s.arm == ArmOn {
			onKeys[s.key] = true
		}
	}
	res.OverheadTokensPerSession = overheadPerSession(ctx, db, optimizer, onKeys)

	if p, has := priors.Optimizers[optimizer]; has {
		c := combine(res.Cost.Value, res.Cost.Low, res.Cost.High, p)
		res.Combined, res.PriorSrc = &c, p.Source
		if ok, why := p.Supports(); ok {
			res.Suggest, res.SuggestReason = true, why
		} else {
			res.SuggestReason = why
		}
	} else {
		res.SuggestReason = "no benchmark evidence for this optimizer yet, so Trove will not suggest turning it on"
	}
	decide(&res, x)
	return res, nil
}

// decide sets the status and the plain-English reason from the user's own data.
func decide(res *Result, x config.Experiment) {
	c := res.Cost
	costLimit := float64(x.CostIncreasePercent) / 100
	outLimit := float64(x.OutcomeWorsenPercent) / 100
	switch {
	case c.Low > 0 && c.Value >= costLimit:
		res.Status = CostsMore
		res.Reason = fmt.Sprintf("with it on, sessions cost %s more (measured, 95%% range %s to %s) than the same kind of sessions without it", pct(c.Value), pct(c.Low), pct(c.High))
	default:
		for _, o := range res.Outcomes {
			limit := outLimit
			if o.Kind == "points" {
				limit = abandonedPointsLimit
			}
			if o.Low > 0 && o.Value >= limit {
				res.Status = WorseOutcomes
				res.Reason = fmt.Sprintf("with it on, %s is higher (measured, %s, 95%% range %s to %s)", strings.ReplaceAll(o.Metric, "_", " "), pct(o.Value), pct(o.Low), pct(o.High))
				return
			}
		}
		switch {
		case c.High < 0:
			res.Status = Saves
			res.Reason = fmt.Sprintf("with it on, sessions cost %s less (measured, 95%% range %s to %s); %s", pct(-c.Value), pct(-c.High), pct(-c.Low), measure.TextNoRegression)
		default:
			res.Status = NoClearEffect
			res.Reason = fmt.Sprintf("no clear difference in cost yet (measured change %s, 95%% range %s to %s)", signed(c.Value), signed(c.Low), signed(c.High))
		}
	}
}

func pct(v float64) string    { return fmt.Sprintf("%.1f%%", v*100) }
func signed(v float64) string { return fmt.Sprintf("%+.1f%%", v*100) }
