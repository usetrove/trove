package measure

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/usetrove/trove/internal/measure/limitfit"
	"github.com/usetrove/trove/internal/optimize"
	"github.com/usetrove/trove/internal/pricing"
	"github.com/usetrove/trove/internal/store"
)

// thinking_clear's shadow verdict. Each gateway request where the optimizer would have acted stores its estimate in
// optimizations_applied (optimize.ClearEstimate): the old thinking it would not have sent, which was read from the
// cache, and on the first request of a new turn the context after the newly cleared thinking, which would have been
// written to the cache again instead of read. This weighs both sides with the user's plan-limit fit when it covers
// every model seen (weights per token type, or API prices on a fitted scale), and with API list prices otherwise,
// and says whether the saving beats the cost. Autopilot may only promote the optimizer when it does.

// ThinkingClearMinRequests is how many requests with an estimate the verdict needs before it can say "net positive".
const ThinkingClearMinRequests = 20

// ThinkingClearWindow is how far back the verdict reads.
const ThinkingClearWindow = 14 * 24 * time.Hour

// ThinkingClearVerdict is the weighed shadow estimate. Every figure is estimated.
type ThinkingClearVerdict struct {
	Requests int `json:"requests"`
	Sessions int `json:"sessions"`
	// Saved and Lost are token totals (estimated from sizes).
	Saved int64 `json:"saved_tokens"`
	Lost  int64 `json:"lost_cache_tokens"`
	// SavedValue, LostValue and Net are in Unit: "% of weekly limit" or "USD".
	SavedValue float64 `json:"saved_value"`
	LostValue  float64 `json:"lost_value"`
	Net        float64 `json:"net_value"`
	Unit       string  `json:"unit"`
	// Basis says what weighed them: the meter fit, API prices on the fitted scale, or API prices.
	Basis       string `json:"basis"`
	NetPositive bool   `json:"net_positive"`
	Label       Label  `json:"label"`
}

// Text is the verdict in one line, labelled.
func (v ThinkingClearVerdict) Text() string {
	if v.Requests == 0 {
		return "no request was long enough to clear any thinking yet"
	}
	return fmt.Sprintf("over %d requests in %d sessions it would have saved %d tokens of old thinking and rewritten %d tokens of cache: %.4g against %.4g %s, net %.4g (estimated, weighed by %s)",
		v.Requests, v.Sessions, v.Saved, v.Lost, v.SavedValue, v.LostValue, v.Unit, v.Net, v.Basis)
}

// ThinkingClearShadow reads the estimates since from and weighs them. dir is the data directory, for the limit fit.
func ThinkingClearShadow(ctx context.Context, db *store.DB, dir string, from, to time.Time) (ThinkingClearVerdict, error) {
	v := ThinkingClearVerdict{Label: Estimated}
	rows, err := db.QueryContext(ctx, `SELECT session_key, optimizations_applied FROM requests
		WHERE ts >= ? AND ts < ? AND optimizations_applied LIKE ?`, from.UnixMilli(), to.UnixMilli(), `%"`+optimize.OptimizerThinkingClear+`"%`)
	if err != nil {
		return v, err
	}
	type sums struct{ saved, lost int64 }
	byModel := map[string]*sums{}
	sessions := map[string]bool{}
	for rows.Next() {
		var key string
		var raw []byte
		if err := rows.Scan(&key, &raw); err != nil {
			_ = rows.Close()
			return v, err
		}
		var applied []optimize.Applied
		if json.Unmarshal(raw, &applied) != nil {
			continue
		}
		for _, a := range applied {
			if a.Name != optimize.OptimizerThinkingClear || a.Clear == nil || a.Clear.Model == "" {
				continue
			}
			s := byModel[a.Clear.Model]
			if s == nil {
				s = &sums{}
				byModel[a.Clear.Model] = s
			}
			s.saved += a.Clear.Saved
			s.lost += a.Clear.Lost
			v.Requests++
			sessions[key] = true
		}
	}
	if err := rows.Close(); err != nil {
		return v, err
	}
	if err := rows.Err(); err != nil {
		return v, err
	}
	v.Sessions = len(sessions)
	if v.Requests == 0 {
		return v, nil
	}
	models := make([]string, 0, len(byModel))
	for m := range byModel {
		models = append(models, m)
		v.Saved += byModel[m].saved
		v.Lost += byModel[m].lost
	}
	sort.Strings(models)
	tbl, _ := pricing.Default()
	fit := limitfit.ReadCache(dir)
	delta := func(model string, typ int, n int64) limitfit.Delta {
		d := limitfit.Delta{Model: model}
		d.Tokens[typ] = n
		limitfit.PriceDelta(tbl, &d, to)
		return d
	}
	// The limit is the unit only when the fit converts every model; otherwise every model is weighed in dollars.
	week := func(d limitfit.Delta) (float64, string, bool) {
		if d.Tokens == ([limitfit.NTypes]int64{}) {
			return 0, "", true
		}
		e := fit.Percent(d)
		if e == nil || e.Week == nil {
			return 0, "", false
		}
		return e.Week.Point, e.Week.Basis, true
	}
	useLimit := fit != nil
	type valued struct{ saved, lostWrite, lostRead float64 }
	vals := map[string]valued{}
	basis := ""
	for _, m := range models {
		s := byModel[m]
		sv, b1, ok1 := week(delta(m, limitfit.TCacheRead, s.saved))
		lw, b2, ok2 := week(delta(m, limitfit.TWrite5m, s.lost))
		lr, _, ok3 := week(delta(m, limitfit.TCacheRead, s.lost))
		if !ok1 || !ok2 || !ok3 {
			useLimit = false
			break
		}
		vals[m] = valued{sv, lw, lr}
		basis = firstNonEmptyBasis(basis, b1, b2)
	}
	if useLimit {
		v.Unit, v.Basis = "% of weekly limit", "your plan-limit fit, "+limitfit.BasisText[basis]
		for _, m := range models {
			x := vals[m]
			v.SavedValue += x.saved
			v.LostValue += x.lostWrite - x.lostRead
		}
	} else {
		v.Unit, v.Basis = "USD", "API list prices"
		for _, m := range models {
			s := byModel[m]
			v.SavedValue += delta(m, limitfit.TCacheRead, s.saved).USD
			v.LostValue += delta(m, limitfit.TWrite5m, s.lost).USD - delta(m, limitfit.TCacheRead, s.lost).USD
		}
	}
	v.Net = v.SavedValue - v.LostValue
	v.NetPositive = v.Requests >= ThinkingClearMinRequests && v.Net > 0
	return v, nil
}

func firstNonEmptyBasis(cur string, more ...string) string {
	if cur != "" {
		return cur
	}
	for _, m := range more {
		if m != "" {
			return m
		}
	}
	return ""
}
