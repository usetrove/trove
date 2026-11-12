package journey

import (
	"fmt"
	"os"
	"runtime"
	"sync"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/optimize"
	"github.com/usetrove/trove/internal/pricing"
)

// Label marks every number the offline tier produces.
const Label = "estimated"

// Options configures one offline run.
type Options struct {
	// Seed fixes every random draw. The same seed and code give identical output.
	Seed uint64
	// Weeks is how many simulated weeks each profile runs. Sessions per profile
	// is the profile's weekly count times Weeks.
	Weeks int
	// Profiles restricts the run to these IDs; empty means all.
	Profiles []string
	// Table is the pricing table; nil uses the built-in table.
	Table *pricing.Table
	// MCPSchemaBytes is the size of Trove's tool definitions, sent with every
	// request in the Trove arm while the trimmer (which needs them) is on. Measure
	// it with mcp.MeasureSchema.
	MCPSchemaBytes int
	// BudgetMS is the engine's per-request time budget. 0 uses the simulator's
	// generous budget, so results depend only on the seed. The production default
	// is 25 (see the sensitivity run).
	BudgetMS int
	// Workers is how many sessions run at once. 0 uses every CPU.
	Workers int
	// SystemTokens is the base prompt a session starts with (system prompt, tool
	// definitions, project context). 0 uses DefaultSystemTokens, the measured value.
	SystemTokens int
}

// SessionRecord is one session run in both arms. Every session is kept.
type SessionRecord struct {
	Index    int  `json:"index"`
	Subagent bool `json:"subagent"`
	Off      Arm  `json:"off"`
	On       Arm  `json:"on"`
}

// GuardDecision is one optimizer the net-savings guard switched off for a profile.
type GuardDecision struct {
	Optimizer string `json:"optimizer"`
	// WindowSessions is how many sessions were measured before the decision.
	WindowSessions int `json:"window_sessions"`
	// AppliedFromSession is the first session that ran without the optimizer.
	AppliedFromSession int     `json:"applied_from_session"`
	Requests           int     `json:"requests"`
	TokensRemoved      int64   `json:"tokens_removed"`
	FailOpenRate       float64 `json:"fail_open_rate"`
	NetTokens          int64   `json:"net_tokens"`
	Reason             string  `json:"reason"`
}

// CostStat compares dollars per session on one pinned model.
type CostStat struct {
	Model string `json:"model"`
	// Label is always "estimated" for the offline tier.
	Label string `json:"label"`
	N     int    `json:"n"`
	// MedianOffUSD and MedianOnUSD are per-session medians.
	MedianOffUSD float64 `json:"median_off_usd"`
	MedianOnUSD  float64 `json:"median_on_usd"`
	// SavedPctMedian is the median of the per-session saving, in percent.
	// Negative means Trove cost more.
	SavedPctMedian float64 `json:"saved_pct_median"`
	SavedPctQ1     float64 `json:"saved_pct_q1"`
	SavedPctQ3     float64 `json:"saved_pct_q3"`
	SavedPctCILow  float64 `json:"saved_pct_ci95_low"`
	SavedPctCIHigh float64 `json:"saved_pct_ci95_high"`
	// SavedPctTotal is the saving on summed dollars across all sessions.
	SavedPctTotal float64 `json:"saved_pct_total"`
	// WithinNoise is true when the 95% interval includes zero.
	WithinNoise bool `json:"within_noise"`
}

// HookSummary is the subscription (hook-level) result. It is in tokens only.
type HookSummary struct {
	Label string `json:"label"`
	// ContextTokensOff and ContextTokensOn are totals of context processed,
	// summed over every request of every session, in estimated tokens.
	ContextTokensOff int64 `json:"context_tokens_off"`
	ContextTokensOn  int64 `json:"context_tokens_on"`
	// SavedPctMedian is the median per-session saving in context tokens.
	SavedPctMedian float64 `json:"saved_pct_median"`
	SavedPctQ1     float64 `json:"saved_pct_q1"`
	SavedPctQ3     float64 `json:"saved_pct_q3"`
	SavedPctCILow  float64 `json:"saved_pct_ci95_low"`
	SavedPctCIHigh float64 `json:"saved_pct_ci95_high"`
	SavedPctTotal  float64 `json:"saved_pct_total"`
	WithinNoise    bool    `json:"within_noise"`
	// DeniedReads and RewrittenInputs are counted across the profile's sessions.
	DeniedReads    int `json:"denied_reads"`
	RewrittenInput int `json:"rewritten_inputs"`
}

// ProfileResult is one profile's journey.
type ProfileResult struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Billing     string `json:"billing"`
	Adapter     string `json:"adapter"`
	// Model is "gateway" (request level, tokens and dollars), "hooks" (tool
	// level, tokens only) or "none" (not measurable).
	Model      string `json:"model"`
	Measurable bool   `json:"measurable"`
	// Reason says why a profile is not measurable here. Empty when it is.
	Reason string `json:"reason,omitempty"`
	// N is the number of sessions, each run in both arms.
	N int `json:"n"`
	// MedianTurns is the median tool calls per session (parent and subagent).
	MedianTurns float64 `json:"median_turns"`
	// TokensOff and TokensOn are totals over the profile's sessions, by class
	// (gateway model only).
	TokensOff Tokens `json:"tokens_off"`
	TokensOn  Tokens `json:"tokens_on"`
	// SavedTokensPctTotal is the saving on billed input tokens across all sessions
	// (gateway model only).
	SavedTokensPctTotal float64 `json:"saved_tokens_pct_total"`
	// Cost is by pinned model; the first entry is the live pilot's model. Empty for
	// the hooks model, which is never priced in dollars.
	Cost []CostStat `json:"cost,omitempty"`
	// Hook is the subscription result (hooks model only).
	Hook *HookSummary `json:"hook,omitempty"`
	// OverheadTokens is what Trove's own tool definitions cost in the Trove arm,
	// over every request while the trimmer is on: fallback requests included,
	// because the tool list is sent whether or not the request was shortened.
	OverheadTokens int64 `json:"overhead_tokens"`
	// FallbackOverheadTokens is the part of OverheadTokens on requests that fell back
	// to the original body.
	FallbackOverheadTokens int64 `json:"fallback_overhead_tokens"`
	// Applied is the estimated tokens removed per optimizer, summed over sessions.
	Applied map[string]int64 `json:"tokens_removed_by_optimizer,omitempty"`
	// Guard lists the optimizers the net-savings guard switched off for this profile.
	Guard []GuardDecision `json:"guard_decisions,omitempty"`
	// StructureOkOff and StructureOkOn are the share of sessions where every tool
	// call kept its result. This is not a task-success measure.
	StructureOkOff float64 `json:"structure_ok_off"`
	StructureOkOn  float64 `json:"structure_ok_on"`
	// TrimmedSessions is how many Trove-arm sessions had output stored for recovery.
	TrimmedSessions int `json:"trimmed_sessions"`
	// VerbatimRecoveredOn is the share of trimmed sessions whose stored text is
	// byte-identical to the tool's own output.
	VerbatimRecoveredOn float64 `json:"verbatim_recovered_on"`
	// FailOpenRequests is how many requests the engine forwarded unchanged after
	// an internal failure. That is correct behavior, reported so it is not hidden.
	FailOpenRequests int             `json:"fail_open_requests"`
	Sessions         []SessionRecord `json:"sessions,omitempty"`
}

// Result is the full offline run. It contains no timestamps or paths, so the
// same seed and budget always produce the same bytes.
type Result struct {
	Label          string          `json:"label"`
	Tier           string          `json:"tier"`
	Seed           uint64          `json:"seed"`
	Weeks          int             `json:"weeks"`
	BudgetMS       int             `json:"optimizer_budget_ms"`
	SystemTokens   int             `json:"base_prompt_tokens"`
	PricingVersion string          `json:"pricing_version"`
	PricingDate    string          `json:"pricing_date"`
	PilotModel     string          `json:"pilot_model"`
	BytesPerToken  float64         `json:"bytes_per_token"`
	MCPSchemaBytes int             `json:"mcp_schema_bytes"`
	Profiles       []ProfileResult `json:"profiles"`
	Caveats        []string        `json:"caveats"`
	// Calibration is the gate against the committed holdout targets. When it
	// fails, the report withholds every savings table; see Markdown.
	Calibration CalibrationStatus `json:"calibration"`
}

// Caveats is printed with every offline result. They are the limits of the model.
var Caveats = []string{
	"Estimated, not billed. Tokens are bytes divided by a fixed bytes-per-token ratio; the provider's tokenizer will differ.",
	"Synthetic sessions. Their shape is checked against real logs by the calibration gate, but they are not your sessions. Grep is calibrated to the developer's logs, not holdout-validated: its tail was set between the two halves.",
	"Base prompt: each session starts with about 19,000 tokens of system prompt and tool definitions (the measured first-request cache write). It is a parameter; the shipped default is the measured value.",
	"Gateway profiles: request-level optimizers, priced in dollars. Hooks profiles (subscription): tool-level hooks only, in tokens only; no dollars and no cache model.",
	"Hooks model: the agent is not modeled. A denied read is not retried, and rewritten tool inputs are counted but not re-executed, so their output is unchanged in this model.",
	"Grep and Glob outputs are not transformed in the hooks model; only Bash outputs get the output hooks, as docs/hooks.md specifies for the trimmer.",
	"Quality is not measured. Structure and recovery are checked; whether the agent finishes the task is only measurable in the live tier.",
	"Cache model (gateway): the cached prefix is the bytes shared with the previous request. Rewriting earlier history costs cache writes, and that is charged.",
	"Net-savings guard: decided on the first simulated week and applied from the next session. Per-optimizer net is tokens removed minus declared overhead; an optimizer's cache effects are inside the arm totals, not attributed to it.",
	"output_trimmer declares 0 overhead (OverheadTokens), which is undeclared. The simulator charges the measured size of Trove's MCP tool list on every request while the trimmer is on, including requests that fell back to the original.",
	"Dollars are list prices from the pinned table at the pricing date.",
}

// Run executes the offline tier.
func Run(opts Options) (Result, error) {
	table := opts.Table
	if table == nil {
		t, err := pricing.Default()
		if err != nil {
			return Result{}, fmt.Errorf("the built-in pricing table did not load: %w", err)
		}
		table = t
	}
	weeks := opts.Weeks
	if weeks < 1 {
		weeks = 1
	}
	budget := opts.BudgetMS
	if budget <= 0 {
		budget = simBudgetMS
	}
	sys := opts.SystemTokens
	if sys <= 0 {
		sys = DefaultSystemTokens
	}
	want := map[string]bool{}
	for _, id := range opts.Profiles {
		want[id] = true
	}
	known := map[string]bool{}
	for _, p := range Profiles() {
		known[p.ID] = true
	}
	for id := range want {
		if !known[id] {
			return Result{}, fmt.Errorf("no profile named %q; run `trove bench journeys --list` to see the profiles", id)
		}
	}
	res := Result{
		Label: Label, Tier: "offline", Seed: opts.Seed, Weeks: weeks, BudgetMS: budget, SystemTokens: sys,
		PricingVersion: table.Version(), PricingDate: PricingDate.Format("2006-01-02"),
		PilotModel: simModels[0], BytesPerToken: bytesPerToken,
		MCPSchemaBytes: opts.MCPSchemaBytes, Caveats: Caveats,
	}
	for _, p := range Profiles() {
		if len(want) > 0 && !want[p.ID] {
			continue
		}
		pr, err := runProfile(p, opts, weeks, table, budget, sys)
		if err != nil {
			return Result{}, err
		}
		res.Profiles = append(res.Profiles, pr)
	}
	targets, err := CommittedTargets()
	if err != nil {
		return Result{}, err
	}
	res.Calibration = CheckCalibration(targets, DefaultTolerance)
	return res, nil
}

// Sensitivity runs the same journeys with the production optimizer budget (25 ms)
// and one session at a time, so the machine is not loaded by the simulator. Its
// numbers can differ from Run because a slow optimizer can now time out, and each
// timeout forwards the request unchanged.
func Sensitivity(opts Options) (Result, error) {
	opts.BudgetMS = 25
	opts.Workers = 1
	return Run(opts)
}

// part is one run of a conversation: the parent session or a subagent.
type part struct {
	turns []Turn
	sub   bool
}

func runProfile(p Profile, opts Options, weeks int, table *pricing.Table, budgetMS, systemTokens int) (ProfileResult, error) {
	out := ProfileResult{
		ID: p.ID, Name: p.Name, Description: p.Description, Billing: p.Billing,
		Adapter: string(p.Adapter), Applied: map[string]int64{}, Model: string(p.Model()),
	}
	ok, why := p.Measurable()
	out.Measurable = ok
	if !ok {
		out.Reason = why
		return out, nil
	}
	n := p.Sessions * weeks
	drv := newDriverWith(p, table, opts.MCPSchemaBytes, systemTokens)
	drv.cfg.Optimize.BudgetMS = budgetMS

	var recs []SessionRecord
	trimmerOffFrom := -1 // first session whose trimmer was switched off; -1 if none
	var err error
	if p.Model() == ModelGateway {
		// The net-savings guard: the first simulated week is the window. Its
		// verdicts switch optimizers off for every later session, at the session
		// boundary (CLAUDE.md rule 5). The window itself is not re-run.
		window := p.Sessions
		if window > n {
			window = n
		}
		recs, err = runSessionsRange(drv, p, opts.Seed, 0, window, opts.Workers)
		if err != nil {
			return out, err
		}
		if window < n {
			// Sessions after the window always run: with the guard's verdicts applied
			// if any optimizer was switched off, otherwise unchanged.
			restDrv := drv
			decisions := guardWindow(recs, window)
			if len(decisions) > 0 {
				out.Guard = decisions
				disabled := map[string]bool{}
				for _, d := range decisions {
					disabled[d.Optimizer] = true
				}
				// The MCP tool list is only needed by the trimmer. With it off, the
				// later sessions send no Trove tool definitions at all.
				mcp := opts.MCPSchemaBytes
				if disabled[config.OptimizerOutputTrimmer] {
					mcp = 0
					trimmerOffFrom = window
				}
				restDrv = newDriverWith(p, table, mcp, systemTokens)
				restDrv.cfg.Optimize.BudgetMS = budgetMS
				for name := range disabled {
					restDrv.cfg.Optimizers[name] = config.StateOff
				}
				for i := range out.Guard {
					out.Guard[i].AppliedFromSession = window
				}
			}
			rest, err := runSessionsRange(restDrv, p, opts.Seed, window, n, opts.Workers)
			if err != nil {
				return out, err
			}
			recs = append(recs, rest...)
		}
	} else {
		recs, err = runSessionsRange(drv, p, opts.Seed, 0, n, opts.Workers)
		if err != nil {
			return out, err
		}
	}

	var turns []float64
	var offAll, onAll []Arm
	var structOff, structOn, trimmed, verbatim int
	for _, rec := range recs {
		off, on := rec.Off, rec.On
		out.Sessions = append(out.Sessions, rec)
		turns = append(turns, float64(off.Turns))
		offAll = append(offAll, off)
		onAll = append(onAll, on)
		if off.Checks.Structure {
			structOff++
		}
		if on.Checks.Structure {
			structOn++
		}
		if on.Checks.Stored > 0 {
			trimmed++
			if on.Checks.VerbatimRecovered {
				verbatim++
			}
		}
		out.FailOpenRequests += on.FailOpen
		for k, v := range on.Applied {
			out.Applied[k] += v
		}
		// The tool list is charged on every request of a session whose trimmer (the
		// optimizer that needs it) was on, fallback requests included.
		mcpBytes := opts.MCPSchemaBytes
		if trimmerOffFrom >= 0 && rec.Index >= trimmerOffFrom {
			mcpBytes = 0
		}
		out.OverheadTokens += int64(on.Requests) * tokens(mcpBytes)
		out.FallbackOverheadTokens += int64(on.FailOpen) * tokens(mcpBytes)
	}
	out.N = n
	out.MedianTurns = Median(turns)
	out.StructureOkOff = frac(structOff, n)
	out.StructureOkOn = frac(structOn, n)
	out.TrimmedSessions = trimmed
	out.VerbatimRecoveredOn = frac(verbatim, trimmed)
	if len(out.Applied) == 0 {
		out.Applied = nil
	}

	if p.Model() == ModelHooks {
		out.Hook = hookSummary(p, opts.Seed, offAll, onAll)
		return out, nil
	}
	out.TokensOff, out.TokensOn = sumTokens(offAll), sumTokens(onAll)
	out.SavedTokensPctTotal = savedPct(inputTokens(out.TokensOff), inputTokens(out.TokensOn))
	for _, m := range simModels {
		out.Cost = append(out.Cost, costStat(m, p.ID, opts.Seed, offAll, onAll))
	}
	return out, nil
}

// guardWindow evaluates every request-level optimizer over the measured window.
func guardWindow(recs []SessionRecord, window int) []GuardDecision {
	var out []GuardDecision
	for _, name := range optimize.PipelineNames {
		var w measure.NetWindow
		for _, r := range recs[:window] {
			w.Requests += r.On.Requests
			w.TokensRemoved += r.On.Applied[name]
			w.FailOpen += r.On.FailOpenBy[name]
		}
		v := measure.DefaultNetGuard.Evaluate(w)
		if !v.Disable {
			continue
		}
		out = append(out, GuardDecision{
			Optimizer: name, WindowSessions: window, Requests: w.Requests,
			TokensRemoved: w.TokensRemoved, FailOpenRate: v.FailOpenRate, NetTokens: v.NetTokens,
			Reason: v.Reason,
		})
	}
	return out
}

// hookSummary compares context tokens per session. No dollars are computed.
func hookSummary(p Profile, seed uint64, off, on []Arm) *HookSummary {
	var pct []float64
	h := &HookSummary{Label: Label}
	for i := range off {
		o, n := off[i].ContextTokens, on[i].ContextTokens
		h.ContextTokensOff += o
		h.ContextTokensOn += n
		if o > 0 {
			pct = append(pct, float64(o-n)/float64(o)*100)
		}
		h.DeniedReads += on[i].Denied
		h.RewrittenInput += on[i].Rewritten
	}
	h.SavedPctMedian = Median(pct)
	h.SavedPctQ1, h.SavedPctQ3 = Quartiles(pct)
	h.SavedPctCILow, h.SavedPctCIHigh = MedianCI(pct, newRand(seed, "bootstrap", p.ID, "hooks"))
	h.SavedPctTotal = savedF(float64(h.ContextTokensOff), float64(h.ContextTokensOn))
	h.WithinNoise = Within(h.SavedPctCILow, h.SavedPctCIHigh)
	return h
}

// costStat compares dollars per session on one model. Savings are computed per
// session first, so the interval is over sessions, not over summed totals.
func costStat(model, profileID string, seed uint64, off, on []Arm) CostStat {
	var offs, ons, pct []float64
	var sumOff, sumOn float64
	for i := range off {
		o, n := off[i].CostUSD[model], on[i].CostUSD[model]
		offs = append(offs, o)
		ons = append(ons, n)
		sumOff += o
		sumOn += n
		if o > 0 {
			pct = append(pct, (o-n)/o*100)
		}
	}
	q1, q3 := Quartiles(pct)
	lo, hi := MedianCI(pct, newRand(seed, "bootstrap", profileID, model))
	return CostStat{
		Model: model, Label: Label, N: len(pct),
		MedianOffUSD: Median(offs), MedianOnUSD: Median(ons),
		SavedPctMedian: Median(pct), SavedPctQ1: q1, SavedPctQ3: q3,
		SavedPctCILow: lo, SavedPctCIHigh: hi,
		SavedPctTotal: savedF(sumOff, sumOn),
		WithinNoise:   Within(lo, hi),
	}
}

// runSessionsRange runs sessions from..to-1 of a profile on a worker pool. Each
// session has its own engine and state, and each result goes to its own index, so
// the output is the same as a serial run.
func runSessionsRange(drv *sessionDriver, p Profile, seed uint64, from, to, workers int) ([]SessionRecord, error) {
	n := to - from
	recs := make([]SessionRecord, n)
	errs := make([]error, n)
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	if workers < 1 {
		workers = 1
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				recs[i], errs[i] = runSession(drv, p, seed, from+i)
			}
		}()
	}
	for i := 0; i < n; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return recs, nil
}

// runSession runs one session in both arms. The parent session and its subagent
// (if any) are each run in both arms; a subagent is billed as its own requests,
// so its arms are summed in.
func runSession(drv *sessionDriver, p Profile, seed uint64, idx int) (SessionRecord, error) {
	sess := genSession(p, seed, idx)
	parts := []part{{sess.Turns, false}}
	if sess.Subagent != nil {
		parts = append(parts, part{sess.Subagent.Turns, true})
	}
	rec := SessionRecord{Index: idx}
	off, on := newArm(), newArm()
	for _, pt := range parts {
		var o, t Arm
		var err error
		if p.Model() == ModelHooks {
			o = drv.runHookOff(pt.turns)
			dir, derr := os.MkdirTemp("", "trove-journey-*")
			if derr != nil {
				return rec, fmt.Errorf("could not create a scratch folder for the hook simulation: %w", derr)
			}
			t, err = drv.runHookArm(pt.turns, idx, dir)
			_ = os.RemoveAll(dir)
		} else {
			o, err = drv.runArm(pt.turns, idx, false)
			if err == nil {
				t, err = drv.runArm(pt.turns, idx, true)
			}
		}
		if err != nil {
			return rec, err
		}
		off = addArm(off, o)
		on = addArm(on, t)
		rec.Subagent = rec.Subagent || pt.sub
	}
	rec.Off, rec.On = off, on
	return rec, nil
}

func newArm() Arm {
	return Arm{
		CostUSD: map[string]float64{}, Applied: map[string]int64{},
		Checks: Checks{Structure: true, VerbatimRecovered: true},
	}
}

func addArm(a, b Arm) Arm {
	a.Requests += b.Requests
	a.Turns += b.Turns
	a.Tokens.UncachedInput += b.Tokens.UncachedInput
	a.Tokens.CacheWrite += b.Tokens.CacheWrite
	a.Tokens.CacheRead += b.Tokens.CacheRead
	a.Tokens.Output += b.Tokens.Output
	a.FailOpen += b.FailOpen
	a.ContextTokens += b.ContextTokens
	a.Denied += b.Denied
	a.Rewritten += b.Rewritten
	for k, v := range b.CostUSD {
		a.CostUSD[k] += v
	}
	for k, v := range b.Applied {
		a.Applied[k] += v
	}
	for k, v := range b.FailOpenBy {
		if a.FailOpenBy == nil {
			a.FailOpenBy = map[string]int{}
		}
		a.FailOpenBy[k] += v
	}
	a.Checks.Structure = a.Checks.Structure && b.Checks.Structure
	a.Checks.VerbatimRecovered = a.Checks.VerbatimRecovered && b.Checks.VerbatimRecovered
	a.Checks.Stored += b.Checks.Stored
	return a
}

func sumTokens(arms []Arm) Tokens {
	var t Tokens
	for _, a := range arms {
		t.UncachedInput += a.Tokens.UncachedInput
		t.CacheWrite += a.Tokens.CacheWrite
		t.CacheRead += a.Tokens.CacheRead
		t.Output += a.Tokens.Output
	}
	return t
}

// inputTokens is the billed input side: everything except output.
func inputTokens(t Tokens) int64 { return t.UncachedInput + t.CacheWrite + t.CacheRead }

func savedPct(off, on int64) float64 {
	if off == 0 {
		return 0
	}
	return float64(off-on) / float64(off) * 100
}

func savedF(off, on float64) float64 {
	if off == 0 {
		return 0
	}
	return (off - on) / off * 100
}

func frac(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return float64(n) / float64(d)
}
