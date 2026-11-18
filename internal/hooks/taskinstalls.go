package hooks

import (
	"context"

	"github.com/usetrove/trove/internal/store"
)

// installedTasks lists the scout and cascade features still installed (not reverted) and the time, in
// milliseconds, the earliest of them was installed (0 when none is). Names and times only.
func installedTasks(ctx context.Context, db *store.DB) (features []string, sinceMS int64, err error) {
	rows, err := db.QueryContext(ctx, `SELECT t.feature, MIN(f.applied_at) FROM task_installs t
		JOIN fixes f ON f.change_id = t.change_id WHERE f.reverted_at IS NULL GROUP BY t.feature ORDER BY t.feature`)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var name string
		var at int64
		if err := rows.Scan(&name, &at); err != nil {
			return nil, 0, err
		}
		features = append(features, name)
		if sinceMS == 0 || at < sinceMS {
			sinceMS = at
		}
	}
	return features, sinceMS, rows.Err()
}
