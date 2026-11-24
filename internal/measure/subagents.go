package measure

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/measure/limitfit"
	"github.com/usetrove/trove/internal/pricing"
	"github.com/usetrove/trove/internal/store"
	"github.com/usetrove/trove/internal/tasks"
)

// Per-agent figures for the delegation features that set a model and effort on a subagent (docs/tasks.md,
// "Measuring per agent"). A run is one row of subagent_runs (written by the SubagentStart and SubagentStop
// hooks); its requests are the history_requests rows with the same session key and agent key. Only counts,
// model names, effort words and status words are read.
//
// What it can and cannot show:
//   - Before and after compare runs of the same agent type before and after the install. It is observational:
//     anything else that changed in that time is mixed in. The session A/B (experiment.AnalyzeHookTrial) is
//     the evidence; these figures explain it.
//   - The routine agents did not exist before their install, so they have no "before". Their after figures
//     stand alone.
//   - Tokens and thinking are measured from the agent's own logs (thinking only where the log reports it).
//     The share of the plan limits is estimated from the limit fit (limitfit) and absent without one.
//   - The status is the agent's own first line. A run whose reply had no STATUS line (or that reported through
//     SubagentHandback) is unknown and left out of the failure rate.

// featureAgentTypes are the agent types each feature's figures cover.
var featureAgentTypes = map[string][]string{
	tasks.FeatureScout:   {tasks.ScoutName},
	tasks.FeatureRoutine: {tasks.TestRunnerName, tasks.LintFixerName},
	tasks.FeatureExplore: {tasks.ExploreAgent},
}

// SubagentFailureRate is the share of runs with a known status that reported failed or unsure above which the
// report suggests removing a feature. A cheap agent that fails one run in four sends that work back to the main
// conversation (re-delegation) often enough that it is unlikely to save anything.
const SubagentFailureRate = 0.25

// AgentRuns is one side of the comparison for one agent type.
type AgentRuns struct {
	Runs int `json:"runs"`
	// RunsWithTokens is how many runs had their requests in the history yet; the token figures cover only them.
	RunsWithTokens int     `json:"runs_with_tokens"`
	TokensPerRun   float64 `json:"tokens_per_run"` // input, cache and output: measured
	OutputPerRun   float64 `json:"output_tokens_per_run"`
	// ThinkingShare is thinking tokens over output tokens, over requests whose log reported thinking; nil when none did.
	ThinkingShare *float64 `json:"thinking_share,omitempty"`
	// Limits is the mean run as a share of the plan limits (estimated); nil without a limit fit.
	Limits *limitfit.Estimate `json:"limits_per_run,omitempty"`
	// Effort counts requests by the effort level the log recorded ("" = not recorded), so the user can see whether
	// effort: low applied (CLAUDE_CODE_EFFORT_LEVEL overrides it).
	Effort map[string]int `json:"effort_requests"`
	Models map[string]int `json:"model_requests"`
	Status map[string]int `json:"status_runs"`
	// FailureRate is failed plus unsure over runs with a known status; nil when none has one.
	FailureRate *float64 `json:"failure_rate,omitempty"`
	// Redelegated is how many failed or unsure runs were followed, in the same session, by a general-purpose run.
	Redelegated int `json:"redelegated"`
	model       string
	tokens      [limitfit.NTypes]float64
}

// AgentEffect is the before/after for one agent type of one installed feature.
type AgentEffect struct {
	Feature   string     `json:"feature"`
	AgentType string     `json:"agent_type"`
	AppliedAt time.Time  `json:"applied_at"`
	Before    *AgentRuns `json:"before,omitempty"` // nil for an agent type that did not exist before
	After     AgentRuns  `json:"after"`
	Design    string     `json:"design"` // always "observational before/after"
	Label     Label      `json:"label"`  // tokens and thinking measured; Limits carries its own "estimated"
	Text      string     `json:"text"`
	Recommend string     `json:"recommend,omitempty"`
}

type agentReq struct {
	model, effort  string
	in, out, read  int64
	w5, w1, wOther int64
	thinking       sql.NullInt64
}

type agentRun struct {
	session, typ, status string
	at                   int64
	reqs                 []agentReq
}

// AgentEffects reports per agent type for every installed scout, routine-agents or Explore feature. fit is the
// limit fit (nil: no share of the limits); now is used to price that share.
func AgentEffects(ctx context.Context, db *store.DB, m config.Measure, fit *limitfit.Result, now time.Time) ([]AgentEffect, error) {
	rows, err := db.QueryContext(ctx, `SELECT t.feature, MIN(f.applied_at) FROM task_installs t JOIN fixes f ON f.change_id = t.change_id
		WHERE f.reverted_at IS NULL AND t.feature IN (?, ?, ?) AND (t.feature <> ? OR t.effort <> '')
		GROUP BY t.feature ORDER BY t.feature`,
		tasks.FeatureScout, tasks.FeatureRoutine, tasks.FeatureExplore, tasks.FeatureScout)
	if err != nil {
		return nil, err
	}
	applied := map[string]int64{}
	var feats []string
	for rows.Next() {
		var f string
		var at int64
		if err := rows.Scan(&f, &at); err != nil {
			_ = rows.Close()
			return nil, err
		}
		applied[f] = at
		feats = append(feats, f)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil || len(feats) == 0 {
		return nil, err
	}
	runs, err := loadAgentRuns(ctx, db)
	if err != nil {
		return nil, err
	}
	tbl, _ := pricing.Default()
	var out []AgentEffect
	for _, f := range feats {
		for _, typ := range featureAgentTypes[f] {
			out = append(out, agentEffect(f, typ, applied[f], runs, m, fit, tbl, now))
		}
	}
	return out, nil
}

func loadAgentRuns(ctx context.Context, db *store.DB) ([]*agentRun, error) {
	rows, err := db.QueryContext(ctx, `SELECT r.session_key, r.agent_key, r.agent_type, r.status, CASE WHEN r.started_at > 0 THEN r.started_at ELSE r.stopped_at END,
		h.model, COALESCE(h.effort, ''), h.input_tokens, h.output_tokens, h.cache_read, h.cache_write_5m, h.cache_write_1h, h.cache_write_other, h.thinking_tokens
		FROM subagent_runs r LEFT JOIN history_requests h ON h.session_key = r.session_key AND h.agent_key = r.agent_key
		ORDER BY r.session_key, 5, r.agent_key, h.ts`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*agentRun
	var cur *agentRun
	var curKey string
	for rows.Next() {
		var sess, agent, typ, status string
		var at int64
		var model sql.NullString
		var effort string
		var in, outT, read, w5, w1, wo sql.NullInt64
		var thinking sql.NullInt64
		if err := rows.Scan(&sess, &agent, &typ, &status, &at, &model, &effort, &in, &outT, &read, &w5, &w1, &wo, &thinking); err != nil {
			return nil, err
		}
		if cur == nil || curKey != sess+"\x00"+agent {
			cur = &agentRun{session: sess, typ: typ, status: status, at: at}
			curKey = sess + "\x00" + agent
			out = append(out, cur)
		}
		if model.Valid {
			cur.reqs = append(cur.reqs, agentReq{model: model.String, effort: effort, in: in.Int64, out: outT.Int64, read: read.Int64,
				w5: w5.Int64, w1: w1.Int64, wOther: wo.Int64, thinking: thinking})
		}
	}
	return out, rows.Err()
}

func agentEffect(feat, typ string, applied int64, runs []*agentRun, m config.Measure, fit *limitfit.Result, tbl *pricing.Table, now time.Time) AgentEffect {
	e := AgentEffect{Feature: feat, AgentType: typ, AppliedAt: time.UnixMilli(applied).UTC(), Design: "observational before/after", Label: Measured}
	var before, after []*agentRun
	for _, r := range runs {
		if r.typ != typ {
			continue
		}
		if r.at >= applied {
			after = append(after, r)
		} else {
			before = append(before, r)
		}
	}
	e.After = summarizeRuns(after, runs, fit, tbl, now)
	if feat != tasks.FeatureRoutine { // the routine agents did not exist before
		b := summarizeRuns(before, runs, fit, tbl, now)
		e.Before = &b
	}
	e.Text, e.Recommend = agentText(e, m)
	return e
}

func summarizeRuns(rs []*agentRun, all []*agentRun, fit *limitfit.Result, tbl *pricing.Table, now time.Time) AgentRuns {
	a := AgentRuns{Runs: len(rs), Effort: map[string]int{}, Models: map[string]int{}, Status: map[string]int{}}
	var tokens, output, think, thinkOut float64
	known, bad := 0, 0
	for _, r := range rs {
		a.Status[r.status]++
		if r.status != tasks.StatusUnknown {
			known++
			if r.status == tasks.StatusFailed || r.status == tasks.StatusUnsure {
				bad++
				if redelegated(r, all) {
					a.Redelegated++
				}
			}
		}
		if len(r.reqs) == 0 {
			continue
		}
		a.RunsWithTokens++
		for _, q := range r.reqs {
			a.Effort[q.effort]++
			a.Models[q.model]++
			w5 := q.w5 + q.wOther // a write with no TTL split is priced as the default 5-minute write
			tokens += float64(q.in + q.out + q.read + w5 + q.w1)
			output += float64(q.out)
			a.tokens[limitfit.TInput] += float64(q.in)
			a.tokens[limitfit.TOutput] += float64(q.out)
			a.tokens[limitfit.TCacheRead] += float64(q.read)
			a.tokens[limitfit.TWrite5m] += float64(w5)
			a.tokens[limitfit.TWrite1h] += float64(q.w1)
			if q.thinking.Valid {
				think += float64(q.thinking.Int64)
				thinkOut += float64(q.out)
			}
		}
	}
	if known > 0 {
		r := float64(bad) / float64(known)
		a.FailureRate = &r
	}
	if a.RunsWithTokens == 0 {
		return a
	}
	n := float64(a.RunsWithTokens)
	a.TokensPerRun, a.OutputPerRun = tokens/n, output/n
	if thinkOut > 0 {
		s := think / thinkOut
		a.ThinkingShare = &s
	}
	a.model = topKey(a.Models)
	d := limitfit.Delta{Model: a.model}
	for i := range d.Tokens {
		d.Tokens[i] = int64(math.Round(a.tokens[i] / n))
	}
	if tbl != nil {
		limitfit.PriceDelta(tbl, &d, now)
	}
	a.Limits = fit.Percent(d)
	return a
}

// redelegated reports whether the session's next run after r is a general-purpose one.
func redelegated(r *agentRun, all []*agentRun) bool {
	var next *agentRun
	for _, o := range all {
		if o.session == r.session && o.at > r.at && (next == nil || o.at < next.at) {
			next = o
		}
	}
	return next != nil && next.typ == "general-purpose"
}

func topKey(m map[string]int) string {
	best, n := "", -1
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if m[k] > n {
			best, n = k, m[k]
		}
	}
	return best
}

func agentText(e AgentEffect, m config.Measure) (text, recommend string) {
	var b strings.Builder
	a := e.After
	fmt.Fprintf(&b, "%s: %d run(s) since the install", e.AgentType, a.Runs)
	if a.RunsWithTokens > 0 {
		fmt.Fprintf(&b, "; %s tokens per run (measured)", humanInt(a.TokensPerRun))
		if a.ThinkingShare != nil {
			fmt.Fprintf(&b, ", thinking %.0f%% of output (measured)", *a.ThinkingShare*100)
		}
		if l := a.Limits; l != nil && l.FiveHour != nil {
			fmt.Fprintf(&b, ", about %.3f%% of the 5-hour limit per run (estimated)", l.FiveHour.Point)
		}
		if low := a.Effort["low"]; low < sumInts(a.Effort) {
			fmt.Fprintf(&b, ". %d of %d requests did not run at low effort (check CLAUDE_CODE_EFFORT_LEVEL and the model)", sumInts(a.Effort)-low, sumInts(a.Effort))
		}
	}
	if bf := e.Before; bf != nil && bf.RunsWithTokens > 0 && a.RunsWithTokens > 0 {
		fmt.Fprintf(&b, ". Before: %s tokens per run over %d run(s) (measured; observational, not an A/B)", humanInt(bf.TokensPerRun), bf.RunsWithTokens)
	}
	min := max(m.MinSessions, 1)
	if a.FailureRate != nil {
		known := a.Runs - a.Status[tasks.StatusUnknown]
		fmt.Fprintf(&b, ". Failed or unsure: %d of %d with a status (measured), %d handed to general-purpose", int(math.Round(*a.FailureRate*float64(known))), known, a.Redelegated)
		if known >= min && *a.FailureRate > SubagentFailureRate {
			recommend = fmt.Sprintf("%s failed or was unsure in more than %.0f%% of its runs. Run `trove tasks remove %s`, or set a stronger routine_model under [tasks] and apply again.",
				e.AgentType, SubagentFailureRate*100, featureArg(e.Feature))
		}
	}
	if a.Runs < min {
		b.WriteString(". Not enough runs yet to judge")
	}
	b.WriteString(".")
	return b.String(), recommend
}

func featureArg(f string) string {
	return map[string]string{tasks.FeatureScout: "scout", tasks.FeatureRoutine: "agents", tasks.FeatureExplore: "explore"}[f]
}

func sumInts(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

func humanInt(f float64) string {
	switch {
	case f >= 1e6:
		return fmt.Sprintf("%.1fM", f/1e6)
	case f >= 1e4:
		return fmt.Sprintf("%.0fk", f/1e3)
	}
	return fmt.Sprintf("%.0f", f)
}
