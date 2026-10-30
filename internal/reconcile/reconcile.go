// Package reconcile compares what Trove recorded for a period with the total on
// the user's provider invoice. It exists to validate accuracy during the beta:
// the recorded figure is derived from provider-reported usage and list prices,
// the invoice figure is typed in by the user, and the gap is shown plainly.
package reconcile

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"sort"
	"time"
)

// DefaultTolerancePct is the agreed match tolerance: a recorded total within
// this percentage of the invoice counts as a match.
const DefaultTolerancePct = 2.0

// Verdicts.
const (
	VerdictMatch    = "match"
	VerdictMismatch = "mismatch"
)

// Input describes one reconciliation. From is inclusive, To is exclusive.
type Input struct {
	Provider     string
	From, To     time.Time
	InvoiceUSD   float64
	TolerancePct float64
}

// ModelRow is the recorded usage of one model in the period. Only token counts,
// request counts and cost are kept: no content.
type ModelRow struct {
	Model         string  `json:"model"`
	Requests      int64   `json:"requests"`
	Unpriced      int64   `json:"unpriced_requests"`
	InputUncached int64   `json:"input_uncached_tokens"`
	CacheWrite    int64   `json:"cache_write_tokens"`
	CacheRead     int64   `json:"cache_read_tokens"`
	Output        int64   `json:"output_tokens"`
	CostUSD       float64 `json:"cost_usd"`
}

// Result is the outcome. RecordedUSD is "measured" in the sense that the tokens
// are provider-reported, but it is priced from list prices, so it is a lower
// bound whenever UnpricedRequests is above zero.
type Result struct {
	Provider         string     `json:"provider"`
	From             string     `json:"from"`
	To               string     `json:"to_exclusive"`
	RecordedUSD      float64    `json:"recorded_usd"`
	InvoiceUSD       float64    `json:"invoice_usd"`
	DiffUSD          float64    `json:"diff_usd"` // recorded minus invoice
	DiffPct          float64    `json:"diff_pct"` // of the invoice; 0 when the invoice is 0
	TolerancePct     float64    `json:"tolerance_pct"`
	Verdict          string     `json:"verdict"`
	Requests         int64      `json:"requests"`
	UnpricedRequests int64      `json:"unpriced_requests"`
	PricingVersions  []string   `json:"pricing_versions"`
	Models           []ModelRow `json:"models"`
	Hints            []string   `json:"hints"`
}

// Query reads the per-model totals for the period. It counts the same requests
// as usage_daily (status 1 to 399) so the figure agrees with `trove report`.
func Query(ctx context.Context, db *sql.DB, in Input) ([]ModelRow, []string, error) {
	rows, err := db.QueryContext(ctx, `
SELECT model, COUNT(*), SUM(CASE WHEN cost_usd IS NULL THEN 1 ELSE 0 END),
       SUM(input_uncached_tokens), SUM(cache_write_tokens), SUM(cache_read_tokens),
       SUM(output_tokens), COALESCE(SUM(cost_usd), 0)
FROM requests
WHERE provider = ? AND ts >= ? AND ts < ? AND status_code BETWEEN 1 AND 399
GROUP BY model ORDER BY model`, in.Provider, in.From.UnixMilli(), in.To.UnixMilli())
	if err != nil {
		return nil, nil, fmt.Errorf("could not read the recorded requests: %v. Run `trove doctor` for details", err)
	}
	defer func() { _ = rows.Close() }()
	var out []ModelRow
	for rows.Next() {
		var m ModelRow
		if err := rows.Scan(&m.Model, &m.Requests, &m.Unpriced, &m.InputUncached, &m.CacheWrite, &m.CacheRead, &m.Output, &m.CostUSD); err != nil {
			return nil, nil, fmt.Errorf("could not read the recorded requests: %v", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("could not read the recorded requests: %v", err)
	}
	vr, err := db.QueryContext(ctx, `
SELECT DISTINCT pricing_version FROM requests
WHERE provider = ? AND ts >= ? AND ts < ? AND status_code BETWEEN 1 AND 399 AND pricing_version <> ''
ORDER BY pricing_version`, in.Provider, in.From.UnixMilli(), in.To.UnixMilli())
	if err != nil {
		return nil, nil, fmt.Errorf("could not read the pricing versions: %v", err)
	}
	defer func() { _ = vr.Close() }()
	var versions []string
	for vr.Next() {
		var v string
		if err := vr.Scan(&v); err != nil {
			return nil, nil, fmt.Errorf("could not read the pricing versions: %v", err)
		}
		versions = append(versions, v)
	}
	return out, versions, vr.Err()
}

// Compare is pure: the same rows and input always give the same result.
func Compare(in Input, models []ModelRow, versions []string) Result {
	tol := in.TolerancePct
	if tol <= 0 {
		tol = DefaultTolerancePct
	}
	r := Result{
		Provider:        in.Provider,
		From:            in.From.Format("2006-01-02"),
		To:              in.To.Format("2006-01-02"),
		InvoiceUSD:      round2(in.InvoiceUSD),
		TolerancePct:    tol,
		PricingVersions: append([]string{}, versions...),
		Models:          append([]ModelRow{}, models...),
	}
	sort.Slice(r.Models, func(i, j int) bool { return r.Models[i].Model < r.Models[j].Model })
	var recorded float64
	for _, m := range r.Models {
		recorded += m.CostUSD
		r.Requests += m.Requests
		r.UnpricedRequests += m.Unpriced
	}
	r.RecordedUSD = round2(recorded)
	r.DiffUSD = round2(recorded - in.InvoiceUSD)
	if in.InvoiceUSD > 0 {
		r.DiffPct = round2((recorded - in.InvoiceUSD) / in.InvoiceUSD * 100)
	}
	// Compare on the unrounded figures so the boundary is exact.
	within := false
	switch {
	case in.InvoiceUSD > 0:
		within = math.Abs(recorded-in.InvoiceUSD)/in.InvoiceUSD*100 <= tol
	default:
		within = recorded < 0.005
	}
	r.Verdict = VerdictMismatch
	if within {
		r.Verdict = VerdictMatch
	}
	r.Hints = hints(r)
	return r
}

func hints(r Result) []string {
	var h []string
	if r.Requests == 0 {
		h = append(h, "Trove recorded no requests for this provider in this period. Trove only sees traffic that went through its gateway; check `trove doctor` and that your agent points at it.")
	}
	if r.UnpricedRequests > 0 {
		h = append(h, fmt.Sprintf("%d request(s) use a model Trove has no price for, so the recorded total is a lower bound. Run `trove pricing show` to see which models are priced.", r.UnpricedRequests))
	}
	if r.Verdict == VerdictMatch {
		return h
	}
	if r.DiffUSD < 0 {
		h = append(h, "Recorded is below the invoice. Common causes: usage that did not go through the gateway (other machines, other tools, API use outside your agent), models without a price, or invoice items that are not token usage.")
	} else {
		h = append(h, "Recorded is above the invoice. Common causes: credits or discounts on the invoice, or a billing period that does not match these dates.")
	}
	h = append(h, "Check that the dates match your invoice's billing period and time zone (pass --utc if your provider bills in UTC).")
	if len(r.PricingVersions) > 1 {
		h = append(h, "More than one pricing data version priced this period; a price change mid-period can explain a small gap.")
	}
	return h
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
