package measure

import (
	"context"

	"github.com/usetrove/trove/internal/store"
)

// SpentUSD is what the gateway's ledger says was spent on successful requests in w,
// at list prices (an estimate, not an invoice), and how many of those requests could
// not be priced and so are not in the total. It counts the same requests as TotalsBy,
// so a budget cap checked with it agrees with the totals in the report.
func SpentUSD(ctx context.Context, db *store.DB, w Window) (usd float64, unpriced int, err error) {
	err = db.QueryRowContext(ctx, `SELECT COALESCE(SUM(cost_usd), 0), COALESCE(SUM(CASE WHEN cost_usd IS NULL THEN 1 ELSE 0 END), 0)
		FROM requests WHERE ts >= ? AND ts < ? AND status_code > 0 AND status_code < 400`,
		w.From.UnixMilli(), w.To.UnixMilli()).Scan(&usd, &unpriced)
	return usd, unpriced, err
}
