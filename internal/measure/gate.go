package measure

import (
	"context"
	"database/sql"
	"errors"
	"hash/fnv"
	"strconv"
	"sync"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/store"
)

// Phase is where an install is in the lifecycle.
type Phase string

const (
	// PhaseBaseline: everything runs in shadow mode only.
	PhaseBaseline Phase = "baseline"
	// PhaseActive: baseline is done; optimizers run as the user configured them.
	PhaseActive Phase = "active"
)

// BaselineStatus describes progress through the baseline.
type BaselineStatus struct {
	Phase          Phase     `json:"phase"`
	InstalledAt    time.Time `json:"installed_at"`
	DaysNeeded     int       `json:"days_needed"`
	SessionsNeeded int       `json:"sessions_needed"`
	SessionsSeen   int       `json:"sessions_seen"`
	DaysElapsed    float64   `json:"days_elapsed"`
}

// PhaseFor decides the phase. Both the day count and the session count must be
// reached before optimizers may run for real: a week with two sessions, or 20
// sessions in an hour, is not a baseline.
func PhaseFor(m config.Measure, installedAt, now time.Time, sessionsSeen int) BaselineStatus {
	st := BaselineStatus{
		InstalledAt: installedAt, DaysNeeded: m.BaselineDays, SessionsNeeded: m.BaselineSessions,
		SessionsSeen: sessionsSeen, DaysElapsed: now.Sub(installedAt).Hours() / 24,
	}
	st.Phase = PhaseBaseline
	if st.DaysElapsed >= float64(m.BaselineDays) && sessionsSeen >= m.BaselineSessions {
		st.Phase = PhaseActive
	}
	return st
}

// Arm says which side of an interleaved comparison a session is on. It depends
// only on the session key and optimizer name, so it is stable and reproducible.
func Arm(sessionKey, optimizer string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(optimizer + "\x00" + sessionKey))
	if h.Sum32()%2 == 0 {
		return config.StateOn
	}
	return config.StateOff
}

// Gate is the optimize.StateGate for a running gateway. It only lowers states:
// "on" becomes "shadow" during the baseline, while a guardrail rollback is in
// effect, or on the off side of an interleaved comparison. Shadow forwards the
// original request, so every failure here lowers rather than raises: a problem
// reading the database means "shadow", never "on".
type Gate struct {
	db  *store.DB
	cfg config.Measure
	now func() time.Time
	ttl time.Duration

	mu       sync.Mutex
	loadedAt time.Time
	baseline BaselineStatus
	rolled   map[string]string // optimizer -> reason
	err      error
}

// NewGate builds a Gate. now may be nil.
func NewGate(db *store.DB, cfg config.Measure, now func() time.Time) *Gate {
	if now == nil {
		now = time.Now
	}
	return &Gate{db: db, cfg: cfg, now: now, ttl: 30 * time.Second}
}

// State implements optimize.StateGate.
func (g *Gate) State(sessionKey, optimizer, configured string, at time.Time) string {
	if configured != config.StateOn {
		return configured
	}
	base, rolled, err := g.snapshot()
	if err != nil || base.Phase == PhaseBaseline {
		return config.StateShadow
	}
	if _, back := rolled[optimizer]; back {
		return config.StateShadow
	}
	if g.cfg.Interleave && Arm(sessionKey, optimizer) == config.StateOff {
		return config.StateShadow
	}
	return config.StateOn
}

// snapshot reads (at most every ttl) the install time, session count and active rollbacks.
func (g *Gate) snapshot() (BaselineStatus, map[string]string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	if !g.loadedAt.IsZero() && now.Sub(g.loadedAt) < g.ttl {
		return g.baseline, g.rolled, g.err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	g.baseline, g.rolled, g.err = LoadStatus(ctx, g.db, g.cfg, now)
	g.loadedAt = now
	return g.baseline, g.rolled, g.err
}

// LoadStatus reads the baseline status and the active rollbacks. The install
// time is recorded the first time it is needed.
func LoadStatus(ctx context.Context, db *store.DB, m config.Measure, now time.Time) (BaselineStatus, map[string]string, error) {
	installed, err := installTime(ctx, db, now)
	if err != nil {
		return BaselineStatus{}, nil, err
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions`).Scan(&n); err != nil {
		return BaselineStatus{}, nil, err
	}
	rolled, err := Overrides(ctx, db)
	if err != nil {
		return BaselineStatus{}, nil, err
	}
	return PhaseFor(m, installed, now, n), rolled, nil
}

// installTime returns when Trove first measured anything, recording it on first use.
func installTime(ctx context.Context, db *store.DB, now time.Time) (time.Time, error) {
	var v string
	err := db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, keyInstallAt).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO settings(key, value) VALUES(?, ?)`, keyInstallAt, strconv.FormatInt(now.UnixMilli(), 10)); err != nil {
			return time.Time{}, err
		}
		return now, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	ms, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return time.Time{}, errors.New("the install time stored in Trove's database is not a number; delete the settings row " + keyInstallAt)
	}
	return time.UnixMilli(ms).UTC(), nil
}

// Overrides returns the optimizers currently rolled back to shadow, with the reason.
func Overrides(ctx context.Context, db *store.DB) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT name, reason FROM optimizations WHERE state = 'shadow' AND reason LIKE ?`, guardrailPrefix+"%")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var n, r string
		if err := rows.Scan(&n, &r); err != nil {
			return nil, err
		}
		out[n] = r[len(guardrailPrefix):]
	}
	return out, rows.Err()
}

// ClearOverride lifts a guardrail rollback after the user decides to try again.
// Sessions started before now no longer count toward the next check.
func ClearOverride(ctx context.Context, db *store.DB, optimizer string, now time.Time) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM optimizations WHERE name = ? AND reason LIKE ?`, optimizer, guardrailPrefix+"%"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE guardrail_events SET cleared_at = ? WHERE optimizer = ? AND cleared_at IS NULL`, now.UnixMilli(), optimizer); err != nil {
		return err
	}
	return tx.Commit()
}
