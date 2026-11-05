package store

import (
	"context"
	"database/sql"
	"time"
)

// AdapterEvent is one row of adapter_events (migration 0015): a decision or a
// harness report from a plugin. Metadata only (CLAUDE.md rule 7).
type AdapterEvent struct {
	TS             time.Time
	SessionKey     string // hashed, never the harness's own id
	Harness        string
	Login          string // api_key | copilot | subscription_or_unknown
	Provider       string
	Model          string
	Kind           string
	Optimizer      string
	State          string
	Bytes          int64
	Tokens         int64
	OverheadTokens int64
	// Usage fields, set only on kind "usage"; nil when the harness did not report them.
	InTokens, OutTokens, CacheReadTokens, CacheWriteTokens *int64
	CostUSD                                                *float64
	Detail                                                 string
}

// AdapterStore reads and writes adapter_events.
type AdapterStore struct{ db *DB }

// AdapterEvents returns the adapter event store for d.
func (d *DB) AdapterEvents() *AdapterStore { return &AdapterStore{db: d} }

// Append stores events in one transaction. It does nothing for an empty list.
func (s *AdapterStore) Append(ctx context.Context, events []AdapterEvent) error {
	if len(events) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	st, err := tx.PrepareContext(ctx, `INSERT INTO adapter_events
		(ts, session_key, harness, login, provider, model, kind, optimizer, state, bytes, tokens, overhead_tokens,
		 in_tokens, out_tokens, cache_read_tokens, cache_write_tokens, cost_usd, detail)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	for _, e := range events {
		if _, err := st.ExecContext(ctx, e.TS.UnixMilli(), e.SessionKey, e.Harness, e.Login, e.Provider, e.Model, e.Kind, e.Optimizer, e.State,
			e.Bytes, e.Tokens, e.OverheadTokens, nullInt(e.InTokens), nullInt(e.OutTokens), nullInt(e.CacheReadTokens), nullInt(e.CacheWriteTokens),
			nullFloat(e.CostUSD), e.Detail); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func nullInt(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func nullFloat(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

// AdapterDecisionRow is the decisions one optimizer made in one harness and login.
type AdapterDecisionRow struct {
	Harness, Login, Optimizer, State string
	Events                           int64
	Changed                          int64 // rewritten, blocked or advised: the call was really changed
	Tokens                           int64 // estimated tokens kept out of context, all kinds
	OverheadTokens                   int64
}

// AdapterUsageRow is the usage a harness reported, per harness and login.
type AdapterUsageRow struct {
	Harness, Login          string
	Calls                   int64
	In, Out, CacheR, CacheW int64
	CostUSD                 float64
	CostKnown               int64 // calls that reported a cost
	Sessions                int64
}

// Decisions groups decision rows since a time by harness, login, optimizer and state.
func (s *AdapterStore) Decisions(ctx context.Context, since time.Time) ([]AdapterDecisionRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT harness, login, optimizer, state, COUNT(*),
		SUM(CASE WHEN kind IN ('rewritten','blocked','advised') THEN 1 ELSE 0 END),
		COALESCE(SUM(tokens),0), COALESCE(SUM(overhead_tokens),0)
		FROM adapter_events WHERE ts >= ? AND kind NOT IN ('usage','outcome')
		GROUP BY harness, login, optimizer, state ORDER BY harness, login, optimizer, state`, since.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []AdapterDecisionRow
	for rows.Next() {
		var r AdapterDecisionRow
		if err := rows.Scan(&r.Harness, &r.Login, &r.Optimizer, &r.State, &r.Events, &r.Changed, &r.Tokens, &r.OverheadTokens); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Usage groups usage rows since a time by harness and login.
func (s *AdapterStore) Usage(ctx context.Context, since time.Time) ([]AdapterUsageRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT harness, login, COUNT(*),
		COALESCE(SUM(in_tokens),0), COALESCE(SUM(out_tokens),0), COALESCE(SUM(cache_read_tokens),0), COALESCE(SUM(cache_write_tokens),0),
		COALESCE(SUM(cost_usd),0), SUM(CASE WHEN cost_usd IS NOT NULL THEN 1 ELSE 0 END), COUNT(DISTINCT session_key)
		FROM adapter_events WHERE ts >= ? AND kind = 'usage'
		GROUP BY harness, login ORDER BY harness, login`, since.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []AdapterUsageRow
	for rows.Next() {
		var r AdapterUsageRow
		var known sql.NullInt64
		if err := rows.Scan(&r.Harness, &r.Login, &r.Calls, &r.In, &r.Out, &r.CacheR, &r.CacheW, &r.CostUSD, &known, &r.Sessions); err != nil {
			return nil, err
		}
		r.CostKnown = known.Int64
		out = append(out, r)
	}
	return out, rows.Err()
}
