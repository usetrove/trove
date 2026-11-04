package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ExperimentRow is one stored A/B result: numbers and labels only.
type ExperimentRow struct {
	Optimizer  string
	Metric     string
	Value      float64
	Low        float64
	High       float64
	Confidence string
	NOn        int
	NOff       int
	Label      string // "measured" or "estimated"
	Status     string
	InputsHash string
}

// Experiments stores A/B results (internal/experiment).
type Experiments struct{ db *DB }

// Experiments returns the experiment result store for d.
func (d *DB) Experiments() *Experiments { return &Experiments{db: d} }

// Put stores rows computed at time at. It does nothing when rows is empty or
// when the newest stored rows for the optimizer came from identical inputs, so
// re-running an analysis does not add duplicates.
func (e *Experiments) Put(ctx context.Context, at time.Time, rows []ExperimentRow) error {
	if len(rows) == 0 {
		return nil
	}
	var last string
	err := e.db.QueryRowContext(ctx, `SELECT inputs_hash FROM experiment_results WHERE optimizer = ? ORDER BY id DESC LIMIT 1`,
		rows[0].Optimizer).Scan(&last)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && last == rows[0].InputsHash {
		return nil
	}
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, r := range rows {
		if _, err := tx.ExecContext(ctx, `INSERT INTO experiment_results
			(optimizer, computed_at, metric, value, low, high, confidence, n_on, n_off, label, status, inputs_hash)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, r.Optimizer, at.UnixMilli(), r.Metric, r.Value, r.Low, r.High, r.Confidence,
			r.NOn, r.NOff, r.Label, r.Status, r.InputsHash); err != nil {
			return err
		}
	}
	return tx.Commit()
}
