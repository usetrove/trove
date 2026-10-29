package report

import (
	"context"
	"fmt"
	"sort"

	"github.com/usetrove/trove/internal/logs"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/store"
)

// HistorySource is the agent label history rows carry in the by-agent table.
// "(history)" tells the reader these rows come from the agent's own logs, not
// from the gateway.
const historyAgentSuffix = " (history)"

// History describes the part of the report that came from agents' local
// session logs. It is nil when there is none.
type History struct {
	Requests int `json:"requests"`
	Sessions int `json:"sessions"`
	// ExcludedSessions were also seen by the gateway; the gateway's measured
	// rows are used and these history rows are left out, so nothing is counted twice.
	ExcludedSessions int `json:"excluded_sessions"`
	Files            int `json:"files"`
	// SkippedLines are history lines that could not be read (corrupted or in a
	// format Trove does not know). They are counted, never guessed.
	SkippedLines int64 `json:"skipped_lines"`
	// Plan is how history usage is paid for: api_key, subscription, unknown or
	// mixed. Estimated: it is inferred from local settings, not confirmed.
	Plan         string   `json:"plan"`
	PlanEvidence []string `json:"plan_evidence,omitempty"`
	// Value is dollars spent when Plan is api_key (estimated at list prices).
	// For any other plan it is API-equivalent value, which is not a bill.
	Value measure.Figure `json:"value_usd"`
	Basis string         `json:"basis"` // spend or api_equivalent_value
	// ByModel is usage under a plan that is not paid per token, priced at API
	// list prices (estimated). Empty when all history is paid per token.
	ByModel []measure.TotalsRow `json:"by_model,omitempty"`
	// GatewaySeen is true once the gateway has recorded any request. Optimizers
	// need it.
	GatewaySeen bool `json:"gateway_seen"`
	// NotAvailable lists detectors that need data logs do not keep.
	NotAvailable map[string]string `json:"not_available"`
}

// Basis values.
const (
	BasisSpend         = "spend"
	BasisAPIEquivalent = "api_equivalent_value"
)

type historyData struct {
	rows      []measure.HistoryRow
	h         History
	waste     measure.WasteTotal
	cats      []measure.WasteRow
	apiRows   []measure.TotalsRow // history usage paid per token
	otherReqs int
	otherTok  float64
	otherUSD  measure.Figure // API-equivalent value of the rest
}

// loadHistory reads history usage and waste for w. It returns nil when there is
// none to show.
func loadHistory(ctx context.Context, db *store.DB, w measure.Window, in Inputs) (*historyData, error) {
	rows, err := measure.HistoryTotals(ctx, db, w)
	if err != nil {
		return nil, err
	}
	overlap, err := measure.HistoryOverlap(ctx, db, w)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 && overlap == 0 {
		return nil, nil
	}
	d := &historyData{rows: rows, otherUSD: measure.Figure{Label: measure.Estimated}}
	d.h.ExcludedSessions = overlap
	d.h.NotAvailable = logs.SkippedDetectors
	if d.h.Sessions, err = measure.HistorySessions(ctx, db, w); err != nil {
		return nil, err
	}
	fs, err := measure.HistoryFileStats(ctx, db)
	if err != nil {
		return nil, err
	}
	d.h.Files, d.h.SkippedLines = fs.Files, fs.SkippedLines
	d.h.PlanEvidence = in.HistoryPlanEvidence
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM requests)`).Scan(&d.h.GatewaySeen); err != nil {
		return nil, err
	}

	plans := map[string]bool{}
	for _, r := range rows {
		plans[r.Plan] = true
		d.h.Requests += r.Requests
		if r.Plan == string(logs.PlanAPIKey) {
			d.apiRows = append(d.apiRows, r.TotalsRow)
			continue
		}
		d.otherReqs += r.Requests
		d.h.ByModel = append(d.h.ByModel, r.TotalsRow)
		d.otherTok += float64(r.InputTokens + r.CacheWriteTokens + r.CacheReadTokens + r.OutputTokens)
		d.otherUSD.Value += r.CostUSD.Value
		d.otherUSD.N += r.CostUSD.N
		d.otherUSD.Unpriced += r.CostUSD.Unpriced
	}
	d.otherUSD.Value = round6(d.otherUSD.Value)
	switch {
	case len(plans) == 0:
		d.h.Plan = string(logs.PlanUnknown)
	case len(plans) > 1:
		d.h.Plan = "mixed"
	default:
		for p := range plans {
			d.h.Plan = p
		}
	}
	apiSpend, _, _ := sumTotals(d.apiRows)
	d.h.Value = d.otherUSD
	d.h.Basis = BasisAPIEquivalent
	if d.h.Plan == string(logs.PlanAPIKey) {
		d.h.Value, d.h.Basis = apiSpend, BasisSpend
	} else if apiSpend.Value > 0 || len(d.apiRows) > 0 { // mixed: report both on the API-equivalent basis
		d.h.Value.Value = round6(d.h.Value.Value + apiSpend.Value)
		d.h.Value.N += apiSpend.N
		d.h.Value.Unpriced += apiSpend.Unpriced
	}

	if d.waste, err = measure.HistoryWasteTotal(ctx, db, w); err != nil {
		return nil, err
	}
	if d.cats, err = measure.HistoryWasteByCategory(ctx, db, w); err != nil {
		return nil, err
	}
	return d, nil
}

// mergeSpend adds history usage that is paid per token to the gateway's tables.
// By agent, history rows stay on their own line (key "claude_code (history)");
// by model they are added to the gateway's row for the same model.
func (d *historyData) mergeSpend(byAgent, byModel []measure.TotalsRow) ([]measure.TotalsRow, []measure.TotalsRow) {
	if d == nil || len(d.apiRows) == 0 {
		return byAgent, byModel
	}
	var all measure.TotalsRow
	all.Key = logs.SourceClaudeCode + historyAgentSuffix
	all.CostUSD = measure.Figure{Label: measure.Estimated}
	idx := map[string]int{}
	for i, r := range byModel {
		idx[r.Key] = i
	}
	for _, r := range d.apiRows {
		all = addTotals(all, r)
		if i, ok := idx[r.Key]; ok {
			byModel[i] = addTotals(byModel[i], r)
		} else {
			idx[r.Key] = len(byModel)
			byModel = append(byModel, r)
		}
	}
	byAgent = append(byAgent, all)
	sortByCost(byAgent)
	sortByCost(byModel)
	return byAgent, byModel
}

func addTotals(a, b measure.TotalsRow) measure.TotalsRow {
	a.Requests += b.Requests
	a.InputTokens += b.InputTokens
	a.CacheWriteTokens += b.CacheWriteTokens
	a.CacheReadTokens += b.CacheReadTokens
	a.OutputTokens += b.OutputTokens
	a.CostUSD.Value = round6(a.CostUSD.Value + b.CostUSD.Value)
	a.CostUSD.N += b.CostUSD.N
	a.CostUSD.Unpriced += b.CostUSD.Unpriced
	return a
}

// mergeWasteCats adds history waste categories to the gateway's.
func mergeWasteCats(gw, hist []measure.WasteRow) []measure.WasteRow {
	if len(hist) == 0 {
		return gw
	}
	by := map[string]measure.WasteRow{}
	for _, r := range gw {
		by[r.Category] = r
	}
	for _, r := range hist {
		g, ok := by[r.Category]
		if !ok {
			by[r.Category] = r
			continue
		}
		g.Tokens.Value += r.Tokens.Value
		g.Tokens.N += r.Tokens.N
		g.USD.Value = round6(g.USD.Value + r.USD.Value)
		g.USD.N += r.USD.N
		g.USD.Unpriced += r.USD.Unpriced
		by[r.Category] = g
	}
	out := make([]measure.WasteRow, 0, len(by))
	for _, r := range by {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Category < out[j].Category })
	return out
}

func addWaste(a, b measure.WasteTotal) measure.WasteTotal {
	a.Tokens.Value += b.Tokens.Value
	a.Tokens.N += b.Tokens.N
	a.USD.Value = round6(a.USD.Value + b.USD.Value)
	a.USD.N += b.USD.N
	a.USD.Unpriced += b.USD.Unpriced
	return a
}

func apiEquivText(h Headline) string {
	s := fmt.Sprintf("%s (estimated at API list prices; what this usage would cost per token, not a bill)", Est(*h.APIEquivalent))
	if h.APIEquivalent.Unpriced > 0 {
		s += fmt.Sprintf(". %d requests could not be priced, so this is at least this much", h.APIEquivalent.Unpriced)
	}
	return s
}

func historyText(h *History) string {
	s := fmt.Sprintf("%d requests in %d sessions read from your agent's local logs (estimated, not seen by the gateway)", h.Requests, h.Sessions)
	switch h.Plan {
	case string(logs.PlanAPIKey):
		s += ". Plan: API key (estimated from local settings), so this counts as spend"
	case string(logs.PlanSubscription):
		s += ". Plan: subscription (estimated from local settings), so this is API-equivalent value, not spend"
	case "mixed":
		s += ". Plans differ between sessions"
	default:
		s += ". Plan: could not be told from local settings, so this is shown as API-equivalent value. Set [plan] in config.toml to change that"
	}
	if h.ExcludedSessions > 0 {
		s += fmt.Sprintf(". %d sessions also went through the gateway and are counted there only", h.ExcludedSessions)
	}
	if h.SkippedLines > 0 {
		s += fmt.Sprintf(". %d log lines could not be read and were skipped", h.SkippedLines)
	}
	return s
}
