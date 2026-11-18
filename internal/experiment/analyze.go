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

// The statuses of an experiment: what the data supports so far.
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
	MetricCost = "cost_per_session"
	// MetricCostPerTurn and MetricCostPerUncached are the primary cost figures: they do not move just because
	// a session ran longer or shorter. Cost per session stays as the secondary figure and still decides the verdict.
	MetricCostPerTurn     = "cost_per_turn"
	MetricCostPerUncached = "cost_per_uncached_input_token"
	// MetricTokens is the headline for a trial judged without priced sessions: tokens (input, cache and output)
	// per session. See AnalyzeHookTrial. The name only says "tokens"; it is not a credential.
	MetricTokens = "tokens_per_session" //nolint:gosec // G101: a metric name
	MetricTurns  = "turns"
	// MetricOutputPerTurn is output tokens per turn: what a rule set that asks for shorter replies should move.
	// It is reported, never used to decide a status, so it cannot change the verdict of any other optimizer.
	MetricOutputPerTurn = "output_tokens_per_turn" //nolint:gosec // G101: a metric name
	MetricToolErrs      = "tool_errors"
	MetricAbandoned     = "abandoned"
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
	// Adjusted is true when Value, Low and High are adjusted for Covariate (see docs/benchmark-methodology.md
	// section 4a). The Unadjusted fields always hold the plain figure, equal to the reported one when not adjusted.
	Adjusted        bool    `json:"adjusted"`
	Covariate       string  `json:"covariate,omitempty"`
	UnadjustedValue float64 `json:"unadjusted_value"`
	UnadjustedLow   float64 `json:"unadjusted_low"`
	UnadjustedHigh  float64 `json:"unadjusted_high"`
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

	// Cost is the headline metric. It is cost per session unless Headline says otherwise.
	Cost     *MetricResult `json:"cost,omitempty"`
	Headline string        `json:"headline,omitempty"`
	// CostPerTurn and CostPerUncached are measured on priced sessions only. Sessions with no turns, or no
	// uncached input tokens, are left out of the figure they cannot have, never counted as zero.
	CostPerTurn     *MetricResult `json:"cost_per_turn,omitempty"`
	CostPerUncached *MetricResult `json:"cost_per_uncached_input_token,omitempty"`
	// OutputPerTurn is output tokens per turn, on minus off (measured, provider-reported usage).
	OutputPerTurn *MetricResult  `json:"output_tokens_per_turn,omitempty"`
	Outcomes      []MetricResult `json:"outcomes,omitempty"`
	// TestFail is the share of sessions that ran an allowlisted test and ended with it failing, per arm. Set for
	// trials the hooks assign, which are the ones that see test runs.
	TestFail *TestFailResult `json:"test_fail_rate,omitempty"`

	// Confounders is set when the compared sessions mix those that started with a
	// known confounder and those that did not. It flags; it does not change the verdict.
	Confounders *ConfounderNote `json:"confounders,omitempty"`

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

// TestFailResult is the test_fail_rate of each arm (measured: exit codes the hooks saw). Sessions that ran no
// allowlisted test are not counted.
type TestFailResult struct {
	OnRate      float64       `json:"on_rate"`
	OffRate     float64       `json:"off_rate"`
	OnSessions  int           `json:"on_sessions"`
	OffSessions int           `json:"off_sessions"`
	Label       measure.Label `json:"label"`
}

// sess is one assigned, finished session.
type sess struct {
	key, arm, stratum string
	seq               int // position in the stratum's assignment order; seq/blockSize is the randomisation block
	cost, turns, errs float64
	abandoned         float64
	uncached          float64 // uncached input tokens over the session; 0 when none were recorded
	output            float64 // output tokens over the session
	// usd is the session's cost in dollars when every request in it was priced (hasUSD). For a gateway
	// comparison it equals cost; a hook trial's cost is tokens, so it carries dollars separately.
	usd    float64
	hasUSD bool
	// tags are the confounder tags pinned at session start (names only).
	tags []string
}

// ConfounderNote says a comparison mixes sessions that started with a known
// confounder (a brevity rule block, a skills listing, a third-party tool) and
// sessions that started without one. The effect shown may belong to the
// confounder rather than to the optimizer.
type ConfounderNote struct {
	Tagged   int      `json:"tagged"`
	Untagged int      `json:"untagged"`
	Tags     []string `json:"tags"`
	Note     string   `json:"note"`
}

// mixedConfounders returns a note when sessions with and without confounder tags
// are compared together, and nil otherwise.
func mixedConfounders(all []sess) *ConfounderNote {
	n := ConfounderNote{}
	seen := map[string]bool{}
	for _, s := range all {
		if len(s.tags) == 0 {
			n.Untagged++
			continue
		}
		n.Tagged++
		for _, t := range s.tags {
			if !seen[t] {
				seen[t] = true
				n.Tags = append(n.Tags, t)
			}
		}
	}
	if n.Tagged == 0 || n.Untagged == 0 {
		return nil
	}
	sort.Strings(n.Tags)
	n.Note = fmt.Sprintf("mixed comparison: %d of %d sessions started with %s present and %d without, so part of the difference may come from that and not from this optimizer",
		n.Tagged, n.Tagged+n.Untagged, strings.ReplaceAll(strings.Join(n.Tags, ", "), "_", " "), n.Untagged)
	return &n
}

// load reads the sessions of one optimizer's experiment. Only sessions that
// really ran the way they were assigned are kept: an "on" session must have been
// pinned on (not held at shadow by the baseline or a rollback), an "off"
// session must not. Sessions with unpriced requests are left out of cost
// rather than guessed. Only assignments after the last time the user cleared a
// rollback count.
func load(ctx context.Context, db *store.DB, optimizer string, notBeforeMS int64) (kept []sess, excluded, unpriced int, err error) {
	var since sql.NullInt64
	if err = db.QueryRowContext(ctx, `SELECT MAX(cleared_at) FROM guardrail_events WHERE optimizer = ? AND cleared_at IS NOT NULL`, optimizer).Scan(&since); err != nil {
		return nil, 0, 0, err
	}
	rows, err := db.QueryContext(ctx, `SELECT a.session_key, a.arm, a.stratum, a.seq, s.total_cost_usd, s.total_input_uncached_tokens, s.total_output_tokens, s.outcome_signals, p.states, p.confounders,
		(SELECT COUNT(*) FROM requests r WHERE r.session_key = a.session_key AND r.cost_usd IS NULL)
		FROM experiment_assignments a
		JOIN sessions s ON s.session_key = a.session_key
		JOIN session_pins p ON p.session_key = a.session_key
		WHERE a.optimizer = ? AND a.assigned_at > ? AND s.outcome_signals IS NOT NULL
		ORDER BY a.session_key`, optimizer, max(since.Int64, notBeforeMS))
	if err != nil {
		return nil, 0, 0, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var s sess
		var sig, states string
		var nulls int
		var conf sql.NullString
		if err = rows.Scan(&s.key, &s.arm, &s.stratum, &s.seq, &s.cost, &s.uncached, &s.output, &sig, &states, &conf, &nulls); err != nil {
			return nil, 0, 0, err
		}
		if conf.Valid {
			_ = json.Unmarshal([]byte(conf.String), &s.tags) // an unreadable tag list counts as none
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
		s.usd, s.hasUSD = s.cost, true
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

// split groups the picked value, with its prior-cost covariate, by arm and stratum. A non-nil keep drops the
// sessions it rejects.
func split(all []sess, pick func(sess) float64, keep func(sess) bool) (on, off pairsByStratum, nOn, nOff int) {
	on, off = pairsByStratum{}, pairsByStratum{}
	x, has := priorCovariate(all, pick, keep)
	for i, s := range all {
		if keep != nil && !keep(s) {
			continue
		}
		p := pair{y: pick(s), x: x[i], has: has[i]}
		if s.arm == ArmOn {
			on[s.stratum] = append(on[s.stratum], p)
			nOn++
		} else {
			off[s.stratum] = append(off[s.stratum], p)
			nOff++
		}
	}
	return
}

func seedFor(base int64, optimizer, metric string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(optimizer + "\x00" + metric))
	return base ^ int64(h.Sum64()) //nolint:gosec // G115: a wrapping hash-to-seed conversion is intended
}

func hashInputs(optimizer string, all []sess, x config.Experiment) string {
	lines := make([]string, 0, len(all))
	for _, s := range all {
		lines = append(lines, fmt.Sprintf("%s|%s|%s|%d|%.9f|%g|%g|%g|%g|%g|%.9f|%t", s.key, s.arm, s.stratum, s.seq, s.cost, s.turns, s.errs, s.abandoned, s.uncached, s.output, s.usd, s.hasUSD))
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d|%d|%d\n%s", optimizer, x.Seed, x.MinSessionsPerArm, x.CostIncreasePercent, x.OutcomeWorsenPercent, strings.Join(lines, "\n"))))
	return hex.EncodeToString(sum[:8])
}

// Analyze compares sessions with the optimizer on against sessions with it in
// shadow. It is a pure function of the stored metadata and the config, so the
// same data always gives the same answer (the bootstrap is seeded).
func Analyze(ctx context.Context, db *store.DB, optimizer string, x config.Experiment, priors Priors) (Result, error) {
	return AnalyzeSince(ctx, db, optimizer, x, priors, 0)
}

// AnalyzeSince is Analyze counting only sessions assigned after notBeforeMS (Unix milliseconds). A trial that
// starts later passes its own start, so sessions from an earlier trial of the same optimizer are not pooled in.
func AnalyzeSince(ctx context.Context, db *store.DB, optimizer string, x config.Experiment, priors Priors, notBeforeMS int64) (Result, error) {
	all, excluded, unpriced, err := load(ctx, db, optimizer, notBeforeMS)
	if err != nil {
		return Result{}, err
	}
	return analyzeAll(ctx, db, optimizer, x, priors, all, excluded, unpriced, MetricCost)
}

// analyzeAll compares the given sessions. headline is the metric that decides "saves" and "costs more".
func analyzeAll(ctx context.Context, db *store.DB, optimizer string, x config.Experiment, priors Priors, all []sess, excluded, unpriced int, headline string) (Result, error) {
	res := Result{Optimizer: optimizer, Excluded: excluded, Unpriced: unpriced, OverheadLabel: string(measure.Estimated),
		InputsHash: hashInputs(optimizer, all, x), Headline: headline, Confounders: mixedConfounders(all)}
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
		keep     func(sess) bool // nil keeps every session
		adjust   bool            // a cost metric: adjusted for prior cost when the pre-registered rule allows
	}
	specs := []spec{
		{headline, true, func(s sess) float64 { return s.cost }, nil, headline == MetricCost},
		{MetricCostPerTurn, true, func(s sess) float64 { return s.usd / s.turns }, func(s sess) bool { return s.hasUSD && s.turns > 0 }, true},
		{MetricCostPerUncached, true, func(s sess) float64 { return s.cost / s.uncached }, func(s sess) bool { return s.uncached > 0 }, true},
		{MetricOutputPerTurn, true, func(s sess) float64 { return s.output / s.turns }, func(s sess) bool { return s.turns > 0 }, false},
		{MetricTurns, true, func(s sess) float64 { return s.turns }, nil, false},
		{MetricToolErrs, true, func(s sess) float64 { return s.errs }, nil, false},
		{MetricAbandoned, false, func(s sess) float64 { return s.abandoned }, nil, false},
	}
	for _, sp := range specs {
		if headline != MetricCost && (sp.metric == MetricAbandoned || sp.metric == MetricCostPerUncached) {
			continue // sessions not seen by the gateway have no source for these, and a zero would be invented
		}
		onP, offP, nOn, nOff := split(all, sp.pick, sp.keep)
		on, off := values(onP), values(offP)
		seed := seedFor(x.Seed, optimizer, sp.metric)
		est, lo, hi, used, ok := bootstrap(on, off, minStratum, sp.relative, seed)
		if !ok {
			continue
		}
		kind := "relative"
		if !sp.relative {
			kind = "points"
		}
		mr := MetricResult{Metric: sp.metric, Kind: kind, Value: est, Low: lo, High: hi, Label: measure.Measured,
			NOn: nOn, NOff: nOff, Strata: used, UnadjustedValue: est, UnadjustedLow: lo, UnadjustedHigh: hi}
		if sp.adjust {
			keys, w := strata(on, off, minStratum)
			if cupedUsable(onP, offP, keys) {
				if aest, alo, ahi, aok := bootstrapCUPED(onP, offP, keys, w, sp.relative, seed); aok {
					mr.Value, mr.Low, mr.High, mr.Adjusted, mr.Covariate = aest, alo, ahi, true, CovariatePriorCost
				}
			}
		}
		mr.Confidence = confidence(mr.Low, mr.High, min(nOn, nOff), minPer)
		switch sp.metric {
		case headline:
			c := mr
			res.Cost = &c
		case MetricCostPerTurn:
			res.CostPerTurn = &mr
		case MetricCostPerUncached:
			res.CostPerUncached = &mr
		case MetricOutputPerTurn:
			res.OutputPerTurn = &mr
		default:
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
	more, less, what := "cost %s more", "cost %s less", "cost"
	if res.Headline == MetricTokens {
		more, less, what = "use %s more tokens", "use %s fewer tokens", "tokens used"
	}
	switch {
	case c.Low > 0 && c.Value >= costLimit:
		res.Status = CostsMore
		res.Reason = fmt.Sprintf("with it on, sessions "+more+" (measured, 95%% range %s to %s) than the same kind of sessions without it", pct(c.Value), pct(c.Low), pct(c.High))
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
			res.Reason = fmt.Sprintf("with it on, sessions "+less+" (measured, 95%% range %s to %s); %s", pct(-c.Value), pct(-c.High), pct(-c.Low), measure.TextNoRegression)
		default:
			res.Status = NoClearEffect
			res.Reason = fmt.Sprintf("no clear difference in "+what+" yet (measured change %s, 95%% range %s to %s)", signed(c.Value), signed(c.Low), signed(c.High))
		}
	}
}

func pct(v float64) string    { return fmt.Sprintf("%.1f%%", v*100) }
func signed(v float64) string { return fmt.Sprintf("%+.1f%%", v*100) }
