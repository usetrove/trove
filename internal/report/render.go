package report

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/usetrove/trove/internal/measure"
)

// Options control text rendering.
type Options struct {
	// Width is the terminal width in columns; 0 means 80. Below 40 it is 40.
	Width int
	// Color turns on ANSI color. Color is used only for meaning: amber when
	// spending is ahead of pace or an optimizer was rolled back, red at the cap.
	Color bool
	// Loc is the time zone for times of day; nil means the local zone.
	Loc *time.Location
}

func (o Options) width() int {
	switch {
	case o.Width == 0:
		return 80
	case o.Width < 40:
		return 40
	case o.Width > 100:
		return 100 // long lines are hard to read
	}
	return o.Width
}

func (o Options) paint(code, s string) string {
	if !o.Color || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func (o Options) amber(s string) string { return o.paint("33", s) }
func (o Options) red(s string) string   { return o.paint("31", s) }

// Render writes the report as text.
func Render(r Report, o Options) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Trove report: %s\n", r.Period.Label)
	if r.State == StateEmpty || r.State == StateQuiet {
		b.WriteString("\n")
		for _, n := range r.Notes {
			b.WriteString(wrap(n, o.width(), "", "   ") + "\n")
		}
		if r.Budget != nil {
			b.WriteString("\n" + RenderPacingLine(r.Budget, o) + "\n")
		}
		return b.String()
	}
	for _, n := range r.Notes {
		b.WriteString(wrap(n, o.width(), "", "") + "\n")
	}

	b.WriteString("\n" + heading("Headline"))
	h := r.Headline
	kv := func(k, v string) {
		if o.width() < 60 { // too narrow for two columns: label on its own line
			b.WriteString("  " + k + "\n" + wrap(v, o.width(), "    ", "    ") + "\n")
			return
		}
		b.WriteString(wrap(v, o.width(), "  "+padRight(k, 13), strings.Repeat(" ", 15)) + "\n")
	}
	kv("Spend", spendText(h))
	if h.APIEquivalent != nil {
		kv("API value", apiEquivText(h))
	}
	if r.History != nil {
		kv("From history", historyText(r.History))
	}
	kv("Waste found", wasteText(h))
	kv("Savings", savingsText(h))
	kv("Measured", h.MeasuredNote)
	if r.Budget != nil {
		kv("Budget", RenderPacingLine(r.Budget, o))
	}

	if len(r.Spend.ByAgent) > 0 || len(r.Spend.ByModel) > 0 {
		b.WriteString("\n" + heading("Spend by agent and model"))
		b.WriteString(spendTable("Agent", r.Spend.ByAgent, o))
		b.WriteString("\n")
		b.WriteString(spendTable("Model", r.Spend.ByModel, o))
	}
	if r.History != nil && len(r.History.ByModel) > 0 {
		b.WriteString("\n" + heading("History usage by model (API-equivalent value, not a bill)"))
		b.WriteString(spendTable("Model", r.History.ByModel, o))
	}

	b.WriteString("\n" + heading("Biggest leaks (estimated, overlapping, not additive)"))
	if len(r.Leaks) == 0 {
		b.WriteString("  No waste found yet.\n")
	}
	for i, l := range r.Leaks {
		b.WriteString(wrap(fmt.Sprintf("%d. %s: %s, %s tokens (estimated)", i+1, l.Title, leakDollars(l), FormatTokens(l.Tokens.Value)), o.width(), "  ", "     ") + "\n")
		if l.Command != "" {
			b.WriteString(wrap("Do this: "+l.Command, o.width(), "     ", "              ") + "\n")
		}
		if l.Advice != "" {
			b.WriteString(wrap(l.Advice, o.width(), "     ", "     ") + "\n")
		}
	}

	b.WriteString("\n" + heading("Optimizers"))
	if r.History != nil && !r.History.GatewaySeen {
		b.WriteString(wrap("Optimizers act on live requests, so they need the gateway. Your history above is read-only. Run `trove setup` to route your agent through Trove.", o.width(), "  ", "  ") + "\n")
	} else {
		renderOptimizers(&b, r.Optimizers, o)
	}

	if len(r.Fixes) > 0 {
		b.WriteString("\n" + heading("Config fixes you applied"))
		for _, f := range r.Fixes {
			b.WriteString(wrap(fmt.Sprintf("%s (%s), applied %s", f.FixID, f.Kind, day(f.AppliedAt.Local())), o.width(), "  ", "    ") + "\n")
			b.WriteString(wrap(fixLine(f), o.width(), "    ", "    ") + "\n")
		}
	}

	if r.Tools != nil {
		b.WriteString("\n" + heading("Trove tools (MCP)"))
		for _, l := range toolsLines(r.Tools) {
			b.WriteString(wrap(l, o.width(), "  ", "    ") + "\n")
		}
	}

	b.WriteString("\n" + heading("Recommendations (suggestions only; Trove changes nothing on its own)"))
	if len(r.Recommendations) == 0 {
		b.WriteString("  None yet. These need more recorded usage.\n")
	}
	for i, s := range r.Recommendations {
		b.WriteString(wrap(fmt.Sprintf("%d. %s (confidence: %s)", i+1, s.Title, s.Confidence), o.width(), "  ", "     ") + "\n")
		b.WriteString(wrap(s.Detail, o.width(), "     ", "     ") + "\n")
		switch {
		case s.EstSavingsUSD != nil:
			b.WriteString(wrap(fmt.Sprintf("Estimated saving: ~%s over %d requests.", FormatUSD(*s.EstSavingsUSD), s.Requests), o.width(), "     ", "     ") + "\n")
		case s.Scenario != "":
			b.WriteString(wrap("No savings figure: "+s.Scenario, o.width(), "     ", "     ") + "\n")
		}
	}
	return b.String()
}

func heading(s string) string { return strings.ToUpper(s[:1]) + s[1:] + "\n" }

func spendText(h Headline) string {
	if h.APIEquivalent != nil && h.Spend.N == 0 && h.Spend.Unpriced == 0 {
		return fmt.Sprintf("not shown. Trove could not tell that this usage is billed per token. %s tokens (measured)", FormatTokens(h.Tokens.Value))
	}
	s := fmt.Sprintf("%s (estimated at list prices, not an invoice); %s tokens (measured)", Est(h.Spend), FormatTokens(h.Tokens.Value))
	if h.Spend.Unpriced > 0 {
		s += fmt.Sprintf(". %d requests could not be priced, so spend is at least this", h.Spend.Unpriced)
	}
	return s
}

func wasteText(h Headline) string {
	if h.Waste.Value <= 0 && h.Waste.N == 0 {
		return "none yet (estimated)"
	}
	s := fmt.Sprintf("at least %s", Est(h.Waste))
	if h.Spend.Value > 0 && h.Waste.Value > 0 {
		s += ", " + FormatPercent(h.WastePercent.Value) + " of spend"
	}
	s += " (estimated; overlapping findings counted once)"
	if h.Waste.Unpriced > 0 {
		s += fmt.Sprintf(". %d findings could not be priced", h.Waste.Unpriced)
	}
	return s
}

func savingsText(h Headline) string {
	if h.Savings.N == 0 {
		return "none yet; no optimizer has been on (estimated)"
	}
	s := fmt.Sprintf("%s so far (estimated)", Est(h.Savings))
	if h.Savings.Unpriced > 0 {
		s += fmt.Sprintf(". %d requests could not be priced", h.Savings.Unpriced)
	}
	return s
}

func leakDollars(l Leak) string {
	if l.USD.Value > 0 {
		s := Est(l.USD)
		if l.USD.Unpriced > 0 {
			s += fmt.Sprintf(" (+%d unpriced)", l.USD.Unpriced)
		}
		return s + " (estimated)"
	}
	return "no dollar figure (could not be priced)"
}

// spendTable renders usage grouped by key. The token column is dropped on narrow
// terminals so the cost column always fits.
func spendTable(keyHeader string, rows []measure.TotalsRow, o Options) string {
	if len(rows) == 0 {
		return ""
	}
	wide := o.width() >= 64
	heads := []string{keyHeader, "Requests", "Cost (estimated)"}
	if wide {
		heads = []string{keyHeader, "Requests", "Tokens (measured)", "Cost (estimated)"}
	}
	data := [][]string{heads}
	for _, r := range rows {
		cost := Est(r.CostUSD)
		if r.CostUSD.Unpriced > 0 {
			cost += "+"
		}
		row := []string{orUnknown(r.Key), fmt.Sprintf("%d", r.Requests), cost}
		if wide {
			tok := r.InputTokens + r.CacheWriteTokens + r.CacheReadTokens + r.OutputTokens
			row = []string{orUnknown(r.Key), fmt.Sprintf("%d", r.Requests), FormatTokens(float64(tok)), cost}
		}
		data = append(data, row)
	}
	return table(data, o.width()) + footnoteUnpriced(rows)
}

func footnoteUnpriced(rows []measure.TotalsRow) string {
	for _, r := range rows {
		if r.CostUSD.Unpriced > 0 {
			return "  + means some requests could not be priced, so the cost is at least this.\n"
		}
	}
	return ""
}

func orUnknown(s string) string {
	if s == "" {
		return "(unknown)"
	}
	return s
}

// table lays out rows with the first column left-aligned and the rest right
// aligned, shrinking the first column to fit width.
func table(rows [][]string, width int) string {
	cols := len(rows[0])
	w := make([]int, cols)
	for _, r := range rows {
		for i, c := range r {
			if n := utf8.RuneCountInString(c); n > w[i] {
				w[i] = n
			}
		}
	}
	rest := 2 // indent
	for i := 1; i < cols; i++ {
		rest += w[i] + 2
	}
	if avail := width - rest; w[0] > avail {
		w[0] = max(avail, 8)
	}
	var b strings.Builder
	for _, r := range rows {
		b.WriteString("  ")
		for i, c := range r {
			if i == 0 {
				b.WriteString(padRight(truncate(c, w[0]), w[0]))
				continue
			}
			b.WriteString("  " + padLeft(c, w[i]))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func padRight(s string, n int) string {
	if d := n - utf8.RuneCountInString(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

func padLeft(s string, n int) string {
	if d := n - utf8.RuneCountInString(s); d > 0 {
		return strings.Repeat(" ", d) + s
	}
	return s
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// wrap breaks s into lines of at most width columns. first prefixes the first
// line and rest the others. Words longer than a line are left whole.
func wrap(s string, width int, first, rest string) string {
	var lines []string
	cur := first
	empty := true
	for _, word := range strings.Fields(s) {
		if !empty && utf8.RuneCountInString(cur)+1+utf8.RuneCountInString(word) > width {
			lines = append(lines, cur)
			cur, empty = rest, true
		}
		if !empty {
			cur += " "
		}
		cur += word
		empty = false
	}
	lines = append(lines, cur)
	return strings.Join(lines, "\n")
}

func renderOptimizers(b *strings.Builder, op Optimizers, o Options) {
	w := o.width()
	if op.KillSwitch {
		b.WriteString(wrap("The kill switch is ON: no request is being changed. Run `trove optimize resume` to lift it.", w, "  ", "  ") + "\n")
	}
	if op.Baseline != nil {
		bl := op.Baseline
		b.WriteString(wrap(fmt.Sprintf("Baseline in progress: day %.0f of %d and %d of %d sessions. Until both pass, every optimizer only estimates and changes nothing.",
			bl.DaysElapsed+1, bl.DaysNeeded, bl.SessionsSeen, bl.SessionsNeeded), w, "  ", "  ") + "\n")
	}
	for _, r := range op.Rows {
		state := r.Effective
		if r.Configured != r.Effective {
			state += " (set to " + r.Configured + ")"
		}
		b.WriteString(wrap(fmt.Sprintf("%s: %s", r.Name, state), w, "  ", "    ") + "\n")
		if r.RolledBack != "" {
			b.WriteString(wrap(o.amber("Rolled back: "+r.RolledBack), w, "    ", "    ") + "\n")
		}
		if r.Saved != nil {
			b.WriteString(wrap(fmt.Sprintf("Saved %s (estimated).", Est(*r.Saved)), w, "    ", "    ") + "\n")
		}
		if r.WouldSave != nil && r.WouldSave.N > 0 {
			b.WriteString(wrap(fmt.Sprintf("Would have saved %s (estimated, shadow mode).", Est(*r.WouldSave)), w, "    ", "    ") + "\n")
		}
		if r.Measured != nil {
			b.WriteString(wrap("Measured: "+measuredLine(r.Measured), w, "    ", "    ") + "\n")
		}
		if r.Enable != "" {
			b.WriteString(wrap("To turn on, add under [optimizers] in config.toml: "+r.Enable, w, "    ", "    ") + "\n")
		}
	}
	if len(op.Guardrails) == 0 {
		b.WriteString("  Guardrail events: none.\n")
	}
	for _, e := range op.Guardrails {
		line := fmt.Sprintf("Guardrail %s: %s", e.When, e.Reason)
		if e.Cleared {
			line += " (cleared)"
		} else {
			line = o.amber(line)
		}
		b.WriteString(wrap(line, w, "  ", "    ") + "\n")
	}
	if op.Quality != "" {
		b.WriteString(wrap(op.Quality, w, "  ", "  ") + "\n")
	}
}

func measuredLine(m *Measured) string {
	if !m.Enough {
		return fmt.Sprintf("%s (%d on sessions, %d off sessions).", measure.TextNotEnough, m.NOn, m.NOff)
	}
	if m.Reduced {
		return fmt.Sprintf("%.0f%% fewer prompt tokens per request with it on (%d on, %d off sessions).", m.Percent, m.NOn, m.NOff)
	}
	return fmt.Sprintf("no clear reduction yet (%d on, %d off sessions).", m.NOn, m.NOff)
}

// RenderPacingLine is the one-line budget summary.
func RenderPacingLine(p *Pacing, o Options) string {
	noun := periodNoun(p.Name)
	s := fmt.Sprintf("%s of %s used (%s, estimated), %s of the %s gone. %s",
		Est(p.Spent), FormatUSD(p.CapUSD), FormatPercent(p.PercentUsed), FormatPercent(p.PercentElapsed), noun, statusText(p, noun))
	switch p.Status {
	case PaceOver, PaceNearCap:
		return o.red(s)
	case PaceAhead:
		return o.amber(s)
	}
	return s
}

func periodNoun(name string) string {
	switch name {
	case Today:
		return "day"
	case Week:
		return "week"
	}
	return "month"
}

func statusText(p *Pacing, noun string) string {
	switch p.Status {
	case PaceOver:
		return "The cap is reached."
	case PaceNearCap:
		return "Close to the cap."
	case PaceAhead:
		return "Ahead of pace."
	case PaceOnPace:
		return "On pace."
	case PaceUnder:
		return "Under pace."
	}
	return "Too early in the " + noun + " to judge pace."
}

// RenderPacing is the full `trove budget show` text.
func RenderPacing(p *Pacing, o Options) string {
	if p == nil {
		return "No budget is set. Set one with `trove budget set 1500 --period month`."
	}
	noun := periodNoun(p.Name)
	var b strings.Builder
	fmt.Fprintf(&b, "Budget: %s per %s (spend is estimated at list prices; it is not an invoice)\n\n", FormatUSD(p.CapUSD), noun)
	b.WriteString(wrap(RenderPacingLine(p, o), o.width(), "  ", "  ") + "\n")
	if p.Spent.Unpriced > 0 {
		b.WriteString(wrap(fmt.Sprintf("%d requests could not be priced, so spend is at least this.", p.Spent.Unpriced), o.width(), "  ", "  ") + "\n")
	}
	switch {
	case p.CapReached:
	case p.ProjectedCapDate != "":
		t, _ := time.Parse(time.DateOnly, p.ProjectedCapDate)
		b.WriteString(wrap(fmt.Sprintf("At this rate you reach the cap around %s (estimated), before the %s ends.",
			t.Format("Jan 2"), noun), o.width(), "  ", "  ") + "\n")
	case p.ProjectedTotal != nil:
		b.WriteString(wrap(fmt.Sprintf("At this rate you spend about %s this %s (estimated), which stays under the cap.",
			FormatUSD(p.ProjectedTotal.Value), noun), o.width(), "  ", "  ") + "\n")
	}
	if p.Lever != "" {
		b.WriteString(wrap("What would change it: "+p.Lever+" See `trove report`.", o.width(), "  ", "  ") + "\n")
	}
	return b.String()
}

// fixLine says what is known about an applied config fix. A measured figure is
// only printed when both sides have enough sessions; the estimate made when the
// fix was proposed is always labeled estimated.
func fixLine(f measure.FixEffect) string {
	est := "no estimate was made"
	if f.EstTokens > 0 {
		est = fmt.Sprintf("about %d tokens saved per session", f.EstTokens)
		if f.EstUSD != nil {
			est += fmt.Sprintf(" (about %s)", FormatUSD(*f.EstUSD))
		}
	}
	if !f.Enough {
		return fmt.Sprintf("Measured: %s (%d sessions before, %d after). Estimated: %s.", measure.TextNotEnough, f.NBefore, f.NAfter, est)
	}
	m := fmt.Sprintf("no clear reduction in prompt tokens per request yet (%d sessions before, %d after)", f.NBefore, f.NAfter)
	if f.Reduced {
		m = fmt.Sprintf("%.0f%% fewer prompt tokens per request after the fix (%d sessions before, %d after)", f.Percent, f.NBefore, f.NAfter)
	}
	return fmt.Sprintf("Measured: %s. Estimated: %s. Note: %s.", m, est, f.Text)
}

// toolsLines describes the Trove MCP tools. Every number says whether it was
// measured (gateway requests) or estimated (session logs, token sizes).
func toolsLines(t *Tools) []string {
	a := t.Adoption
	var out []string
	if t.Registered {
		l := "Registered with your agent."
		if t.SchemaTokens != nil {
			l += fmt.Sprintf(" The tool definitions add about %s tokens to every request (estimated).", FormatTokens(t.SchemaTokens.Value))
		}
		out = append(out, l)
	} else {
		out = append(out, "Not registered now, but your session logs show the tools in use.")
	}
	switch {
	case a.Rate != nil:
		out = append(out, fmt.Sprintf("Use: Claude Code chose a Trove tool for %s of its file reads and searches (estimated from session logs; %d calls in %d session(s)).",
			FormatPercent(a.Rate.Value), a.Rate.N, a.ClaudeSessions))
	case t.Registered:
		out = append(out, "Use: no Claude Code file reads or searches in this period yet. If your agent ignores the tools, `trove mcp snippet` prints a few lines for CLAUDE.md or AGENTS.md.")
	}
	if a.TroveRun > 0 || a.CodexTroveCalls > 0 {
		var parts []string
		if a.TroveRun > 0 {
			parts = append(parts, fmt.Sprintf("trove_run %d calls (Claude Code also made %d Bash calls, most not test or build runs)", a.TroveRun, a.BuiltinBash))
		}
		if a.CodexTroveCalls > 0 {
			parts = append(parts, fmt.Sprintf("Codex made %d Trove tool calls (no built-in comparison for Codex)", a.CodexTroveCalls))
		}
		out = append(out, "Also: "+strings.Join(parts, "; ")+" (estimated from session logs).")
	}
	switch {
	case a.FetchBack != nil:
		l := fmt.Sprintf("Fetch-back (measured): %d trove_fetch_full calls for %d trimmed outputs, %s.", a.Fetches, a.Trimmed, FormatPercent(a.FetchBack.Value))
		if a.TooAggressive {
			l += " That is high: the agent often needs what was trimmed, so trimming may be too aggressive. Consider setting output_trimmer = \"shadow\" under [optimizers]; it applies from the next session."
		}
		out = append(out, l)
	case a.Trimmed > 0:
		out = append(out, fmt.Sprintf("Fetch-back (measured): %s (%d trimmed outputs so far, %d needed).", measure.TextNotEnough, a.Trimmed, measure.MinFetchBackSample))
	}
	return out
}
