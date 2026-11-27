package report

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/experiment"
	"github.com/usetrove/trove/internal/gateway/session"
	"github.com/usetrove/trove/internal/logs"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/measure/limitfit"
	"github.com/usetrove/trove/internal/store"
)

// Status line output formats.
const (
	FormatClaude = "claude" // ANSI color, for Claude Code's statusLine setting
	FormatTmux   = "tmux"   // tmux #[...] style codes
	FormatShell  = "shell"  // plain text for a shell prompt
)

// Status is the data behind the one-line status. Every dollar figure is an
// estimate from list prices.
type Status struct {
	Period   string         `json:"period"`
	Requests int            `json:"requests"`
	Spend    measure.Figure `json:"spend_usd"`
	Saved    measure.Figure `json:"saved_usd"`
	// SavedLimit is Saved as percent of Claude Code's plan limits, by the per-user API-price scale
	// (limitfit; "estimated from API prices"). Nil when there is no fit yet.
	SavedLimit *limitfit.Estimate `json:"saved_limit_pct,omitempty"`
	Budget     *Pacing            `json:"budget,omitempty"`
	// Basis is what the figures are: spend (the gateway's requests, or session-log
	// usage on an API key) or api_equivalent_value (session-log usage under a
	// subscription or an unknown plan, which is not a bill). Empty when there is no usage.
	Basis string `json:"basis,omitempty"`
	// Equivalent is the API list-price value of session-log usage that is not paid
	// per token. Estimated. Spend and Budget stay empty in that case.
	Equivalent *measure.Figure `json:"api_equivalent_usd,omitempty"`
	// UsageBudget is usage against the user's subscription usage budget. Set only
	// for api_equivalent_value usage with a usage budget. A warning, never a cap.
	UsageBudget *UsageBudget `json:"usage_budget,omitempty"`
	// Session is the cost of the session the status line runs in, when known.
	Session *SessionCost `json:"session,omitempty"`
	// Verdict summarises the measured A/B results for cost per session. Nil when
	// no result says "saves" or "costs more".
	Verdict *StatusVerdict `json:"ab_verdict,omitempty"`
}

// Usage budget states.
const (
	UsageUnder = "under"
	UsageNear  = "near" // at or past budget.warn_at_percent
	UsageOver  = "over" // at or past the budget
)

// UsageBudget is API-equivalent usage as a share of the user's usage budget.
type UsageBudget struct {
	BudgetUSD float64        `json:"budget_usd"`
	Share     measure.Figure `json:"share_percent"` // estimated, from list prices
	Status    string         `json:"status"`        // under, near or over
}

func usageBudget(b config.Budget, used measure.Figure) *UsageBudget {
	if b.UsageUSD <= 0 {
		return nil
	}
	pct := round6(100 * used.Value / b.UsageUSD)
	u := &UsageBudget{BudgetUSD: b.UsageUSD, Share: measure.Figure{Value: pct, Label: measure.Estimated, N: used.N, Unpriced: used.Unpriced}, Status: UsageUnder}
	switch {
	case pct >= 100:
		u.Status = UsageOver
	case pct >= float64(b.WarnAtPercent):
		u.Status = UsageNear
	}
	return u
}

// SessionCost is one session's spend so far (estimated; calls that could not be
// priced count as zero, so it is a lower bound).
type SessionCost struct {
	Cost  measure.Figure `json:"cost_usd"`
	Calls int            `json:"calls"`
	// Share is this session's percent of the period's usage. Set only for
	// api_equivalent_value usage. Estimated.
	Share *measure.Figure `json:"share_of_usage_percent,omitempty"`
}

// StatusVerdict counts the optimizers whose latest A/B result on cost per
// session is decisive, and names the biggest saver.
type StatusVerdict struct {
	Saves     int `json:"saves"`
	CostsMore int `json:"costs_more"`
	// Best is the optimizer with the largest measured cost cut, and BestChange its
	// relative change in cost per session (-0.12 is 12% lower). Empty when none saves.
	Best       string         `json:"best,omitempty"`
	BestChange measure.Figure `json:"best_change"`
}

// buildVerdict reads the stored A/B results. They were computed from sessions
// (measured), so this is cheap and never touches the request tables.
func buildVerdict(ctx context.Context, db *store.DB) *StatusVerdict {
	rows, err := db.Experiments().LatestCost(ctx)
	if err != nil {
		return nil
	}
	v := &StatusVerdict{}
	for _, r := range rows {
		switch r.Status {
		case string(experiment.Saves):
			v.Saves++
			if v.Best == "" || r.Value < v.BestChange.Value {
				v.Best = r.Optimizer
				v.BestChange = measure.Figure{Value: r.Value, Label: measure.Label(r.Label), N: r.NOn + r.NOff}
			}
		case string(experiment.CostsMore):
			v.CostsMore++
		}
	}
	if v.Saves == 0 && v.CostsMore == 0 {
		return nil
	}
	return v
}

// historyStatus fills st from the agent's own session logs, for people with no
// gateway traffic in the period (plugin-only and subscription users). It reports
// API-equivalent value, never spend, unless every row is on an API key.
func historyStatus(ctx context.Context, db *store.DB, cfg config.Config, st *Status, from, to, now time.Time) error {
	rows, err := measure.HistoryTotals(ctx, db, measure.Window{From: from, To: now.Add(time.Millisecond)})
	if err != nil || len(rows) == 0 {
		return err
	}
	var total measure.Figure
	total.Label = measure.Estimated
	apiOnly := true
	for _, r := range rows {
		st.Requests += r.Requests
		total.Value += r.CostUSD.Value
		total.N += r.CostUSD.N
		total.Unpriced += r.CostUSD.Unpriced
		apiOnly = apiOnly && r.Plan == string(logs.PlanAPIKey)
	}
	total.Value = round6(total.Value)
	if apiOnly {
		st.Basis, st.Spend = BasisSpend, total
		if cfg.Budget.CapUSD > 0 {
			st.Budget = Pace(cfg.Budget, st.Period, st.Spend, from, to, now)
		}
		return nil
	}
	// A dollar budget is a cap on a bill; a subscription has none, so no pace is judged.
	st.Basis, st.Equivalent, st.Budget = BasisAPIEquivalent, &total, nil
	st.UsageBudget = usageBudget(cfg.Budget, total)
	return nil
}

// BuildStatus reads the small per-day summary the gateway keeps current
// (usage_daily) and, if sessionID is a Claude Code session id, that session's
// row. It never scans the requests table, so it stays fast however much history
// there is.
func BuildStatus(ctx context.Context, db *store.DB, cfg config.Config, now time.Time, sessionID string) (Status, error) {
	name := Month
	if cfg.Budget.CapUSD > 0 || cfg.Budget.UsageUSD > 0 {
		name = PeriodName(cfg.Budget.Period)
	}
	from := StartOf(name, now)
	to := EndOf(name, from)
	st := Status{Period: name, Spend: measure.Figure{Label: measure.Estimated}, Saved: measure.Figure{Label: measure.Estimated}}

	var reqs, unpriced int
	var spend, saved float64
	err := db.QueryRowContext(ctx, `SELECT COALESCE(SUM(requests),0), COALESCE(SUM(unpriced),0), COALESCE(SUM(spend_usd),0), COALESCE(SUM(saved_usd),0)
		FROM usage_daily WHERE day >= ? AND day <= ?`, store.DayKey(from), store.DayKey(now)).Scan(&reqs, &unpriced, &spend, &saved)
	if err != nil {
		return st, err
	}
	st.Requests = reqs
	st.Spend = measure.Figure{Value: round6(spend), Label: measure.Estimated, N: reqs - unpriced, Unpriced: unpriced}
	st.Saved = measure.Figure{Value: round6(saved), Label: measure.Estimated, N: reqs}
	if reqs > 0 {
		st.Basis = BasisSpend
	}
	if cfg.Budget.CapUSD > 0 {
		st.Budget = Pace(cfg.Budget, name, st.Spend, from, to, now)
	}
	// With no gateway traffic in the period, fall back to the agent's own logs. It
	// is skipped when the gateway has traffic, so the usual status line never scans
	// the history table or the requests table.
	if reqs == 0 {
		if err := historyStatus(ctx, db, cfg, &st, from, to, now); err != nil {
			return st, err
		}
	}

	if sessionID != "" {
		base := session.ClaudeCodeKey(sessionID)
		var cost float64
		var calls int
		// The key range covers the base key and its "~n" idle-gap successors.
		err := db.QueryRowContext(ctx, `SELECT total_cost_usd, request_count FROM sessions
			WHERE session_key >= ? AND session_key < ? ORDER BY ended_at DESC LIMIT 1`, base, base+"\x7f").Scan(&cost, &calls)
		if err == nil {
			st.Session = &SessionCost{Cost: measure.Figure{Value: round6(cost), Label: measure.Estimated, N: calls}, Calls: calls}
		} else if st.Basis != "" && reqs == 0 {
			if f, herr := measure.HistorySessionCost(ctx, db, base); herr == nil && f.N > 0 {
				st.Session = &SessionCost{Cost: f, Calls: f.N}
				if st.Equivalent != nil && st.Equivalent.Value > 0 {
					st.Session.Share = &measure.Figure{Value: round6(100 * f.Value / st.Equivalent.Value), Label: measure.Estimated, N: f.N}
				}
			}
		}
	}
	st.Verdict = buildVerdict(ctx, db)
	return st, nil
}

// RenderStatus is the one-line text, for example:
//
//	~$612 / $1,500 · 41% used · 48% of month · on pace · session ~$3.20 · saved ~$47
//
// "~" marks an estimate; "+" after a dollar amount means some calls could not be
// priced, so it is at least that much.
func RenderStatus(s Status, format string, color bool) string {
	if s.Requests == 0 && s.Budget == nil && s.Session == nil {
		return "Trove: no usage yet"
	}
	var parts []string
	spend := Est(s.Spend)
	if s.Spend.Unpriced > 0 {
		spend += "+"
	}
	noun := periodNoun(s.Period)
	if e := s.Equivalent; e != nil {
		// Usage that is not paid per token: show it as what it would cost at list
		// prices, never as spend, and judge no budget pace.
		v := Est(*e)
		if e.Unpriced > 0 {
			v += "+"
		}
		parts = append(parts, v+" API-equivalent this "+noun)
		if u := s.UsageBudget; u != nil {
			parts = append(parts, FormatPercent(u.Share.Value)+" of "+FormatUSD(u.BudgetUSD)+" usage budget")
			switch u.Status {
			case UsageNear:
				parts = append(parts, styled("near usage budget", PaceNearCap, format, color))
			case UsageOver:
				parts = append(parts, styled("over usage budget", PaceOver, format, color))
			}
		}
	} else if b := s.Budget; b != nil {
		parts = append(parts, spend+" / "+FormatUSD(b.CapUSD),
			FormatPercent(b.PercentUsed)+" used",
			FormatPercent(b.PercentElapsed)+" of "+noun,
			styled(paceWord(b.Status), b.Status, format, color))
	} else {
		parts = append(parts, spend+" this "+noun)
	}
	if s.Session != nil {
		p := "session " + Est(s.Session.Cost)
		if sh := s.Session.Share; sh != nil {
			p += " (" + FormatPercent(sh.Value) + " of usage)"
		}
		parts = append(parts, p)
	}
	if s.Saved.Value >= 0.01 {
		p := "saved " + Est(s.Saved)
		if l := s.SavedLimit; l != nil && l.FiveHour != nil {
			p += " (~" + num(l.FiveHour.Point) + "% of 5h)"
		}
		parts = append(parts, p)
	}
	if v := s.Verdict; v != nil {
		parts = append(parts, verdictText(v))
	}
	return strings.Join(parts, " · ")
}

// verdictText is the measured A/B result: the biggest saver by name and how many
// optimizers cost more. Figures come from sessions, so they are "measured".
func verdictText(v *StatusVerdict) string {
	s := "A/B: "
	if v.Best != "" {
		s += fmt.Sprintf("%s %+.0f%% cost per session (measured)", v.Best, v.BestChange.Value*100)
		if v.CostsMore > 0 {
			s += fmt.Sprintf(", %d costs more", v.CostsMore)
		}
		return s
	}
	return s + fmt.Sprintf("%d costs more (measured)", v.CostsMore)
}

func paceWord(status string) string {
	switch status {
	case PaceOver:
		return "over cap"
	case PaceNearCap:
		return "near cap"
	case PaceAhead:
		return "ahead of pace"
	case PaceOnPace:
		return "on pace"
	case PaceUnder:
		return "under pace"
	}
	return "too early to judge pace"
}

// styled colors the pace word: amber when ahead of pace, red near or over the cap.
func styled(word, status, format string, color bool) string {
	if !color || format == FormatShell {
		return word
	}
	var ansi, tmux string
	switch status {
	case PaceAhead:
		ansi, tmux = "33", "yellow"
	case PaceNearCap, PaceOver:
		ansi, tmux = "31", "red"
	default:
		return word
	}
	if format == FormatTmux {
		return fmt.Sprintf("#[fg=%s]%s#[default]", tmux, word)
	}
	return "\x1b[" + ansi + "m" + word + "\x1b[0m"
}
