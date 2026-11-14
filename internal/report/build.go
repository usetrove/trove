package report

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/usetrove/trove/internal/analyze"
	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/external"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/optimize"
	"github.com/usetrove/trove/internal/optimize/suggest"
	"github.com/usetrove/trove/internal/pricing"
	"github.com/usetrove/trove/internal/recommend"
	"github.com/usetrove/trove/internal/store"
)

// Inputs are what Build needs besides the database.
type Inputs struct {
	Config config.Config
	Now    time.Time
	// Prices may be nil; suggestions that need prices are then skipped.
	Prices *pricing.Table
	// KillSwitch is true when the user stopped every optimizer.
	KillSwitch bool
	// HistoryPlanEvidence names the local signals behind the detected plan (names
	// only, never values). It is shown with history figures.
	HistoryPlanEvidence []string
	// SchemaTokens is the estimated size of the Trove tool definitions; 0 when
	// unknown.
	SchemaTokens int
	// Recoverable is true when the Trove MCP server is registered, so trimmed
	// output can be fetched back and output_trimmer may be on.
	Recoverable bool
	// ExternalManifests describes installed third-party tools; nil means the
	// manifests built into this binary.
	ExternalManifests []external.Manifest
	// RepoKey identifies the repo the report is for (features.RepoKey), or "" when
	// unknown. Task is the task type the user named, or "". Both only steer the
	// local recommender; neither is printed.
	RepoKey string
	Task    string
	// Provider supplies the recommender's suggestions. Nil means the open local
	// one; a hosted provider (opt-in, later) is composed in front by the caller.
	Providers []recommend.Provider
}

// Build assembles the report for p. It reads only through the measurement layer.
func Build(ctx context.Context, db *store.DB, in Inputs, p Period) (Report, error) {
	w := p.Window()
	r := newReport(p, in.KillSwitch)

	byAgent, err := measure.TotalsBy(ctx, db, w, measure.ByAgent)
	if err != nil {
		return r, err
	}
	byModel, err := measure.TotalsBy(ctx, db, w, measure.ByModel)
	if err != nil {
		return r, err
	}
	sortByCost(byAgent)
	sortByCost(byModel)
	hist, err := loadHistory(ctx, db, w, in)
	if err != nil {
		return r, err
	}
	byAgent, byModel = hist.mergeSpend(byAgent, byModel)
	r.Spend = Spend{ByAgent: byAgent, ByModel: byModel}
	if r.Spend.ByKind, err = kindSpend(ctx, db, w); err != nil {
		return r, err
	}
	if r.Upstreams, err = measure.UpstreamStats(ctx, db, w, in.Config); err != nil {
		return r, err
	}
	r.Headline.Spend, r.Headline.Tokens, r.Requests = sumTotals(byAgent)
	if hist != nil {
		r.History = &hist.h
		r.Requests += hist.otherReqs
		r.Headline.Tokens.Value += hist.otherTok
		r.Headline.Tokens.N = r.Requests
		if hist.otherUSD.N+hist.otherUSD.Unpriced > 0 {
			v := hist.otherUSD
			r.Headline.APIEquivalent = &v
		}
	}

	var ever int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT 1 FROM requests UNION ALL SELECT 1 FROM history_requests LIMIT 1)`).Scan(&ever); err != nil {
		return r, err
	}
	switch {
	case ever == 0:
		r.State, r.Notes = StateEmpty, emptyNotes()
	case r.Requests == 0:
		r.State = StateQuiet
		r.Notes = []string{"No requests were recorded " + p.Label() + ". Try `trove report --month`, or check that `trove gateway` is running."}
	case r.Requests < SmallSample:
		r.State = StateSmall
		r.Notes = []string{fmt.Sprintf("Only %d requests so far, so these numbers are rough. They firm up as you use your agent.", r.Requests)}
	default:
		r.State = StateOK
	}

	if r.Hooks, err = measure.HookTotals(ctx, db, p.From); err != nil {
		return r, err
	}
	if r.Adapters, err = buildAdapters(ctx, db, p.From); err != nil {
		return r, err
	}
	if r.Adapters != nil && (r.State == StateEmpty || r.State == StateQuiet) {
		// The default advice here is about the gateway (`trove setup`, `trove gateway`),
		// which is wrong for someone using a plugin, possibly with a subscription.
		r.Notes = []string{"Nothing has gone through the Trove gateway " + p.Label() + ". Below is what the plugin for your coding tool saw."}
	}

	// Budget pacing is shown even when this period is quiet.
	pace, err := BuildPacing(ctx, db, in.Config, in.Now)
	if err != nil {
		return r, err
	}

	if r.State == StateEmpty || r.State == StateQuiet {
		r.Budget = pace
		return r, nil
	}

	waste, err := measure.WasteTotalIn(ctx, db, w)
	if err != nil {
		return r, err
	}
	if hist != nil {
		waste = addWaste(waste, hist.waste)
	}
	r.Headline.Waste = waste.USD
	base := r.Headline.Spend.Value
	if r.Headline.APIEquivalent != nil {
		base += r.Headline.APIEquivalent.Value
	}
	r.Headline.WastePercent = measure.Figure{Label: measure.Estimated, N: waste.USD.N, Value: percent(waste.USD.Value, base)}

	savings, err := measure.SavingsByOptimizer(ctx, db, w, in.Config.Measure)
	if err != nil {
		return r, err
	}
	r.Headline.Savings = sumSavings(savings)
	r.Headline.MeasuredNote = measuredNote(savings)

	cats, err := measure.WasteByCategory(ctx, db, w)
	if err != nil {
		return r, err
	}
	if hist != nil {
		cats = mergeWasteCats(cats, hist.cats)
	}
	rules := measure.DefaultOfferRules
	rules.Recoverable = in.Recoverable
	offers, err := measure.Offers(ctx, db, w, in.Config, rules, in.Now)
	if err != nil {
		return r, err
	}
	if r.Optimizers, err = buildOptimizers(ctx, db, in, w, savings, offers); err != nil {
		return r, err
	}
	r.Leaks = buildLeaks(cats, in.Config, offers, r.Optimizers.Baseline != nil)

	r.Budget = pace

	if r.ExternalTools, err = buildExternal(ctx, db, in); err != nil {
		return r, err
	}
	if r.Experiments, err = buildExperiments(ctx, db, in); err != nil {
		return r, err
	}

	sugg, err := suggest.Generate(ctx, db, in.Prices, p.From, p.To)
	if err != nil {
		return r, err
	}
	for _, s := range sugg {
		r.Recommendations = append(r.Recommendations, Recommendation{Suggestion: s})
	}
	r.Recommendations = append(r.Recommendations, recommenderSuggestions(ctx, db, in)...)
	if r.Fixes, err = measure.FixEffects(ctx, db, in.Config.Measure); err != nil {
		return r, err
	}
	if r.SettingsProfiles, err = measure.SettingsEffects(ctx, db, in.Config.Measure); err != nil {
		return r, err
	}
	if r.Tasks, err = measure.TasksEffects(ctx, db, in.Config.Measure); err != nil {
		return r, err
	}
	ad, err := measure.AdoptionFor(ctx, db, w, in.Config.Measure)
	if err != nil {
		return r, err
	}
	if in.Recoverable || ad.Any() {
		r.Tools = &Tools{Registered: in.Recoverable, Adoption: ad}
		if in.SchemaTokens > 0 {
			r.Tools.SchemaTokens = &measure.Figure{Value: float64(in.SchemaTokens), Label: measure.Estimated}
		}
	}
	return r, nil
}

func newReport(p Period, killSwitch bool) Report {
	est := measure.Figure{Label: measure.Estimated}
	return Report{
		Period:   PeriodInfo{Name: p.Name, Label: p.Label(), From: day(p.From), To: day(p.To.Add(-time.Millisecond))},
		Headline: Headline{Spend: est, Tokens: measure.Figure{Label: measure.Measured}, Waste: est, WastePercent: est, Savings: est},
		Leaks:    []Leak{}, Recommendations: []Recommendation{},
		Spend:      Spend{ByAgent: []measure.TotalsRow{}, ByModel: []measure.TotalsRow{}},
		Optimizers: Optimizers{KillSwitch: killSwitch, Rows: []OptimizerRow{}, Guardrails: []GuardrailEvent{}},
	}
}

func emptyNotes() []string {
	return []string{
		"Trove has not recorded any requests yet.",
		"1. Run `trove setup` to point your coding agent at Trove.",
		"2. Start the gateway with `trove gateway` and use your agent as usual.",
		"3. Run `trove` again. Your first numbers show up after a few requests.",
	}
}

// EmptyReport is the report for a data directory that has no database yet. It
// lets `trove` explain what to do without creating any files.
func EmptyReport(p Period) Report {
	r := newReport(p, false)
	r.State, r.Notes = StateEmpty, emptyNotes()
	return r
}

func sortByCost(rows []measure.TotalsRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].CostUSD.Value != rows[j].CostUSD.Value {
			return rows[i].CostUSD.Value > rows[j].CostUSD.Value
		}
		return rows[i].Key < rows[j].Key
	})
}

// sumTotals adds the rows into the headline spend (estimated), the token total
// (measured) and the request count.
func sumTotals(rows []measure.TotalsRow) (spend, tokens measure.Figure, requests int) {
	var usd float64
	var tok int64
	var unpriced, priced int
	for _, r := range rows {
		usd += r.CostUSD.Value
		priced += r.CostUSD.N
		unpriced += r.CostUSD.Unpriced
		tok += r.InputTokens + r.CacheWriteTokens + r.CacheReadTokens + r.OutputTokens
		requests += r.Requests
	}
	spend = measure.Figure{Value: round6(usd), Label: measure.Estimated, N: priced, Unpriced: unpriced}
	tokens = measure.Figure{Value: float64(tok), Label: measure.Measured, N: requests}
	return spend, tokens, requests
}

func sumSavings(rows []measure.OptimizerSavings) measure.Figure {
	f := measure.Figure{Label: measure.Estimated}
	for _, r := range rows {
		f.Value += r.USD.Value
		f.N += r.USD.N
		f.Unpriced += r.USD.Unpriced
	}
	f.Value = round6(f.Value)
	return f
}

// measuredNote says what is measured about savings. Dollars are always
// estimated; the only measured evidence is the on-versus-off token comparison.
func measuredNote(rows []measure.OptimizerSavings) string {
	for _, r := range rows {
		c := r.Corroboration
		if c != nil && c.Reduced && c.OffMean > 0 {
			return fmt.Sprintf("sessions with %s on sent %.0f%% fewer prompt tokens per request than sessions with it off (%d on, %d off). Sessions differ, so this supports the saving but does not prove it.",
				r.Optimizer, 100*(c.OffMean-c.OnMean)/c.OffMean, c.NOn, c.NOff)
		}
	}
	return measure.TextNotEnough + " to compare sessions with and without an optimizer. The dollar savings above are estimates."
}

func percent(part, whole float64) float64 {
	if whole <= 0 {
		return 0
	}
	return 100 * part / whole
}

func round6(v float64) float64 { return math.Round(v*1e6) / 1e6 }

// leakInfo is how each detector reads to a user and what to do about it.
type leakInfo struct {
	title     string
	optimizer string // the optimizer that removes it, if any
	advice    string // used when there is no optimizer command
	command   string // used when there is no optimizer
}

var leakInfos = map[string]leakInfo{
	analyze.DetDuplicate: {title: "Repeated tool results", optimizer: analyze.OptDedupe},
	analyze.DetNoisy:     {title: "Noisy tool output", optimizer: analyze.OptNoise},
	analyze.DetOversized: {title: "Oversized tool output", optimizer: analyze.OptTruncate},
	analyze.DetUnusedTools: {title: "Tool definitions never used", optimizer: analyze.OptPrune,
		advice: "Remove tool servers you do not use from your agent's configuration."},
	analyze.DetContextFiles: {title: "Heavy context files",
		advice: "Shorten the context files your agent loads every turn (for example CLAUDE.md or AGENTS.md)."},
	analyze.DetExpensive: {title: "Large model on small tasks", command: "trove optimize suggest",
		advice: "Use a smaller model for small tasks."},
	analyze.DetDrift: {title: "Long sessions that drift",
		advice: "Start a fresh session when the task changes."},
	analyze.DetCacheMiss: {title: "Prompt cache misses",
		advice: "Cache misses happen when the start of a request changes between turns. Avoid editing early context mid-session."},
}

const maxLeaks = 5

// LeakTitle is the plain-English name of a waste category. Only the fixed names
// in the table are known; anything else returns false, so a caller that must not
// show anything identifying (the share card) can rely on it.
func LeakTitle(category string) (string, bool) {
	info, ok := leakInfos[category]
	return info.title, ok
}

func buildLeaks(cats []measure.WasteRow, cfg config.Config, offers []measure.Offer, baseline bool) []Leak {
	offer := map[string]measure.Offer{}
	for _, o := range offers {
		offer[o.Optimizer] = o
	}
	var out []Leak
	for _, c := range cats {
		if c.Tokens.Value <= 0 && c.USD.Value <= 0 {
			continue
		}
		info, ok := leakInfos[c.Category]
		if !ok {
			info = leakInfo{title: c.Category, advice: "Run `trove optimize suggest` for ideas."}
		}
		l := Leak{Category: c.Category, Title: info.title, USD: c.USD, Tokens: c.Tokens, Command: info.command, Advice: info.advice}
		if info.optimizer != "" {
			l.Command, l.Advice = optimizerAction(info, cfg, offer, baseline)
		}
		out = append(out, l)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].USD.Value != out[j].USD.Value {
			return out[i].USD.Value > out[j].USD.Value
		}
		return out[i].Tokens.Value > out[j].Tokens.Value
	})
	if len(out) > maxLeaks {
		out = out[:maxLeaks]
	}
	return out
}

func optimizerAction(info leakInfo, cfg config.Config, offer map[string]measure.Offer, baseline bool) (command, advice string) {
	name := info.optimizer
	switch {
	case name == config.OptimizerOutputTrimmer:
		return "trove mcp install", "Estimate only until the Trove MCP server is registered, so your agent can fetch trimmed output back. Run `trove mcp install`, then add output_trimmer = \"on\" under [optimizers]; it applies from the next session."
	case name == analyze.OptPrune:
		return "", info.advice
	case baseline:
		return "trove optimize status", name + " can remove this. Nothing is offered or switched on until the baseline period ends (see Optimizers)."
	case cfg.OptimizerState(name) == config.StateOn:
		return "trove optimize status", name + " is on. See what it saved under Optimizers."
	}
	if o, ok := offer[name]; ok {
		return o.Enable, "Add this line under [optimizers] in config.toml. It applies from your next session."
	}
	return "trove optimize status", name + " can remove this. It is not offered yet: Trove needs more sessions to be sure it is worth it."
}

func buildOptimizers(ctx context.Context, db *store.DB, in Inputs, w measure.Window, savings []measure.OptimizerSavings, offers []measure.Offer) (Optimizers, error) {
	out := Optimizers{KillSwitch: in.KillSwitch, Rows: []OptimizerRow{}, Guardrails: []GuardrailEvent{}}
	st, rolled, err := measure.LoadStatus(ctx, db, in.Config.Measure, in.Now)
	if err != nil {
		return out, err
	}
	if st.Phase == measure.PhaseBaseline {
		out.Baseline = &Baseline{Active: true, DaysElapsed: st.DaysElapsed, DaysNeeded: st.DaysNeeded,
			SessionsSeen: st.SessionsSeen, SessionsNeeded: st.SessionsNeeded}
	}
	shadows, err := measure.ShadowEstimates(ctx, db, w, in.Config)
	if err != nil {
		return out, err
	}
	shadow := map[string]measure.ShadowRow{}
	for _, s := range shadows {
		shadow[s.Optimizer] = s
	}
	saved := map[string]measure.OptimizerSavings{}
	for _, s := range savings {
		saved[s.Optimizer] = s
	}
	offer := map[string]measure.Offer{}
	for _, o := range offers {
		offer[o.Optimizer] = o
	}

	// The pipeline's optimizers, then the gateway's own behaviors (in-flight dedup), which have a
	// state and a shadow estimate but do not act on a request's bytes.
	names := append(append([]string{}, optimize.PipelineNames...), optimize.GatewayNames...)
	for _, name := range names {
		configured := in.Config.OptimizerState(name)
		row := OptimizerRow{Name: name, Configured: configured, Effective: optimize.EffectiveStateFor(name, configured, in.Recoverable)}
		if name == config.OptimizerInflightDedup {
			row.Note = "estimate only: counts identical requests that arrived while one was still waiting for the provider. Sharing the call is not built, so nothing is changed"
		}
		if out.Baseline != nil && row.Effective == config.StateOn {
			row.Effective = config.StateShadow // the baseline keeps every optimizer in shadow
		}
		if in.KillSwitch {
			row.Effective = config.StateOff
		}
		if why, back := rolled[name]; back {
			row.RolledBack = why
			if row.Effective == config.StateOn {
				row.Effective = config.StateShadow
			}
		}
		if s, ok := saved[name]; ok {
			usd := s.USD
			row.Saved = &usd
			row.Measured = measuredFrom(name, s.Corroboration)
		}
		if s, ok := shadow[name]; ok && row.Effective != config.StateOn {
			usd := s.USD
			row.WouldSave = &usd
		}
		if o, ok := offer[name]; ok {
			row.Enable = o.Enable
		}
		if isGatewayName(name) && configured == config.StateOff && row.WouldSave == nil {
			continue // nothing to say about a gateway behavior that is off and has no estimate
		}
		out.Rows = append(out.Rows, row)
	}

	evs, err := measure.GuardrailEvents(ctx, db, 20)
	if err != nil {
		return out, err
	}
	for _, e := range evs {
		if len(out.Guardrails) == 5 {
			break
		}
		inWindow := !e.At.Before(w.From) && e.At.Before(w.To)
		if !inWindow && e.ClearedAt != nil {
			continue
		}
		out.Guardrails = append(out.Guardrails, GuardrailEvent{When: day(e.At.Local()), Optimizer: e.Optimizer, Rule: e.Rule,
			Reason: e.Reason, Cleared: e.ClearedAt != nil})
	}
	if len(out.Guardrails) == 0 && st.Phase == measure.PhaseActive {
		out.Quality = "No regression detected locally. Trove checks retries, tool errors and re-fetches; it does not prove output quality."
	}
	return out, nil
}

func measuredFrom(name string, c *measure.Corroboration) *Measured {
	if c == nil {
		return nil
	}
	m := &Measured{Label: c.Label, Reduced: c.Reduced, Verdict: string(c.Verdict), NOn: c.NOn, NOff: c.NOff,
		OnMean: c.OnMean, OffMean: c.OffMean, Text: c.Text, Optimize: name,
		Enough: c.Verdict != measure.NotEnoughData}
	if c.Reduced && c.OffMean > 0 {
		m.Percent = 100 * (c.OffMean - c.OnMean) / c.OffMean
	}
	return m
}

// buildAdapters reads what harness plugins recorded since from. It returns nil
// when there is nothing, so reports without a plugin are unchanged.
func buildAdapters(ctx context.Context, db *store.DB, from time.Time) (*Adapters, error) {
	dec, err := db.AdapterEvents().Decisions(ctx, from)
	if err != nil {
		return nil, err
	}
	use, err := db.AdapterEvents().Usage(ctx, from)
	if err != nil {
		return nil, err
	}
	if len(dec) == 0 && len(use) == 0 {
		return nil, nil
	}
	return &Adapters{Decisions: dec, Usage: use}, nil
}

// recommenderSuggestions asks the recommender for suggestions. It is best effort:
// a failure to gather the facts or run a provider drops the suggestions and never
// fails the report (rule 4).
func recommenderSuggestions(ctx context.Context, db *store.DB, in Inputs) []Recommendation {
	facts, err := recommend.Gather(ctx, db, in.Config, in.RepoKey, in.Task, in.Now)
	if err != nil {
		return nil
	}
	providers := in.Providers
	if len(providers) == 0 {
		providers = []recommend.Provider{recommend.NewLocal()}
	}
	recs, _ := recommend.Compose(ctx, facts, providers...)
	release := recommend.BundledDefaults().Release
	out := make([]Recommendation, 0, len(recs))
	for _, rc := range recs {
		out = append(out, FromRecommendation(rc, release))
	}
	return out
}

// FromRecommendation puts a recommender suggestion in the report's shape.
func FromRecommendation(rc recommend.Recommendation, defaultsRelease string) Recommendation {
	r := Recommendation{
		Suggestion: suggest.Suggestion{Kind: string(rc.Kind), Title: recommendationTitle(rc), Detail: rc.Reason,
			Confidence: string(rc.Confidence), Label: rc.Label},
		Source: "recommender", Name: rc.Name, Action: string(rc.Action), Evidence: string(rc.Evidence), SuggestedValue: rc.Value,
	}
	if rc.Evidence != recommend.EvidenceLocal {
		r.DefaultsRelease = defaultsRelease
	}
	return r
}

func recommendationTitle(rc recommend.Recommendation) string {
	switch rc.Action {
	case recommend.ActionShadow:
		return "Try " + rc.Name + " in shadow mode"
	case recommend.ActionEnable:
		return "Turn on " + rc.Name
	case recommend.ActionAvoid:
		return "Leave " + rc.Name + " off"
	case recommend.ActionUseProfile:
		return "Use the " + rc.Name + " settings profile"
	case recommend.ActionTune:
		if rc.Value != nil {
			return fmt.Sprintf("Set %s to %d for this repo", rc.Name, int(*rc.Value))
		}
		return "Tune " + rc.Name + " for this repo"
	}
	return rc.Name
}

func isGatewayName(name string) bool {
	for _, g := range optimize.GatewayNames {
		if g == name {
			return true
		}
	}
	return false
}
