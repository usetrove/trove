package measure

import (
	"context"
	"encoding/json"

	"github.com/usetrove/trove/internal/analyze"
	"github.com/usetrove/trove/internal/store"
)

// WasteTotal is the waste found in w as one number that does not double count.
// Findings in one request can overlap (the same tokens may be both a duplicate
// and oversized), so the categories from WasteByCategory cannot be added up.
// Here each request contributes only its largest finding, which makes the total
// a lower bound: the real waste is at least this much, as estimated by the
// analyzer. Unpriced counts requests whose largest finding could not be priced.
type WasteTotal struct {
	Tokens Figure `json:"tokens"`
	USD    Figure `json:"usd"`
}

// WasteTotalIn computes WasteTotal for w. Both figures are estimated.
func WasteTotalIn(ctx context.Context, db *store.DB, w Window) (WasteTotal, error) {
	rows, err := db.QueryContext(ctx, `SELECT findings FROM requests WHERE ts >= ? AND ts < ? AND findings IS NOT NULL`,
		w.From.UnixMilli(), w.To.UnixMilli())
	if err != nil {
		return WasteTotal{}, err
	}
	defer func() { _ = rows.Close() }()
	var tokens int64
	var usd float64
	n, unpriced := 0, 0
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return WasteTotal{}, err
		}
		var fs []analyze.Finding
		if json.Unmarshal([]byte(raw), &fs) != nil {
			continue
		}
		var bestTok int64
		var bestUSD float64
		var found, priced bool
		for _, f := range fs {
			if f.Tokens <= 0 && (f.USD == nil || *f.USD <= 0) {
				continue
			}
			found = true
			if f.Tokens > bestTok {
				bestTok = f.Tokens
			}
			if f.USD != nil {
				priced = true
				if *f.USD > bestUSD {
					bestUSD = *f.USD
				}
			}
		}
		if !found {
			continue
		}
		n++
		tokens += bestTok
		usd += bestUSD
		if !priced {
			unpriced++
		}
	}
	if err := rows.Err(); err != nil {
		return WasteTotal{}, err
	}
	return WasteTotal{
		Tokens: Figure{Value: float64(tokens), Label: Estimated, N: n},
		USD:    Figure{Value: roundUSD(usd), Label: Estimated, N: n, Unpriced: unpriced},
	}, nil
}
