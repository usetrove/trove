package store

import (
	"context"
	"time"
)

// Tuning states.
const (
	TuningSuggested = "suggested"
	TuningApplied   = "applied"
	TuningReverted  = "reverted"
)

// TuningRow is one tuned parameter of one optimizer in one repo: a hashed repo
// key, names and numbers only.
type TuningRow struct {
	RepoKey    string
	Optimizer  string
	Param      string
	Value      int
	State      string
	TunedAt    time.Time
	Source     string
	BeforeRate float64
	NBefore    int
}

// Tunings stores per-repo tuning (internal/tuning).
type Tunings struct{ db *DB }

// Tuning returns the tuning store for d.
func (d *DB) Tuning() *Tunings { return &Tunings{db: d} }

// Suggest stores rows as suggestions. A parameter that is already applied is left
// alone: a suggestion never replaces a value in effect.
func (t *Tunings) Suggest(ctx context.Context, rows []TuningRow) error {
	tx, err := t.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, r := range rows {
		if _, err := tx.ExecContext(ctx, `INSERT INTO repo_tuning (repo_key, optimizer, param, value, state, tuned_at, source, before_rate, n_before)
			VALUES (?,?,?,?, 'suggested', ?, ?, ?, ?)
			ON CONFLICT(repo_key, optimizer, param) DO UPDATE SET value=excluded.value, state='suggested',
				tuned_at=excluded.tuned_at, source=excluded.source, before_rate=excluded.before_rate, n_before=excluded.n_before
			WHERE repo_tuning.state != 'applied'`,
			r.RepoKey, r.Optimizer, r.Param, r.Value, r.TunedAt.UnixMilli(), r.Source, r.BeforeRate, r.NBefore); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Apply puts a repo's suggested values in effect from at and returns how many.
// New sessions pin them; a session already running keeps what it started with.
func (t *Tunings) Apply(ctx context.Context, repoKey string, at time.Time) (int, error) {
	res, err := t.db.ExecContext(ctx, `UPDATE repo_tuning SET state='applied', tuned_at=? WHERE repo_key=? AND state='suggested'`,
		at.UnixMilli(), repoKey)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// Revert marks a repo's applied values for an optimizer as reverted, so the
// shipped defaults apply to new sessions again.
func (t *Tunings) Revert(ctx context.Context, repoKey, optimizer string) error {
	_, err := t.db.ExecContext(ctx, `UPDATE repo_tuning SET state='reverted' WHERE repo_key=? AND optimizer=? AND state='applied'`,
		repoKey, optimizer)
	return err
}

// Reset removes everything stored for a repo, so the shipped defaults apply.
func (t *Tunings) Reset(ctx context.Context, repoKey string) error {
	_, err := t.db.ExecContext(ctx, `DELETE FROM repo_tuning WHERE repo_key=?`, repoKey)
	return err
}

// List returns a repo's rows, ordered by optimizer and parameter. An empty
// repoKey returns every repo's.
func (t *Tunings) List(ctx context.Context, repoKey string) ([]TuningRow, error) {
	q := `SELECT repo_key, optimizer, param, value, state, tuned_at, source, before_rate, n_before FROM repo_tuning`
	var args []any
	if repoKey != "" {
		q += ` WHERE repo_key = ?`
		args = append(args, repoKey)
	}
	rows, err := t.db.QueryContext(ctx, q+` ORDER BY repo_key, optimizer, param`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []TuningRow
	for rows.Next() {
		var r TuningRow
		var at int64
		if err := rows.Scan(&r.RepoKey, &r.Optimizer, &r.Param, &r.Value, &r.State, &at, &r.Source, &r.BeforeRate, &r.NBefore); err != nil {
			return nil, err
		}
		r.TunedAt = time.UnixMilli(at).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}
