package autopilot

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/measure/limitfit"
	"github.com/usetrove/trove/internal/store"
)

// PolicyView is what the optimize policy has done, for `trove status` and `trove report`: what it turned on, what
// each item measured or estimated, and what it rolled back. Numbers and labels only; every figure says whether it
// is measured or estimated (rule 6).
type PolicyView struct {
	Policy string `json:"policy"`
	Paused bool   `json:"paused,omitempty"`
	// Days is the window the figures cover.
	Days int `json:"days"`
	// Outcome says what trials are judged on for this user (OutcomeText).
	Outcome string       `json:"outcome"`
	Items   []PolicyItem `json:"items"`
	// RolledBack are the rollbacks in the window, newest last.
	RolledBack []Change `json:"rolled_back,omitempty"`
}

// PolicyItem is one item the policy names.
type PolicyItem struct {
	Name string `json:"name"`
	// Group is "on_at_next_session", "shadow_then_evidence", "shadow_only" or "setting".
	Group   string         `json:"group"`
	State   string         `json:"state"`
	Reason  string         `json:"reason,omitempty"`
	Figures []PolicyFigure `json:"figures,omitempty"`
}

// PolicyFigure is one figure with its label.
type PolicyFigure struct {
	Text  string `json:"text"`
	Label string `json:"label"` // "measured" or "estimated"
}

// Policy groups.
const (
	GroupDirect     = "on_at_next_session"
	GroupEvidence   = "shadow_then_evidence"
	GroupShadowOnly = "shadow_only"
	GroupSetting    = "setting"
)

// PolicyViewDays is the window `trove status` and the report cover.
const PolicyViewDays = 30

// BuildPolicyView reads what the optimize policy did. It returns nil unless the user agreed to it. db may be nil
// (no database yet): the states and reasons are still shown, with no figures. A read error drops only the figures.
func BuildPolicyView(ctx context.Context, db *store.DB, f config.AutopilotFile, gatewaySeen bool, fit *limitfit.Result, now time.Time) *PolicyView {
	if !f.Enabled || !f.IsOptimize() {
		return nil
	}
	p := PolicyOf(f)
	v := &PolicyView{Policy: f.Policy, Paused: f.Paused, Days: PolicyViewDays, Outcome: OutcomeText(gatewaySeen, CanWeighLimits(fit))}
	w := measure.Window{From: now.AddDate(0, 0, -PolicyViewDays), To: now}
	var hookShadow map[string]measure.HookShadow
	if db != nil {
		hookShadow, _ = measure.HookShadowEstimates(ctx, db, w)
	}
	plan := map[string]config.AutopilotPlanItem{}
	for _, it := range f.Plan {
		plan[it.Optimizer] = it
	}
	for _, n := range p.Names() {
		e, ok := f.Entries[n]
		if !ok {
			continue // the user set it in config.toml, so the policy stays out of it
		}
		it := PolicyItem{Name: n, State: e.State, Reason: e.Reason, Group: GroupEvidence}
		switch {
		case p.IsDirect(n):
			it.Group = GroupDirect
			if db != nil {
				it.Figures = guardFigures(ctx, db, w, n)
			}
		case p.IsShadowOnly(n):
			it.Group = GroupShadowOnly
		}
		if it.Group != GroupDirect {
			it.Figures = append(it.Figures, shadowFigures(n, plan, hookShadow)...)
		}
		v.Items = append(v.Items, it)
	}
	if st, ok := f.Settings[SubTTLName]; ok && f.SettingsActive() {
		it := PolicyItem{Name: SubTTLName, Group: GroupSetting, State: st.State, Reason: st.Reason}
		if e := st.Estimate; e != nil {
			t := fmt.Sprintf("one-hour lifetime nets $%.2f over %d days at API list prices", e.NetUSD, e.WindowDays)
			if e.Basis == limitfit.BasisFit {
				t = fmt.Sprintf("one-hour lifetime nets %.2g%% of the 5-hour limit (90%% range %.2g to %.2g) over %d days", e.NetPct5h, e.NetPct5hLo, e.NetPct5hHi, e.WindowDays)
			}
			it.Figures = append(it.Figures, PolicyFigure{Text: t, Label: string(measure.Estimated)})
		}
		v.Items = append(v.Items, it)
	}
	cut := now.AddDate(0, 0, -PolicyViewDays).UnixMilli()
	for _, h := range f.History {
		if h.AtMS >= cut && h.To == config.AutopilotRolledBack {
			v.RolledBack = append(v.RolledBack, Change{Date: time.UnixMilli(h.AtMS).Format("2006-01-02"), Optimizer: h.Optimizer, State: h.To, Reason: h.Reason})
		}
	}
	return v
}

// OutcomeText says, in plain English, what this user's trials are judged on under the optimize policy (BillWeigher).
func OutcomeText(gatewaySeen, limitsFitted bool) string {
	if gatewaySeen {
		return "API-equivalent cost per session at list prices (measured usage, estimated cost), plus the guardrails"
	}
	if limitsFitted {
		return "share of your 5-hour plan limit per session, from your own meter (estimated), plus the guardrails"
	}
	return "tokens per session (measured) until Trove has fitted your plan-limit meter, plus the guardrails"
}

// guardFigures are a guard's counts in the window (measured) and the rebuilds its stops avoided (estimated).
func guardFigures(ctx context.Context, db *store.DB, w measure.Window, name string) []PolicyFigure {
	g, err := measure.GuardSummary(ctx, db, w, name)
	if err != nil || g == nil {
		return []PolicyFigure{{Text: "nothing to act on yet", Label: string(measure.Measured)}}
	}
	m := string(measure.Measured)
	var out []PolicyFigure
	if name == config.OptimizerHookModelSwitch {
		out = append(out, PolicyFigure{Text: fmt.Sprintf("warned %d times; you switched anyway %d times", g.Advised, g.Overridden), Label: m})
	} else {
		out = append(out, PolicyFigure{Text: fmt.Sprintf("stopped %d, warned %d; you went ahead %d times and cleared %d sessions", g.Blocked, g.Advised, g.Overridden, g.Cleared), Label: m})
		if g.TokensAvoided.Value > 0 {
			out = append(out, PolicyFigure{Text: fmt.Sprintf("about %s tokens not rebuilt", thousands(int64(g.TokensAvoided.Value))), Label: string(measure.Estimated)})
		}
	}
	if h := g.HourAfter; h != nil {
		out = append(out, PolicyFigure{Text: fmt.Sprintf("5-hour meter moved %+.1f points in the hour after a stop against the same size of resume in shadow (95%% range %+.1f to %+.1f; not randomized)",
			h.Diff.Value, h.DiffLow, h.DiffHi), Label: m})
	}
	return out
}

// shadowFigures are the shadow estimate the planner keeps for an optimizer, or the hooks' own shadow counts.
func shadowFigures(name string, plan map[string]config.AutopilotPlanItem, hook map[string]measure.HookShadow) []PolicyFigure {
	est := string(measure.Estimated)
	if it, ok := plan[name]; ok && it.Sessions > 0 {
		t := fmt.Sprintf("%d shadow sessions, about %s tokens net after its own cost", it.Sessions, thousands(it.NetTokens))
		if l := it.Limit; l != nil && l.FiveHour != nil {
			t += fmt.Sprintf(" (about %.2g%% of the 5-hour limit per session, %s)", l.FiveHour.Point, limitBasis(l.FiveHour.Basis))
		}
		return []PolicyFigure{{Text: t, Label: est}}
	}
	if h, ok := hook[name]; ok && h.Sessions > 0 {
		t := fmt.Sprintf("in shadow it would have acted %d times in %d sessions", h.Events, h.Sessions)
		if h.Tokens > 0 {
			t += fmt.Sprintf(", about %s tokens", thousands(h.Tokens))
		}
		return []PolicyFigure{{Text: t, Label: est}}
	}
	return nil
}

func thousands(n int64) string {
	s := fmt.Sprint(n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// groupTitle is how each group reads.
var groupTitle = map[string]string{
	GroupDirect:     "On from the next session (guards; they never change a request)",
	GroupEvidence:   "Shadow first, turned on only by evidence",
	GroupShadowOnly: "Shadow only (estimated, never turned on)",
	GroupSetting:    "Claude Code setting (subagent cache lifetime)",
}

// Text renders the view under an "Optimize policy" heading.
func (v *PolicyView) Text() string {
	if v == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("Optimize policy")
	if v.Paused {
		b.WriteString(" (paused: new sessions use only config.toml)")
	}
	fmt.Fprintf(&b, "\n  Trials are judged on %s.\n", v.Outcome)
	for _, g := range []string{GroupDirect, GroupEvidence, GroupShadowOnly, GroupSetting} {
		var lines []string
		for _, it := range v.Items {
			if it.Group != g {
				continue
			}
			state := it.State
			if state == config.AutopilotProbation {
				state = "on trial"
			}
			line := fmt.Sprintf("    %-26s %s", it.Name, strings.ReplaceAll(state, "_", " "))
			if it.Reason != "" {
				line += "  (" + it.Reason + ")"
			}
			lines = append(lines, line)
			for _, fg := range it.Figures {
				lines = append(lines, fmt.Sprintf("      %s (%s)", fg.Text, fg.Label))
			}
		}
		if len(lines) > 0 {
			b.WriteString("  " + groupTitle[g] + ":\n" + strings.Join(lines, "\n") + "\n")
		}
	}
	if len(v.RolledBack) == 0 {
		fmt.Fprintf(&b, "  Nothing rolled back in the last %d days.\n", v.Days)
	} else {
		fmt.Fprintf(&b, "  Rolled back in the last %d days:\n", v.Days)
		for _, r := range v.RolledBack {
			fmt.Fprintf(&b, "    %s  %s  (%s)\n", r.Date, r.Optimizer, r.Reason)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
