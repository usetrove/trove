package measure

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/usetrove/trove/internal/analyze"
	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/store"
)

// Grouping choices for TotalsBy.
const (
	ByAgent = "agent"
	ByModel = "model"
	ByDay   = "day"
	ByKind  = "kind" // interactive, automated or unknown, as the user declared
)

// groupExpr maps a grouping to a fixed SQL expression; user input never reaches SQL.
var groupExpr = map[string]string{
	ByAgent: `agent`,
	ByModel: `model`,
	ByDay:   `strftime('%Y-%m-%d', ts / 1000, 'unixepoch')`,
	ByKind:  `kind`,
}

// TotalsRow is usage for one agent, model or day.
type TotalsRow struct {
	Key              string `json:"key"`
	Requests         int    `json:"requests"`
	InputTokens      int64  `json:"input_uncached_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	// CostUSD is provider-reported token usage (measured) priced at published
	// list prices, so the dollar figure is estimated: it is not an invoice.
	// Unpriced counts requests left out because they could not be priced exactly.
	CostUSD Figure `json:"cost_usd"`
}

// TotalsBy sums recorded usage in w by agent, model or day, sorted by key.
func TotalsBy(ctx context.Context, db *store.DB, w Window, by string) ([]TotalsRow, error) {
	expr, ok := groupExpr[by]
	if !ok {
		return nil, fmt.Errorf("cannot group by %q; use agent, model, day or kind", by)
	}
	rows, err := db.QueryContext(ctx, `SELECT `+expr+` AS k, COUNT(*), SUM(input_uncached_tokens), SUM(cache_write_tokens),
		SUM(cache_read_tokens), SUM(output_tokens), COALESCE(SUM(cost_usd), 0), SUM(CASE WHEN cost_usd IS NULL THEN 1 ELSE 0 END)
		FROM requests WHERE ts >= ? AND ts < ? AND status_code > 0 AND status_code < 400 GROUP BY k ORDER BY k`,
		w.From.UnixMilli(), w.To.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []TotalsRow{}
	for rows.Next() {
		var r TotalsRow
		var cost float64
		var unpriced int
		if err := rows.Scan(&r.Key, &r.Requests, &r.InputTokens, &r.CacheWriteTokens, &r.CacheReadTokens, &r.OutputTokens, &cost, &unpriced); err != nil {
			return nil, err
		}
		r.CostUSD = Figure{Value: roundUSD(cost), Label: Estimated, N: r.Requests - unpriced, Unpriced: unpriced}
		out = append(out, r)
	}
	return out, rows.Err()
}

// WasteRow is the waste one detector found.
type WasteRow struct {
	Category string `json:"category"`
	Tokens   Figure `json:"tokens"`
	USD      Figure `json:"usd"`
}

// WasteByCategory sums the analyzer's findings in w. Every figure is estimated,
// and categories can overlap, so the rows are not meant to be added up.
func WasteByCategory(ctx context.Context, db *store.DB, w Window) ([]WasteRow, error) {
	return wasteByCategory(ctx, db, `SELECT findings FROM requests WHERE ts >= ? AND ts < ? AND findings IS NOT NULL`,
		w.From.UnixMilli(), w.To.UnixMilli())
}

// wasteByCategory is WasteByCategory over any query that returns one findings
// JSON column per row.
func wasteByCategory(ctx context.Context, db *store.DB, query string, args ...any) ([]WasteRow, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	type acc struct {
		tokens   int64
		usd      float64
		n        int
		unpriced int
	}
	by := map[string]*acc{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var fs []analyze.Finding
		if json.Unmarshal([]byte(raw), &fs) != nil {
			continue
		}
		for _, f := range fs {
			x := by[f.Detector]
			if x == nil {
				x = &acc{}
				by[f.Detector] = x
			}
			x.n++
			x.tokens += f.Tokens
			if f.USD != nil {
				x.usd += *f.USD
			} else {
				x.unpriced++
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(by))
	for n := range by {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]WasteRow, 0, len(names))
	for _, n := range names {
		x := by[n]
		out = append(out, WasteRow{Category: n,
			Tokens: Figure{Value: float64(x.tokens), Label: Estimated, N: x.n},
			USD:    Figure{Value: roundUSD(x.usd), Label: Estimated, N: x.n, Unpriced: x.unpriced}})
	}
	return out, nil
}

// ShadowRow is what an optimizer that is not on would have saved.
type ShadowRow struct {
	Optimizer string `json:"optimizer"`
	Tokens    Figure `json:"tokens"`
	USD       Figure `json:"usd"`
	Sessions  int    `json:"sessions"`
	// Events is how many times it would have acted, when the source counts that. Not in the JSON.
	Events int `json:"-"`
}

// ShadowEstimates reports the shadow-mode estimate for every optimizer that is
// not currently on (not configured on, or rolled back). Each optimizer is
// valued on its own, so rows overlap and must not be added together.
func ShadowEstimates(ctx context.Context, db *store.DB, w Window, cfg config.Config) ([]ShadowRow, error) {
	ru, err := analyze.WindowRollup(ctx, db, w.From, w.To)
	if err != nil {
		return nil, err
	}
	rolled, err := Overrides(ctx, db)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(ru.Optimizers))
	for n := range ru.Optimizers {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []ShadowRow
	for _, n := range names {
		_, back := rolled[n]
		// A gateway behavior set to "on" still only estimates (none has an active mode), so its
		// shadow figure is still what it "would" save and must not be hidden.
		if cfg.OptimizerState(n) == config.StateOn && !back && !config.IsGatewayBehavior(n) {
			continue
		}
		o := ru.Optimizers[n]
		sessions, err := sessionsWithShadow(ctx, db, w, n)
		if err != nil {
			return nil, err
		}
		out = append(out, ShadowRow{Optimizer: n, Sessions: sessions,
			Tokens: Figure{Value: float64(o.Tokens), Label: Estimated, N: o.Requests},
			USD:    Figure{Value: roundUSD(o.USD), Label: Estimated, N: o.Requests, Unpriced: o.UnpricedRequests}})
	}
	return out, nil
}

func sessionsWithShadow(ctx context.Context, db *store.DB, w Window, optimizer string) (int, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT session_key) FROM requests
		WHERE ts >= ? AND ts < ? AND shadow_savings LIKE ?`, w.From.UnixMilli(), w.To.UnixMilli(), `%"`+optimizer+`":%`).Scan(&n)
	if err != nil && err != sql.ErrNoRows {
		return 0, err
	}
	return n, nil
}

// Offer tells the user an optimizer looks worth enabling. Nothing is changed.
type Offer struct {
	Optimizer string `json:"optimizer"`
	Estimate  ShadowRow
	// Enable is the exact line to put under [optimizers] in config.toml.
	Enable string `json:"enable"`
	Note   string `json:"note"`
}

// OfferRules sets when an offer is made.
type OfferRules struct {
	MinSessions int     // sessions the estimate must rest on
	MinUSD      float64 // estimated dollars over the window
	// Recoverable is true when trimmed output can be fetched back (the Trove MCP
	// server is registered), which is what lets output_trimmer be offered.
	Recoverable bool
}

// DefaultOfferRules are modest: enough sessions to be more than one lucky day,
// and enough money to be worth the switch.
var DefaultOfferRules = OfferRules{MinSessions: 10, MinUSD: 1}

// Offers returns the optimizers whose shadow estimate passes the rules. No
// offers are made during the baseline, and none for optimizers that cannot be
// turned on (output_trimmer until the MCP server is registered) or that are
// rolled back.
func Offers(ctx context.Context, db *store.DB, w Window, cfg config.Config, rules OfferRules, now time.Time) ([]Offer, error) {
	st, _, err := LoadStatus(ctx, db, cfg.Measure, now)
	if err != nil {
		return nil, err
	}
	if st.Phase == PhaseBaseline {
		return nil, nil
	}
	shadows, err := ShadowEstimates(ctx, db, w, cfg)
	if err != nil {
		return nil, err
	}
	rolled, err := Overrides(ctx, db)
	if err != nil {
		return nil, err
	}
	var out []Offer
	for _, s := range shadows {
		if _, back := rolled[s.Optimizer]; back {
			continue
		}
		if (s.Optimizer == config.OptimizerOutputTrimmer && !rules.Recoverable) || s.Optimizer == analyze.OptPrune || config.IsGatewayBehavior(s.Optimizer) {
			continue // cannot be switched on (a gateway behavior has only its shadow mode)
		}
		if s.Sessions < rules.MinSessions || s.USD.Value < rules.MinUSD {
			continue
		}
		out = append(out, Offer{Optimizer: s.Optimizer, Estimate: s, Enable: fmt.Sprintf("%s = \"on\"", s.Optimizer),
			Note: "estimated from shadow mode; nothing turns on unless you add this line under [optimizers] in config.toml, and it applies from the next session"})
	}
	return out, nil
}
